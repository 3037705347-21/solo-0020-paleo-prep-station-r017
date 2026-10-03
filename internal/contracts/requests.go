package contracts

type RegisterSpecimenRequest struct {
	CatalogNumber string `json:"catalog_number"`
	FieldLabel    string `json:"field_label"`
	TaxonHint     string `json:"taxon_hint"`
	MatrixKind    string `json:"matrix_kind"`
	JacketNotes   string `json:"jacket_notes"`
}

type CreatePlanRequest struct {
	Objective      string `json:"objective"`
	Method         string `json:"method"`
	Adhesive       string `json:"adhesive"`
	TargetExposure int    `json:"target_exposure"`
	// SupersedesPlanID 非空时表示新方案在同一事务中接替该方案。
	// 被接替方案若仍开放，必须是零进度的草稿或已审批方案，会被带原因取消。
	SupersedesPlanID string `json:"supersedes_plan_id"`
	// 以下字段仅在被接替方案仍开放、需要原子取消时使用。
	CancelReason string `json:"cancel_reason"`
	CancelledBy  string `json:"cancelled_by"`
}

type CancelPlanRequest struct {
	Reason      string `json:"reason"`
	CancelledBy string `json:"cancelled_by"`
}

type ApprovalRequest struct {
	ApprovedBy string `json:"approved_by"`
}

type ProgressRequest struct {
	Stage      string `json:"stage"`
	Percent    int    `json:"percent"`
	Note       string `json:"note"`
	RecordedBy string `json:"recorded_by"`
}

type ReleaseRequest struct {
	Purpose    string `json:"purpose"`
	Recipient  string `json:"recipient"`
	ApprovedBy string `json:"approved_by"`
}
