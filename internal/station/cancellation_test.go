package station

import (
	"context"
	"sync"
	"testing"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/domain"
	"example.com/paleo-prep-station/internal/repository"
)

func newTestService() *Service {
	return New(repository.NewMemory())
}

func registerTestSpecimen(t *testing.T, svc *Service) domain.Specimen {
	t.Helper()
	specimen, err := svc.RegisterSpecimen(context.Background(), contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-TEST-" + t.Name(),
		FieldLabel:    "北坡-07",
		MatrixKind:    "粉砂岩",
	})
	if err != nil {
		t.Fatalf("register specimen: %v", err)
	}
	return specimen
}

func createTestPlan(t *testing.T, svc *Service, specimenID string) domain.PrepPlan {
	t.Helper()
	plan, err := svc.CreatePlan(context.Background(), specimenID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "手工显微剥离",
		Adhesive:       "可逆树脂",
		TargetExposure: 80,
	})
	if err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return plan
}

func approveTestPlan(t *testing.T, svc *Service, planID string) domain.PrepPlan {
	t.Helper()
	plan, err := svc.ApprovePlan(context.Background(), planID, contracts.ApprovalRequest{
		ApprovedBy: "curator-lin",
	})
	if err != nil {
		t.Fatalf("approve plan: %v", err)
	}
	return plan
}

func TestCancelDraftPlanRevertsSpecimenAndRetainsRecord(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	plan := createTestPlan(t, svc, specimen.ID)

	cancelled, err := svc.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{
		Reason:      "草稿方法与基质类型不匹配",
		CancelledBy: "curator-lin",
	})
	if err != nil {
		t.Fatalf("cancel draft plan: %v", err)
	}
	if cancelled.Status != domain.PlanCancelled {
		t.Fatalf("expected cancelled status, got %s", cancelled.Status)
	}
	if cancelled.CancelledReason != "草稿方法与基质类型不匹配" || cancelled.CancelledBy != "curator-lin" || cancelled.CancelledAt == nil {
		t.Fatalf("cancellation fact not retained: %+v", cancelled)
	}
	snapshot, err := svc.GetPlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatalf("get cancelled plan: %v", err)
	}
	if snapshot.Plan.Status != domain.PlanCancelled {
		t.Fatalf("cancelled plan record must be retained, got %s", snapshot.Plan.Status)
	}
	refreshed, _ := svc.GetSpecimen(context.Background(), specimen.ID)
	if refreshed.Status != domain.SpecimenRegistered {
		t.Fatalf("expected specimen back to registered after draft cancel, got %s", refreshed.Status)
	}
}

func TestCancelApprovedPlanRewindsToPlanned(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	plan := createTestPlan(t, svc, specimen.ID)
	plan = approveTestPlan(t, svc, plan.ID)
	_ = plan

	cancelled, err := svc.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{
		Reason:      "审批后发现黏结材料不适用于该基质",
		CancelledBy: "keeper-zhou",
	})
	if err != nil {
		t.Fatalf("cancel approved plan: %v", err)
	}
	if cancelled.Status != domain.PlanCancelled {
		t.Fatalf("expected cancelled status, got %s", cancelled.Status)
	}
	refreshed, _ := svc.GetSpecimen(context.Background(), specimen.ID)
	if refreshed.Status != domain.SpecimenPlanned {
		t.Fatalf("expected specimen back to planned after approved cancel, got %s", refreshed.Status)
	}
}

