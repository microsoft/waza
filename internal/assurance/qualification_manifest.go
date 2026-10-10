package assurance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const qualificationProfile = "waza.durable-qualification.v1"

var qualificationExecutionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLocalSchema("grader-assurance-1.1.schema.json#/$defs/execution", map[string]string{
		"evidence-manifest-1.0.schema.json": schemas.EvidenceManifestSchemaJSON,
		"grader-reference-1.0.schema.json":  schemas.GraderReferenceSchemaJSON,
		"grader-assurance-1.0.schema.json":  schemas.GraderAssuranceSchemaJSON,
		"grader-assurance-1.1.schema.json":  schemas.CalibratedGraderAssuranceSchemaJSON,
	})
})

var qualificationNativeResultSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLocalSchema("grader-assurance-1.0.schema.json#/$defs/observation/properties/result", map[string]string{
		"evidence-manifest-1.0.schema.json": schemas.EvidenceManifestSchemaJSON,
		"grader-reference-1.0.schema.json":  schemas.GraderReferenceSchemaJSON,
		"grader-assurance-1.0.schema.json":  schemas.GraderAssuranceSchemaJSON,
	})
})

type qualificationJobWire struct {
	Kind                   string                     `json:"kind"`
	Version                string                     `json:"version"`
	Ordinal                uint64                     `json:"ordinal"`
	ContractSHA256         string                     `json:"contract_sha256"`
	CID                    string                     `json:"cid"`
	Selector               qualificationSelector      `json:"selector"`
	DomainID               string                     `json:"domain_id"`
	Classification         string                     `json:"classification"`
	ExpectedPassed         bool                       `json:"expected_passed"`
	TaskDeclarationSHA256  string                     `json:"task_declaration_sha256"`
	GraderBindingSHA256    string                     `json:"grader_binding_sha256"`
	AuthoredSourceID       string                     `json:"authored_source_id"`
	AuthoredDocumentSHA256 string                     `json:"authored_document_sha256"`
	AuthoredProfileSHA256  string                     `json:"authored_profile_sha256"`
	OutputSHA256           string                     `json:"output_sha256"`
	RubricSourceID         string                     `json:"rubric_source_id"`
	RubricContentSHA256    string                     `json:"rubric_content_sha256"`
	NativeRequest          qualificationNativeRequest `json:"native_request"`
	NativeRequestSHA256    string                     `json:"native_request_sha256"`
	JudgeIdentitySHA256    string                     `json:"judge_identity_sha256"`
	StimulusSHA256         string                     `json:"stimulus_sha256"`
	RequestedModel         string                     `json:"requested_model"`
	EffectiveReasoning     string                     `json:"effective_reasoning"`
	Protocol               string                     `json:"protocol"`
	BoundaryProfile        string                     `json:"boundary_profile"`
}

type qualificationManifestWire struct {
	Kind         string                 `json:"kind"`
	Version      string                 `json:"version"`
	Profile      string                 `json:"profile"`
	InvocationID string                 `json:"invocation_id"`
	InputsSHA256 string                 `json:"inputs_sha256"`
	Inputs       qualificationInputs    `json:"inputs"`
	Plan         CalibrationPlan        `json:"plan"`
	Jobs         []qualificationJobWire `json:"jobs"`
}

type qualificationAdmittedInputContext struct {
	sources qualificationSources
	input   qualificationDocument
	jobs    qualificationDocument
}
type qualificationManifest struct {
	document qualificationDocument
	context  qualificationAdmittedInputContext
}
type qualificationEvent struct{ document qualificationDocument }
type qualificationTerminal struct{ document qualificationDocument }
type qualificationRecord struct{ document qualificationDocument }

