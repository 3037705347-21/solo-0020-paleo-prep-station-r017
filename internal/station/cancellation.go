package station

import (
	"context"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

// CancelPlan terminates a draft or approved plan that has no progress recorded.
// The cancellation fact is retained on the plan itself; the specimen status is
// rewound so a replacement plan can be planned and approved again.
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
		specimen, ok := m.GetSpecimen(plan.SpecimenID)
		if !ok {
			return notFoundSpecimen(plan.SpecimenID)
		}
		// Plans that already carry progress cannot take this path; the recorded
		// preparation history must not be discarded.
		if len(m.ProgressForPlan(planID)) > 0 {
			return contracts.Conflict("plan_progress_recorded", "方案已写入制备进度，不能取消")
		}
		wasApproved := plan.Status == domain.PlanApproved
		if err := plan.Cancel(request.Reason, request.CancelledBy, s.now()); err != nil {
			return err
		}
		if wasApproved {
			if err := specimen.ReopenForPlanning(s.now()); err != nil {
				return err
			}
		} else {
			if err := specimen.RevertToRegistered(s.now()); err != nil {
				return err
			}
		}
		m.PutPlan(plan)
		m.PutSpecimen(specimen)
		cancelled = plan
		return nil
	})
	return cancelled, err
}
