package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"example.com/paleo-prep-station/internal/contracts"
	"example.com/paleo-prep-station/internal/httpapi"
	"example.com/paleo-prep-station/internal/repository"
	"example.com/paleo-prep-station/internal/station"
)

type specimenView struct {
	ID            string `json:"id"`
	CatalogNumber string `json:"catalog_number"`
	Status        string `json:"status"`
}

type planView struct {
	ID               string `json:"id"`
	SpecimenID       string `json:"specimen_id"`
	Status           string `json:"status"`
	CancelledReason  string `json:"cancelled_reason"`
	SupersedesPlanID string `json:"supersedes_plan_id"`
	SupersededByID   string `json:"superseded_by_id"`
}

type planSnapshot struct {
	Plan     planView `json:"plan"`
	Progress []struct {
		Percent int `json:"percent"`
	} `json:"progress"`
}

type releaseView struct {
	ID         string `json:"id"`
	SpecimenID string `json:"specimen_id"`
}

func main() {
	workflow := flag.String("workflow", "", "workflow id to check")
	flag.Parse()

	if err := run(*workflow); err != nil {
		fmt.Fprintln(os.Stderr, "workflow check failed:", err)
		os.Exit(1)
	}
	fmt.Printf("workflow check passed: %s\n", *workflow)
}

func run(workflow string) error {
	handler := httpapi.NewRouter(station.New(repository.NewMemory()))
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}

	switch workflow {
	case "register-specimen":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		if specimen.Status != "registered" {
			return fmt.Errorf("expected registered status, got %s", specimen.Status)
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.ID != specimen.ID {
			return errors.New("registered specimen cannot be read back")
		}
		return nil

	case "approve-prep-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		var approved planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&approved,
		); err != nil {
			return err
		}
		if approved.Status != "approved" {
			return fmt.Errorf("expected approved plan, got %s", approved.Status)
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("expected preparing specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "log-preparation":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 35, "矩阵剥离"); err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "completed" || len(snapshot.Progress) != 2 {
			return errors.New("plan did not complete with exactly two progress entries")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "stabilized" {
			return fmt.Errorf("expected stabilized specimen, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "cancel-and-replace-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/plans/"+plan.ID+"/approval",
			contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
			http.StatusOK,
			&plan,
		); err != nil {
			return err
		}
		if err := cancelPlan(client, server.URL, plan.ID, "审批后确认酸蚀法不适用", http.StatusOK, &plan); err != nil {
			return err
		}
		if plan.Status != "cancelled" || plan.CancelledReason != "审批后确认酸蚀法不适用" {
			return fmt.Errorf("cancellation fact missing on plan: %+v", plan)
		}
		// Repeating the cancellation has a deterministic conflict result.
		if err := cancelPlan(client, server.URL, plan.ID, "重复取消", http.StatusConflict, nil); err != nil {
			return err
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "planned" {
			return fmt.Errorf("expected specimen reopened to planned, got %s", fetchedSpecimen.Status)
		}

		var replacement planView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/plans",
			contracts.CreatePlanRequest{
				Objective:      "暴露主要关节面",
				Method:         "手工显微剥离",
				TargetExposure: 80,
				ReplacesPlanID: plan.ID,
			},
			http.StatusCreated,
			&replacement,
		); err != nil {
			return err
		}
		if replacement.Status != "draft" || replacement.SupersedesPlanID != plan.ID {
			return fmt.Errorf("replacement link not recorded: %+v", replacement)
		}
		var oldSnapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &oldSnapshot); err != nil {
			return err
		}
		if oldSnapshot.Plan.SupersededByID != replacement.ID || oldSnapshot.Plan.Status != "cancelled" {
			return fmt.Errorf("original plan must stay cancelled and point to successor: %+v", oldSnapshot.Plan)
		}
		// A second replacement attempt is rejected deterministically and leaves
		// the single open replacement plan in place.
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/plans",
			contracts.CreatePlanRequest{
				Objective:      "再次调整目标",
				Method:         "手工显微剥离",
				TargetExposure: 60,
				ReplacesPlanID: plan.ID,
			},
			http.StatusConflict,
			nil,
		); err != nil {
			return err
		}
		if err := cancelPlan(client, server.URL, "plan-unknown", "取消未知方案", http.StatusNotFound, nil); err != nil {
			return err
		}
		return nil

	case "cancel-plan-with-progress-rejected":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 20, "矩阵剥离"); err != nil {
			return err
		}
		if err := cancelPlan(client, server.URL, plan.ID, "开工后才发现方法不适用", http.StatusConflict, nil); err != nil {
			return err
		}
		var snapshot planSnapshot
		if err := requestJSON(client, http.MethodGet, server.URL+"/plans/"+plan.ID, nil, http.StatusOK, &snapshot); err != nil {
			return err
		}
		if snapshot.Plan.Status != "active" || len(snapshot.Progress) != 1 {
			return errors.New("plan with progress must remain active with its history intact")
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "preparing" {
			return fmt.Errorf("specimen must stay preparing, got %s", fetchedSpecimen.Status)
		}
		return nil

	case "release-for-study":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 100, "表面加固"); err != nil {
			return err
		}
		var release releaseView
		if err := requestJSON(
			client,
			http.MethodPost,
			server.URL+"/specimens/"+specimen.ID+"/release",
			contracts.ReleaseRequest{
				Purpose:    "比较解剖研究",
				Recipient:  "地层研究组",
				ApprovedBy: "keeper-zhou",
			},
			http.StatusCreated,
			&release,
		); err != nil {
			return err
		}
		if release.SpecimenID != specimen.ID {
			return errors.New("release record references the wrong specimen")
		}
		var fetched specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetched); err != nil {
			return err
		}
		if fetched.Status != "available" {
			return fmt.Errorf("expected available specimen, got %s", fetched.Status)
		}
		return nil
	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: "PPS-2026-001",
			FieldLabel:    "北坡-07",
			TaxonHint:     "大型脊椎动物",
			MatrixKind:    "粉砂岩",
			JacketNotes:   "夹克边角完好",
		},
		http.StatusCreated,
		&specimen,
	)
	return specimen, err
}