type qualificationCurrentnessRequest struct {
	Kind                     string  `json:"kind"`
	Version                  string  `json:"version"`
	InvocationID             string  `json:"invocation_id"`
	ContractSHA256           string  `json:"contract_sha256"`
	CID                      string  `json:"cid"`
	ArmID                    string  `json:"arm_id"`
	ManifestSHA256           string  `json:"manifest_sha256"`
	InputsSHA256             string  `json:"inputs_sha256"`
	CurrentnessProfileSHA256 string  `json:"currentness_profile_sha256"`
	Stage                    string  `json:"stage"`
	JobSHA256                *string `json:"job_sha256"`
	Ordinal                  *uint64 `json:"ordinal"`
	Challenge                string  `json:"challenge"`
}
type qualificationAdmissionWire struct {
	Allowed    bool   `json:"allowed"`
	ReasonCode string `json:"reason_code"`
}
type qualificationCompletionWire struct {
	State                   string  `json:"state"`
	ReasonCode              string  `json:"reason_code"`
	CompletedJobs           uint64  `json:"completed_jobs"`
	ActualReportSHA256      *string `json:"actual_report_sha256"`
	ArtifactInventorySHA256 *string `json:"artifact_inventory_sha256"`
}
type qualificationEventWire struct {
	Kind                      string                                  `json:"kind"`
	Version                   string                                  `json:"version"`
	InvocationID              string                                  `json:"invocation_id"`
	ManifestSHA256            string                                  `json:"manifest_sha256"`
	ContractSHA256            string                                  `json:"contract_sha256"`
	CID                       string                                  `json:"cid"`
	ArmID                     string                                  `json:"arm_id"`
	Sequence                  uint64                                  `json:"sequence"`
	PreviousSHA256            string                                  `json:"previous_sha256"`
	Type                      string                                  `json:"type"`
	JobSHA256                 *string                                 `json:"job_sha256"`
	Ordinal                   *uint64                                 `json:"ordinal"`
	Selector                  *qualificationSelector                  `json:"selector"`
	Admission                 *qualificationAdmissionWire             `json:"admission"`
	CurrentnessRequest        *qualificationCurrentnessRequest        `json:"currentness_request"`
	CurrentnessAcknowledgment *qualificationCurrentnessAcknowledgment `json:"currentness_acknowledgment"`
	PayloadSHA256             *string                                 `json:"payload_sha256"`
	Completion                *qualificationCompletionWire            `json:"completion"`
}

// Native nested values retain their frozen optional-field rules and are
// separately validated; RawMessage changes neither their wire nor number tokens.
type qualificationTerminalWire struct {
	Kind                  string                `json:"kind"`
	Version               string                `json:"version"`
	InvocationID          string                `json:"invocation_id"`
	ManifestSHA256        string                `json:"manifest_sha256"`
	ContractSHA256        string                `json:"contract_sha256"`
	CID                   string                `json:"cid"`
	Selector              qualificationSelector `json:"selector"`
	JobSHA256             string                `json:"job_sha256"`
	Ordinal               uint64                `json:"ordinal"`
	Execution             json.RawMessage       `json:"execution"`
	NativeResult          *json.RawMessage      `json:"native_result"`
	FactoryReturnedEngine bool                  `json:"factory_returned_engine"`
	InitializeAttempted   bool                  `json:"initialize_attempted"`
	ShutdownAttempted     bool                  `json:"shutdown_attempted"`
	CleanupComplete       bool                  `json:"cleanup_complete"`
	AccountingFinalized   bool                  `json:"accounting_finalized"`
	ObservationState      string                `json:"observation_state"`
	Agreement             *bool                 `json:"agreement"`
	FailureCodes          []string              `json:"failure_codes"`
}

func qualificationAdmitInputContext(ctx context.Context, data []byte, sources qualificationSources) (qualificationAdmittedInputContext, error) {
	admitted, err := qualificationParseInputs(data, sources)
	if err != nil {
		return qualificationAdmittedInputContext{}, err
	}
	input, err := qualificationDecode[qualificationInputs](admitted.bytes(), qualificationDocumentLimit)
	if err != nil {
		return qualificationAdmittedInputContext{}, err
	}
	jobs, err := qualificationRecomputeJobs(ctx, sources, input)
	if err != nil {
		return qualificationAdmittedInputContext{}, err
	}
	return qualificationAdmittedInputContext{sources: sources, input: admitted, jobs: jobs}, nil
}

