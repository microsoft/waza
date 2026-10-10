package assurance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/snapshot"
)

const maxSnapshotBytes = 16 * 1024 * 1024

// VerifyRequest is evaluator-owned. Spec/Tasks are the selected, already-loaded
// native declarations and must remain read-only for the duration of Verify.
// EvalSource is the exact source file, distinct from the resolved config.
type VerifyRequest struct {
	References   *ReferenceSet
	Review       SuppliedReview
	Acceptance   ReviewSourceAcceptance
	Now          time.Time
	EvalSource   []byte
	Spec         *models.EvalSpec
	Tasks        map[string]*models.TestCase
	SnapshotRoot *os.Root
	Calibrate    bool
}

// Verify assesses complete preserved files or finite authored output. It never
// executes a task agent, model or program; unavailable capabilities stay explicit.
func Verify(ctx context.Context, request VerifyRequest) (*Report, error) {
	document, err := request.References.Document()
	if err != nil {
		return nil, err
	}
	if request.Now.IsZero() || request.Spec == nil || len(request.EvalSource) == 0 || request.SnapshotRoot == nil {
		return nil, errors.New("assurance: selected native declarations, source, rooted evidence and current time are required")
	}
	subject, err := request.References.Subject()
	if err != nil {
		return nil, err
	}
	report := &Report{
		Kind: ReportKind, SchemaVersion: ReferenceVersion, CreatedAt: request.Now.UTC(),
		State: AssessmentPassed, Reason: "corpus_agreement", LabelsSHA256: byteSHA256(subject.Labels),
		CachePolicy:  "no_reuse",
		Requirements: []RequirementAssessment{}, Domains: []DomainAssessment{},
		Limitations: []string{
			"Agreement applies only to the supplied finite cases, not all possible agent behavior.",
			"Supplied-review eligibility does not authenticate humans or discover withheld revocations.",
			"Executable-byte identity is specific to this exact platform and build.",
			"Historical snapshots do not certify complete output, event, session or checkpoint inputs.",
			"Subset file capture cannot prove absence; unsupported capabilities remain not assessed.",
			"Authored output is evaluator-supplied finite input, not historical execution or observed billing.",
		},
		Calibration: CalibrationReport{State: AssessmentNotAssessed, Reason: "calibration_not_selected"},
	}
	if request.Review.Decision != nil {
		report.Review.DeclaredState = request.Review.Decision.State
	}
	report.Review.SourceID = request.Review.SourceID
	report.Review.CurrentSourceAccepted = request.Acceptance.AcceptCurrentDecision &&
		request.Acceptance.SourceID == request.Review.SourceID
	eligibility, reviewErr := CheckAuthorReview(subject, request.Review, request.Acceptance, request.Now)
	report.Review.Eligible, report.Review.Reason = eligibility.Eligible, eligibility.Reason
	if reviewErr != nil {
		setReportState(report, AssessmentInvalid, "invalid_review")
	} else if !eligibility.Eligible {
		setReportState(report, AssessmentNotAssessed, "review_not_eligible")
	}
	if plan := document.Calibration; plan != nil {
		report.Calibration.Protocol, report.Calibration.Model, report.Calibration.MaxJudgeExecutions = plan.Protocol, plan.Model, plan.MaxJudgeExecutions
	}
	if request.Calibrate {
		report.Calibration.Reason = "calibration_executor_unavailable"
		if document.Calibration == nil {
			report.Calibration.Reason = "calibration_plan_missing"
		}
		setReportState(report, AssessmentNotAssessed, report.Calibration.Reason)
	}
	resolved, err := evidence.JSONDigest(request.Spec)
	if err != nil {
		return nil, errors.New("assurance: native eval configuration cannot be fingerprinted")
	}
	executable, executableErr := CurrentExecutableSHA256()
	report.Bindings = []Binding{
		checkBinding("eval_source_bytes", new(document.EvalSourceSHA256), new(byteSHA256(request.EvalSource))),
		checkBinding("eval_resolved_config_json_v1", new(document.EvalResolvedConfigSHA256), new(resolved.SHA256)),
		checkBinding("implementation_executable_bytes", document.ImplementationExecutableSHA256, nil),
	}
	if executableErr == nil {
		report.Bindings[2] = checkBinding("implementation_executable_bytes", document.ImplementationExecutableSHA256, new(executable))
	}
	for _, binding := range report.Bindings {
		switch binding.State {
		case "mismatch":
			setReportState(report, AssessmentInvalid, "binding_mismatch")
		case "missing":
			setReportState(report, AssessmentNotAssessed, "provenance_unavailable")
		}
	}
	type key struct {
		task, requirement string
		check             models.RequirementCheck
	}
	indices := map[key]int{}
	for _, taskID := range slices.Sorted(maps.Keys(request.Tasks)) {
		task := request.Tasks[taskID]
		if task == nil || task.TestID != taskID {
			return nil, errors.New("assurance: selected task declaration is unavailable or ambiguous")
		}
		seen := map[string]bool{}
		if len(task.Requirements) == 0 {
			setReportState(report, AssessmentNotAssessed, "requirement_uncovered")
		}
		for _, requirement := range task.Requirements {
			if !referenceIdentifier(requirement.ID) || seen[requirement.ID] {
				return nil, errors.New("assurance: native requirement identity is invalid or repeated")
			}
			seen[requirement.ID] = true
			if len(requirement.Checks) == 0 {
				setReportState(report, AssessmentNotAssessed, "requirement_uncovered")
			}
			for _, check := range requirement.Checks {
				identity := key{taskID, requirement.ID, check}
				if _, duplicate := indices[identity]; duplicate {
					return nil, errors.New("assurance: native requirement repeats a scoped check")
				}
				indices[identity] = len(report.Requirements)
				report.Requirements = append(report.Requirements, RequirementAssessment{
					TaskID: taskID, RequirementID: requirement.ID, Check: check,
					State: AssessmentPassed, Reason: "corpus_agreement", Observations: []ChallengeObservation{},
				})
			}
		}
	}
	if len(report.Requirements) == 0 {
		setReportState(report, AssessmentNotAssessed, "requirement_uncovered")
	}
	domainIndices := map[string]int{}
	for _, criteria := range document.Domains {
		domainIndices[criteria.ID] = len(report.Domains)
		report.Domains = append(report.Domains, DomainAssessment{
			ID: criteria.ID, MinimumCases: criteria.MinimumCases, MinimumAgreement: criteria.MinimumAgreement,
			State: AssessmentPassed,
		})
	}
	for _, candidate := range document.Cases {
		domain := &report.Domains[domainIndices[candidate.Domain]]
		domain.DeclaredCases++
		allObserved := true
		for _, check := range candidate.Checks {
			index, exists := indices[key{candidate.TaskID, check.RequirementID, check.Check}]
			if !exists {
				return nil, errors.New("assurance: labels select an undeclared scoped requirement check")
			}
			assessment := &report.Requirements[index]
			addCoverage(&assessment.Declared, candidate.Classification, *check.ExpectedPassed)
			domain.ExpectedChecks++
			observation := verifyFileCase(ctx, request, candidate, check)
			assessment.Observations = append(assessment.Observations, observation)
			for _, binding := range observation.Bindings {
				if binding.Applicable && binding.State == "mismatch" {
					assessment.State, assessment.Reason = AssessmentInvalid, "binding_mismatch"
					domain.State = AssessmentInvalid
					setReportState(report, AssessmentInvalid, "binding_mismatch")
				}
			}
			if observation.State == Observed && observation.Agreement != nil {
				addCoverage(&assessment.Observed, candidate.Classification, *check.ExpectedPassed)
				domain.ObservedChecks++
				if *observation.Agreement {
					domain.Agreements++
				} else {
					if statePriority(AssessmentFailed) > statePriority(assessment.State) {
						assessment.State, assessment.Reason = AssessmentFailed, "label_disagreement"
					}
					if statePriority(AssessmentFailed) > statePriority(domain.State) {
						domain.State = AssessmentFailed
					}
					if candidate.Classification == CaseCriticalBad && !*check.ExpectedPassed && observation.Result.Passed {
						if assessment.State == AssessmentFailed {
							assessment.Reason = "critical_false_acceptance"
						}
						setReportState(report, AssessmentFailed, "critical_false_acceptance")
					}
					setReportState(report, AssessmentFailed, assessment.Reason)
				}
			} else {
				allObserved = false
				state := assessmentState(observation.State)
				if statePriority(state) > statePriority(domain.State) {
					domain.State = state
				}
				if statePriority(state) > statePriority(assessment.State) {
					assessment.State, assessment.Reason = state, observation.Reason
				}
				setReportState(report, state, observation.Reason)
			}
		}
		if allObserved {
			domain.ObservedCases++
		}
	}
	for i := range report.Requirements {
		assessment := &report.Requirements[i]
		if assessment.State == AssessmentPassed &&
			(assessment.Observed.Good < 1 || assessment.Observed.AlternativeValid < 1 ||
				assessment.Observed.CriticalBad < 2 || assessment.Observed.IntendedNegative < 1) {
			assessment.State, assessment.Reason = AssessmentNotAssessed, "challenge_coverage_missing"
			setReportState(report, assessment.State, assessment.Reason)
		}
		if assessment.State == AssessmentPassed && !report.Review.Eligible {
			assessment.State, assessment.Reason = AssessmentNotAssessed, "review_not_eligible"
		}
		for _, binding := range report.Bindings {
			if assessment.State == AssessmentPassed && binding.State != "verified" {
				assessment.State, assessment.Reason = AssessmentNotAssessed, "provenance_unavailable"
				if binding.State == "mismatch" {
					assessment.State, assessment.Reason = AssessmentInvalid, "binding_mismatch"
				}
			}
		}
	}
	for i := range report.Domains {
		domain := &report.Domains[i]
		if domain.ObservedChecks > 0 {
			domain.Agreement = new(float64(domain.Agreements) / float64(domain.ObservedChecks))
		}
		if domain.ObservedCases < domain.MinimumCases || domain.ObservedChecks != domain.ExpectedChecks {
			if statePriority(AssessmentInsufficient) > statePriority(domain.State) {
				domain.State = AssessmentInsufficient
			}
			setReportState(report, AssessmentInsufficient, "domain_samples_unavailable")
		} else if domain.Agreement == nil || *domain.Agreement < domain.MinimumAgreement {
			if statePriority(AssessmentFailed) > statePriority(domain.State) {
				domain.State = AssessmentFailed
			}
			setReportState(report, AssessmentFailed, "domain_criterion_failed")
		}
		if domain.State == AssessmentPassed && !report.Review.Eligible {
			domain.State = AssessmentNotAssessed
		}
		for _, binding := range report.Bindings {
			if domain.State == AssessmentPassed && binding.State != "verified" {
				domain.State = AssessmentNotAssessed
				if binding.State == "mismatch" {
					domain.State = AssessmentInvalid
				}
			}
		}
	}
	return report, nil
}

