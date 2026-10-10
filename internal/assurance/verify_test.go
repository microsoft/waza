package assurance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func fileVerificationFixture(t *testing.T) (VerifyRequest, ReferenceDocument, string) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("secure preserved-file restoration is supported on Linux and macOS")
	}
	task := &models.TestCase{TestID: "task", DisplayName: "Finite state", Requirements: []models.Requirement{{
		ID: "state", Category: "outcome", Description: "State is ready without forbidden content.",
		Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
	}}}
	spec := &models.EvalSpec{Graders: []models.GraderConfig{{
		Identifier: "check", Kind: models.GraderKindFile,
		Parameters: models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
			Path: "state.txt", MustMatch: []string{`(?m)^state:\s*ready$`}, MustNotMatch: []string{`forbidden:\s*yes`},
		}}},
	}}}
	rootPath := t.TempDir()
	root, err := os.OpenRoot(rootPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	specDigest, err := evidence.JSONDigest(spec)
	require.NoError(t, err)
	taskDigest, err := evidence.JSONDigest(task)
	require.NoError(t, err)
	declarationDigest, err := evidence.JSONDigest(spec.Graders[0])
	require.NoError(t, err)
	executable, err := CurrentExecutableSHA256()
	require.NoError(t, err)
	source := []byte("exact evaluator-owned source bytes")
	document := admissionDocument()
	document.EvalSourceSHA256, document.EvalResolvedConfigSHA256 = byteSHA256(source), specDigest.SHA256
	document.ImplementationExecutableSHA256 = new(executable)
	document.Cases = nil
	for index, candidate := range []struct {
		id             string
		classification CaseClassification
		content        string
		expected       bool
	}{
		{"good", CaseGood, "state: ready\n", true},
		{"alternative", CaseAlternative, "note: same outcome by another route\nstate: ready\n", true},
		{"wrong-state", CaseCriticalBad, "state: wrong\n", false},
		{"forbidden-state", CaseCriticalBad, "state: ready\nforbidden: yes\n", false},
	} {
		workspace := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(workspace, "state.txt"), []byte(candidate.content), 0o600))
		snap, err := snapshot.Capture(snapshot.CaptureInput{
			EvalID: "fixture-eval", Task: task, Spec: spec, ExecutionMode: "mock",
			Request:         &execution.ExecutionRequest{Message: "finite test input", WorkspaceDir: workspace},
			Run:             &models.RunResult{RunNumber: index + 1, Attempts: 1, Status: models.StatusPassed, FinalOutput: "Everything succeeded.", WorkspaceDir: workspace},
			WorkspacePaths:  []string{"state.txt"},
			WorkspaceLimits: snapshot.WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 4096},
		})
		require.NoError(t, err)
		name := candidate.id + ".json"
		require.NoError(t, os.WriteFile(filepath.Join(rootPath, name), marshalReferenceTest(t, snap), 0o600))
		reference, err := evidence.Reference(snap.Evidence, "workspace-file/state.txt")
		require.NoError(t, err)
		document.Cases = append(document.Cases, ReferenceCase{
			ID: candidate.id, ScenarioID: "target", Domain: "cli", Classification: candidate.classification,
			TaskID: task.TestID, TaskDeclarationSHA256: taskDigest.SHA256, Snapshot: name,
			ManifestSHA256: snap.Evidence.SHA256,
			Checks: []ReferenceCheck{{
				RequirementID: "state", Check: task.Requirements[0].Checks[0],
				GraderDeclarationSHA256: declarationDigest.SHA256, ExpectedPassed: new(candidate.expected),
				Evidence: []models.EvidenceReference{reference},
			}},
		})
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	request := VerifyRequest{
		Now: now, EvalSource: source, Spec: spec, Tasks: map[string]*models.TestCase{task.TestID: task},
		SnapshotRoot: root, Acceptance: ReviewSourceAcceptance{SourceID: "synthetic-source", AcceptCurrentDecision: true},
	}
	rebindSyntheticReview(t, &request, document)
	return request, document, rootPath
}