func createPlan(client *http.Client, baseURL, specimenID string) (planView, error) {
	var plan planView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens/"+specimenID+"/plans",
		contracts.CreatePlanRequest{
			Objective:      "暴露主要关节面",
			Method:         "手工显微剥离",
			Adhesive:       "可逆树脂",
			TargetExposure: 80,
		},
		http.StatusCreated,
		&plan,
	)
	return plan, err
}

func completedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	specimen, err := registerSpecimen(client, baseURL)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func cancelPlan(client *http.Client, baseURL, planID, reason string, wantStatus int, target any) error {
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/cancellation",
		contracts.CancelPlanRequest{
			Reason:      reason,
			CancelledBy: "curator-lin",
		},
		wantStatus,
		target,
	)
}

func recordProgress(client *http.Client, baseURL, planID string, percent int, stage string) error {
	var ignored struct{}
	return requestJSON(
		client,
		http.MethodPost,
		baseURL+"/plans/"+planID+"/progress",
		contracts.ProgressRequest{
			Stage:      stage,
			Percent:    percent,
			Note:       "本地链路检查",
			RecordedBy: "preparator-chen",
		},
		http.StatusCreated,
		&ignored,
	)
}

func requestJSON(
	client *http.Client,
	method string,
	url string,
	body any,
	wantStatus int,
	target any,
) error {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, payload)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != wantStatus {
		return fmt.Errorf("request %s %s returned %d: %s", method, url, response.StatusCode, string(raw))
	}
	if target == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode response for %s %s: %w", method, url, err)
	}
	return nil
}
