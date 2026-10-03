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
	"sync"
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
	ID                 string `json:"id"`
	SpecimenID         string `json:"specimen_id"`
	Status             string `json:"status"`
	SupersedesPlanID   string `json:"supersedes_plan_id"`
	SupersededByPlanID string `json:"superseded_by_plan_id"`
	CancelledReason    string `json:"cancelled_reason"`
	CancelledBy        string `json:"cancelled_by"`
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

	case "cancel-unstarted-plan":
		specimen, err := registerSpecimen(client, server.URL)
		if err != nil {
			return err
		}
		plan, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		if err := approvePlan(client, server.URL, plan.ID); err != nil {
			return err
		}
		var cancelled planView
		if err := postJSON(
			client,
			server.URL+"/plans/"+plan.ID+"/cancellation",
			contracts.CancelPlanRequest{Reason: "酸蚀法会破坏围岩中的痕迹化石", CancelledBy: "curator-lin"},
			http.StatusOK,
			&cancelled,
		); err != nil {
			return err
		}
		if cancelled.Status != "cancelled" || cancelled.CancelledReason == "" || cancelled.CancelledBy != "curator-lin" {
			return errors.New("取消事实没有完整保留在原方案上")
		}
		// 重复取消必须得到确定的冲突结果。
		if err := postExpectStatus(
			client,
			server.URL+"/plans/"+plan.ID+"/cancellation",
			contracts.CancelPlanRequest{Reason: "再次取消", CancelledBy: "curator-lin"},
			http.StatusConflict,
		); err != nil {
			return err
		}
		var fetchedSpecimen specimenView
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID, nil, http.StatusOK, &fetchedSpecimen); err != nil {
			return err
		}
		if fetchedSpecimen.Status != "planned" {
			return fmt.Errorf("取消零进度的已审批方案后标本应回到 planned，实际 %s", fetchedSpecimen.Status)
		}
		// 取消后可以重新规划，且原方案继续可读。
		replacement, err := createPlan(client, server.URL, specimen.ID)
		if err != nil {
			return err
		}
		original, err := getPlan(client, server.URL, plan.ID)
		if err != nil {
			return err
		}
		if original.Status != "cancelled" {
			return errors.New("原方案与取消事实必须保留")
		}
		if replacement.ID == plan.ID {
			return errors.New("接替规划必须生成新方案而不是覆盖原方案")
		}
		return nil

	case "supersede-plan":
		specimen, plan, err := approvedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		var replacement planView
		if err := postJSON(
			client,
			server.URL+"/specimens/"+specimen.ID+"/plans",
			contracts.CreatePlanRequest{
				Objective:        "改用机械剥离暴露关节面",
				Method:           "气动刻笔低功率清理",
				Adhesive:         "Paraloid B-72",
				TargetExposure:   75,
				SupersedesPlanID: plan.ID,
				CancelReason:     "原酸蚀方案对当前基质不适用",
				CancelledBy:      "curator-lin",
			},
			http.StatusCreated,
			&replacement,
		); err != nil {
			return err
		}
		if replacement.SupersedesPlanID != plan.ID || replacement.Status != "draft" {
			return errors.New("接替方案没有明确指向原方案或状态异常")
		}
		var original planView
		if err := getPlanInto(client, server.URL, plan.ID, &original); err != nil {
			return err
		}
		if original.Status != "cancelled" || original.SupersededByPlanID != replacement.ID || original.CancelledReason == "" {
			return errors.New("原方案未保留取消事实或接替链没有双向闭合")
		}
		var plans struct {
			Items []planView `json:"items"`
		}
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen.ID+"/plans", nil, http.StatusOK, &plans); err != nil {
			return err
		}
		open := 0
		for _, item := range plans.Items {
			if item.Status == "draft" || item.Status == "approved" || item.Status == "active" {
				open++
			}
		}
		if open != 1 {
			return fmt.Errorf("接替后标本上必须恰好存在一个开放方案，实际 %d 个", open)
		}
		// 并发接替一个仍开放的原方案：恰好一个成功，其余全部冲突，
		// 最终标本上只有一个开放方案，且不存在半截链。
		specimen2, openPlan, err := approvedPlanSetupWithCatalog(client, server.URL, "PPS-2026-002")
		if err != nil {
			return err
		}
		raceCodes := concurrentSupersede(client, server.URL, specimen2.ID, openPlan.ID, 8)
		raceSuccess, raceConflict, raceOther := 0, 0, 0
		for _, code := range raceCodes {
			switch code {
			case http.StatusCreated:
				raceSuccess++
			case http.StatusConflict:
				raceConflict++
			default:
				raceOther++
			}
		}
		if raceSuccess != 1 || raceConflict != 7 || raceOther != 0 {
			return fmt.Errorf("并发接替开放方案结果不确定: success=%d conflict=%d other=%d", raceSuccess, raceConflict, raceOther)
		}
		var afterRace struct {
			Items []planView `json:"items"`
		}
		if err := requestJSON(client, http.MethodGet, server.URL+"/specimens/"+specimen2.ID+"/plans", nil, http.StatusOK, &afterRace); err != nil {
			return err
		}
		openAfter, cancelledAfter := 0, 0
		var winner planView
		for _, item := range afterRace.Items {
			switch item.Status {
			case "draft", "approved", "active":
				openAfter++
				winner = item
			case "cancelled":
				cancelledAfter++
			}
		}
		if openAfter != 1 || cancelledAfter != 1 {
			return fmt.Errorf("并发接替后应有 1 个开放方案和 1 个取消方案，实际 open=%d cancelled=%d", openAfter, cancelledAfter)
		}
		if winner.SupersedesPlanID != openPlan.ID {
			return errors.New("并发胜出的接替方案没有指向原方案")
		}
		oldOpen, err := getPlan(client, server.URL, openPlan.ID)
		if err != nil {
			return err
		}
		if oldOpen.Status != "cancelled" || oldOpen.SupersededByPlanID != winner.ID {
			return errors.New("并发接替后原方案取消事实或接替链不完整")
		}
		return nil

	case "cannot-cancel-with-progress":
		specimen, plan, err := completedPlanSetup(client, server.URL)
		if err != nil {
			return err
		}
		if err := recordProgress(client, server.URL, plan.ID, 40, "初步清理"); err != nil {
			return err
		}
		if err := postExpectStatus(
			client,
			server.URL+"/plans/"+plan.ID+"/cancellation",
			contracts.CancelPlanRequest{Reason: "方法不适用", CancelledBy: "curator-lin"},
			http.StatusConflict,
		); err != nil {
			return err
		}
		// 带进度的方案也不能被"接替创建"偷偷取消。
		if err := postExpectStatus(
			client,
			server.URL+"/specimens/"+specimen.ID+"/plans",
			contracts.CreatePlanRequest{
				Objective:        "绕过限制",
				Method:           "手工清理",
				TargetExposure:   60,
				SupersedesPlanID: plan.ID,
				CancelReason:     "方法不适用",
				CancelledBy:      "curator-lin",
			},
			http.StatusConflict,
		); err != nil {
			return err
		}
		var stillActive planView
		if err := getPlanInto(client, server.URL, plan.ID, &stillActive); err != nil {
			return err
		}
		if stillActive.Status != "active" {
			return fmt.Errorf("写入进度的方案必须保持 active，实际 %s", stillActive.Status)
		}
		return nil

	case "cancel-unknown-plan":
		if err := postExpectStatus(
			client,
			server.URL+"/plans/plan-does-not-exist/cancellation",
			contracts.CancelPlanRequest{Reason: "不存在", CancelledBy: "nobody"},
			http.StatusNotFound,
		); err != nil {
			return err
		}
		return nil

	default:
		return fmt.Errorf("unknown workflow %q", workflow)
	}
}