func rebindSyntheticReview(t *testing.T, request *VerifyRequest, document ReferenceDocument) {
	t.Helper()
	set, err := ParseReferences(marshalReferenceTest(t, document))
	require.NoError(t, err)
	request.References = set
	subject, err := set.Subject()
	require.NoError(t, err)
	// This is an explicit synthetic declaration, not actual human review.
	request.Review, err = ParseReview(marshalReferenceTest(t, ReviewDocument{
		SchemaVersion: ReferenceVersion, Kind: ReviewKind, SourceID: "synthetic-source",
		SubjectID: subject.ID, SubjectVersion: subject.Version, LabelsSHA256: byteSHA256(subject.Labels),
		State: ReviewReviewed, Reviewer: "synthetic-test-reviewer", ReviewedAt: request.Now.Add(-time.Hour),
	}))
	require.NoError(t, err)
}

func TestVerifyCompletePreservedFileInputsSyntheticReview(t *testing.T) {
	request, document, root := fileVerificationFixture(t)
	before, err := os.ReadFile(filepath.Join(root, document.Cases[0].Snapshot))
	require.NoError(t, err)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentPassed, report.State)
	require.True(t, report.Review.Eligible)
	require.Len(t, report.Requirements, 1)
	assessment := report.Requirements[0]
	require.Equal(t, AssessmentPassed, assessment.State)
	require.Equal(t, Coverage{Good: 1, AlternativeValid: 1, CriticalBad: 2, IntendedNegative: 2}, assessment.Observed)
	require.Len(t, assessment.Observations, 4)
	for i, observation := range assessment.Observations {
		require.Equal(t, Observed, observation.State)
		require.NotNil(t, observation.Result)
		require.Equal(t, "check", observation.Result.Name)
		require.Equal(t, models.GraderKindFile, observation.Result.Type)
		require.Equal(t, *document.Cases[i].Checks[0].ExpectedPassed, observation.Result.Passed)
		require.True(t, *observation.Agreement)
		require.NotEmpty(t, observation.Result.Feedback)
		if i < 2 {
			require.Equal(t, 1.0, observation.Result.Score)
		} else {
			require.Less(t, observation.Result.Score, 1.0)
		}
	}
	require.Equal(t, 4, report.Domains[0].ObservedCases)
	require.Equal(t, 4, report.Domains[0].ExpectedChecks)
	require.Equal(t, 1.0, *report.Domains[0].Agreement)
	require.Equal(t, AssessmentNotAssessed, report.Calibration.State)
	require.Zero(t, report.Calibration.Executions)
	require.Nil(t, report.Calibration.Credits)
	require.Nil(t, report.Calibration.Usage)
	require.False(t, report.Calibration.ConfidenceSupported)
	for _, binding := range report.Bindings {
		require.Equal(t, "verified", binding.State)
	}
	after, err := os.ReadFile(filepath.Join(root, document.Cases[0].Snapshot))
	require.NoError(t, err)
	require.Equal(t, before, after)
	data := string(marshalReferenceTest(t, report))
	require.Contains(t, data, `"kind":"waza.grader-assurance"`)
	require.Contains(t, data, `"credits":null`)
	require.Contains(t, data, `"usage":null`)
	require.Contains(t, data, `"scope":"eval","grader":"check"`)
	require.NotContains(t, data, `"workspace_dir"`)
	require.NotContains(t, data, os.TempDir())
}

func TestVerifyUnlabelledNativeTaskCannotPass(t *testing.T) {
	request, _, _ := fileVerificationFixture(t)
	request.Tasks["unlabelled"] = &models.TestCase{
		TestID: "unlabelled", Requirements: []models.Requirement{{
			ID: "other", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}},
		}},
	}

	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentNotAssessed, report.State)
	require.Len(t, report.Requirements, 2)
	require.Equal(t, "unlabelled", report.Requirements[1].TaskID)
	require.Equal(t, AssessmentNotAssessed, report.Requirements[1].State)
	require.Empty(t, report.Requirements[1].Observations)
}

func TestVerifySelectedTaskWithoutRequirementsCannotPass(t *testing.T) {
	request, _, _ := fileVerificationFixture(t)
	request.Tasks["unmapped"] = &models.TestCase{TestID: "unmapped"}
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentNotAssessed, report.State)
	require.Equal(t, "requirement_uncovered", report.Reason)
	require.Len(t, report.Requirements, 1)
	require.Equal(t, AssessmentPassed, report.Requirements[0].State)
	require.Len(t, report.Requirements[0].Observations, 4)
}