func qualificationRecomputeJobs(ctx context.Context, sources qualificationSources, input qualificationInputs) (result qualificationDocument, returnErr error) {
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	if err != nil {
		return result, err
	}
	store, err := newCalibrationRubricStore()
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, store.close()) }()
	var spec *models.EvalSpec
	var references *ReferenceSet
	tasks := map[string]*models.TestCase{}
	scoped := map[string]qualificationSource{}
	for _, source := range set.Sources {
		if source.Selector != nil {
			scoped[source.Role+":"+qualificationSelectorKey(*source.Selector)] = source
		}
		if source.Role != "eval" && source.Role != "task" && source.Role != "references" {
			continue
		}
		data, err := qualificationSourceBytes(source)
		if err != nil {
			return result, err
		}
		if source.Role == "references" {
			references, err = ParseReferences(data)
		} else {
			if err := qualificationNoIncludes(data); err != nil {
				return result, err
			}
			name, writeErr := store.write(data)
			if writeErr != nil {
				return result, writeErr
			}
			if source.Role == "eval" {
				spec, err = models.LoadEvalSpecOffline(name)
			} else {
				var task *models.TestCase
				task, err = models.LoadTestCaseOffline(name)
				if err == nil {
					tasks[source.TaskID] = task
				}
			}
		}
		if err != nil {
			return result, errors.New("qualification: retained native source reload failed")
		}
	}
	document, err := references.Document()
	if err != nil || document.Calibration == nil || len(input.MechanicalObservations) != 0 {
		return result, errors.New("qualification: finite calibrated source plan required")
	}
	plan := *document.Calibration
	if plan.Protocol != AgreementProtocol || !qualificationIdentifier(plan.Model) ||
		plan.MaxJudgeExecutions < 1 || plan.MaxJudgeExecutions > 4096 {
		return result, errors.New("qualification: source plan unsupported")
	}
	// Existing coverage admission is source-bound, not a fabricated review.
	coverage := map[string]*Coverage{}
	for _, candidate := range document.Cases {
		for _, check := range candidate.Checks {
			key := candidate.Domain + ":" + check.RequirementID + ":" + check.Check.Scope + ":" + check.Check.Grader
			if coverage[key] == nil {
				coverage[key] = &Coverage{}
			}
			if check.ExpectedPassed == nil {
				return result, errors.New("qualification: explicit expected label required")
			}
			addCoverage(coverage[key], candidate.Classification, *check.ExpectedPassed)
		}
	}
	for _, item := range coverage {
		if !calibrationCoverageComplete(*item) {
			return result, errors.New("qualification: independent source coverage insufficient")
		}
	}
	caseMap := map[string]ReferenceCase{}
	for _, candidate := range document.Cases {
		caseMap[candidate.ID] = candidate
	}
	jobs := []qualificationJobWire{}
	seen := map[string]bool{}
	domains := map[string]string{}
	remaining := qualificationDocumentLimit - 2
	for ordinal, selector := range input.Association.Selectors {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate := caseMap[selector.CaseID]
		var check *ReferenceCheck
		for _, item := range candidate.Checks {
			if item.RequirementID == selector.RequirementID &&
				(qualificationCheck{item.Check.Scope, item.Check.AfterTurn, item.Check.Grader}) == selector.Check {
				copy := item
				check = &copy
			}
		}
		if check == nil || check.ExpectedPassed == nil || tasks[selector.TaskID] == nil {
			return result, errors.New("qualification: actual selected check missing")
		}
		resolved, err := preflight.ResolveGrader(check.Check, tasks[selector.TaskID], spec)
		if err != nil || resolved.Config == nil {
			return result, errors.New("qualification: initial profile requires selected eval/task config")
		}
		config := *resolved.Config
		parameters, ok := config.Parameters.(models.PromptGraderParameters)
		if !ok || config.Kind != models.GraderKindPrompt || parameters.ContinueSession ||
			(parameters.Mode != "" && parameters.Mode != models.PromptGraderModeIndependent) || !calibrationLocalRubric(parameters.Rubric) {
			return result, errors.New("qualification: unsupported selected judge configuration")
		}
		key := qualificationSelectorKey(selector)
		authoredSource, hasAuthored := scoped["authored_input:"+key]
		rubricSource, hasRubric := scoped["rubric:"+key]
		if !hasAuthored || !hasRubric || check.RubricContentSHA256 == nil || rubricSource.SHA256 != *check.RubricContentSHA256 {
			return result, errors.New("qualification: retained original authored/rubric obligation missing")
		}
		authoredBytes, err := qualificationSourceBytes(authoredSource)
		if err != nil {
			return result, err
		}
		authored, err := ParseAuthoredOutput(authoredBytes)
		if err != nil || authored.Output() == nil {
			return result, errors.New("qualification: finite original output unavailable")
		}
		rubric, err := qualificationSourceBytes(rubricSource)
		if err != nil {
			return result, err
		}
		parsed, err := graders.ParseRubric(rubric)
		present, presenceErr := calibrationRubricGoldensPresent(rubric)
		if err != nil || presenceErr != nil || parsed.Goldens != nil || present {
			return result, errors.New("qualification: rubric profile unsupported")
		}
		job := calibrationJob{config: config, task: tasks[selector.TaskID], output: *authored.Output(),
			rubric: rubric, rubricSHA: rubricSource.SHA256, judgeModel: spec.Config.JudgeModel, judgeEffort: spec.Config.JudgeReasoningEffort}
		if job.judgeModel == "" {
			job.judgeModel = plan.Model
		}
		capture := &qualificationCapture{}
		_, err = runCalibrationGrade(ctx, job, store, capture)
		if !errors.Is(err, errCalibrationCapture) || capture.calls != 1 {
			return result, errors.New("qualification: actual capture unavailable")
		}
		native, err := qualificationDecode[qualificationNativeRequest](capture.document.bytes(), qualificationDocumentLimit)
		if err != nil || native.Model != plan.Model {
			return result, errors.New("qualification: captured model differs from plan")
		}
		domainJob := job
		domainJob.task, domainJob.output = &models.TestCase{}, ""
		domainCapture := &qualificationCapture{}
		_, err = runCalibrationGrade(ctx, domainJob, store, domainCapture)
		if !errors.Is(err, errCalibrationCapture) || domainCapture.calls != 1 {
			return result, errors.New("qualification: domain capture unavailable")
		}
		domain, err := qualificationSeal(struct{ Rubric, NativeRequest, Protocol string }{rubricSource.SHA256, domainCapture.document.sha256(), plan.Protocol})
		if err != nil {
			return result, err
		}
		stimulus, err := qualificationSeal(struct{ Request, Output, Rubric, Protocol string }{capture.document.sha256(), job.output, rubricSource.SHA256, plan.Protocol})
		if err != nil || seen[stimulus.sha256()] || (domains[candidate.Domain] != "" && domains[candidate.Domain] != domain.sha256()) {
			return result, errors.New("qualification: duplicate stimulus or heterogeneous domain")
		}
		seen[stimulus.sha256()], domains[candidate.Domain] = true, domain.sha256()
		wire := qualificationJobWire{
			Kind: "waza.qualification-job", Version: qualificationVersion, Ordinal: uint64(ordinal),
			ContractSHA256: input.Association.ContractSHA256, CID: input.Association.CID, Selector: selector,
			DomainID: candidate.Domain, Classification: string(candidate.Classification), ExpectedPassed: *check.ExpectedPassed,
			TaskDeclarationSHA256: candidate.TaskDeclarationSHA256, GraderBindingSHA256: check.GraderDeclarationSHA256,
			AuthoredSourceID: authoredSource.ID, AuthoredDocumentSHA256: authoredSource.SHA256,
			AuthoredProfileSHA256: authored.Manifest().SHA256, OutputSHA256: byteSHA256([]byte(job.output)),
			RubricSourceID: rubricSource.ID, RubricContentSHA256: rubricSource.SHA256,
			NativeRequest: native, NativeRequestSHA256: capture.document.sha256(), JudgeIdentitySHA256: domain.sha256(),
			StimulusSHA256: stimulus.sha256(), RequestedModel: native.Model, EffectiveReasoning: native.Reasoning,
			Protocol: plan.Protocol, BoundaryProfile: qualificationBoundary,
		}
		if ordinal > 0 {
			remaining--
		}
		before := remaining
		if err := qualificationProjectionSize(reflect.ValueOf(wire), &remaining, 0); err != nil {
			return result, err
		}
		if before == remaining {
			return result, errors.New("qualification: empty job")
		}
		jobs = append(jobs, wire)
	}
	if len(jobs) == 0 || len(jobs) > plan.MaxJudgeExecutions {
		return result, errors.New("qualification: source job ceiling exceeded")
	}
	return qualificationSeal(jobs)
}

