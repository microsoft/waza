package assurance

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/snapshot"
)

const (
	preservedVerifyMaxFileBytes  = 4 * 1024 * 1024
	preservedVerifyMaxTotalBytes = 8 * 1024 * 1024
	preservedVerifyMaxFileCount  = 256
)

// VerifyPreserved is a separately selected, purely offline operation. It does
// not call Verify's file-only reducer or select a calibration executor.
// It applies the currently selected native declarations to preserved artifacts.
// Declaration bindings identify those current declarations, not the historical
// grader/configuration/version that originally produced a snapshot.
func VerifyPreserved(ctx context.Context, request VerifyRequest) (*Report, error) {
	return verifyPreserved(ctx, request, preservedMechanicalHooks{})
}

func verifyPreserved(ctx context.Context, request VerifyRequest, hooks preservedMechanicalHooks) (*Report, error) {
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
		Kind: ReportKind, SchemaVersion: PreservedReportVersion, AssessmentMode: PreservedAssessmentMode,
		CreatedAt: request.Now.UTC(), LabelsSHA256: byteSHA256(subject.Labels), CachePolicy: "no_reuse",
		Requirements: []RequirementAssessment{}, Domains: []DomainAssessment{},
		Calibration: CalibrationReport{State: AssessmentNotAssessed, Reason: "calibration_not_selected"},
		Limitations: []string{
			"Agreement applies only to the supplied finite selected native artifacts, not all possible agent behavior.",
			"Supplied-review eligibility does not authenticate humans or discover withheld revocations.",
			"Executable-byte identity is specific to this exact platform and build.",
			"Native grader declaration bindings identify the currently selected grader applied to preserved artifacts, not the historical grader, configuration or version that produced a snapshot.",
			"Complete synthetic native fixtures qualify only their declared finite artifacts, never authentic historical or provider completeness.",
			"Actual collector completeness remains unknown or partial; mock, live and unknown runtime labels do not authenticate completeness.",
			"Selected file capture cannot prove absence, full workspace state or authoritative external MCP/CLI state.",
			"Event projection does not establish session, checkpoint, usage, skill invocation or command receipt evidence.",
			"No task agent, judge, paid execution or observed billing is selected.",
		},
	}
	report.Review.SourceID = request.Review.SourceID
	report.Review.CurrentSourceAccepted = request.Acceptance.AcceptCurrentDecision &&
		request.Acceptance.SourceID == request.Review.SourceID
	if request.Review.Decision != nil {
		report.Review.DeclaredState = request.Review.Decision.State
	}
	eligibility, reviewErr := CheckAuthorReview(subject, request.Review, request.Acceptance, request.Now)
	report.Review.Eligible, report.Review.Reason = eligibility.Eligible, eligibility.Reason
	if reviewErr != nil {
		report.Review.Eligible, report.Review.Reason = false, ReviewInvalid
	}
	resolved, err := evidence.JSONDigest(request.Spec)
	if err != nil {
		return nil, errors.New("assurance: native eval configuration cannot be fingerprinted")
	}
	report.Bindings = []Binding{
		checkBinding("eval_source_bytes", new(document.EvalSourceSHA256), new(byteSHA256(request.EvalSource))),
		checkBinding("eval_resolved_config_json_v1", new(document.EvalResolvedConfigSHA256), new(resolved.SHA256)),
		checkBinding("implementation_executable_bytes", document.ImplementationExecutableSHA256, nil),
	}
	if executable, err := CurrentExecutableSHA256(); err == nil {
		report.Bindings[2] = checkBinding("implementation_executable_bytes", document.ImplementationExecutableSHA256, new(executable))
	}
	indices := map[preservedRequirementKey]int{}
	for _, taskID := range slices.Sorted(maps.Keys(request.Tasks)) {
		task := request.Tasks[taskID]
		if task == nil || task.TestID != taskID {
			return nil, errors.New("assurance: selected task declaration is unavailable or ambiguous")
		}
		seen := map[string]bool{}
		if len(task.Requirements) == 0 {
			return nil, errors.New("assurance: selected task has no scoped requirements")
		}
		for _, requirement := range task.Requirements {
			if !referenceIdentifier(requirement.ID) || seen[requirement.ID] || len(requirement.Checks) == 0 {
				return nil, errors.New("assurance: native requirement identity or scoped coverage is invalid")
			}
			seen[requirement.ID] = true
			for _, check := range requirement.Checks {
				key := preservedRequirementKey{taskID, requirement.ID, check}
				if _, duplicate := indices[key]; duplicate {
					return nil, errors.New("assurance: native requirement repeats a scoped check")
				}
				indices[key] = len(report.Requirements)
				report.Requirements = append(report.Requirements, RequirementAssessment{
					TaskID: taskID, RequirementID: requirement.ID, Check: check, Observations: []ChallengeObservation{},
				})
			}
		}
	}
	for _, domain := range document.Domains {
		report.Domains = append(report.Domains, DomainAssessment{
			ID: domain.ID, MinimumCases: domain.MinimumCases, MinimumAgreement: domain.MinimumAgreement,
		})
	}
	for _, candidate := range document.Cases {
		for _, check := range candidate.Checks {
			index, exists := indices[preservedRequirementKey{candidate.TaskID, check.RequirementID, check.Check}]
			if !exists {
				return nil, errors.New("assurance: labels select an undeclared scoped requirement check")
			}
			observation := preservedCase(ctx, request, candidate, check, hooks)
			report.Requirements[index].Observations = append(report.Requirements[index].Observations, observation)
		}
	}
	if err := reducePreserved(report); err != nil {
		return nil, err
	}
	return report, nil
}