func TestVerifyInvalidAndOperationalStatesSurviveLaterDisagreements(t *testing.T) {
	for _, first := range []string{"binding", "malformed", "directory"} {
		t.Run(first, func(t *testing.T) {
			request, document, root := fileVerificationFixture(t)
			request.Spec.Graders[0].Parameters = models.FileGraderParameters{MustExist: []string{"state.txt"}}
			config, err := evidence.JSONDigest(request.Spec)
			require.NoError(t, err)
			declaration, err := evidence.JSONDigest(request.Spec.Graders[0])
			require.NoError(t, err)
			document.EvalResolvedConfigSHA256 = config.SHA256
			for i := range document.Cases {
				document.Cases[i].Checks[0].GraderDeclarationSHA256 = declaration.SHA256
			}
			expected := AssessmentInvalid
			switch first {
			case "binding":
				document.Cases[0].TaskDeclarationSHA256 = strings.Repeat("b", 64)
			case "malformed":
				require.NoError(t, os.WriteFile(filepath.Join(root, document.Cases[0].Snapshot), []byte("{"), 0o600))
			case "directory":
				document.Cases[0].Snapshot = "directory"
				require.NoError(t, os.Mkdir(filepath.Join(root, "directory"), 0o700))
				expected = AssessmentError
			}
			rebindSyntheticReview(t, &request, document)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, expected, report.State)
			require.Equal(t, expected, report.Requirements[0].State)
			require.Equal(t, expected, report.Domains[0].State)
			for _, observation := range report.Requirements[0].Observations[2:] {
				require.Equal(t, Observed, observation.State)
				require.True(t, observation.Result.Passed)
				require.False(t, *observation.Agreement)
			}
		})
	}
}

func TestVerifyUnavailableArtifactIsInsufficientBeforeLocatorResolution(t *testing.T) {
	for _, availability := range []string{"unavailable", "digest_only", "not_requested"} {
		t.Run(availability, func(t *testing.T) {
			request, document, root := fileVerificationFixture(t)
			name := filepath.Join(root, document.Cases[0].Snapshot)
			data, err := os.ReadFile(name)
			require.NoError(t, err)
			snap, err := snapshot.ParseSnapshot(data, "synthetic test")
			require.NoError(t, err)
			for i := range snap.Evidence.Artifacts {
				artifact := &snap.Evidence.Artifacts[i]
				if artifact.ID != "workspace-file/state.txt" {
					continue
				}
				artifact.Availability = availability
				artifact.Document, artifact.Pointer = "", ""
				artifact.ContentDigest, artifact.SourceDigest = nil, nil
				artifact.Completeness, artifact.Reason = "unknown", "synthetic unavailable content"
				if availability == "digest_only" {
					artifact.SourceDigest = &models.EvidenceDigest{Encoding: "source-bytes", SHA256: strings.Repeat("b", 64)}
				}
			}
			require.NoError(t, evidence.Seal(snap.Evidence))
			document.Cases[0].ManifestSHA256 = snap.Evidence.SHA256
			require.NoError(t, os.WriteFile(name, marshalReferenceTest(t, snap), 0o600))
			rebindSyntheticReview(t, &request, document)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, AssessmentInsufficient, report.State)
			observation := report.Requirements[0].Observations[0]
			require.Equal(t, InsufficientEvidence, observation.State)
			require.Equal(t, "evidence_content_unavailable", observation.Reason)
			require.Nil(t, observation.Result)
			require.Nil(t, observation.Agreement)
		})
	}
}
func TestVerifyDeclarationDriftPreservesActualObservation(t *testing.T) {
	request, document, _ := fileVerificationFixture(t)
	document.Cases[2].TaskDeclarationSHA256 = strings.Repeat("b", 64)
	document.Cases[2].Checks[0].GraderDeclarationSHA256 = strings.Repeat("b", 64)
	rebindSyntheticReview(t, &request, document)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentInvalid, report.State)
	observation := report.Requirements[0].Observations[2]
	require.Equal(t, Observed, observation.State)
	require.False(t, observation.Result.Passed)
	require.True(t, *observation.Agreement)
	require.Equal(t, "mismatch", observation.Bindings[0].State)
	require.Equal(t, "mismatch", observation.Bindings[1].State)
}

