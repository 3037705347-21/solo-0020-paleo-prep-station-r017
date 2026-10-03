package domain

import (
	"time"

	"example.com/paleo-prep-station/internal/contracts"
)

type PlanStatus string

const (
	PlanDraft     PlanStatus = "draft"
	PlanApproved  PlanStatus = "approved"
	PlanActive    PlanStatus = "active"
	PlanCompleted PlanStatus = "completed"
	PlanCancelled PlanStatus = "cancelled"
)

type PrepPlan struct {
	ID                 string     `json:"id"`
	SpecimenID         string     `json:"specimen_id"`
	Objective          string     `json:"objective"`
	Method             string     `json:"method"`
	Adhesive           string     `json:"adhesive"`
	TargetExposure     int        `json:"target_exposure"`
	Status             PlanStatus `json:"status"`
	ApprovedBy         string     `json:"approved_by,omitempty"`
	ApprovedAt         *time.Time `json:"approved_at,omitempty"`
	SupersedesPlanID   string     `json:"supersedes_plan_id,omitempty"`
	CancelledReason    string     `json:"cancelled_reason,omitempty"`
	CancelledBy        string     `json:"cancelled_by,omitempty"`
	CancelledAt        *time.Time `json:"cancelled_at,omitempty"`
	SupersededByPlanID string     `json:"superseded_by_plan_id,omitempty"`
	Version            int        `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

func NewPrepPlan(
	id string,
	specimen Specimen,
	objective string,
	method string,
	adhesive string,
	targetExposure int,
	supersedesPlanID string,
	now time.Time,
) (PrepPlan, error) {
	if id == "" {
		return PrepPlan{}, contracts.Internal("missing_generated_id", "方案标识未生成", nil)
	}
	if !specimen.CanCreatePlan() {
		return PrepPlan{}, contracts.Conflict("specimen_not_plannable", "当前标本状态不能创建修复方案")
	}
	goal, err := requiredText("修复目标", objective, 200)
	if err != nil {
		return PrepPlan{}, err
	}
	approach, err := requiredText("修复方法", method, 160)
	if err != nil {
		return PrepPlan{}, err
	}
	adhesiveValue, err := optionalText("黏结材料", adhesive, 120)
	if err != nil {
		return PrepPlan{}, err
	}
	if err := validatePercent(targetExposure); err != nil {
		return PrepPlan{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须位于 0 到 100 之间")
	}
	if targetExposure == 0 {
		return PrepPlan{}, contracts.Invalid("invalid_target_exposure", "目标暴露比例必须大于零")
	}
	return PrepPlan{
		ID:               id,
		SpecimenID:       specimen.ID,
		Objective:        goal,
		Method:           approach,
		Adhesive:         adhesiveValue,
		TargetExposure:   targetExposure,
		Status:           PlanDraft,
		SupersedesPlanID: supersedesPlanID,
		Version:          1,
		CreatedAt:        now.UTC(),
		UpdatedAt:        now.UTC(),
	}, nil
}

func (p PrepPlan) IsOpen() bool {
	return p.Status == PlanDraft || p.Status == PlanApproved || p.Status == PlanActive
}

func (p PrepPlan) IsCompleted() bool {
	return p.Status == PlanCompleted
}

func (p *PrepPlan) Approve(approvedBy string, now time.Time) error {
	if p.Status != PlanDraft {
		return contracts.Conflict("plan_not_approvable", "只有草稿方案可以审批")
	}
	approver, err := requiredText("审批人", approvedBy, 80)
	if err != nil {
		return err
	}
	stamp := now.UTC()
	p.Status = PlanApproved
	p.ApprovedBy = approver
	p.ApprovedAt = &stamp
	p.Version++
	p.UpdatedAt = stamp
	return nil
}

func (p *PrepPlan) Begin(now time.Time) error {
	if p.Status == PlanActive {
		return nil
	}
	if p.Status != PlanApproved {
		return contracts.Conflict("plan_not_active", "只有已审批方案可以开始制备")
	}
	p.Status = PlanActive
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}

func (p *PrepPlan) Complete(now time.Time) error {
	if p.Status == PlanCompleted {
		return nil
	}
	if p.Status != PlanActive {
		return contracts.Conflict("plan_not_completable", "只有进行中的方案可以完成")
	}
	p.Status = PlanCompleted
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}

// Cancel 只允许未写入进度的草稿或已审批方案带原因取消。
// 取消事实（原因、取消人、时间）永久保留在方案上。
func (p *PrepPlan) Cancel(reason string, cancelledBy string, hasProgress bool, now time.Time) error {
	if p.Status == PlanCancelled {
		return contracts.Conflict("plan_already_cancelled", "方案已经取消，不能重复取消")
	}
	if p.Status == PlanCompleted {
		return contracts.Conflict("plan_not_cancellable", "已完成的方案不能取消")
	}
	if p.Status == PlanActive || hasProgress {
		return contracts.Conflict("plan_progress_recorded", "方案已写入制备进度，不能取消，必须继续推进或完成")
	}
	if p.Status != PlanDraft && p.Status != PlanApproved {
		return contracts.Conflict("plan_not_cancellable", "只有未开始的草稿或已审批方案可以取消")
	}
	reasonValue, err := requiredText("取消原因", reason, 300)
	if err != nil {
		return err
	}
	operator, err := requiredText("取消人", cancelledBy, 80)
	if err != nil {
		return err
	}
	stamp := now.UTC()
	p.Status = PlanCancelled
	p.CancelledReason = reasonValue
	p.CancelledBy = operator
	p.CancelledAt = &stamp
	p.Version++
	p.UpdatedAt = stamp
	return nil
}

// MarkSupersededBy 在原子接替事务中把取消事实链接到接替方案，
// 保证取消事实不被覆盖，方案链不出现分叉。
func (p *PrepPlan) MarkSupersededBy(replacementID string, now time.Time) error {
	if replacementID == "" {
		return contracts.Internal("missing_generated_id", "接替方案标识未生成", nil)
	}
	if p.Status != PlanCancelled {
		return contracts.Conflict("plan_not_cancelled", "只有已取消方案可以登记接替关系")
	}
	if p.SupersededByPlanID != "" {
		return contracts.Conflict("plan_already_superseded", "原方案已经被其他方案接替")
	}
	p.SupersededByPlanID = replacementID
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