func verifyFileCase(ctx context.Context, request VerifyRequest, candidate ReferenceCase, check ReferenceCheck) ChallengeObservation {
	observation := ChallengeObservation{
		CaseID: candidate.ID, ScenarioID: candidate.ScenarioID, Domain: candidate.Domain,
		Classification: candidate.Classification, ExpectedPassed: *check.ExpectedPassed,
		State: Invalid, Reason: "invalid_declaration", Evidence: slices.Clone(check.Evidence),
		SourceScope: "preserved_file_subset",
	}
	if candidate.AuthoredInput != nil {
		observation.SourceScope = "authored_finite_output"
	}
	task := request.Tasks[candidate.TaskID]
	declaration, err := preflight.ResolveGrader(check.Check, task, request.Spec)
	if err != nil {
		return observation
	}
	var native any
	var parameters models.GraderParameters
	if declaration.Config != nil {
		native, parameters = declaration.Config, declaration.Config.Parameters
		if declaration.Config.Ref != "" {
			observation.State, observation.Reason = NotAssessed, "unexpanded_grader_reference"
			return observation
		}
	} else {
		native, parameters = declaration.Inline, declaration.Inline.Parameters
	}
	declDigest, err := evidence.JSONDigest(native)
	if err != nil {
		observation.State, observation.Reason = OperationalError, "declaration_digest_unavailable"
		return observation
	}
	taskDigest, err := evidence.JSONDigest(task)
	if err != nil {
		observation.State, observation.Reason = OperationalError, "task_digest_unavailable"
		return observation
	}
	observation.Bindings = []Binding{
		checkBinding("native_grader_declaration_json_v1", new(check.GraderDeclarationSHA256), new(declDigest.SHA256)),
		checkBinding("native_task_declaration_json_v1", new(candidate.TaskDeclarationSHA256), new(taskDigest.SHA256)),
		{Domain: "rubric_content_bytes", Applicable: false, State: "not_applicable"},
	}
	if check.Check.Scope == "checkpoint" {
		observation.State, observation.Reason = NotAssessed, "checkpoint_inputs_not_preserved"
		return observation
	}
	if candidate.AuthoredInput != nil {
		return verifyAuthoredCase(ctx, request, candidate, check, parameters, observation)
	}
	parametersFile, supported := parameters.(models.FileGraderParameters)
	if !supported {
		observation.State, observation.Reason = InsufficientEvidence, "candidate_inputs_not_preserved"
		switch parameters.(type) {
		case models.PromptGraderParameters, models.ProgramGraderParameters,
			models.InlineScriptGraderParameters, models.TriggerHeuristicGraderParameters:
			observation.State, observation.Reason = NotAssessed, "grader_capability_not_supported"
		}
		return observation
	}
	if check.RubricContentSHA256 != nil {
		observation.Reason = "rubric_not_applicable"
		return observation
	}
	paths := slices.Clone(parametersFile.MustExist)
	paths = append(paths, parametersFile.MustNotExist...)
	for _, pattern := range parametersFile.ContentPatterns {
		paths = append(paths, pattern.Path)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	if len(paths) == 0 {
		observation.State, observation.Reason = InsufficientEvidence, "file_inputs_missing"
		return observation
	}
	if err := ctx.Err(); err != nil {
		observation.State, observation.Reason = OperationalError, "assessment_interrupted"
		return observation
	}
	data, err := ReadDocument(ctx, request.SnapshotRoot, candidate.Snapshot, maxSnapshotBytes)
	if err != nil {
		observation.State, observation.Reason = InsufficientEvidence, "snapshot_unavailable"
		if !errors.Is(err, os.ErrNotExist) {
			observation.State, observation.Reason = OperationalError, "snapshot_read_failed"
		}
		return observation
	}
	raw, err := jsonutil.Parse(data)
	if err != nil {
		observation.Reason = "snapshot_json_invalid"
		return observation
	}
	snap, err := snapshot.ParseSnapshot(data, "assurance reference")
	if err != nil || snap.Evidence == nil || evidence.Validate(snap.Evidence) != nil ||
		snap.Evidence.SHA256 != candidate.ManifestSHA256 {
		observation.Reason = "snapshot_binding_invalid"
		return observation
	}
	origin := check.Evidence[0].Origin
	for _, reference := range check.Evidence {
		artifact, err := evidence.Resolve(snap.Evidence, reference)
		if err != nil {
			observation.Reason = "evidence_reference_invalid"
			return observation
		}
		if artifact.Availability != "captured" {
			observation.State, observation.Reason = InsufficientEvidence, "evidence_content_unavailable"
			return observation
		}
		value, err := pointerValue(raw, artifact.Pointer)
		if err != nil {
			observation.Reason = "evidence_locator_invalid"
			return observation
		}
		content, err := json.Marshal(value)
		if err != nil {
			observation.State, observation.Reason = OperationalError, "evidence_content_unavailable"
			return observation
		}
		if err := evidence.VerifyContent(snap.Evidence, reference, content, true, true); err != nil {
			observation.State, observation.Reason = InsufficientEvidence, "evidence_content_not_verified"
			return observation
		}
	}
	for _, name := range paths {
		reference, err := evidence.Reference(snap.Evidence, "workspace-file/"+name)
		if err != nil || !slices.ContainsFunc(check.Evidence, func(selected models.EvidenceReference) bool {
			return selected.Origin == reference.Origin && selected.ArtifactID == reference.ArtifactID
		}) {
			observation.State, observation.Reason = InsufficientEvidence, "required_file_reference_missing"
			return observation
		}
	}
	if err := snapshot.VerifyWorkspace(snap, origin, candidate.ManifestSHA256, paths); err != nil {
		observation.State, observation.Reason = InsufficientEvidence, "required_file_not_verified"
		return observation
	}
	workspace, err := snapshot.MaterializeWorkspace(snap, origin, candidate.ManifestSHA256, paths)
	if err != nil {
		observation.State, observation.Reason = OperationalError, "private_workspace_unavailable"
		return observation
	}
	observed := ObserveDeclaredMechanical(ctx, task, request.Spec, check.Check,
		&graders.Context{WorkspaceDir: workspace.Path(), TestCase: task})
	observation.State, observation.Result = observed.State, observed.Result
	if observed.Result != nil {
		result := *observed.Result
		result.Details = maps.Clone(observed.Result.Details)
		delete(result.Details, "workspace_dir")
		observation.Result = &result
	}
	if err := workspace.Close(); err != nil {
		observation.State, observation.Reason = OperationalError, "workspace_cleanup_failed"
		return observation
	}
	if observed.State != Observed || observed.Err != nil || observed.Result == nil {
		observation.Reason = "grader_observation_unavailable"
		return observation
	}
	observation.Agreement = new(observed.Result.Passed == *check.ExpectedPassed)
	observation.Reason = "label_agreement"
	if !*observation.Agreement {
		observation.Reason = "label_disagreement"
	}
	return observation
}

func verifyAuthoredCase(ctx context.Context, request VerifyRequest, candidate ReferenceCase, check ReferenceCheck, parameters models.GraderParameters, observation ChallengeObservation) ChallengeObservation {
	switch parameters.(type) {
	case models.TextGraderParameters, models.JSONSchemaGraderParameters:
	default:
		observation.State, observation.Reason = NotAssessed, "authored_output_capability_not_supported"
		return observation
	}
	if check.RubricContentSHA256 != nil {
		observation.Reason = "rubric_not_applicable"
		return observation
	}
	input, observation := verifyAuthoredInput(ctx, request, candidate, check, observation)
	if input == nil {
		return observation
	}
	observed := ObserveDeclaredMechanical(ctx, request.Tasks[candidate.TaskID], request.Spec, check.Check,
		&graders.Context{Output: *input.Output(), TestCase: request.Tasks[candidate.TaskID]})
	observation.State, observation.Result = observed.State, observed.Result
	if observed.State != Observed || observed.Err != nil || observed.Result == nil {
		observation.Reason = "grader_observation_unavailable"
		return observation
	}
	observation.Agreement = new(observed.Result.Passed == *check.ExpectedPassed)
	observation.Reason = "label_agreement"
	if !*observation.Agreement {
		observation.Reason = "label_disagreement"
	}
	return observation
}

func verifyAuthoredInput(ctx context.Context, request VerifyRequest, candidate ReferenceCase, check ReferenceCheck, observation ChallengeObservation) (*AuthoredOutput, ChallengeObservation) {
	if err := ctx.Err(); err != nil {
		observation.State, observation.Reason = OperationalError, "assessment_interrupted"
		return nil, observation
	}
	data, err := ReadDocument(ctx, request.SnapshotRoot, candidate.AuthoredInput.Path, maxSnapshotBytes)
	if err != nil {
		observation.State, observation.Reason = InsufficientEvidence, "authored_input_unavailable"
		if !errors.Is(err, os.ErrNotExist) {
			observation.State, observation.Reason = OperationalError, "authored_input_read_failed"
		}
		return nil, observation
	}
	if byteSHA256(data) != candidate.AuthoredInput.DocumentSHA256 {
		observation.Reason = "authored_document_binding_invalid"
		return nil, observation
	}
	input, err := ParseAuthoredOutput(data)
	if err != nil || input.CaseID() != candidate.ID || input.Manifest().SHA256 != candidate.ManifestSHA256 {
		observation.Reason = "authored_profile_binding_invalid"
		return nil, observation
	}
	if input.Output() == nil {
		observation.State, observation.Reason = InsufficientEvidence, "authored_output_unavailable"
		return nil, observation
	}
	document, err := request.References.Document()
	if err != nil {
		observation.State, observation.Reason = OperationalError, "reference_document_unavailable"
		return nil, observation
	}
	for _, reference := range check.Evidence {
		if reference.Origin.EvalID != "reference:"+document.ID ||
			reference.Origin.TaskID != candidate.TaskID || reference.Pointer != "/output" {
			observation.Reason = "authored_reference_invalid"
			return nil, observation
		}
		if err := evidence.VerifyContent(input.Manifest(), reference, input.ProjectionBytes(), true, true); err != nil {
			observation.State, observation.Reason = InsufficientEvidence, "authored_content_not_verified"
			return nil, observation
		}
	}
	return input, observation
}

func pointerValue(value any, pointer string) (any, error) {
	if !relativePointer(pointer) || pointer == "" {
		return nil, errors.New("assurance: invalid document locator")
	}
	for _, encoded := range strings.Split(pointer[1:], "/") {
		token := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch object := value.(type) {
		case map[string]any:
			var exists bool
			value, exists = object[token]
			if !exists {
				return nil, errors.New("assurance: document locator unavailable")
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(object) || strconv.Itoa(index) != token {
				return nil, errors.New("assurance: invalid document ordinal")
			}
			value = object[index]
		default:
			return nil, errors.New("assurance: document locator unavailable")
		}
	}
	return value, nil
}

// ReadDocument confines bounded reads to regular files without blocking on a FIFO.
func ReadDocument(ctx context.Context, root *os.Root, name string, maxBytes int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil || maxBytes < 1 || maxBytes > maxSnapshotBytes {
		return nil, errors.New("assurance: invalid bounded document reader")
	}
	file, err := openReferenceDocument(root, name)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("assurance: input must be a regular file"), statErr, file.Close())
	}
	reader := io.LimitReader(file, int64(maxBytes)+1)
	data := make([]byte, 0, min(maxBytes, 64*1024))
	buffer := make([]byte, 64*1024)
	var readErr error
	for {
		if err := ctx.Err(); err != nil {
			readErr = err
			break
		}
		count, err := reader.Read(buffer)
		data = append(data, buffer[:count]...)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
	}
	closeErr := file.Close()
	if readErr == nil {
		readErr = ctx.Err()
	}
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(data) > maxBytes {
		return nil, errors.New("assurance: document exceeds byte limit")
	}
	return data, nil
}