func TestVerifyUnreviewedAndUnavailableProvenanceNeverPass(t *testing.T) {
	request, document, _ := fileVerificationFixture(t)
	for _, candidate := range []struct {
		name   string
		mutate func(*VerifyRequest, *ReferenceDocument)
		state  AssessmentState
		reason string
	}{
		{"not accepted", func(r *VerifyRequest, _ *ReferenceDocument) { r.Acceptance.AcceptCurrentDecision = false }, AssessmentNotAssessed, "review_not_eligible"},
		{"missing decision", func(r *VerifyRequest, _ *ReferenceDocument) { r.Review.Decision = nil }, AssessmentNotAssessed, "review_not_eligible"},
		{"unreviewed", func(r *VerifyRequest, _ *ReferenceDocument) { r.Review.Decision.State = ReviewUnreviewed }, AssessmentNotAssessed, "review_not_eligible"},
		{"revoked", func(r *VerifyRequest, _ *ReferenceDocument) { r.Review.Decision.State = ReviewRevoked }, AssessmentNotAssessed, "review_not_eligible"},
		{"future decision", func(r *VerifyRequest, _ *ReferenceDocument) { r.Review.Decision.ReviewedAt = r.Now.Add(time.Second) }, AssessmentInvalid, "invalid_review"},
		{"missing executable", func(_ *VerifyRequest, d *ReferenceDocument) { d.ImplementationExecutableSHA256 = nil }, AssessmentNotAssessed, "provenance_unavailable"},
		{"wrong executable", func(_ *VerifyRequest, d *ReferenceDocument) {
			d.ImplementationExecutableSHA256 = new(strings.Repeat("b", 64))
		}, AssessmentInvalid, "binding_mismatch"},
		{"wrong eval source", func(r *VerifyRequest, _ *ReferenceDocument) { r.EvalSource = []byte("other source") }, AssessmentInvalid, "binding_mismatch"},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			copyRequest := request
			copyDocument, err := request.References.Document()
			require.NoError(t, err)
			rebindSyntheticReview(t, &copyRequest, copyDocument)
			candidate.mutate(&copyRequest, &copyDocument)
			if candidate.name == "missing executable" || candidate.name == "wrong executable" {
				rebindSyntheticReview(t, &copyRequest, copyDocument)
			}
			report, err := Verify(t.Context(), copyRequest)
			require.NoError(t, err)
			require.Equal(t, candidate.state, report.State)
			require.Equal(t, candidate.reason, report.Reason)
			require.NotEqual(t, AssessmentPassed, report.Requirements[0].State)
		})
	}
	document.Calibration = &CalibrationPlan{Protocol: AgreementProtocol, Model: "deterministic-test-model", MaxJudgeExecutions: 4}
	rebindSyntheticReview(t, &request, document)
	request.Calibrate = true
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentNotAssessed, report.State)
	require.Equal(t, "calibration_executor_unavailable", report.Calibration.Reason)
	require.Zero(t, report.Calibration.Executions)
	require.Nil(t, report.Calibration.Credits)
}

func TestVerifyCriticalFalseAcceptanceCannotAverageAway(t *testing.T) {
	request, document, root := fileVerificationFixture(t)
	document.Domains[0].MinimumAgreement = 0.25
	data, err := os.ReadFile(filepath.Join(root, document.Cases[2].Snapshot))
	require.NoError(t, err)
	snap, err := snapshot.ParseSnapshot(data, "synthetic test")
	require.NoError(t, err)
	snap.WorkspaceFiles[0].Content = "state: ready\n"
	snap.WorkspaceFiles[0].SHA256 = byteSHA256([]byte(snap.WorkspaceFiles[0].Content))
	for i := range snap.Evidence.Artifacts {
		if snap.Evidence.Artifacts[i].ID == "workspace-file/state.txt" {
			snap.Evidence.Artifacts[i].ContentDigest, err = evidence.JSONDigest(snap.WorkspaceFiles[0])
			require.NoError(t, err)
		}
	}
	require.NoError(t, evidence.Seal(snap.Evidence))
	document.Cases[2].ManifestSHA256 = snap.Evidence.SHA256
	require.NoError(t, os.WriteFile(filepath.Join(root, document.Cases[2].Snapshot), marshalReferenceTest(t, snap), 0o600))
	rebindSyntheticReview(t, &request, document)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentFailed, report.State)
	require.Equal(t, "critical_false_acceptance", report.Requirements[0].Reason)
	require.True(t, report.Requirements[0].Observations[2].Result.Passed)
	require.False(t, *report.Requirements[0].Observations[2].Agreement)
	require.Equal(t, 0.75, *report.Domains[0].Agreement)
}

