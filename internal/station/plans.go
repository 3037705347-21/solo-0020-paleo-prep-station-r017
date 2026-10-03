package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func (s *Service) CreatePlan(
	ctx context.Context,
	specimenID string,
	request contracts.CreatePlanRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var created domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		specimen, ok := m.GetSpecimen(specimenID)
		if !ok {
			return notFoundSpecimen(specimenID)
		}

		var previous *domain.PrepPlan
		if request.SupersedesPlanID != "" {
			target, exists := m.GetPlan(request.SupersedesPlanID)
			if !exists {
				return notFoundPlan(request.SupersedesPlanID)
			}
			if target.SpecimenID != specimenID {
				return contracts.Conflict("supersedes_plan_mismatch", "被接替方案不属于该标本")
			}
			switch {
			case target.Status == domain.PlanCompleted || target.Status == domain.PlanActive:
				return contracts.Conflict("plan_not_supersedable", "已完成或进行中的方案不能被接替")
			case target.Status == domain.PlanCancelled && target.SupersededByPlanID != "":
				return contracts.Conflict("plan_already_superseded", "原方案已经被其他方案接替")
			}
			if other, exists := m.OpenPlanOtherThan(specimenID, target.ID); exists {
				return contracts.Conflict("open_plan_exists", "该标本已存在其他未结束的修复方案: "+other.ID)
			}
			if target.IsOpen() {
				// 原子路径：把仍开放但零进度的原方案带原因取消。
				hasProgress := m.ProgressCountForPlan(target.ID) > 0
				if err := target.Cancel(request.CancelReason, request.CancelledBy, hasProgress, s.now()); err != nil {
					return err
				}
				if err := specimen.ReopenForPlanning(s.now()); err != nil {
					return err
				}
			}
			previous = &target
		} else if m.HasOpenPlan(specimenID) {
			return contracts.Conflict("open_plan_exists", "该标本已存在未结束的修复方案")
		}

		newID := s.nextID("plan")
		plan, err := domain.NewPrepPlan(
			newID,
			specimen,
			request.Objective,
			request.Method,
			request.Adhesive,
			request.TargetExposure,
			request.SupersedesPlanID,
			s.now(),
		)
		if err != nil {
			return err
		}
		if err := specimen.MarkPlanned(s.now()); err != nil {
			return err
		}
		if previous != nil {
			// 原方案与取消事实保留，并双向闭合方案链。
			if err := previous.MarkSupersededBy(plan.ID, s.now()); err != nil {
				return err
			}
			m.PutPlan(*previous)
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		created = plan
		return nil
	})
	return created, err
}

func (s *Service) CancelPlan(
	ctx context.Context,
	planID string,
	request contracts.CancelPlanRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var cancelled domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		specimen, exists := m.GetSpecimen(plan.SpecimenID)
		if !exists {
			return notFoundSpecimen(plan.SpecimenID)
		}
		hasProgress := m.ProgressCountForPlan(planID) > 0
		if err := plan.Cancel(request.Reason, request.CancelledBy, hasProgress, s.now()); err != nil {
			return err
		}
		if err := specimen.ReopenForPlanning(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		cancelled = plan
		return nil
	})
	return cancelled, err
}

func (s *Service) ApprovePlan(
	ctx context.Context,
	planID string,
	request contracts.ApprovalRequest,
) (domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return domain.PrepPlan{}, err
	}

	var approved domain.PrepPlan
	err := s.repo.Mutate(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		if err := plan.Approve(request.ApprovedBy, s.now()); err != nil {
			return err
		}
		if err := specimen.MarkPreparing(s.now()); err != nil {
			return err
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		approved = plan
		return nil
	})
	return approved, err
}

func (s *Service) GetPlan(ctx context.Context, planID string) (PlanSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return PlanSnapshot{}, err
	}
	var snapshot PlanSnapshot
	err := s.repo.Read(func(m *repository.Mutation) error {
		plan, ok := m.GetPlan(planID)
		if !ok {
			return notFoundPlan(planID)
		}
		snapshot = PlanSnapshot{
			Plan:     plan,
			Progress: m.ProgressForPlan(planID),
		}
		return nil
	})
	return snapshot, err
}

func (s *Service) ListPlansForSpecimen(
	ctx context.Context,
	specimenID string,
) ([]domain.PrepPlan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var values []domain.PrepPlan
	err := s.repo.Read(func(m *repository.Mutation) error {
		if _, ok := m.GetSpecimen(specimenID); !ok {
			return notFoundSpecimen(specimenID)
		}
		values = m.ListPlansForSpecimen(specimenID)
		return nil
	})
	return values, err
}