func TestCancelPlanWithProgressRejected(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	plan := createTestPlan(t, svc, specimen.ID)
	plan = approveTestPlan(t, svc, plan.ID)
	if _, err := svc.LogProgress(context.Background(), plan.ID, contracts.ProgressRequest{
		Stage:      "矩阵剥离",
		Percent:    20,
		RecordedBy: "preparator-chen",
	}); err != nil {
		t.Fatalf("log progress: %v", err)
	}

	_, err := svc.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{
		Reason:      "发现方法不适用",
		CancelledBy: "curator-lin",
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindConflict {
		t.Fatalf("expected conflict for plan with progress, got %v", err)
	}
	snapshot, _ := svc.GetPlan(context.Background(), plan.ID)
	if snapshot.Plan.Status != domain.PlanActive {
		t.Fatalf("plan with progress must stay active, got %s", snapshot.Plan.Status)
	}
}

func TestRepeatedCancellationIsConflict(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	plan := createTestPlan(t, svc, specimen.ID)
	request := contracts.CancelPlanRequest{Reason: "方案重拟", CancelledBy: "curator-lin"}
	if _, err := svc.CancelPlan(context.Background(), plan.ID, request); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	_, err := svc.CancelPlan(context.Background(), plan.ID, request)
	appErr, ok := contracts.AsError(err)
	if !ok || appErr.Kind != contracts.KindConflict || appErr.Code != "plan_not_cancellable" {
		t.Fatalf("expected deterministic conflict on repeated cancel, got %v", err)
	}
}

func TestCancelRequiresReasonAndOperator(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	plan := createTestPlan(t, svc, specimen.ID)

	_, err := svc.CancelPlan(context.Background(), plan.ID, contracts.CancelPlanRequest{
		Reason:      "   ",
		CancelledBy: "curator-lin",
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindInvalid {
		t.Fatalf("expected invalid request for blank reason, got %v", err)
	}
	snapshot, _ := svc.GetPlan(context.Background(), plan.ID)
	if snapshot.Plan.Status != domain.PlanDraft {
		t.Fatalf("failed cancel must not change plan, got %s", snapshot.Plan.Status)
	}
}

func TestCancelUnknownPlanNotFound(t *testing.T) {
	svc := newTestService()
	_, err := svc.CancelPlan(context.Background(), "plan-unknown", contracts.CancelPlanRequest{
		Reason:      "无关取消",
		CancelledBy: "curator-lin",
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindNotFound {
		t.Fatalf("expected not found for unknown plan, got %v", err)
	}
}

func TestReplaceCancelledPlanLinksChain(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)
	original = approveTestPlan(t, svc, original.ID)
	if _, err := svc.CancelPlan(context.Background(), original.ID, contracts.CancelPlanRequest{
		Reason:      "方法不适用，改用酸解法",
		CancelledBy: "curator-lin",
	}); err != nil {
		t.Fatalf("cancel original: %v", err)
	}

	replacement, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "弱酸基质溶解",
		TargetExposure: 70,
		ReplacesPlanID: original.ID,
	})
	if err != nil {
		t.Fatalf("create replacement plan: %v", err)
	}
	if replacement.SupersedesPlanID != original.ID {
		t.Fatalf("replacement must reference original plan, got %q", replacement.SupersedesPlanID)
	}
	if replacement.Status != domain.PlanDraft {
		t.Fatalf("replacement starts as draft, got %s", replacement.Status)
	}
	oldSnapshot, _ := svc.GetPlan(context.Background(), original.ID)
	if oldSnapshot.Plan.SupersededByID != replacement.ID {
		t.Fatalf("original plan must point to successor, got %q", oldSnapshot.Plan.SupersededByID)
	}
	plans, _ := svc.ListPlansForSpecimen(context.Background(), specimen.ID)
	openCount := 0
	for _, item := range plans {
		if item.IsOpen() {
			openCount++
		}
	}
	if openCount != 1 {
		t.Fatalf("expected exactly one open plan, got %d", openCount)
	}
}

func TestReplaceNonCancelledPlanRejected(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)

	_, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "弱酸基质溶解",
		TargetExposure: 70,
		ReplacesPlanID: original.ID,
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindConflict {
		t.Fatalf("expected conflict replacing an open plan, got %v", err)
	}
}

func TestReplaceAlreadySupersededPlanRejected(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)
	if _, err := svc.CancelPlan(context.Background(), original.ID, contracts.CancelPlanRequest{
		Reason:      "方法不适用",
		CancelledBy: "curator-lin",
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	first, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "弱酸基质溶解",
		TargetExposure: 70,
		ReplacesPlanID: original.ID,
	})
	if err != nil {
		t.Fatalf("first replacement: %v", err)
	}
	_, err = svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "再次调整修复方法",
		Method:         "手工显微剥离",
		TargetExposure: 60,
		ReplacesPlanID: original.ID,
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Code != "plan_already_superseded" {
		t.Fatalf("expected plan_already_superseded conflict, got %v", err)
	}
	oldSnapshot, _ := svc.GetPlan(context.Background(), original.ID)
	if oldSnapshot.Plan.SupersededByID != first.ID {
		t.Fatalf("successor link must remain unchanged, got %q", oldSnapshot.Plan.SupersededByID)
	}
}

func TestReplaceUnknownPlanNotFound(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	_, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "手工显微剥离",
		TargetExposure: 70,
		ReplacesPlanID: "plan-missing",
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Kind != contracts.KindNotFound {
		t.Fatalf("expected not found for unknown predecessor, got %v", err)
	}
}

func TestReplacePlanFromOtherSpecimenRejected(t *testing.T) {
	svc := newTestService()
	specimenA := registerTestSpecimen(t, svc)
	planA := createTestPlan(t, svc, specimenA.ID)
	if _, err := svc.CancelPlan(context.Background(), planA.ID, contracts.CancelPlanRequest{
		Reason:      "方法不适用",
		CancelledBy: "curator-lin",
	}); err != nil {
		t.Fatalf("cancel plan A: %v", err)
	}

	otherSpecimen, err := svc.RegisterSpecimen(context.Background(), contracts.RegisterSpecimenRequest{
		CatalogNumber: "PPS-TEST-OTHER",
		FieldLabel:    "北坡-08",
		MatrixKind:    "泥岩",
	})
	if err != nil {
		t.Fatalf("register second specimen: %v", err)
	}
	_, err = svc.CreatePlan(context.Background(), otherSpecimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "手工显微剥离",
		TargetExposure: 70,
		ReplacesPlanID: planA.ID,
	})
	if appErr, ok := contracts.AsError(err); !ok || appErr.Code != "replacement_plan_mismatch" {
		t.Fatalf("expected replacement_plan_mismatch, got %v", err)
	}
}