func qualificationParseManifest(data []byte, admitted qualificationAdmittedInputContext) (qualificationManifest, error) {
	return qualificationParseManifestBounded(data, admitted, qualificationDocumentLimit, qualificationTotalLimit, nil, nil)
}
func qualificationParseManifestBounded(data []byte, admitted qualificationAdmittedInputContext, documentLimit int, total uint64, role func(string) uint64, materializing func()) (qualificationManifest, error) {
	manifest, _, err := qualificationParseManifestProjection(data, admitted, documentLimit, total, role, materializing)
	return manifest, err
}
func qualificationParseManifestProjection(data []byte, admitted qualificationAdmittedInputContext, documentLimit int, total uint64, role func(string) uint64, materializing func()) (qualificationManifest, qualificationManifestWire, error) {
	if err := qualificationProtocolBlobPreflight(data, documentLimit, total, role); err != nil {
		return qualificationManifest{}, qualificationManifestWire{}, err
	}
	if materializing != nil {
		materializing()
	}
	if admitted.input.canonical == "" || admitted.jobs.canonical == "" || admitted.sources.native.canonical == "" {
		return qualificationManifest{}, qualificationManifestWire{}, errors.New("qualification: source-admitted context required")
	}
	wire, err := qualificationDecode[qualificationManifestWire](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationManifest{}, qualificationManifestWire{}, err
	}
	input, err := qualificationSeal(wire.Inputs)
	if err != nil || input.canonical != admitted.input.canonical || wire.InputsSHA256 != admitted.input.sha256() ||
		wire.Kind != "waza.qualification-manifest" || wire.Version != qualificationVersion ||
		wire.Profile != qualificationProfile || !qualificationIdentifier(wire.InvocationID) {
		return qualificationManifest{}, qualificationManifestWire{}, errors.New("qualification: source manifest identity")
	}
	jobs, err := qualificationSeal(wire.Jobs)
	if err != nil || jobs.canonical != admitted.jobs.canonical {
		return qualificationManifest{}, qualificationManifestWire{}, errors.New("qualification: manifest jobs differ from actual source capture")
	}
	labels, err := ParseReferences(mustSource(admitted.sources, "references"))
	if err != nil {
		return qualificationManifest{}, qualificationManifestWire{}, err
	}
	reference, err := labels.Document()
	if err != nil || reference.Calibration == nil || *reference.Calibration != wire.Plan {
		return qualificationManifest{}, qualificationManifestWire{}, errors.New("qualification: manifest source plan mismatch")
	}
	document, err := qualificationSeal(wire)
	return qualificationManifest{document: document, context: admitted}, wire, err
}

