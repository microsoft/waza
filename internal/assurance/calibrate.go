package assurance

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/graders"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"gopkg.in/yaml.v3"
)

// CalibrationEngine is an owned, construction-only injection. A factory must
// not initialize or execute an engine. Calibrate owns every nonnil return,
// including an engine returned together with a construction error.
type CalibrationEngine interface {
	Initialize(context.Context) error
	Execute(context.Context, *execution.ExecutionRequest) (*execution.ExecutionResponse, error)
	Shutdown(context.Context) error
	SessionUsage(string) *models.UsageStats
	SessionUsageObservation(string) execution.SessionUsageObservation
	SessionEventModelObservation(string) execution.SessionEventModelObservation
}

type CalibrateRequest struct {
	VerifyRequest
	// RubricRoot confines original local rubric reads independently of snapshots.
	// Rubric paths in resolved native parameters must be relative to this root.
	RubricRoot *os.Root
	// There is deliberately no default engine or notice.
	EngineFactory  func(model string, observer func(execution.ExecutionDiagnostic) error) (CalibrationEngine, error)
	PaidCallNotice func(plan CalibrationPlan, admittedUniqueExecutions int) error
}

type calibrationJob struct {
	requirement, observation int
	config                   models.GraderConfig
	task                     *models.TestCase
	output                   string
	rubric                   []byte
	rubricSHA                string
	requestIdentity          string
	requestedModel           string
	judgeModel, judgeEffort  string
}

var errCalibrationCapture = errors.New("assurance: capture-only native judge request")