func TestConcurrentReplacementsOnlyOneSucceeds(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)
	if _, err := svc.CancelPlan(context.Background(), original.ID, contracts.CancelPlanRequest{
		Reason:      "方法不适用",
		CancelledBy: "curator-lin",
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	const runners = 8
	var wg sync.WaitGroup
	results := make(chan error, runners)
	wg.Add(runners)
	for i := 0; i < runners; i++ {
		go func() {
			defer wg.Done()
			_, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
				Objective:      "暴露主要关节面并清理围岩",
				Method:         "手工显微剥离",
				TargetExposure: 70,
				ReplacesPlanID: original.ID,
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		default:
			appErr, ok := contracts.AsError(err)
			if !ok {
				t.Fatalf("unexpected error type: %v", err)
			}
			if appErr.Code == "plan_already_superseded" || appErr.Code == "open_plan_exists" {
				conflicts++
				continue
			}
			t.Fatalf("unexpected conflict code %q: %v", appErr.Code, err)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one successful replacement, got %d (conflicts %d)", successes, conflicts)
	}
	if conflicts != runners-1 {
		t.Fatalf("expected %d deterministic conflicts, got %d", runners-1, conflicts)
	}

	plans, _ := svc.ListPlansForSpecimen(context.Background(), specimen.ID)
	open := 0
	for _, item := range plans {
		if item.IsOpen() {
			open++
		}
	}
	if open != 1 {
		t.Fatalf("specimen must keep exactly one open plan, got %d", open)
	}
	oldSnapshot, _ := svc.GetPlan(context.Background(), original.ID)
	if oldSnapshot.Plan.SupersededByID == "" {
		t.Fatal("original plan must retain the single successor link")
	}
}

func TestConcurrentCancelAndProgressHasDeterministicOutcome(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)
	original = approveTestPlan(t, svc, original.ID)

	const runners = 4
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan string, runners*2)

	for i := 0; i < runners; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.CancelPlan(context.Background(), original.ID, contracts.CancelPlanRequest{
				Reason:      "方法不适用",
				CancelledBy: "curator-lin",
			})
			if err == nil {
				results <- "cancelled"
			} else {
				results <- "cancel-conflict"
			}
		}()
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.LogProgress(context.Background(), original.ID, contracts.ProgressRequest{
				Stage:      "矩阵剥离",
				Percent:    10,
				RecordedBy: "preparator-chen",
			})
			if err == nil {
				results <- "progress"
			} else {
				results <- "progress-conflict"
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	cancels, progresses := 0, 0
	for result := range results {
		switch result {
		case "cancelled":
			cancels++
		case "progress":
			progresses++
		}
	}
	if cancels > 1 {
		t.Fatalf("cancellation must win at most once, got %d successes", cancels)
	}

	// Whichever operation claims the approved plan first becomes the single
	// committed outcome; the loser is rejected for every remaining attempt.
	snapshot, _ := svc.GetPlan(context.Background(), original.ID)
	switch snapshot.Plan.Status {
	case domain.PlanCancelled:
		if cancels != 1 {
			t.Fatalf("cancelled plan needs one cancel commit, got %d", cancels)
		}
		if progresses != 0 || len(snapshot.Progress) != 0 {
			t.Fatalf("cancelled plan must retain no progress, got committed=%d stored=%d", progresses, len(snapshot.Progress))
		}
		refreshed, _ := svc.GetSpecimen(context.Background(), specimen.ID)
		if refreshed.Status != domain.SpecimenPlanned {
			t.Fatalf("specimen should reopen for planning, got %s", refreshed.Status)
		}
	case domain.PlanActive:
		if cancels != 0 {
			t.Fatal("no cancellation may commit once progress exists")
		}
		if progresses != len(snapshot.Progress) || progresses == 0 {
			t.Fatalf("active plan must retain exactly the committed progress entries, got %d/%d", progresses, len(snapshot.Progress))
		}
		refreshed, _ := svc.GetSpecimen(context.Background(), specimen.ID)
		if refreshed.Status != domain.SpecimenPreparing {
			t.Fatalf("specimen should stay preparing, got %s", refreshed.Status)
		}
	default:
		t.Fatalf("unexpected final status %s", snapshot.Plan.Status)
	}
}

func TestNewPlanAfterCancellationWithoutReplacementLink(t *testing.T) {
	svc := newTestService()
	specimen := registerTestSpecimen(t, svc)
	original := createTestPlan(t, svc, specimen.ID)
	if _, err := svc.CancelPlan(context.Background(), original.ID, contracts.CancelPlanRequest{
		Reason:      "方法不适用",
		CancelledBy: "curator-lin",
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	replan, err := svc.CreatePlan(context.Background(), specimen.ID, contracts.CreatePlanRequest{
		Objective:      "暴露主要关节面并清理围岩",
		Method:         "手工显微剥离",
		TargetExposure: 50,
	})
	if err != nil {
		t.Fatalf("replan without replacement link: %v", err)
	}
	if replan.SupersedesPlanID != "" {
		t.Fatalf("plain replan must not claim a predecessor, got %q", replan.SupersedesPlanID)
	}
}