// CurrentExecutableSHA256 hashes actual running executable bytes, not an
// author-supplied version or a mutable source checkout.
func CurrentExecutableSHA256() (string, error) {
	name, err := os.Executable()
	if err != nil {
		return "", errors.New("assurance: executable identity unavailable")
	}
	file, err := os.Open(name)
	if err != nil {
		return "", errors.New("assurance: executable identity unavailable")
	}
	digest := sha256.New()
	_, readErr := io.Copy(digest, file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return "", errors.New("assurance: executable identity unavailable")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func byteSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func checkBinding(domain string, expected, actual *string) Binding {
	binding := Binding{Domain: domain, Applicable: true, State: "missing", Expected: expected, Actual: actual}
	if expected != nil && actual != nil {
		binding.State = "mismatch"
		if *expected == *actual {
			binding.State = "verified"
		}
	}
	return binding
}

func addCoverage(coverage *Coverage, classification CaseClassification, expected bool) {
	switch classification {
	case CaseGood:
		coverage.Good++
	case CaseAlternative:
		coverage.AlternativeValid++
	case CaseCriticalBad:
		coverage.CriticalBad++
		if !expected {
			coverage.IntendedNegative++
		}
	}
}

func assessmentState(state ObservationState) AssessmentState {
	switch state {
	case InsufficientEvidence:
		return AssessmentInsufficient
	case OperationalError:
		return AssessmentError
	case Invalid:
		return AssessmentInvalid
	default:
		return AssessmentNotAssessed
	}
}

func statePriority(state AssessmentState) int {
	switch state {
	case AssessmentInvalid:
		return 5
	case AssessmentError:
		return 4
	case AssessmentFailed:
		return 3
	case AssessmentInsufficient:
		return 2
	case AssessmentNotAssessed:
		return 1
	default:
		return 0
	}
}

func setReportState(report *Report, state AssessmentState, reason string) {
	if statePriority(state) > statePriority(report.State) {
		report.State, report.Reason = state, reason
	}
}