// Calibrate explicitly assesses independent judges over reviewed finite authored
// output. It never runs a task agent and never constructs an engine before all
// admission checks and the caller's paid-call notice have succeeded.
func Calibrate(ctx context.Context, request CalibrateRequest) (*Report, error) {
	frozen, err := freezeCalibrationRequest(request.VerifyRequest)
	if err != nil {
		return nil, err
	}
	selected := frozen.Calibrate
	frozen.Calibrate = false
	offline, err := Verify(ctx, frozen)
	if err != nil {
		return nil, err
	}
	document, err := frozen.References.Document()
	if err != nil {
		return nil, err
	}
	report := calibrationReport(offline)
	finish := func() (*Report, error) {
		data, err := json.Marshal(report)
		if err != nil {
			setReportState(report, AssessmentError, "calibration_report_encoding_failed")
			if statePriority(AssessmentError) > statePriority(report.Calibration.State) {
				report.Calibration.State, report.Calibration.Reason = AssessmentError, "calibration_report_encoding_failed"
			}
			return report, errors.New("assurance: encode calibration report")
		}
		if _, err := ParseCalibratedReport(data); err != nil {
			setReportState(report, AssessmentError, "calibration_report_contract_failed")
			if statePriority(AssessmentError) > statePriority(report.Calibration.State) {
				report.Calibration.State, report.Calibration.Reason = AssessmentError, "calibration_report_contract_failed"
			}
			return report, errors.New("assurance: produced calibration report failed its internal contract check")
		}
		return report, nil
	}
	reject := func(state AssessmentState, reason string) (*Report, error) {
		report.Calibration.State, report.Calibration.Reason = state, reason
		setReportState(report, state, reason)
		return finish()
	}
	if !selected {
		return reject(AssessmentNotAssessed, "calibration_not_selected")
	}
	if document.Calibration == nil {
		return reject(AssessmentNotAssessed, "calibration_plan_missing")
	}
	plan := *document.Calibration
	if !report.Review.Eligible {
		if report.Review.Reason == ReviewInvalid {
			return reject(AssessmentInvalid, "invalid_review")
		}
		return reject(AssessmentNotAssessed, "review_not_eligible")
	}
	for _, binding := range report.Bindings {
		if binding.State == "mismatch" {
			return reject(AssessmentInvalid, "binding_mismatch")
		}
		if binding.Applicable && binding.State != "verified" {
			return reject(AssessmentNotAssessed, "provenance_unavailable")
		}
	}
	if plan.Protocol != AgreementProtocol || !calibrationModelIDsValid([]string{plan.Model}) {
		return reject(AssessmentNotAssessed, "calibration_protocol_or_model_unsupported")
	}
	if len(report.Requirements) == 0 || len(report.Domains) == 0 {
		return reject(AssessmentInsufficient, "challenge_coverage_missing")
	}
	for _, task := range frozen.Tasks {
		if len(task.Requirements) == 0 {
			return reject(AssessmentInsufficient, "requirement_uncovered")
		}
		for _, requirement := range task.Requirements {
			if len(requirement.Checks) == 0 {
				return reject(AssessmentInsufficient, "requirement_uncovered")
			}
		}
	}
	for _, requirement := range report.Requirements {
		if !calibrationCoverageComplete(requirement.Declared) {
			return reject(AssessmentInsufficient, "challenge_coverage_missing")
		}
	}
	for _, domain := range report.Domains {
		if domain.DeclaredCases < domain.MinimumCases {
			return reject(AssessmentInsufficient, "domain_samples_unavailable")
		}
	}

	private, err := newCalibrationRubricStore()
	if err != nil {
		return reject(AssessmentError, "rubric_copy_unavailable")
	}
	defer func() {
		if err := private.close(); err != nil {
			report.Calibration.State, report.Calibration.Reason = AssessmentError, "rubric_cleanup_failed"
			setReportState(report, AssessmentError, "rubric_cleanup_failed")
		}
	}()
	jobs := []calibrationJob{}
	domainModes := map[string]bool{}
	domainJudgingIdentities := map[string]string{}
	seenJobs := map[string]bool{}
	for ri := range report.Requirements {
		requirement := &report.Requirements[ri]
		for oi := range requirement.Observations {
			observation := &requirement.Observations[oi]
			candidate, check, ok := calibrationReference(document, *requirement, observation.CaseID)
			if !ok {
				return reject(AssessmentInvalid, "scoped_reference_unavailable")
			}
			declaration, err := preflight.ResolveGrader(requirement.Check, frozen.Tasks[requirement.TaskID], frozen.Spec)
			if err != nil {
				return reject(AssessmentInvalid, "native_declaration_unavailable")
			}
			var config models.GraderConfig
			if declaration.Config != nil {
				config = *declaration.Config
			} else {
				config = models.GraderConfig{
					Identifier: declaration.Inline.Identifier, Kind: declaration.Inline.Kind,
					Parameters: declaration.Inline.Parameters, Weight: declaration.Inline.Weight,
				}
			}
			parameters, paid := config.Parameters.(models.PromptGraderParameters)
			if mode, exists := domainModes[candidate.Domain]; exists && mode != paid {
				return reject(AssessmentNotAssessed, "domain_mixes_paid_and_mechanical")
			}
			domainModes[candidate.Domain] = paid
			for _, binding := range observation.Bindings {
				if binding.Applicable && binding.State != "verified" {
					state := AssessmentNotAssessed
					if binding.State == "mismatch" {
						state = AssessmentInvalid
					}
					return reject(state, "binding_mismatch")
				}
			}
			if !paid {
				// These are actual source-bound local native verdicts from Verify,
				// not metadata proxies. Its sticky reducer is not reused.
				if observation.State != Observed || observation.Result == nil || observation.Agreement == nil {
					return reject(assessmentState(observation.State), observation.Reason)
				}
				if !*observation.Agreement {
					if observation.Classification == CaseCriticalBad && observation.Result.Passed {
						return reject(AssessmentFailed, "critical_false_acceptance")
					}
					return reject(AssessmentFailed, "mechanical_label_disagreement")
				}
				continue
			}
			if config.Ref != "" || requirement.Check.Scope == "checkpoint" ||
				(config.Kind != models.GraderKindPrompt) || parameters.ContinueSession ||
				(parameters.Mode != "" && parameters.Mode != models.PromptGraderModeIndependent) ||
				candidate.AuthoredInput == nil || request.RubricRoot == nil ||
				!calibrationLocalRubric(parameters.Rubric) {
				return reject(AssessmentNotAssessed, "calibration_mode_unsupported")
			}
			if err := graders.ValidateConfig(config.Identifier, parameters); err != nil {
				return reject(AssessmentInvalid, "native_grader_configuration_invalid")
			}
			input, admission := verifyAuthoredInput(ctx, frozen, candidate, check, ChallengeObservation{
				State: Invalid, Reason: "authored_input_invalid",
			})
			if input == nil {
				return reject(assessmentState(admission.State), admission.Reason)
			}
			rubric, err := ReadDocument(ctx, request.RubricRoot, parameters.Rubric, maxSnapshotBytes)
			if err != nil {
				return reject(AssessmentNotAssessed, "rubric_input_unavailable")
			}
			rubricSHA := byteSHA256(rubric)
			observation.Bindings[len(observation.Bindings)-1] = checkBinding("rubric_content_bytes", check.RubricContentSHA256, new(rubricSHA))
			if check.RubricContentSHA256 == nil {
				return reject(AssessmentNotAssessed, "rubric_binding_unavailable")
			}
			if rubricSHA != *check.RubricContentSHA256 {
				return reject(AssessmentInvalid, "rubric_binding_mismatch")
			}
			parsedRubric, err := graders.ParseRubric(rubric)
			if err != nil {
				return reject(AssessmentInvalid, "rubric_configuration_invalid")
			}
			goldensPresent, err := calibrationRubricGoldensPresent(rubric)
			if err != nil {
				return reject(AssessmentInvalid, "rubric_configuration_invalid")
			}
			if parsedRubric.Goldens != nil || goldensPresent {
				return reject(AssessmentNotAssessed, "rubric_goldens_unsupported")
			}
			job := calibrationJob{
				requirement: ri, observation: oi, config: config,
				task: frozen.Tasks[candidate.TaskID], output: *input.Output(),
				rubric: rubric, rubricSHA: rubricSHA,
				judgeModel: frozen.Spec.Config.JudgeModel, judgeEffort: frozen.Spec.Config.JudgeReasoningEffort,
			}
			if job.judgeModel == "" {
				job.judgeModel = plan.Model
			}
			capture := &calibrationCapture{}
			_, captureErr := runCalibrationGrade(ctx, job, private, capture)
			if !errors.Is(captureErr, errCalibrationCapture) || capture.calls != 1 || capture.identity == "" {
				return reject(AssessmentNotAssessed, "native_judge_capture_unavailable")
			}
			if capture.model != plan.Model {
				return reject(AssessmentNotAssessed, "requested_model_differs_from_plan")
			}
			job.requestIdentity, job.requestedModel = capture.identity, capture.model
			// A domain's judging identity must not include case/task stimulus,
			// selectors, or private rubric paths. Capture the same native
			// grader with fixed empty input instead of reconstructing its
			// effective prompt, defaults, reasoning, tools, or policy.
			identityJob := job
			identityJob.task, identityJob.output = &models.TestCase{}, ""
			identityCapture := &calibrationCapture{}
			_, identityErr := runCalibrationGrade(ctx, identityJob, private, identityCapture)
			if !errors.Is(identityErr, errCalibrationCapture) || identityCapture.calls != 1 ||
				identityCapture.identity == "" || identityCapture.model != plan.Model {
				return reject(AssessmentNotAssessed, "domain_judging_identity_unavailable")
			}
			domainIdentity, err := evidence.JSONDigest(struct {
				Rubric, NativeRequest, Protocol string
			}{rubricSHA, identityCapture.identity, plan.Protocol})
			if err != nil {
				return reject(AssessmentInvalid, "domain_judging_identity_unavailable")
			}
			if previous, exists := domainJudgingIdentities[candidate.Domain]; exists && previous != domainIdentity.SHA256 {
				return reject(AssessmentNotAssessed, "domain_judging_identity_mismatch")
			}
			domainJudgingIdentities[candidate.Domain] = domainIdentity.SHA256
			// No case IDs or labels in the job identity: repeated stimulus must
			// not manufacture paid executions or independent sample coverage.
			key, err := evidence.JSONDigest(struct {
				Request, Output, Rubric, Protocol string
			}{job.requestIdentity, job.output, job.rubricSHA, plan.Protocol})
			if err != nil {
				return reject(AssessmentInvalid, "job_identity_unavailable")
			}
			if seenJobs[key.SHA256] {
				return reject(AssessmentInsufficient, "duplicate_judge_stimulus")
			}
			seenJobs[key.SHA256] = true
			observation.State, observation.Reason = NotAssessed, "calibration_pending"
			observation.Result, observation.Agreement = nil, nil
			jobs = append(jobs, job)
		}
	}
	if len(jobs) == 0 {
		return reject(AssessmentNotAssessed, "no_supported_calibration_checks")
	}
	if len(jobs) > plan.MaxJudgeExecutions {
		return reject(AssessmentInsufficient, "judge_execution_budget_insufficient")
	}
	if request.PaidCallNotice == nil {
		return reject(AssessmentNotAssessed, "paid_call_notice_required")
	}
	if request.EngineFactory == nil {
		return reject(AssessmentNotAssessed, "calibration_engine_factory_required")
	}
	if ctx.Err() != nil {
		return reject(AssessmentError, "assessment_interrupted")
	}
	noticeErr := request.PaidCallNotice(plan, len(jobs))
	if ctx.Err() != nil {
		return reject(AssessmentError, "assessment_interrupted")
	}
	if noticeErr != nil {
		return reject(AssessmentNotAssessed, "paid_call_notice_refused")
	}
	for _, job := range jobs {
		observation := &report.Requirements[job.requirement].Observations[job.observation]
		entry, result := executeCalibrationJob(ctx, request.EngineFactory, job, private, *observation, report.Requirements[job.requirement])
		*report.Calibration.ExecutionLedger = append(*report.Calibration.ExecutionLedger, entry)
		if result != nil {
			observation.Result = result
		}
		if entry.State == AssessmentPassed && result != nil {
			observation.State, observation.Reason = Observed, "native_judge_observed"
			observation.Agreement = new(result.Passed == observation.ExpectedPassed)
		} else {
			observation.State, observation.Reason = calibrationObservationState(entry.State), entry.Reason
			observation.Agreement = nil
		}
	}
	reduceCalibrationReport(report)
	if err := private.close(); err != nil {
		return reject(AssessmentError, "rubric_cleanup_failed")
	}
	return finish()
}