func mustSource(sources qualificationSources, role string) []byte {
	set, err := qualificationDecode[qualificationSourceSet](sources.document.bytes(), qualificationEncodedLimit)
	if err != nil {
		return nil
	}
	for _, source := range set.Sources {
		if source.Role == role {
			data, err := qualificationSourceBytes(source)
			if err == nil {
				return data
			}
		}
	}
	return nil
}

func qualificationManifestValue(manifest qualificationManifest) (qualificationManifestWire, error) {
	return qualificationDecode[qualificationManifestWire](manifest.document.bytes(), qualificationDocumentLimit)
}

func qualificationJobAt(manifest qualificationManifest, ordinal uint64) (qualificationJobWire, qualificationDocument, error) {
	wire, err := qualificationManifestValue(manifest)
	if err != nil || ordinal >= uint64(len(wire.Jobs)) {
		return qualificationJobWire{}, qualificationDocument{}, errors.New("qualification: job ordinal absent")
	}
	document, err := qualificationSeal(wire.Jobs[ordinal])
	return wire.Jobs[ordinal], document, err
}

func qualificationParseEvent(data []byte, manifest qualificationManifest) (qualificationEvent, error) {
	return qualificationParseEventBounded(data, manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil)
}
func qualificationParseEventBounded(data []byte, manifest qualificationManifest, documentLimit int, total uint64, role func(string) uint64, materializing func()) (qualificationEvent, error) {
	return qualificationParseEventBoundedView(data, manifest, documentLimit, total, role, materializing, nil)
}
func qualificationParseEventBoundedView(data []byte, manifest qualificationManifest, documentLimit int, total uint64, role func(string) uint64, materializing func(), view *qualificationManifestView) (qualificationEvent, error) {
	if err := qualificationProtocolBlobPreflight(data, documentLimit, total, role); err != nil {
		return qualificationEvent{}, err
	}
	if materializing != nil {
		materializing()
	}
	event, err := qualificationDecode[qualificationEventWire](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationEvent{}, err
	}
	if view == nil {
		view, err = qualificationNewManifestView(manifest)
		if err != nil {
			return qualificationEvent{}, err
		}
	}
	if view.manifest.document.canonical != manifest.document.canonical {
		return qualificationEvent{}, errors.New("qualification: operation view exact manifest mismatch")
	}
	if err != nil || event.Kind != "waza.qualification-event" || event.Version != qualificationVersion ||
		event.InvocationID != view.invocationID || event.ManifestSHA256 != manifest.document.sha256() ||
		event.ContractSHA256 != view.contractSHA256 || event.CID != view.cid ||
		event.ArmID != view.armID || event.Sequence < 1 || event.Sequence > 32768 ||
		!qualificationDigest(event.PreviousSHA256) {
		return qualificationEvent{}, errors.New("qualification: event identity")
	}
	hasJob := event.Ordinal != nil || event.Selector != nil || event.JobSHA256 != nil
	if hasJob {
		if event.Ordinal == nil || event.Selector == nil || event.JobSHA256 == nil {
			return qualificationEvent{}, errors.New("qualification: complete scoped job identity required")
		}
		job, document, err := view.job(*event.Ordinal)
		if err != nil || *event.Selector != job.Selector || *event.JobSHA256 != document.sha256() {
			return qualificationEvent{}, errors.New("qualification: event job mismatch")
		}
	}
	admission, currentness, payload, completion := event.Admission != nil, event.CurrentnessRequest != nil || event.CurrentnessAcknowledgment != nil, event.PayloadSHA256 != nil, event.Completion != nil
	valid := false
	switch event.Type {
	case "run_admission":
		valid = !hasJob && admission && !currentness && !payload && !completion
	case "job_admission":
		valid = hasJob && admission && !currentness && !payload && !completion
	case "job_start":
		valid = hasJob && !admission && !currentness && !payload && !completion
	case "job_terminal":
		valid = hasJob && !admission && !currentness && payload && !completion && qualificationDigest(*event.PayloadSHA256)
	case "run_terminal":
		valid = !hasJob && !admission && !currentness && !payload && completion &&
			event.Completion.CompletedJobs <= uint64(len(view.jobs)) && qualificationState(event.Completion.State) &&
			qualificationReason(event.Completion.ReasonCode)
		if valid {
			for _, digest := range []*string{event.Completion.ActualReportSHA256, event.Completion.ArtifactInventorySHA256} {
				valid = valid && (digest == nil || qualificationDigest(*digest))
			}
			if event.Completion.State == "passed" {
				valid = valid && event.Completion.ReasonCode == "qualification_passed" &&
					event.Completion.CompletedJobs == uint64(len(view.jobs)) &&
					event.Completion.ActualReportSHA256 != nil && event.Completion.ArtifactInventorySHA256 != nil
			}
		}
	case "currentness":
		valid = !admission && !payload && !completion && event.CurrentnessRequest != nil && event.CurrentnessAcknowledgment != nil
		if valid {
			request := *event.CurrentnessRequest
			ack := *event.CurrentnessAcknowledgment
			profile := view.currentness
			requestDoc, requestErr := qualificationSeal(request)
			ackDoc, ackErr := qualificationSeal(ack)
			_, parseErr := qualificationParseCurrentnessAcknowledgment(ackDoc.bytes())
			valid = requestErr == nil && ackErr == nil && parseErr == nil &&
				request.Kind == "waza.qualification-currentness-request" && request.Version == qualificationVersion &&
				request.InvocationID == view.invocationID && request.ContractSHA256 == event.ContractSHA256 &&
				request.CID == event.CID && request.ArmID == event.ArmID && request.ManifestSHA256 == event.ManifestSHA256 &&
				request.InputsSHA256 == view.inputsSHA256 && request.CurrentnessProfileSHA256 == profile.sha256() &&
				ack.CurrentnessProfileSHA256 == profile.sha256() && ack.RequestSHA256 == requestDoc.sha256() &&
				request.Stage == ack.Stage && qualificationDigest(request.Challenge) &&
				((request.Stage == "before_job" && hasJob && equalPointer(request.Ordinal, event.Ordinal) && equalPointer(request.JobSHA256, event.JobSHA256)) ||
					((request.Stage == "after_cleanup" || request.Stage == "before_decision") && !hasJob && request.Ordinal == nil && request.JobSHA256 == nil))
		}
	}
	if admission && (!qualificationReason(event.Admission.ReasonCode) || (event.Admission.Allowed && event.Admission.ReasonCode != "qualification_passed")) {
		valid = false
	}
	if !valid {
		return qualificationEvent{}, errors.New("qualification: discriminated event grammar")
	}
	document, err := qualificationSeal(event)
	return qualificationEvent{document}, err
}