func TestVerifyMissingAndTamperedReferenceEvidence(t *testing.T) {
	for _, candidate := range []struct {
		name   string
		mutate func(*testing.T, string, *snapshot.Snapshot)
		state  ObservationState
	}{
		{"missing file", func(t *testing.T, name string, _ *snapshot.Snapshot) { require.NoError(t, os.Remove(name)) }, InsufficientEvidence},
		{"tampered content", func(t *testing.T, name string, s *snapshot.Snapshot) {
			s.WorkspaceFiles[0].Content = "state: wrong"
			require.NoError(t, os.WriteFile(name, marshalReferenceTest(t, s), 0o600))
		}, InsufficientEvidence},
		{"malformed JSON", func(t *testing.T, name string, _ *snapshot.Snapshot) {
			require.NoError(t, os.WriteFile(name, []byte(`{`), 0o600))
		}, Invalid},
		{"missing manifest", func(t *testing.T, name string, s *snapshot.Snapshot) {
			s.Evidence = nil
			require.NoError(t, os.WriteFile(name, marshalReferenceTest(t, s), 0o600))
		}, Invalid},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			request, document, root := fileVerificationFixture(t)
			name := filepath.Join(root, document.Cases[0].Snapshot)
			data, err := os.ReadFile(name)
			require.NoError(t, err)
			snap, err := snapshot.ParseSnapshot(data, "synthetic test")
			require.NoError(t, err)
			candidate.mutate(t, name, snap)
			report, err := Verify(t.Context(), request)
			require.NoError(t, err)
			require.NotEqual(t, AssessmentPassed, report.State)
			require.Equal(t, candidate.state, report.Requirements[0].Observations[0].State)
			require.Nil(t, report.Requirements[0].Observations[0].Agreement)
			require.Nil(t, report.Requirements[0].Observations[0].Result)
		})
	}
}

func TestVerifyAbsenceCannotBeManufacturedBySubsetRestoration(t *testing.T) {
	request, document, _ := fileVerificationFixture(t)
	request.Spec.Graders[0].Parameters = models.FileGraderParameters{MustNotExist: []string{"unpreserved.txt"}}
	decl, err := evidence.JSONDigest(request.Spec.Graders[0])
	require.NoError(t, err)
	resolved, err := evidence.JSONDigest(request.Spec)
	require.NoError(t, err)
	document.EvalResolvedConfigSHA256 = resolved.SHA256
	for i := range document.Cases {
		document.Cases[i].Checks[0].GraderDeclarationSHA256 = decl.SHA256
	}
	rebindSyntheticReview(t, &request, document)
	report, err := Verify(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentInsufficient, report.State)
	for _, observation := range report.Requirements[0].Observations {
		require.Equal(t, InsufficientEvidence, observation.State)
		require.Equal(t, "required_file_reference_missing", observation.Reason)
		require.Nil(t, observation.Result)
	}
}

func TestVerifyCancellationAndNativeSelectionErrors(t *testing.T) {
	request, _, _ := fileVerificationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	report, err := Verify(ctx, request)
	require.NoError(t, err)
	require.Equal(t, AssessmentError, report.State)
	require.Equal(t, OperationalError, report.Requirements[0].Observations[0].State)
	for _, mutate := range []func(*VerifyRequest){
		func(r *VerifyRequest) { r.References = nil },
		func(r *VerifyRequest) { r.Spec = nil },
		func(r *VerifyRequest) { r.EvalSource = nil },
		func(r *VerifyRequest) { r.Now = time.Time{} },
		func(r *VerifyRequest) { r.SnapshotRoot = nil },
		func(r *VerifyRequest) { r.Tasks = nil },
	} {
		copyRequest := request
		mutate(&copyRequest)
		_, err := Verify(t.Context(), copyRequest)
		require.Error(t, err)
	}
}

func TestDocumentPointerResolutionUsesActualOrdinals(t *testing.T) {
	value := map[string]any{"files": []any{map[string]any{"a/b~c": json.Number("9007199254740993")}}}
	actual, err := pointerValue(value, "/files/0/a~1b~0c")
	require.NoError(t, err)
	require.Equal(t, json.Number("9007199254740993"), actual)
	for _, invalid := range []string{"", "/files/01", "/files/-1", "/files/1", "/files/0/missing", "/files/0/a~2b", "/files/0/a~1b~0c/child"} {
		_, err := pointerValue(value, invalid)
		require.Error(t, err)
	}
}