func calibrationReport(offline *Report) *Report {
	return &Report{
		Kind: ReportKind, SchemaVersion: CalibratedReportVersion, AssessmentMode: CalibrationOperation,
		CreatedAt: offline.CreatedAt, State: AssessmentPassed, Reason: "finite_calibration_agreement",
		LabelsSHA256: offline.LabelsSHA256, Review: offline.Review, Bindings: offline.Bindings,
		Requirements: offline.Requirements, Domains: offline.Domains, CachePolicy: "no_reuse",
		Limitations: append(slices.Clone(offline.Limitations),
			"Calibration is explicit paid judging, not task-agent execution or statistical confidence.",
			"Max judge executions bounds admitted independent jobs, not billable provider calls or spending.",
			"Only complete finalized SDK snapshots are representable as usage in the 1.1 ledger; partial snapshots remain unavailable.",
			"Cleanup uses a 30-second context for cooperative waits; an uncooperative injected shutdown is not forcibly bounded.",
			"Event model attribution and finalized SDK accounting are independently checked, not provider authentication."),
		Calibration: CalibrationReport{
			State: AssessmentNotAssessed, Reason: "calibration_not_selected",
			Protocol: offline.Calibration.Protocol, Model: offline.Calibration.Model,
			MaxJudgeExecutions: offline.Calibration.MaxJudgeExecutions, ExecutionLedger: new([]JudgeExecution{}),
		},
	}
}

