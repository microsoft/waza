package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
)

type qualificationNativeTool struct {
	Name        string          `json:"Name"`
	Description string          `json:"Description"`
	Parameters  json.RawMessage `json:"Parameters"`
}

type qualificationNativeRequest struct {
	Message   string                    `json:"Message"`
	Model     string                    `json:"Model"`
	Reasoning string                    `json:"Reasoning"`
	Tools     []qualificationNativeTool `json:"Tools"`
	Policy    []string                  `json:"Policy"`
}

func qualificationCaptureProjection(request *execution.ExecutionRequest) (qualificationDocument, error) {
	return qualificationCaptureProjectionBounded(request, qualificationDocumentLimit, nil)
}

func qualificationCaptureProjectionBounded(request *execution.ExecutionRequest, limit int, materializing func()) (qualificationDocument, error) {
	if request == nil || len(request.Tools) != 2 {
		return qualificationDocument{}, errors.New("qualification: native request required")
	}
	type toolIdentity struct {
		Name, Description string
		Parameters        any
	}
	original := struct {
		Message, Model, Reasoning string
		Tools                     []toolIdentity
		Policy                    []string
	}{request.Message, request.ModelID, request.ReasoningEffort, []toolIdentity{},
		[]string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"}}
	for _, tool := range request.Tools {
		original.Tools = append(original.Tools, toolIdentity{tool.Name, tool.Description, tool.Parameters})
	}
	budget := limit
	if limit <= 0 {
		return qualificationDocument{}, errors.New("qualification: explicit capture projection budget required")
	}
	if err := qualificationProjectionSize(reflect.ValueOf(original), &budget, 0); err != nil {
		return qualificationDocument{}, err
	}
	if materializing != nil {
		materializing()
	}
	identity, err := calibrationRequestIdentity(request)
	if err != nil {
		return qualificationDocument{}, err
	}
	projection := qualificationNativeRequest{
		Message: request.Message, Model: request.ModelID, Reasoning: request.ReasoningEffort,
		Tools:  []qualificationNativeTool{},
		Policy: []string{"custom:set_waza_grade_pass", "custom:set_waza_grade_fail"},
	}
	for _, tool := range request.Tools {
		parameters, err := qualificationMarshalBounded(tool.Parameters, limit, nil)
		if err != nil {
			return qualificationDocument{}, err
		}
		projection.Tools = append(projection.Tools, qualificationNativeTool{tool.Name, tool.Description, parameters})
	}
	document, err := qualificationSealBounded(projection, limit, nil)
	if err != nil || document.sha256() != identity {
		return qualificationDocument{}, errors.New("qualification: captured projection differs from frozen native identity")
	}
	return document, nil
}

const qualificationBoundary = "native-independent-two-callbacks-v1"

type qualificationExecutionViewWire struct {
	Kind            string `json:"kind"`
	Version         string `json:"version"`
	ScopedJobSHA256 string `json:"scoped_job_sha256"`
	RequestedModel  string `json:"requested_model"`
	BoundaryProfile string `json:"boundary_profile"`
}

// This restricted handle has no full-job, source, selector or label resolver.
type qualificationExecutionView struct {
	canonical string
	jobSHA    string
	model     string
}

func qualificationNewExecutionView(jobSHA, model string) (qualificationExecutionView, error) {
	if !qualificationDigest(jobSHA) || !qualificationIdentifier(model) {
		return qualificationExecutionView{}, errors.New("qualification: execution view digest/model required")
	}
	wire := qualificationExecutionViewWire{
		Kind: "waza.qualification-execution-view", Version: qualificationVersion,
		ScopedJobSHA256: jobSHA, RequestedModel: model, BoundaryProfile: qualificationBoundary,
	}
	document, err := qualificationSeal(wire)
	if err != nil {
		return qualificationExecutionView{}, err
	}
	return qualificationExecutionView{canonical: document.canonical, jobSHA: jobSHA, model: model}, nil
}

func (view qualificationExecutionView) bytes() []byte           { return []byte(view.canonical) }
func (view qualificationExecutionView) scopedJobSHA256() string { return view.jobSHA }
func (view qualificationExecutionView) requestedModel() string  { return view.model }
func (view qualificationExecutionView) boundaryProfile() string {
	if view.canonical == "" {
		return ""
	}
	return qualificationBoundary
}

// Only used as a capture-only native grader executor: no SDK or lifecycle.
type qualificationCapture struct {
	document qualificationDocument
	calls    int
}

func (capture *qualificationCapture) Execute(_ context.Context, request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
	capture.calls++
	document, err := qualificationCaptureProjection(request)
	if err != nil {
		return nil, err
	}
	capture.document = document
	return nil, errCalibrationCapture
}

type qualificationAuthority struct {
	AuthorityID               string `json:"authority_id"`
	VerificationBindingSHA256 string `json:"verification_binding_sha256"`
}

type qualificationCurrentnessPolicy struct {
	Kind                  string                   `json:"kind"`
	Version               string                   `json:"version"`
	Authorities           []qualificationAuthority `json:"authorities"`
	MaxReceiptAgeSeconds  uint64                   `json:"max_receipt_age_seconds"`
	MaxValiditySeconds    uint64                   `json:"max_validity_seconds"`
	MaxClockSkewSeconds   uint64                   `json:"max_clock_skew_seconds"`
	CheckTimeoutSeconds   uint64                   `json:"check_timeout_seconds"`
	VerifierBindingSHA256 string                   `json:"verifier_binding_sha256"`
}

type qualificationBackendPolicy struct {
	Kind                     string `json:"kind"`
	Version                  string `json:"version"`
	BackendID                string `json:"backend_id"`
	DurabilityContractSHA256 string `json:"durability_contract_sha256"`
	ReservationPolicySHA256  string `json:"reservation_policy_sha256"`
	ReservationAuthorityID   string `json:"reservation_authority_id"`
	Namespace                string `json:"namespace"`
	CallbackTimeoutSeconds   uint64 `json:"callback_timeout_seconds"`
}

// Shape/range admission only, never authority verification or fsync evidence.
func qualificationParseCurrentnessPolicy(data []byte) (qualificationDocument, error) {
	policy, err := qualificationDecode[qualificationCurrentnessPolicy](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	if policy.Kind != "waza.caller-currentness-profile" || policy.Version != qualificationVersion ||
		len(policy.Authorities) == 0 || len(policy.Authorities) > qualificationSourceLimit ||
		policy.MaxReceiptAgeSeconds == 0 || policy.MaxReceiptAgeSeconds > 3600 ||
		policy.MaxValiditySeconds == 0 || policy.MaxValiditySeconds > 3600 ||
		policy.MaxClockSkewSeconds > 300 || policy.CheckTimeoutSeconds == 0 || policy.CheckTimeoutSeconds > 120 ||
		!qualificationDigest(policy.VerifierBindingSHA256) {
		return qualificationDocument{}, errors.New("qualification: currentness policy bounds")
	}
	seen := map[string]bool{}
	for _, authority := range policy.Authorities {
		if !qualificationIdentifier(authority.AuthorityID) || !qualificationDigest(authority.VerificationBindingSHA256) || seen[authority.AuthorityID] {
			return qualificationDocument{}, errors.New("qualification: duplicate/invalid caller authority")
		}
		seen[authority.AuthorityID] = true
	}
	return qualificationSeal(policy)
}

func qualificationParseBackendPolicy(data []byte) (qualificationDocument, error) {
	policy, err := qualificationDecode[qualificationBackendPolicy](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	if policy.Kind != "waza.caller-journal-profile" || policy.Version != qualificationVersion ||
		!qualificationIdentifier(policy.BackendID) || !qualificationIdentifier(policy.ReservationAuthorityID) ||
		!qualificationIdentifier(policy.Namespace) || !qualificationDigest(policy.DurabilityContractSHA256) ||
		!qualificationDigest(policy.ReservationPolicySHA256) || policy.CallbackTimeoutSeconds == 0 || policy.CallbackTimeoutSeconds > 120 {
		return qualificationDocument{}, errors.New("qualification: backend policy bounds")
	}
	return qualificationSeal(policy)
}

type qualificationAssociation struct {
	Kind                     string                  `json:"kind"`
	Version                  string                  `json:"version"`
	ContractSHA256           string                  `json:"contract_sha256"`
	CID                      string                  `json:"cid"`
	ArmID                    string                  `json:"arm_id"`
	ArmSourceInventorySHA256 string                  `json:"arm_source_inventory_sha256"`
	Selectors                []qualificationSelector `json:"selectors"`
}

type qualificationArmTask struct {
	TaskID                string                  `json:"task_id"`
	SourceID              string                  `json:"source_id"`
	TaskSourceSHA256      string                  `json:"task_source_sha256"`
	TaskDeclarationSHA256 string                  `json:"task_declaration_sha256"`
	Selectors             []qualificationSelector `json:"selectors"`
}

type qualificationArmInventory struct {
	EvalSourceID          string                 `json:"eval_source_id"`
	EvalSourceSHA256      string                 `json:"eval_source_sha256"`
	ReferenceSourceID     string                 `json:"reference_source_id"`
	ReferenceSourceSHA256 string                 `json:"reference_source_sha256"`
	ReviewSourceID        string                 `json:"review_source_id"`
	ReviewSourceSHA256    string                 `json:"review_source_sha256"`
	ResolvedSpecSHA256    string                 `json:"resolved_spec_sha256"`
	Tasks                 []qualificationArmTask `json:"tasks"`
}

type qualificationArm struct {
	Kind            string                    `json:"kind"`
	Version         string                    `json:"version"`
	ContractSHA256  string                    `json:"contract_sha256"`
	CID             string                    `json:"cid"`
	ArmID           string                    `json:"arm_id"`
	SourceInventory qualificationArmInventory `json:"source_inventory"`
}

type qualificationSourceIdentity struct {
	ID         string                 `json:"id"`
	Role       string                 `json:"role"`
	TaskID     string                 `json:"task_id"`
	Selector   *qualificationSelector `json:"selector"`
	ByteLength uint64                 `json:"byte_length"`
	SHA256     string                 `json:"sha256"`
}

type qualificationAcceptance struct {
	SourceID              string `json:"source_id"`
	AcceptCurrentDecision bool   `json:"accept_current_decision"`
}

type qualificationMechanicalObservation struct {
	Selector    qualificationSelector `json:"selector"`
	Observation json.RawMessage       `json:"observation"`
}

type qualificationInputs struct {
	Kind                   string                               `json:"kind"`
	Version                string                               `json:"version"`
	Association            qualificationAssociation             `json:"association"`
	ArmInput               qualificationArm                     `json:"arm_input"`
	Sources                []qualificationSourceIdentity        `json:"sources"`
	ResolvedSpec           json.RawMessage                      `json:"resolved_spec"`
	Tasks                  []qualificationNativeTask            `json:"tasks"`
	ReferenceDocument      json.RawMessage                      `json:"reference_document"`
	SuppliedReview         json.RawMessage                      `json:"supplied_review"`
	ReviewAcceptance       qualificationAcceptance              `json:"review_acceptance"`
	AdmissionTime          string                               `json:"admission_time"`
	MechanicalObservations []qualificationMechanicalObservation `json:"mechanical_observations"`
	CurrentnessProfile     qualificationCurrentnessPolicy       `json:"currentness_profile"`
	BackendProfile         qualificationBackendPolicy           `json:"backend_profile"`
}

func qualificationSelectorKey(selector qualificationSelector) string {
	// JSON preserves all fields without delimiter collision; ordering itself
	// uses the explicit tuple below, not this equality key.
	data, err := json.Marshal(selector)
	if err != nil {
		return ""
	}
	return string(data)
}

func qualificationCompareSelectors(a, b qualificationSelector) int {
	for _, pair := range [][2]string{{a.ArmID, b.ArmID}, {a.TaskID, b.TaskID}, {a.RequirementID, b.RequirementID}, {a.Check.Scope, b.Check.Scope}} {
		if result := strings.Compare(pair[0], pair[1]); result != 0 {
			return result
		}
	}
	if a.Check.AfterTurn < b.Check.AfterTurn {
		return -1
	}
	if a.Check.AfterTurn > b.Check.AfterTurn {
		return 1
	}
	if result := strings.Compare(a.Check.Grader, b.Check.Grader); result != 0 {
		return result
	}
	return strings.Compare(a.CaseID, b.CaseID)
}

func qualificationBuildInputs(sources qualificationSources, associationData, armData []byte,
	acceptance ReviewSourceAcceptance, now time.Time, currentnessData, backendData []byte,
) (qualificationDocument, error) {
	if sources.native.canonical == "" {
		return qualificationDocument{}, errors.New("qualification: actual rooted eval-selected acquisition required")
	}
	association, err := qualificationDecode[qualificationAssociation](associationData, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	arm, err := qualificationDecode[qualificationArm](armData, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	if association.Kind != "waza.qualification-association" || arm.Kind != "waza.qualification-arm-input" ||
		association.Version != qualificationVersion || arm.Version != qualificationVersion ||
		!qualificationDigest(association.ContractSHA256) || !qualificationIdentifier(association.CID) ||
		!qualificationIdentifier(association.ArmID) || association.ContractSHA256 != arm.ContractSHA256 ||
		association.CID != arm.CID || association.ArmID != arm.ArmID || now.IsZero() {
		return qualificationDocument{}, errors.New("qualification: explicit homogeneous arm/time required")
	}
	currentness, err := qualificationParseCurrentnessPolicy(currentnessData)
	if err != nil {
		return qualificationDocument{}, err
	}
	backend, err := qualificationParseBackendPolicy(backendData)
	if err != nil {
		return qualificationDocument{}, err
	}
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	native, err := qualificationDecode[qualificationNativeSelection](sources.native.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	input := qualificationInputs{
		Kind: "waza.qualification-inputs", Version: qualificationVersion, Association: association, ArmInput: arm,
		Sources: []qualificationSourceIdentity{}, ResolvedSpec: native.ResolvedSpec, Tasks: native.Tasks,
		ReviewAcceptance: qualificationAcceptance(acceptance),
		AdmissionTime:    now.UTC().Format(time.RFC3339Nano), MechanicalObservations: []qualificationMechanicalObservation{},
	}
	input.CurrentnessProfile, err = qualificationDecode[qualificationCurrentnessPolicy](currentness.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	input.BackendProfile, err = qualificationDecode[qualificationBackendPolicy](backend.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	var labels *ReferenceSet
	var review SuppliedReview
	taskSources := map[string]qualificationSource{}
	executableSHA := ""
	for _, source := range set.Sources {
		if source.Selector != nil && source.Selector.ArmID != association.ArmID {
			return qualificationDocument{}, errors.New("qualification: actual source inventory belongs to another arm")
		}
		input.Sources = append(input.Sources, qualificationSourceIdentity{source.ID, source.Role, source.TaskID, source.Selector, source.ByteLength, source.SHA256})
		switch source.Role {
		case "task":
			taskSources[source.TaskID] = source
		case "implementation_executable":
			executableSHA = source.SHA256
		case "eval":
			if source.ID != arm.SourceInventory.EvalSourceID || source.SHA256 != arm.SourceInventory.EvalSourceSHA256 {
				return qualificationDocument{}, errors.New("qualification: actual eval source binding")
			}
		case "review", "references":
			data, err := qualificationSourceBytes(source)
			if err != nil {
				return qualificationDocument{}, err
			}
			if source.Role == "review" {
				if source.ID != arm.SourceInventory.ReviewSourceID || source.SHA256 != arm.SourceInventory.ReviewSourceSHA256 {
					return qualificationDocument{}, errors.New("qualification: original review byte binding")
				}
				review, err = ParseReview(data)
				if err != nil {
					return qualificationDocument{}, err
				}
				input.SuppliedReview, err = json.Marshal(review)
				if err != nil {
					return qualificationDocument{}, err
				}
			} else {
				if source.ID != arm.SourceInventory.ReferenceSourceID || source.SHA256 != arm.SourceInventory.ReferenceSourceSHA256 {
					return qualificationDocument{}, errors.New("qualification: original label byte binding")
				}
				labels, err = ParseReferences(data)
				if err != nil {
					return qualificationDocument{}, err
				}
				document, err := labels.Document()
				if err != nil {
					return qualificationDocument{}, err
				}
				input.ReferenceDocument, err = json.Marshal(document)
				if err != nil {
					return qualificationDocument{}, err
				}
			}
		}
	}
	subject, err := labels.Subject()
	if err != nil {
		return qualificationDocument{}, err
	}
	eligible, err := CheckAuthorReview(subject, review, acceptance, now)
	if err != nil || !eligible.Eligible {
		return qualificationDocument{}, errors.New("qualification: current supplied review not eligible")
	}
	resolved, err := qualificationCanonical(native.ResolvedSpec)
	if err != nil || byteSHA256(resolved) != arm.SourceInventory.ResolvedSpecSHA256 {
		return qualificationDocument{}, errors.New("qualification: actual native spec binding")
	}
	referenceDocument, err := labels.Document()
	if err != nil {
		return qualificationDocument{}, err
	}
	if referenceDocument.EvalSourceSHA256 != arm.SourceInventory.EvalSourceSHA256 ||
		referenceDocument.EvalResolvedConfigSHA256 != arm.SourceInventory.ResolvedSpecSHA256 ||
		referenceDocument.ImplementationExecutableSHA256 == nil ||
		*referenceDocument.ImplementationExecutableSHA256 != executableSHA {
		return qualificationDocument{}, errors.New("qualification: source-bound reference inventory differs")
	}
	expected, err := qualificationValidateSelectors(association, arm, native, taskSources, labels)
	if err != nil {
		return qualificationDocument{}, err
	}
	inventory, err := qualificationSeal(arm.SourceInventory)
	if err != nil || inventory.sha256() != association.ArmSourceInventorySHA256 || len(expected) == 0 {
		return qualificationDocument{}, errors.New("qualification: actual arm inventory binding")
	}
	return qualificationSeal(input)
}

func qualificationValidateSelectors(association qualificationAssociation, arm qualificationArm, native qualificationNativeSelection,
	taskSources map[string]qualificationSource, references *ReferenceSet,
) (map[string]bool, error) {
	if len(arm.SourceInventory.Tasks) != len(native.Tasks) || len(association.Selectors) == 0 || len(association.Selectors) > 4096 {
		return nil, errors.New("qualification: task/selector coverage")
	}
	document, err := references.Document()
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{}
	for i, nativeTask := range native.Tasks {
		binding := arm.SourceInventory.Tasks[i]
		source := taskSources[nativeTask.TaskID]
		canonical, err := qualificationCanonical(nativeTask.Declaration)
		if err != nil || binding.TaskID != nativeTask.TaskID || binding.SourceID != source.ID ||
			binding.TaskSourceSHA256 != source.SHA256 || binding.TaskDeclarationSHA256 != byteSHA256(canonical) {
			return nil, errors.New("qualification: native task/source identity")
		}
		var declaration struct {
			Requirements []models.Requirement `json:"requirements"`
		}
		if err := json.Unmarshal(nativeTask.Declaration, &declaration); err != nil {
			return nil, err
		}
		actual := map[string]bool{}
		for _, candidate := range document.Cases {
			if candidate.TaskID != nativeTask.TaskID {
				continue
			}
			if candidate.TaskDeclarationSHA256 != binding.TaskDeclarationSHA256 {
				return nil, errors.New("qualification: reference task declaration differs")
			}
			for _, check := range candidate.Checks {
				found := false
				for _, requirement := range declaration.Requirements {
					if requirement.ID == check.RequirementID && slices.Contains(requirement.Checks, check.Check) {
						found = true
					}
				}
				if !found {
					return nil, errors.New("qualification: reference selector absent from actual native requirement")
				}
				selector := qualificationSelector{association.ArmID, nativeTask.TaskID, check.RequirementID,
					qualificationCheck{check.Check.Scope, check.Check.AfterTurn, check.Check.Grader}, candidate.ID}
				actual[qualificationSelectorKey(selector)] = true
			}
		}
		if len(binding.Selectors) != len(actual) {
			return nil, errors.New("qualification: native task selector coverage")
		}
		for j, selector := range binding.Selectors {
			key := qualificationSelectorKey(selector)
			if !selector.valid() || selector.ArmID != association.ArmID || selector.TaskID != nativeTask.TaskID ||
				!actual[key] || expected[key] || (j > 0 && qualificationCompareSelectors(binding.Selectors[j-1], selector) >= 0) {
				return nil, errors.New("qualification: duplicate/cross-arm/unselected scoped selector")
			}
			expected[key] = true
		}
	}
	if len(expected) != len(association.Selectors) {
		return nil, errors.New("qualification: association selector bijection")
	}
	for i, selector := range association.Selectors {
		if !expected[qualificationSelectorKey(selector)] ||
			(i > 0 && qualificationCompareSelectors(association.Selectors[i-1], selector) >= 0) {
			return nil, errors.New("qualification: association selector order or membership")
		}
	}
	return expected, nil
}

func qualificationOriginalReport(artifact qualificationArtifact) (*Report, error) {
	return qualificationOriginalReportBounded(artifact, qualificationDocumentLimit, nil)
}

func qualificationOriginalReportBounded(artifact qualificationArtifact, limit uint64, materializing func()) (*Report, error) {
	if artifact.Role != "actual_calibration_report" {
		return nil, errors.New("qualification: original standalone report required")
	}
	// Already validated and bounded; decode exact originals, never RawMessage
	// reserialization, for unchanged explicitly selected 1.1 admission.
	original, err := qualificationValidateArtifact(artifact, limit, materializing)
	if err != nil {
		return nil, err
	}
	return ParseCalibratedReport(original)
}

func qualificationParseInputs(data []byte, sources qualificationSources) (qualificationDocument, error) {
	input, err := qualificationDecode[qualificationInputs](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationDocument{}, err
	}
	if input.Kind != "waza.qualification-inputs" || input.Version != qualificationVersion ||
		len(input.MechanicalObservations) != 0 {
		return qualificationDocument{}, errors.New("qualification: unsupported input/mechanical integration profile")
	}
	now, err := time.Parse(time.RFC3339Nano, input.AdmissionTime)
	if err != nil || now.IsZero() || now.UTC().Format(time.RFC3339Nano) != input.AdmissionTime {
		return qualificationDocument{}, errors.New("qualification: canonical UTC admission time required")
	}
	association, err := json.Marshal(input.Association)
	if err != nil {
		return qualificationDocument{}, err
	}
	arm, err := json.Marshal(input.ArmInput)
	if err != nil {
		return qualificationDocument{}, err
	}
	currentness, err := json.Marshal(input.CurrentnessProfile)
	if err != nil {
		return qualificationDocument{}, err
	}
	backend, err := json.Marshal(input.BackendProfile)
	if err != nil {
		return qualificationDocument{}, err
	}
	expected, err := qualificationBuildInputs(sources, association, arm,
		ReviewSourceAcceptance{input.ReviewAcceptance.SourceID, input.ReviewAcceptance.AcceptCurrentDecision}, now, currentness, backend)
	if err != nil {
		return qualificationDocument{}, err
	}
	actual, err := qualificationCanonical(data)
	if err != nil || string(actual) != expected.canonical {
		return qualificationDocument{}, errors.New("qualification: inputs differ from actual acquired sources")
	}
	return expected, nil
}
