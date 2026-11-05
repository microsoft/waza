package assurance

import (
	"time"

	"github.com/microsoft/waza/internal/models"
)

const ReportKind = "waza.grader-assurance"

const (
	CalibratedReportVersion = "1.1"
	CalibrationOperation    = "independent_authored_rubric_calibration"
)

type AssessmentState string

const (
	AssessmentPassed       AssessmentState = "passed"
	AssessmentFailed       AssessmentState = "failed"
	AssessmentNotAssessed  AssessmentState = "not_assessed"
	AssessmentInsufficient AssessmentState = "insufficient_evidence"
	AssessmentError        AssessmentState = "operational_error"
	AssessmentInvalid      AssessmentState = "invalid"
)

type Binding struct {
	Domain     string  `json:"domain"`
	Applicable bool    `json:"applicable"`
	State      string  `json:"state"`
	Expected   *string `json:"expected"`
	Actual     *string `json:"actual"`
}

type ReviewReport struct {
	SourceID              string       `json:"source_id"`
	CurrentSourceAccepted bool         `json:"current_source_accepted"`
	DeclaredState         ReviewState  `json:"declared_state"`
	Eligible              bool         `json:"eligible"`
	Reason                ReviewReason `json:"reason,omitempty"`
}

type Coverage struct {
	Good             int `json:"good"`
	AlternativeValid int `json:"alternative_valid"`
	CriticalBad      int `json:"critical_bad"`
	IntendedNegative int `json:"intended_negative"`
}

type ChallengeObservation struct {
	CaseID         string                     `json:"case_id"`
	ScenarioID     string                     `json:"scenario_id"`
	Domain         string                     `json:"domain"`
	Classification CaseClassification         `json:"classification"`
	SourceScope    string                     `json:"source_scope"`
	ExpectedPassed bool                       `json:"expected_passed"`
	State          ObservationState           `json:"state"`
	Reason         string                     `json:"reason"`
	Agreement      *bool                      `json:"agreement"`
	Result         *models.GraderResults      `json:"result"`
	Evidence       []models.EvidenceReference `json:"evidence"`
	Bindings       []Binding                  `json:"bindings"`
}

type RequirementAssessment struct {
	TaskID        string                  `json:"task_id"`
	RequirementID string                  `json:"requirement_id"`
	Check         models.RequirementCheck `json:"check"`
	State         AssessmentState         `json:"state"`
	Reason        string                  `json:"reason"`
	Declared      Coverage                `json:"declared_coverage"`
	Observed      Coverage                `json:"observed_coverage"`
	Observations  []ChallengeObservation  `json:"observations"`
}

// CalibrationReport never converts absent observed billing into zero credits.
// Agreement over supplied cases is descriptive, not statistical confidence.
type CalibrationReport struct {
	State                AssessmentState    `json:"state"`
	Reason               string             `json:"reason"`
	Protocol             string             `json:"protocol,omitempty"`
	Model                string             `json:"model,omitempty"`
	MaxJudgeExecutions   int                `json:"max_judge_executions"`
	Executions           int                `json:"executions"`
	BillableCallsBounded bool               `json:"billable_calls_bounded"`
	CostBounded          bool               `json:"cost_bounded"`
	Samples              int                `json:"samples"`
	ConfidenceSupported  bool               `json:"confidence_supported"`
	Usage                *models.UsageStats `json:"usage"`
	Credits              *float64           `json:"credits"`
	ExecutionLedger      *[]JudgeExecution  `json:"execution_ledger,omitempty"`
}

type JudgeDiagnostic struct {
	Stage string `json:"stage"`
	Code  string `json:"code"`
}

// Initialized denotes successful initialization; Executed denotes Execute
// entry even on failure. Callbacks counts native grade-handler invocations.
// Models are independently observed, never copied from RequestedModel.
type JudgeExecution struct {
	CaseID                             string                  `json:"case_id"`
	TaskID                             string                  `json:"task_id"`
	RequirementID                      string                  `json:"requirement_id"`
	Check                              models.RequirementCheck `json:"check"`
	Initialized                        bool                    `json:"initialized"`
	Executed                           bool                    `json:"executed"`
	Callbacks                          int                     `json:"callbacks"`
	RequestedModel                     string                  `json:"requested_model"`
	EventModels                        []string                `json:"event_models"`
	AccountingModels                   []string                `json:"accounting_models"`
	EventModelAttributionComplete      bool                    `json:"event_model_attribution_complete"`
	AccountingModelAttributionComplete bool                    `json:"accounting_model_attribution_complete"`
	UsageSource                        string                  `json:"usage_source"`
	UsageComplete                      bool                    `json:"usage_complete"`
	State                              AssessmentState         `json:"state"`
	Reason                             string                  `json:"reason"`
	Diagnostics                        []JudgeDiagnostic       `json:"diagnostics"`
	Usage                              *models.UsageStats      `json:"usage"`
	Credits                            *float64                `json:"credits"`
}

type DomainAssessment struct {
	ID               string          `json:"id"`
	MinimumCases     int             `json:"minimum_cases"`
	MinimumAgreement float64         `json:"minimum_agreement"`
	DeclaredCases    int             `json:"declared_cases"`
	ObservedCases    int             `json:"observed_cases"`
	ExpectedChecks   int             `json:"expected_checks"`
	ObservedChecks   int             `json:"observed_checks"`
	Agreements       int             `json:"agreements"`
	Agreement        *float64        `json:"agreement"`
	State            AssessmentState `json:"state"`
}

type Report struct {
	Kind           string                  `json:"kind"`
	SchemaVersion  string                  `json:"schema_version"`
	AssessmentMode string                  `json:"assessment_mode,omitempty"`
	CreatedAt      time.Time               `json:"created_at"`
	State          AssessmentState         `json:"state"`
	Reason         string                  `json:"reason"`
	LabelsSHA256   string                  `json:"labels_sha256"`
	Review         ReviewReport            `json:"review"`
	Bindings       []Binding               `json:"bindings"`
	Requirements   []RequirementAssessment `json:"requirements"`
	Domains        []DomainAssessment      `json:"domains"`
	Calibration    CalibrationReport       `json:"calibration"`
	CachePolicy    string                  `json:"cache_policy"`
	Limitations    []string                `json:"limitations"`
}
