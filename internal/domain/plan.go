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
	ID               string     `json:"id"`
	SpecimenID       string     `json:"specimen_id"`
	Objective        string     `json:"objective"`
	Method           string     `json:"method"`
	Adhesive         string     `json:"adhesive"`
	TargetExposure   int        `json:"target_exposure"`
	Status           PlanStatus `json:"status"`
	ApprovedBy       string     `json:"approved_by,omitempty"`
	ApprovedAt       *time.Time `json:"approved_at,omitempty"`
	CancelledBy      string     `json:"cancelled_by,omitempty"`
	CancelledReason  string     `json:"cancelled_reason,omitempty"`
	CancelledAt      *time.Time `json:"cancelled_at,omitempty"`
	SupersedesPlanID string     `json:"supersedes_plan_id,omitempty"`
	SupersededByID   string     `json:"superseded_by_id,omitempty"`
	Version          int        `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
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

func (p PrepPlan) IsCancelled() bool {
	return p.Status == PlanCancelled
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

// Cancel terminates a plan before any preparation progress has been recorded.
// Drafts and approved plans may be cancelled with a mandatory reason; plans that
// already carry progress (active or completed) are closed forever and must be
// revised through other means. Repeating a cancellation returns a conflict so
// callers get a deterministic result instead of mutating the retained record.
func (p *PrepPlan) Cancel(reason string, cancelledBy string, now time.Time) error {
	if p.Status == PlanCancelled {
		return contracts.Conflict("plan_not_cancellable", "方案已经取消，不能重复取消")
	}
	if p.Status != PlanDraft && p.Status != PlanApproved {
		return contracts.Conflict("plan_not_cancellable", "只有未开始的草稿或已审批方案可以取消")
	}
	reasonValue, err := requiredText("取消原因", reason, 300)
	if err != nil {
		return err
	}
	canceller, err := requiredText("取消操作人", cancelledBy, 80)
	if err != nil {
		return err
	}
	stamp := now.UTC()
	p.Status = PlanCancelled
	p.CancelledReason = reasonValue
	p.CancelledBy = canceller
	p.CancelledAt = &stamp
	p.Version++
	p.UpdatedAt = stamp
	return nil
}

// MarkSupersededBy records that a newly created plan replaces this cancelled
// plan. A plan can be succeeded at most once, and only cancelled plans may be
// succeeded; this keeps the replacement chain a single unbroken line.
func (p *PrepPlan) MarkSupersededBy(successorID string, now time.Time) error {
	if successorID == "" {
		return contracts.Internal("missing_generated_id", "接替方案标识未生成", nil)
	}
	if p.Status != PlanCancelled {
		return contracts.Conflict("plan_not_replaceable", "只有已取消方案可以被新方案接替")
	}
	if p.SupersededByID != "" {
		return contracts.Conflict("plan_already_superseded", "该方案已经存在接替方案")
	}
	p.SupersededByID = successorID
	p.Version++
	p.UpdatedAt = now.UTC()
	return nil
}
