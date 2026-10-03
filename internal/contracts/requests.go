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
	// ReplacesPlanID optionally names a cancelled plan on the same specimen
	// that this new plan formally succeeds.
	ReplacesPlanID string `json:"replaces_plan_id"`
}

type ApprovalRequest struct {
	ApprovedBy string `json:"approved_by"`
}

type CancelPlanRequest struct {
	Reason      string `json:"reason"`
	CancelledBy string `json:"cancelled_by"`
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