func registerSpecimen(client *http.Client, baseURL string) (specimenView, error) {
	return registerSpecimenWithCatalog(client, baseURL, "PPS-2026-001")
}

func registerSpecimenWithCatalog(client *http.Client, baseURL, catalog string) (specimenView, error) {
	var specimen specimenView
	err := requestJSON(
		client,
		http.MethodPost,
		baseURL+"/specimens",
		contracts.RegisterSpecimenRequest{
			CatalogNumber: catalog,
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
	if err := postJSON(
		client,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func approvedPlanSetup(client *http.Client, baseURL string) (specimenView, planView, error) {
	return completedPlanSetup(client, baseURL)
}

func approvedPlanSetupWithCatalog(client *http.Client, baseURL, catalog string) (specimenView, planView, error) {
	specimen, err := registerSpecimenWithCatalog(client, baseURL, catalog)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	plan, err := createPlan(client, baseURL, specimen.ID)
	if err != nil {
		return specimenView{}, planView{}, err
	}
	var approved planView
	if err := postJSON(
		client,
		baseURL+"/plans/"+plan.ID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		&approved,
	); err != nil {
		return specimenView{}, planView{}, err
	}
	return specimen, approved, nil
}

func approvePlan(client *http.Client, baseURL, planID string) error {
	return postJSON(
		client,
		baseURL+"/plans/"+planID+"/approval",
		contracts.ApprovalRequest{ApprovedBy: "curator-lin"},
		http.StatusOK,
		nil,
	)
}

// planDetail 对应 GET /plans/{id} 返回的 {plan, progress} 快照。
type planDetail struct {
	Plan planView `json:"plan"`
}

func getPlanInto(client *http.Client, baseURL, planID string, target *planView) error {
	var detail planDetail
	if err := requestJSON(client, http.MethodGet, baseURL+"/plans/"+planID, nil, http.StatusOK, &detail); err != nil {
		return err
	}
	*target = detail.Plan
	return nil
}

func getPlan(client *http.Client, baseURL, planID string) (planView, error) {
	var target planView
	err := getPlanInto(client, baseURL, planID, &target)
	return target, err
}

func postJSON(client *http.Client, url string, body any, wantStatus int, target any) error {
	return requestJSON(client, http.MethodPost, url, body, wantStatus, target)
}

func postExpectStatus(client *http.Client, url string, body any, wantStatus int) error {
	return requestJSON(client, http.MethodPost, url, body, wantStatus, nil)
}

// concurrentSupersede 同时发起对同一原方案的接替创建，
// 返回每个请求的 HTTP 状态码，用于判定竞态下没有半截方案链。
func concurrentSupersede(client *http.Client, baseURL, specimenID, planID string, attempts int) []int {
	codes := make([]int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			codes[index] = postStatusOnly(client, baseURL+"/specimens/"+specimenID+"/plans", contracts.CreatePlanRequest{
				Objective:        "并发接替尝试",
				Method:           "机械剥离",
				TargetExposure:   50 + index,
				SupersedesPlanID: planID,
				CancelReason:     "并发请求",
				CancelledBy:      "curator-lin",
			})
		}(i)
	}
	wg.Wait()
	return codes
}

func postStatusOnly(client *http.Client, url string, body any) int {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return 0
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return response.StatusCode
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