func equalPointer[T comparable](a, b *T) bool { return a != nil && b != nil && *a == *b }

func qualificationState(value string) bool {
	return slices.Contains([]string{"passed", "failed", "insufficient_evidence", "not_assessed", "invalid", "operational_error"}, value)
}
func qualificationReason(value string) bool {
	return slices.Contains([]string{"admission_rejected", "notice_refused", "association_invalid", "source_invalid", "source_changed", "review_ineligible", "coverage_insufficient", "duplicate_stimulus", "native_binding_invalid", "model_conflict", "accounting_unavailable", "callback_missing", "factory_failed", "initialize_failed", "execute_failed", "cleanup_failed", "currentness_rejected", "currentness_expired", "authority_unverified", "clock_invalid", "persistence_failed", "acknowledgment_invalid", "acknowledgment_uncertain", "interrupted", "report_invalid", "artifact_invalid", "qualification_passed", "grade_disagreement"}, value)
}

func qualificationParseTerminal(data []byte, manifest qualificationManifest) (qualificationTerminal, error) {
	return qualificationParseTerminalBounded(data, manifest, qualificationDocumentLimit, qualificationTotalLimit, nil, nil)
}
func qualificationParseTerminalBounded(data []byte, manifest qualificationManifest, documentLimit int, total uint64, role func(string) uint64, materializing func()) (qualificationTerminal, error) {
	return qualificationParseTerminalBoundedView(data, manifest, documentLimit, total, role, materializing, nil)
}
func qualificationParseTerminalBoundedView(data []byte, manifest qualificationManifest, documentLimit int, total uint64, role func(string) uint64, materializing func(), view *qualificationManifestView) (qualificationTerminal, error) {
	if err := qualificationProtocolBlobPreflight(data, documentLimit, total, role); err != nil {
		return qualificationTerminal{}, err
	}
	if materializing != nil {
		materializing()
	}
	wire, err := qualificationDecode[qualificationTerminalWire](data, qualificationDocumentLimit)
	if err != nil {
		return qualificationTerminal{}, err
	}
	if view == nil {
		view, err = qualificationNewManifestView(manifest)
		if err != nil {
			return qualificationTerminal{}, err
		}
	}
	if view.manifest.document.canonical != manifest.document.canonical {
		return qualificationTerminal{}, errors.New("qualification: operation view exact manifest mismatch")
	}
	job, jobDoc, jobErr := view.job(wire.Ordinal)
	if err != nil || jobErr != nil || wire.Kind != "waza.qualification-terminal-payload" || wire.Version != qualificationVersion ||
		wire.InvocationID != view.invocationID || wire.ManifestSHA256 != manifest.document.sha256() ||
		wire.ContractSHA256 != view.contractSHA256 || wire.CID != view.cid ||
		wire.Selector != job.Selector || wire.JobSHA256 != jobDoc.sha256() || wire.FailureCodes == nil {
		return qualificationTerminal{}, errors.New("qualification: terminal identity")
	}
	for _, code := range wire.FailureCodes {
		if !qualificationReason(code) {
			return qualificationTerminal{}, errors.New("qualification: terminal failure allowlist")
		}
	}
	var execution JudgeExecution
	schema, schemaErr := qualificationExecutionSchema()
	instance, instanceErr := jsonschema.UnmarshalJSON(bytes.NewReader(wire.Execution))
	if schemaErr != nil || instanceErr != nil {
		return qualificationTerminal{}, errors.New("qualification: frozen execution admission unavailable")
	}
	if err := schema.Validate(instance); err != nil {
		return qualificationTerminal{}, errors.New("qualification: frozen execution shape/counters/accounting invalid")
	}
	if err := qualificationDecodeNative(wire.Execution, &execution); err != nil {
		return qualificationTerminal{}, err
	}
	if execution.CaseID != job.Selector.CaseID || execution.TaskID != job.Selector.TaskID ||
		execution.RequirementID != job.Selector.RequirementID || execution.Check != (models.RequirementCheck{Scope: job.Selector.Check.Scope, AfterTurn: job.Selector.Check.AfterTurn, Grader: job.Selector.Check.Grader}) ||
		execution.RequestedModel != job.RequestedModel || !qualificationState(string(execution.State)) ||
		wire.InitializeAttempted && !wire.FactoryReturnedEngine || wire.ShutdownAttempted && !wire.FactoryReturnedEngine ||
		wire.CleanupComplete && (!wire.ShutdownAttempted || !wire.AccountingFinalized) {
		return qualificationTerminal{}, errors.New("qualification: terminal native lifecycle binding")
	}
	if execution.UsageComplete && (execution.Usage == nil || execution.Usage.AICredits == nil || execution.Credits == nil ||
		*execution.Usage.AICredits != *execution.Credits) {
		return qualificationTerminal{}, errors.New("qualification: complete accounting needs actual independent values")
	}
	if execution.Callbacks < 0 || uint64(execution.Callbacks) > qualificationSafeCounter {
		return qualificationTerminal{}, errors.New("qualification: owner callback counter range")
	}
	if execution.UsageComplete {
		keys := slices.Sorted(maps.Keys(execution.Usage.ModelMetrics))
		accounting := slices.Clone(execution.AccountingModels)
		slices.Sort(accounting)
		if !slices.Equal(keys, accounting) {
			return qualificationTerminal{}, errors.New("qualification: native accounting model projection differs")
		}
	}
	var nativeResult *models.GraderResults
	if wire.NativeResult != nil {
		resultSchema, schemaErr := qualificationNativeResultSchema()
		resultInstance, instanceErr := jsonschema.UnmarshalJSON(bytes.NewReader(*wire.NativeResult))
		if schemaErr != nil || instanceErr != nil || resultInstance == nil {
			return qualificationTerminal{}, errors.New("qualification: frozen native result admission unavailable")
		}
		if err := resultSchema.Validate(resultInstance); err != nil {
			return qualificationTerminal{}, errors.New("qualification: frozen native result required fields/tokens invalid")
		}
		nativeResult = &models.GraderResults{}
		if err := qualificationDecodeNative(*wire.NativeResult, nativeResult); err != nil || nativeResult.Details != nil ||
			nativeResult.Type != models.GraderKindPrompt || nativeResult.Score < 0 || nativeResult.Score > 1 ||
			nativeResult.Weight < 0 || nativeResult.DurationMs < 0 ||
			nativeResult.Name != job.Selector.Check.Grader {
			return qualificationTerminal{}, errors.New("qualification: terminal native result invalid")
		}
	}
	if wire.ObservationState == "observed" {
		if execution.State != AssessmentPassed || nativeResult == nil || wire.Agreement == nil ||
			*wire.Agreement != (nativeResult.Passed == job.ExpectedPassed) || !wire.CleanupComplete ||
			!wire.InitializeAttempted || !execution.Initialized || !execution.UsageComplete ||
			!execution.EventModelAttributionComplete || !execution.AccountingModelAttributionComplete ||
			!slices.Equal(execution.EventModels, []string{job.RequestedModel}) ||
			!slices.Equal(execution.AccountingModels, []string{job.RequestedModel}) ||
			!execution.Executed || execution.Callbacks < 1 || len(wire.FailureCodes) != 0 {
			return qualificationTerminal{}, errors.New("qualification: observed terminal lacks independent evidence")
		}
	} else if !slices.Contains([]string{"not_assessed", "insufficient_evidence", "invalid", "operational_error"}, wire.ObservationState) || wire.Agreement != nil || execution.State == AssessmentPassed {
		return qualificationTerminal{}, errors.New("qualification: nonpass terminal invalid")
	}
	document, err := qualificationSeal(wire)
	return qualificationTerminal{document}, err
}