func preservedCase(ctx context.Context, request VerifyRequest, candidate ReferenceCase, check ReferenceCheck, hooks preservedMechanicalHooks) ChallengeObservation {
	observation := ChallengeObservation{
		CaseID: candidate.ID, ScenarioID: candidate.ScenarioID, Domain: candidate.Domain,
		Classification: candidate.Classification, ExpectedPassed: *check.ExpectedPassed,
		SourceScope: PreservedNativeSourceScope, Evidence: slices.Clone(check.Evidence),
		State: Invalid, Reason: "invalid_declaration",
	}
	task := request.Tasks[candidate.TaskID]
	declaration, err := preflight.ResolveGrader(check.Check, task, request.Spec)
	if err != nil {
		return observation
	}
	var native any
	var parameters models.GraderParameters
	if declaration.Config != nil {
		native = declaration.Config
		parameters = declaration.Config.Parameters
		if declaration.Config.Kind == models.GraderKindFile {
			observation.SourceScope = "preserved_file_subset"
		}
	} else {
		native = declaration.Inline
		parameters = declaration.Inline.Parameters
		if declaration.Inline.Kind == models.GraderKindFile {
			observation.SourceScope = "preserved_file_subset"
		}
	}
	taskDigest, err := evidence.JSONDigest(task)
	if err != nil {
		observation.State, observation.Reason = OperationalError, "task_digest_unavailable"
		return observation
	}
	declarationDigest, err := evidence.JSONDigest(native)
	if err != nil {
		observation.State, observation.Reason = OperationalError, "declaration_digest_unavailable"
		return observation
	}
	observation.Bindings = []Binding{
		checkBinding("native_grader_declaration_json_v1", new(check.GraderDeclarationSHA256), new(declarationDigest.SHA256)),
		checkBinding("native_task_declaration_json_v1", new(candidate.TaskDeclarationSHA256), new(taskDigest.SHA256)),
		{Domain: "rubric_content_bytes", Applicable: false, State: "not_applicable"},
		checkBinding(preservedManifestBinding, new(candidate.ManifestSHA256), nil),
	}
	if request.Calibrate {
		observation.State, observation.Reason = NotAssessed, "calibration_not_selected"
		return observation
	}
	if declaration.Config != nil && declaration.Config.Ref != "" {
		observation.State, observation.Reason = NotAssessed, "unexpanded_grader_reference"
		return observation
	}
	if candidate.AuthoredInput != nil {
		observation.State, observation.Reason = NotAssessed, "authored_output_not_selected"
		return observation
	}
	if check.RubricContentSHA256 != nil {
		observation.Reason = "rubric_not_applicable"
		return observation
	}
	for _, binding := range observation.Bindings {
		if binding.State == "mismatch" {
			observation.Reason = "binding_mismatch"
			return observation
		}
	}
	data, err := ReadDocument(ctx, request.SnapshotRoot, candidate.Snapshot, maxSnapshotBytes)
	if err != nil {
		observation.State, observation.Reason = InsufficientEvidence, "snapshot_unavailable"
		if !errors.Is(err, os.ErrNotExist) {
			observation.State, observation.Reason = OperationalError, "snapshot_read_failed"
		}
		return observation
	}
	// Bound selected decoded contents before typed parsing or the adapter can
	// copy/materialize them. ReadDocument already bounds raw bytes to 16 MiB.
	if err := preservedSelectedFileBudget(data, parameters); err != nil {
		observation.Reason = "preserved_input_invalid_or_over_limit"
		return observation
	}
	snap, parseErr := snapshot.ParseSnapshot(data, "preserved assurance binding")
	if parseErr == nil && snap.Evidence != nil && evidence.ValidateNative(snap.Evidence) == nil {
		observation.Bindings[3] = checkBinding(preservedManifestBinding, new(candidate.ManifestSHA256), new(snap.Evidence.SHA256))
	}
	actual := observePreservedMechanical(ctx, PreservedMechanicalInput{
		Task: task, Spec: request.Spec, Check: check.Check, SnapshotBytes: data,
		ManifestSHA256: candidate.ManifestSHA256, Evidence: slices.Clone(check.Evidence),
	}, hooks)
	observation.State, observation.Result = actual.State, actual.Result
	if actual.Result != nil {
		result := *actual.Result
		result.Details = maps.Clone(actual.Result.Details)
		delete(result.Details, "workspace_dir")
		observation.Result = &result
	}
	if actual.State != Observed || actual.Err != nil || actual.Result == nil {
		observation.Reason = "native_observation_unavailable"
		return observation
	}

	observation.Agreement = new(actual.Result.Passed == *check.ExpectedPassed)
	observation.Reason = "label_agreement"
	if !*observation.Agreement {
		observation.Reason = "label_disagreement"
	}
	return observation
}

func preservedSelectedFileBudget(data []byte, parameters models.GraderParameters) error {
	file, supported := parameters.(models.FileGraderParameters)
	if !supported {
		return nil
	}
	paths := map[string]bool{}
	for _, path := range file.MustExist {
		paths[path] = true
	}
	for _, pattern := range file.ContentPatterns {
		paths[pattern.Path] = true
	}
	if len(paths) > preservedVerifyMaxFileCount {
		return errors.New("assurance: selected preserved files exceed 256 paths")
	}
	value, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("assurance: preserved file input JSON: %w", err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	files, ok := root["workspaceFiles"].([]any)
	if !ok {
		return nil
	}
	total := 0
	for _, value := range files {
		captured, ok := value.(map[string]any)
		if !ok {
			continue
		}
		path, _ := captured["path"].(string)
		if !paths[path] {
			continue
		}
		content, ok := captured["content"].(string)
		if !ok {
			continue
		}
		if len(content) > preservedVerifyMaxFileBytes {
			return errors.New("assurance: selected preserved file exceeds 4 MiB")
		}
		total += len(content)
		if total > preservedVerifyMaxTotalBytes {
			return errors.New("assurance: selected preserved files exceed 8 MiB")
		}
	}
	return nil
}