func calibrationReference(document ReferenceDocument, requirement RequirementAssessment, caseID string) (ReferenceCase, ReferenceCheck, bool) {
	for _, candidate := range document.Cases {
		if candidate.ID != caseID || candidate.TaskID != requirement.TaskID {
			continue
		}
		for _, check := range candidate.Checks {
			if check.RequirementID == requirement.RequirementID && check.Check == requirement.Check {
				return candidate, check, true
			}
		}
	}
	return ReferenceCase{}, ReferenceCheck{}, false
}

func calibrationCoverageComplete(coverage Coverage) bool {
	return coverage.Good >= 1 && coverage.AlternativeValid >= 1 && coverage.CriticalBad >= 2 && coverage.IntendedNegative >= 1
}

func calibrationLocalRubric(name string) bool {
	return name != "" && filepath.IsLocal(name) &&
		(strings.ContainsAny(name, `/\`) || strings.HasSuffix(strings.ToLower(name), ".md"))
}

// Inspect the exact native frontmatter boundary. Native decoding flattens an
// explicitly null golden field into the same nil slice as an absent field.
// Presence, including an inherited YAML merge field, is inadmissible here.
func calibrationRubricGoldensPresent(data []byte) (bool, error) {
	trimmed := bytes.TrimLeft(data, " \t\r\n")
	if !bytes.HasPrefix(trimmed, []byte("---")) {
		return false, errors.New("assurance: rubric frontmatter unavailable")
	}
	frontmatter := bytes.TrimLeft(bytes.TrimPrefix(trimmed, []byte("---")), "\r\n")
	end := bytes.Index(frontmatter, []byte("\n---"))
	if end < 0 {
		return false, errors.New("assurance: rubric frontmatter unavailable")
	}
	var document yaml.Node
	if err := yaml.Unmarshal(frontmatter[:end], &document); err != nil {
		return false, errors.New("assurance: rubric frontmatter invalid")
	}
	visited := map[*yaml.Node]bool{}
	var present func(*yaml.Node) bool
	present = func(node *yaml.Node) bool {
		if node == nil || visited[node] {
			return false
		}
		visited[node] = true
		switch node.Kind {
		case yaml.AliasNode:
			return present(node.Alias)
		case yaml.DocumentNode, yaml.SequenceNode:
			for _, child := range node.Content {
				if present(child) {
					return true
				}
			}
		case yaml.MappingNode:
			for i := 0; i+1 < len(node.Content); i += 2 {
				key, value := node.Content[i], node.Content[i+1]
				if key.Value == "goldens" {
					return true
				}
				if key.Tag == "!!merge" && present(value) {
					return true
				}
			}
		}
		return false
	}
	return present(&document), nil
}

type calibrationRubricStore struct {
	directory string
	files     []string
}

func newCalibrationRubricStore() (*calibrationRubricStore, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(".cache", 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(".cache")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("assurance: calibration cache must be a local directory")
	}
	directory, err := filepath.Abs(filepath.Join(".cache", "assurance-calibration-"+hex.EncodeToString(id)))
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	return &calibrationRubricStore{directory: directory}, nil
}

func (store *calibrationRubricStore) write(data []byte) (string, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return "", err
	}
	name := filepath.Join(store.directory, hex.EncodeToString(id)+".md")
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	store.files = append(store.files, name)
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return "", err
	}
	return name, nil
}

func (store *calibrationRubricStore) close() error {
	if store.directory == "" {
		return nil
	}
	var failures []error
	for _, name := range store.files {
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	if err := os.Remove(store.directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		store.directory, store.files = "", nil
	}
	return errors.Join(failures...)
}

func runCalibrationGrade(ctx context.Context, job calibrationJob, store *calibrationRubricStore, executor graders.Executor) (map[string]models.GraderResults, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config, err := calibrationConfigCopy(job.config)
	if err != nil {
		return nil, err
	}
	parameters, ok := config.Parameters.(models.PromptGraderParameters)
	if !ok {
		return nil, errors.New("assurance: calibrated native grader is not a prompt grader")
	}
	name, err := store.write(job.rubric)
	if err != nil {
		return nil, err
	}
	parameters.Rubric = name
	config.Parameters = parameters
	// The real frozen task supplies prompt input, but the runner task must be
	// empty so unrelated validators and expectations can never execute.
	return graders.RunAll(ctx, []models.GraderConfig{config}, &models.TestCase{}, &graders.Context{
		TestCase: job.task, Output: job.output, OutputPresent: true, Executor: executor,
	}, job.judgeModel, job.judgeEffort, false)
}

func calibrationObservationState(state AssessmentState) ObservationState {
	switch state {
	case AssessmentInvalid:
		return Invalid
	case AssessmentError:
		return OperationalError
	case AssessmentInsufficient:
		return InsufficientEvidence
	default:
		return NotAssessed
	}
}

// This reducer starts fresh; Verify's mechanical-only/not-selected reducer
// cannot decide the outcome of the separate calibrated operation.
func reduceCalibrationReport(report *Report) {
	report.State, report.Reason = AssessmentPassed, "finite_calibration_agreement"
	report.Calibration.State, report.Calibration.Reason = AssessmentPassed, "finite_calibration_agreement"
	stats := make([]*models.UsageStats, 0, len(*report.Calibration.ExecutionLedger))
	usagePresent := false
	usableCases := map[string]bool{}
	for _, entry := range *report.Calibration.ExecutionLedger {
		stats = append(stats, entry.Usage)
		usagePresent = usagePresent || entry.Usage != nil
		if entry.Executed {
			report.Calibration.Executions++
		}
		if _, exists := usableCases[entry.CaseID]; !exists {
			usableCases[entry.CaseID] = true
		}
		if entry.State != AssessmentPassed {
			usableCases[entry.CaseID] = false
			if statePriority(entry.State) > statePriority(report.Calibration.State) {
				report.Calibration.State, report.Calibration.Reason = entry.State, entry.Reason
			}
			setReportState(report, entry.State, entry.Reason)
		}
	}
	for _, complete := range usableCases {
		if complete {
			report.Calibration.Samples++
		}
	}
	if usagePresent {
		report.Calibration.Usage = models.AggregateUsageStats(stats)
		report.Calibration.Credits = report.Calibration.Usage.AICredits
	}
	domains := map[string]*DomainAssessment{}
	domainCases := map[string]map[string]bool{}
	domainCompleteCases := map[string]map[string]bool{}
	for i := range report.Domains {
		domain := &report.Domains[i]
		domain.State = AssessmentPassed
		domain.DeclaredCases, domain.ObservedCases, domain.ExpectedChecks, domain.ObservedChecks, domain.Agreements = 0, 0, 0, 0, 0
		domain.Agreement = nil
		domains[domain.ID] = domain
		domainCases[domain.ID], domainCompleteCases[domain.ID] = map[string]bool{}, map[string]bool{}
	}
	requirementDomains := make([]map[string]bool, len(report.Requirements))
	for i := range report.Requirements {
		requirement := &report.Requirements[i]
		requirement.State, requirement.Reason = AssessmentPassed, "finite_calibration_agreement"
		requirement.Observed = Coverage{}
		requirementDomains[i] = map[string]bool{}
		for _, observation := range requirement.Observations {
			domain := domains[observation.Domain]
			requirementDomains[i][domain.ID] = true
			domainCases[domain.ID][observation.CaseID] = true
			if _, exists := domainCompleteCases[domain.ID][observation.CaseID]; !exists {
				domainCompleteCases[domain.ID][observation.CaseID] = true
			}
			domain.ExpectedChecks++
			if observation.State != Observed || observation.Result == nil || observation.Agreement == nil {
				domainCompleteCases[domain.ID][observation.CaseID] = false
				state := assessmentState(observation.State)
				if statePriority(state) > statePriority(requirement.State) {
					requirement.State, requirement.Reason = state, observation.Reason
				}
				if statePriority(state) > statePriority(domain.State) {
					domain.State = state
				}
				setReportState(report, state, observation.Reason)
				continue
			}
			addCoverage(&requirement.Observed, observation.Classification, observation.ExpectedPassed)
			domain.ObservedChecks++
			if *observation.Agreement {
				domain.Agreements++
			} else if observation.Result.Type != models.GraderKindPrompt ||
				(observation.Classification == CaseCriticalBad && observation.Result.Passed) {
				reason := "mechanical_label_disagreement"
				if observation.Classification == CaseCriticalBad && observation.Result.Passed {
					reason = "critical_false_acceptance"
				}
				if statePriority(AssessmentFailed) > statePriority(requirement.State) {
					requirement.State, requirement.Reason = AssessmentFailed, reason
				}
				if statePriority(AssessmentFailed) > statePriority(domain.State) {
					domain.State = AssessmentFailed
				}
				setReportState(report, AssessmentFailed, reason)
			}
		}
		if !calibrationCoverageComplete(requirement.Observed) {
			if statePriority(AssessmentInsufficient) > statePriority(requirement.State) {
				requirement.State, requirement.Reason = AssessmentInsufficient, "challenge_coverage_missing"
			}
			setReportState(report, AssessmentInsufficient, "challenge_coverage_missing")
		}
	}
	for _, domain := range report.Domains {
		actual := domains[domain.ID]
		actual.DeclaredCases = len(domainCases[domain.ID])
		for _, complete := range domainCompleteCases[domain.ID] {
			if complete {
				actual.ObservedCases++
			}
		}
		if actual.ObservedChecks > 0 {
			actual.Agreement = new(float64(actual.Agreements) / float64(actual.ObservedChecks))
		}
		if actual.ObservedCases < actual.MinimumCases || actual.ObservedChecks != actual.ExpectedChecks {
			if statePriority(AssessmentInsufficient) > statePriority(actual.State) {
				actual.State = AssessmentInsufficient
			}
			setReportState(report, AssessmentInsufficient, "domain_samples_unavailable")
		} else if actual.Agreement == nil || *actual.Agreement < actual.MinimumAgreement {
			if statePriority(AssessmentFailed) > statePriority(actual.State) {
				actual.State = AssessmentFailed
			}
			setReportState(report, AssessmentFailed, "domain_criterion_failed")
		}
	}
	for i := range report.Requirements {
		requirement := &report.Requirements[i]
		for domainID := range requirementDomains[i] {
			domain := domains[domainID]
			if statePriority(domain.State) > statePriority(requirement.State) {
				requirement.State, requirement.Reason = domain.State, "domain_criterion_not_met"
			}
		}
	}
	if report.State != AssessmentPassed && statePriority(report.State) > statePriority(report.Calibration.State) {
		report.Calibration.State, report.Calibration.Reason = report.State, report.Reason
	}
}

func calibrationModels(models []string) []string {
	models = slices.Clone(models)
	slices.Sort(models)
	return slices.Compact(models)
}

func calibrationAccountingMatches(usage *models.UsageStats, models []string) bool {
	return usage != nil && slices.Equal(slices.Sorted(maps.Keys(usage.ModelMetrics)), models)
}