func qualificationMatchTerminalEvent(terminal qualificationTerminal, event qualificationEventWire) error {
	payload, err := qualificationDecode[qualificationTerminalWire](terminal.document.bytes(), qualificationDocumentLimit)
	if err != nil || event.Type != "job_terminal" || event.Ordinal == nil || event.JobSHA256 == nil ||
		event.Selector == nil || event.PayloadSHA256 == nil || payload.Ordinal != *event.Ordinal ||
		payload.JobSHA256 != *event.JobSHA256 || payload.Selector != *event.Selector ||
		terminal.document.sha256() != *event.PayloadSHA256 {
		return errors.New("qualification: terminal payload exact scoped event identity differs")
	}
	return nil
}

func qualificationDecodeNative(data []byte, target any) error {
	if _, err := qualificationCanonical(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errors.New("qualification: frozen native nested fields invalid")
	}
	return nil
}

// Explicit admission time is already bound into Inputs; no ambient clock.
func qualificationAdmissionTime(input qualificationInputs) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, input.AdmissionTime)
}

// Scan bounded byte spans before constructing DTO strings or trees. The
// existing strict decoder subsequently checks Unicode, fields and numbers.
func qualificationProtocolBlobPreflight(data []byte, documentLimit int, totalLimit uint64, roleLimit func(string) uint64) error {
	if documentLimit < 1 || documentLimit > qualificationDocumentLimit || totalLimit < 1 || totalLimit > qualificationTotalLimit ||
		len(data) == 0 || len(data) > documentLimit || !json.Valid(data) {
		return errors.New("qualification: bounded protocol JSON required")
	}
	budget := documentLimit
	if err := qualificationRawSize(data, &budget); err != nil {
		return err
	}
	scan := qualificationBlobScanner{data: data}
	total := uint64(0)
	if roleLimit == nil {
		roleLimit = func(role string) uint64 {
			if role == "attestation_evidence_base64" {
				return MaxLabelBytes
			}
			return qualificationSourceByteLimit(role)
		}
	}
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 64 {
			return errors.New("qualification: protocol nesting limit")
		}
		scan.whitespace()
		switch data[scan.pos] {
		case '"':
			scan.stringSpan()
		case '{':
			scan.pos++
			scan.whitespace()
			fields := map[string]qualificationJSONSpan{}
			for data[scan.pos] != '}' {
				key, err := scan.smallString(scan.stringSpan())
				if err != nil {
					return err
				}
				if _, exists := fields[key]; exists || len(fields) >= 128 {
					return errors.New("qualification: duplicate/unbounded protocol object")
				}
				scan.whitespace()
				scan.pos++
				scan.whitespace()
				start := scan.pos
				if err := walk(depth + 1); err != nil {
					return err
				}
				fields[key] = qualificationJSONSpan{start, scan.pos}
				scan.whitespace()
				if data[scan.pos] == ',' {
					scan.pos++
					scan.whitespace()
				}
			}
			scan.pos++
			for _, name := range []string{"bytes_base64", "attestation_evidence_base64"} {
				span, present := fields[name]
				if !present {
					continue
				}
				role := name
				if name == "bytes_base64" {
					roleSpan, exists := fields["role"]
					if !exists {
						return errors.New("qualification: blob role required before materialization")
					}
					var err error
					role, err = scan.smallString(roleSpan)
					if err != nil {
						return err
					}
				}
				size, err := qualificationBase64Span(data, span, min(roleLimit(role), totalLimit-total))
				if err != nil {
					return err
				}
				total += size
			}
		case '[':
			scan.pos++
			scan.whitespace()
			for data[scan.pos] != ']' {
				if err := walk(depth + 1); err != nil {
					return err
				}
				scan.whitespace()
				if data[scan.pos] == ',' {
					scan.pos++
					scan.whitespace()
				}
			}
			scan.pos++
		default:
			for scan.pos < len(data) && !strings.ContainsRune(",]} \r\n\t", rune(data[scan.pos])) {
				scan.pos++
			}
		}
		return nil
	}
	return walk(0)
}
