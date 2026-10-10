package assurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func preservedVerificationFixture(t *testing.T) (VerifyRequest, ReferenceDocument, string) {
	t.Helper()
	eventConfig := models.GraderConfig{
		Identifier: "check", Kind: models.GraderKindToolCalls,
		Parameters: models.ToolCallsGraderParameters{RequiredTools: []string{"write"}, ForbiddenTools: []string{"destroy"}},
	}
	fileConfig := models.GraderConfig{
		Identifier: "state", Kind: models.GraderKindFile,
		Parameters: models.FileGraderParameters{ContentPatterns: []models.FileContentPatternParameters{{
			Path: "state.txt", MustMatch: []string{"state: ready"},
		}}},
	}
	task := &models.TestCase{TestID: "synthetic-finite-task", Requirements: []models.Requirement{{
		ID: "outcome", Checks: []models.RequirementCheck{{Scope: "eval", Grader: "check"}, {Scope: "eval", Grader: "state"}},
	}}}
	spec := &models.EvalSpec{Graders: []models.GraderConfig{eventConfig, fileConfig}}
	path := t.TempDir()
	root, err := os.OpenRoot(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, root.Close()) })
	source := []byte("synthetic complete finite native declaration source, not a historical capture")
	executable, err := CurrentExecutableSHA256()
	require.NoError(t, err)
	specDigest, err := evidence.JSONDigest(spec)
	require.NoError(t, err)
	taskDigest, err := evidence.JSONDigest(task)
	require.NoError(t, err)
	document := admissionDocument()
	document.EvalSourceSHA256, document.EvalResolvedConfigSHA256 = byteSHA256(source), specDigest.SHA256
	document.ImplementationExecutableSHA256 = new(executable)
	document.Cases = nil
	for _, candidate := range []struct {
		id             string
		classification CaseClassification
		content        string
		forbidden      bool
	}{
		{"good", CaseGood, "state: ready\n", false},
		{"alternative", CaseAlternative, "# alternative valid route\nstate: ready\n", false},
		{"wrong-state", CaseCriticalBad, "state: wrong\n", false},
		{"forbidden-action", CaseCriticalBad, "state: ready\n", true},
	} {
		tape := []any{preservedEvent("write", 1, map[string]any{"path": "state.txt"})}
		if candidate.forbidden {
			tape = append(tape, preservedEvent("destroy", 2, map[string]any{}))
		}
		fixture := newPreservedFixture(t, eventConfig.Kind, eventConfig.Parameters, tape,
			[]snapshot.WorkspaceFile{preservedFile("state.txt", candidate.content)})
		name := candidate.id + ".json"
		require.NoError(t, os.WriteFile(filepath.Join(path, name), fixture.input.SnapshotBytes, 0o600))
		label := ReferenceCase{
			ID: candidate.id, ScenarioID: "synthetic-native-target", Domain: "cli", Classification: candidate.classification,
			TaskID: task.TestID, TaskDeclarationSHA256: taskDigest.SHA256, Snapshot: name, ManifestSHA256: fixture.input.ManifestSHA256,
		}
		for i, config := range spec.Graders {
			digest, err := evidence.JSONDigest(config)
			require.NoError(t, err)
			expected := !candidate.forbidden
			if i == 1 {
				expected = candidate.id != "wrong-state"
			}
			label.Checks = append(label.Checks, ReferenceCheck{
				RequirementID: "outcome", Check: task.Requirements[0].Checks[i], GraderDeclarationSHA256: digest.SHA256,
				ExpectedPassed: new(expected), Evidence: fixture.input.Evidence,
			})
		}
		document.Cases = append(document.Cases, label)
	}
	request := VerifyRequest{
		Now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), EvalSource: source, Spec: spec,
		Tasks: map[string]*models.TestCase{task.TestID: task}, SnapshotRoot: root,
		Acceptance: ReviewSourceAcceptance{SourceID: "synthetic-source", AcceptCurrentDecision: true},
	}
	rebindSyntheticReview(t, &request, document)
	return request, document, path
}

func TestVerifyPreservedActualNativeResultsAndExport(t *testing.T) {
	request, document, root := preservedVerificationFixture(t)
	before, err := os.ReadFile(filepath.Join(root, document.Cases[0].Snapshot))
	require.NoError(t, err)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentPassed, report.State)
	require.Equal(t, PreservedReportVersion, report.SchemaVersion)
	require.Equal(t, PreservedAssessmentMode, report.AssessmentMode)
	require.Len(t, report.Requirements, 2)
	for _, assessment := range report.Requirements {
		require.Equal(t, AssessmentPassed, assessment.State)
		require.Equal(t, Coverage{Good: 1, AlternativeValid: 1, CriticalBad: 2, IntendedNegative: 1}, assessment.Observed)
		for _, observation := range assessment.Observations {
			require.Equal(t, Observed, observation.State)
			require.True(t, *observation.Agreement)
			require.Equal(t, observation.ExpectedPassed, observation.Result.Passed)
			require.NotEmpty(t, observation.Result.Feedback)
			require.NotContains(t, observation.Result.Details, "workspace_dir")
			require.Equal(t, "verified", observation.Bindings[3].State)
		}
	}
	// Critical-bad cases can legitimately pass a different scoped check.
	require.True(t, report.Requirements[0].Observations[2].Result.Passed)
	require.False(t, report.Requirements[1].Observations[2].Result.Passed)
	require.False(t, report.Requirements[0].Observations[3].Result.Passed)
	require.True(t, report.Requirements[1].Observations[3].Result.Passed)
	require.Equal(t, 4, report.Domains[0].ObservedCases)
	require.Equal(t, 8, report.Domains[0].ObservedChecks)
	require.Equal(t, 8, report.Domains[0].Agreements)
	require.Equal(t, 1.0, *report.Domains[0].Agreement)
	require.Equal(t, CalibrationReport{State: AssessmentNotAssessed, Reason: "calibration_not_selected"}, report.Calibration)
	encoded := marshalReferenceTest(t, report)
	require.NotContains(t, string(encoded), "execution_ledger")
	require.NotContains(t, string(encoded), root)
	parsed, err := ParsePreservedReport(encoded)
	require.NoError(t, err)
	require.JSONEq(t, string(encoded), string(marshalReferenceTest(t, parsed)))
	after, err := os.ReadFile(filepath.Join(root, document.Cases[0].Snapshot))
	require.NoError(t, err)
	require.Equal(t, before, after)
	exportPreservedReport(t, "positive.json", encoded)
	request.Acceptance.AcceptCurrentDecision = false
	require.NoError(t, os.Remove(filepath.Join(root, document.Cases[0].Snapshot)))
	nonpass, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentInsufficient, nonpass.State)
	require.Nil(t, nonpass.Requirements[0].Observations[0].Result)
	require.Nil(t, nonpass.Requirements[0].Observations[0].Agreement)
	encoded = marshalReferenceTest(t, nonpass)
	_, err = ParsePreservedReport(encoded)
	require.NoError(t, err)
	exportPreservedReport(t, "nonpass.json", encoded)
}

func exportPreservedReport(t *testing.T, name string, data []byte) {
	t.Helper()
	directory := os.Getenv("WAZA_PRESERVED_REPORT_DIR")
	if directory == "" {
		return
	}
	worktree, err := filepath.Abs("../..")
	require.NoError(t, err)
	cache := filepath.Join(worktree, ".cache")
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(worktree, directory)
	}
	relative, err := filepath.Rel(cache, directory)
	require.NoError(t, err)
	require.True(t, filepath.IsLocal(relative), "report export must remain under this worktree's .cache")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	root, err := os.OpenRoot(cache)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.NoError(t, root.MkdirAll(relative, 0o700))
	file, err := root.OpenFile(filepath.Join(relative, name), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = file.Write(data)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	readme, err := root.OpenFile(filepath.Join(relative, "README.txt"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, err = readme.WriteString("Actual returned VerifyPreserved 1.2 positive/nonpass reports from synthetic complete finite native artifact declarations and explicitly synthetic accepted review. Not authentic historical/provider completeness or human review. No live, paid or task-agent execution. Frozen 1.0/1.1 rejection controls remain a separate publication gate.\n")
	require.NoError(t, err)
	require.NoError(t, readme.Close())
	t.Logf("actual preserved report exported: %s", filepath.Join(directory, name))
}

func TestVerifyPreservedSourceReviewAndUnavailableGates(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *VerifyRequest, *ReferenceDocument, string)
		state  AssessmentState
	}{
		{"source mismatch", func(_ *testing.T, r *VerifyRequest, _ *ReferenceDocument, _ string) {
			r.EvalSource = []byte("different")
		}, AssessmentInvalid},
		{"config mismatch", func(_ *testing.T, r *VerifyRequest, _ *ReferenceDocument, _ string) { r.Spec.Graders[0].Weight = 2 }, AssessmentInvalid},
		{"missing executable", func(t *testing.T, r *VerifyRequest, d *ReferenceDocument, _ string) {
			d.ImplementationExecutableSHA256 = nil
			rebindSyntheticReview(t, r, *d)
		}, AssessmentNotAssessed},
		{"task mismatch", func(_ *testing.T, r *VerifyRequest, _ *ReferenceDocument, _ string) {
			r.Tasks["synthetic-finite-task"].DisplayName = "different"
		}, AssessmentInvalid},
		{"review not accepted", func(_ *testing.T, r *VerifyRequest, _ *ReferenceDocument, _ string) {
			r.Acceptance.AcceptCurrentDecision = false
		}, AssessmentNotAssessed},
		{"review labels mismatch", func(_ *testing.T, r *VerifyRequest, _ *ReferenceDocument, _ string) {
			r.Review.Decision.LabelsDigest[0] ^= 1
		}, AssessmentNotAssessed},
		{"missing snapshot", func(t *testing.T, _ *VerifyRequest, d *ReferenceDocument, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, d.Cases[0].Snapshot)))
		}, AssessmentInsufficient},
		{"manifest mismatch", func(t *testing.T, r *VerifyRequest, d *ReferenceDocument, _ string) {
			d.Cases[0].ManifestSHA256 = byteSHA256([]byte("other"))
			rebindSyntheticReview(t, r, *d)
		}, AssessmentInvalid},
		{"paid selector no adapter", func(t *testing.T, r *VerifyRequest, d *ReferenceDocument, root string) {
			r.Calibrate = true
			require.NoError(t, os.WriteFile(filepath.Join(root, d.Cases[0].Snapshot), []byte("malformed"), 0o600))
		}, AssessmentInsufficient},
		{"canceled", func(_ *testing.T, _ *VerifyRequest, _ *ReferenceDocument, _ string) {}, AssessmentError},
		{"unknown tape completeness", func(t *testing.T, r *VerifyRequest, d *ReferenceDocument, root string) {
			var raw map[string]any
			require.NoError(t, json.Unmarshal(mustPreservedRead(t, filepath.Join(root, d.Cases[0].Snapshot)), &raw))
			var manifest models.EvidenceManifest
			require.NoError(t, json.Unmarshal(marshalReferenceTest(t, raw["evidence"]), &manifest))
			manifest.Artifacts[0].Completeness = "unknown"
			manifest.Artifacts[0].Reason = "Synthetic unknown completeness control"
			require.NoError(t, evidence.Seal(&manifest))
			raw["evidence"] = manifest
			require.NoError(t, os.WriteFile(filepath.Join(root, d.Cases[0].Snapshot), marshalReferenceTest(t, raw), 0o600))
			d.Cases[0].ManifestSHA256 = manifest.SHA256
			rebindSyntheticReview(t, r, *d)
		}, AssessmentInsufficient},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, document, root := preservedVerificationFixture(t)
			test.mutate(t, &request, &document, root)
			ctx := t.Context()
			if test.name == "canceled" {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			report, err := VerifyPreserved(ctx, request)
			require.NoError(t, err)
			require.Equal(t, test.state, report.State)
			_, err = ParsePreservedReport(marshalReferenceTest(t, report))
			require.NoError(t, err)
			require.Zero(t, report.Calibration.Executions)
			require.Nil(t, report.Calibration.ExecutionLedger)
			require.Nil(t, report.Calibration.Usage)
			require.Nil(t, report.Calibration.Credits)
			if request.Calibrate {
				for _, requirement := range report.Requirements {
					for _, observation := range requirement.Observations {
						require.Equal(t, NotAssessed, observation.State)
						require.Nil(t, observation.Result)
						require.Nil(t, observation.Agreement)
					}
				}
			}
		})
	}
}

func mustPreservedRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func TestPreservedSelectedFileBudgetBoundaries(t *testing.T) {
	for _, test := range []struct {
		name         string
		count, bytes int
		lastExtra    int
		invalid      bool
	}{
		{"single exact 4 MiB", 1, preservedVerifyMaxFileBytes, 0, false},
		{"single 4 MiB plus one", 1, preservedVerifyMaxFileBytes, 1, true},
		{"aggregate exact 8 MiB", 4, preservedVerifyMaxTotalBytes / 4, 0, false},
		{"aggregate 8 MiB plus one", 4, preservedVerifyMaxTotalBytes / 4, 1, true},
		{"exact 256 paths", preservedVerifyMaxFileCount, 0, 0, false},
		{"257 paths", preservedVerifyMaxFileCount + 1, 0, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			params := models.FileGraderParameters{}
			files := []any{}
			for i := range test.count {
				path := fmt.Sprintf("file-%d.txt", i)
				size := test.bytes
				if i == test.count-1 {
					size += test.lastExtra
				}
				params.ContentPatterns = append(params.ContentPatterns, models.FileContentPatternParameters{Path: path})
				files = append(files, map[string]any{"path": path, "content": strings.Repeat("x", size)})
			}
			err := preservedSelectedFileBudget(marshalReferenceTest(t, map[string]any{"workspaceFiles": files}), params)
			if test.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	params := models.FileGraderParameters{MustExist: []string{"selected", "selected"}}
	raw := map[string]any{"workspaceFiles": []any{
		map[string]any{"path": "selected", "content": "valid"},
		map[string]any{"path": "unused", "content": strings.Repeat("x", preservedVerifyMaxFileBytes+1)},
		map[string]any{"path": "selected", "content": nil}, "invalid-unselected",
	}}
	require.NoError(t, preservedSelectedFileBudget(marshalReferenceTest(t, raw), params))
	require.Error(t, preservedSelectedFileBudget([]byte("malformed"), params))
	for _, data := range []string{"null", "[]", "{}", `{"workspaceFiles":null}`, `{"workspaceFiles":[{"path":null}]}`} {
		require.NoError(t, preservedSelectedFileBudget([]byte(data), params))
	}
	require.NoError(t, preservedSelectedFileBudget([]byte("malformed"), models.ToolCallsGraderParameters{}))
}

func TestVerifyPreservedCriticalFalseAcceptanceCannotUseDomainTolerance(t *testing.T) {
	request, document, root := preservedVerificationFixture(t)
	candidate := &document.Cases[2]
	var raw map[string]any
	require.NoError(t, json.Unmarshal(mustPreservedRead(t, filepath.Join(root, candidate.Snapshot)), &raw))
	file := preservedFile("state.txt", "# deceptive previously wrong state\nstate: ready\n")
	raw["workspaceFiles"] = []any{map[string]any{
		"path": file.Path, "content": file.Content, "sha256": file.SHA256, "redacted": false,
	}}
	var manifest models.EvidenceManifest
	require.NoError(t, json.Unmarshal(marshalReferenceTest(t, raw["evidence"]), &manifest))
	for i := range manifest.Artifacts {
		value, err := pointerValue(raw, manifest.Artifacts[i].Pointer)
		require.NoError(t, err)
		manifest.Artifacts[i].ContentDigest, err = evidence.JSONDigest(value)
		require.NoError(t, err)
	}
	require.NoError(t, evidence.Seal(&manifest))
	raw["evidence"] = manifest
	candidate.ManifestSHA256 = manifest.SHA256
	document.Domains[0].MinimumAgreement = 0.5
	require.NoError(t, os.WriteFile(filepath.Join(root, candidate.Snapshot), marshalReferenceTest(t, raw), 0o600))
	rebindSyntheticReview(t, &request, document)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, AssessmentFailed, report.State)
	require.Equal(t, "critical_false_acceptance", report.Reason)
	require.True(t, report.Requirements[1].Observations[2].Result.Passed)
	require.False(t, *report.Requirements[1].Observations[2].Agreement)
	require.Greater(t, *report.Domains[0].Agreement, report.Domains[0].MinimumAgreement)
	_, err = ParsePreservedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	report.State, report.Reason = AssessmentPassed, "corpus_agreement"
	_, err = ParsePreservedReport(marshalReferenceTest(t, report))
	require.Error(t, err)
}

func TestVerifyPreservedInputAndAuthoredSelection(t *testing.T) {
	request, _, _ := authoredVerificationFixture(t)
	report, err := VerifyPreserved(t.Context(), request)
	require.NoError(t, err)
	require.NotEqual(t, AssessmentPassed, report.State)
	for _, requirement := range report.Requirements {
		for _, observation := range requirement.Observations {
			require.Equal(t, NotAssessed, observation.State)
			require.Equal(t, "authored_output_not_selected", observation.Reason)
			require.Nil(t, observation.Result)
			require.Nil(t, observation.Agreement)
			require.NotEqual(t, "authored_finite_output", observation.SourceScope)
		}

	}
	_, err = ParsePreservedReport(marshalReferenceTest(t, report))
	require.NoError(t, err)
	request.Spec = nil
	_, err = VerifyPreserved(t.Context(), request)
	require.Error(t, err)
	_, err = VerifyPreserved(t.Context(), VerifyRequest{})
	require.Error(t, err)
}

func TestVerifyPreservedActualCleanupFailureRetainsNativeResult(t *testing.T) {
	request, _, _ := preservedVerificationFixture(t)
	cleanupFailure := errors.New("synthetic cleanup failure after actual native Grade")
	closes := 0
	report, err := verifyPreserved(t.Context(), request, preservedMechanicalHooks{
		close: func(close func() error) error {
			closes++
			return errors.Join(close(), cleanupFailure)
		},
	})
	require.NoError(t, err)
	require.Equal(t, 4, closes)
	require.Equal(t, AssessmentError, report.State)
	require.Equal(t, Coverage{}, report.Requirements[1].Observed)
	for _, observation := range report.Requirements[1].Observations {
		require.Equal(t, OperationalError, observation.State)
		require.NotNil(t, observation.Result)
		require.Equal(t, models.GraderKindFile, observation.Result.Type)
		require.Equal(t, observation.ExpectedPassed, observation.Result.Passed)
		require.NotEmpty(t, observation.Result.Feedback)
		require.Nil(t, observation.Agreement)
		require.NotContains(t, observation.Result.Details, "workspace_dir")
	}
	require.Equal(t, 0, report.Domains[0].ObservedCases)
	require.Equal(t, 4, report.Domains[0].ObservedChecks)
	require.Equal(t, 4, report.Domains[0].Agreements)
	data := marshalReferenceTest(t, report)
	parsed, err := ParsePreservedReport(data)
	require.NoError(t, err)
	require.JSONEq(t, string(data), string(marshalReferenceTest(t, parsed)))
	require.Equal(t, AssessmentError, parsed.State)
	exportPreservedReport(t, "cleanup-failure.json", data)
}

func TestVerifyPreservedCurrentDeclarationNotHistoricalIdentity(t *testing.T) {
	for _, change := range []string{"changed captured declaration", "missing captured declaration"} {
		t.Run(change, func(t *testing.T) {
			request, document, root := preservedVerificationFixture(t)
			for i := range document.Cases {
				candidate := &document.Cases[i]
				var raw map[string]any
				require.NoError(t, json.Unmarshal(mustPreservedRead(t, filepath.Join(root, candidate.Snapshot)), &raw))
				var manifest models.EvidenceManifest
				require.NoError(t, json.Unmarshal(marshalReferenceTest(t, raw["evidence"]), &manifest))
				switch change {
				case "changed captured declaration":
					raw["nativeDeclaration"] = models.GraderConfig{
						Identifier: "historical-other-grader", Kind: models.GraderKindToolCalls,
						Parameters: models.ToolCallsGraderParameters{RequiredTools: []string{"historical-other-tool"}},
					}
					for j := range manifest.Artifacts {
						artifact := &manifest.Artifacts[j]
						if artifact.ID == "native-declaration" {
							digest, err := evidence.JSONDigest(raw["nativeDeclaration"])
							require.NoError(t, err)
							artifact.ContentDigest = digest
						}
					}
				case "missing captured declaration":
					delete(raw, "nativeDeclaration")
					manifest.Artifacts = slices.DeleteFunc(manifest.Artifacts, func(artifact models.EvidenceArtifact) bool {
						return artifact.ID == "native-declaration"
					})
					for j := range candidate.Checks {
						candidate.Checks[j].Evidence = slices.DeleteFunc(slices.Clone(candidate.Checks[j].Evidence), func(reference models.EvidenceReference) bool {
							return reference.ArtifactID == "native-declaration"
						})
					}
				}
				require.NoError(t, evidence.Seal(&manifest))
				candidate.ManifestSHA256 = manifest.SHA256
				raw["evidence"] = manifest
				require.NoError(t, os.WriteFile(filepath.Join(root, candidate.Snapshot), marshalReferenceTest(t, raw), 0o600))
			}
			rebindSyntheticReview(t, &request, document)
			report, err := VerifyPreserved(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, AssessmentPassed, report.State)
			require.Contains(t, strings.Join(report.Limitations, "\n"), "currently selected grader")
			for _, requirement := range report.Requirements {
				for _, observation := range requirement.Observations {
					require.Equal(t, "native_grader_declaration_json_v1", observation.Bindings[0].Domain)
					require.Equal(t, "verified", observation.Bindings[0].State)
					require.True(t, *observation.Agreement)
				}
			}
			_, err = ParsePreservedReport(marshalReferenceTest(t, report))
			require.NoError(t, err)
		})
	}
	t.Run("mismatched currently selected native parameters", func(t *testing.T) {
		request, document, _ := preservedVerificationFixture(t)
		request.Spec.Graders[0].Parameters = models.ToolCallsGraderParameters{RequiredTools: []string{"other-current-tool"}}
		// Keep the eval binding current to isolate the selected grader binding.
		digest, err := evidence.JSONDigest(request.Spec)
		require.NoError(t, err)
		document.EvalResolvedConfigSHA256 = digest.SHA256
		rebindSyntheticReview(t, &request, document)
		report, err := VerifyPreserved(t.Context(), request)
		require.NoError(t, err)
		require.Equal(t, AssessmentInvalid, report.State)
		for _, observation := range report.Requirements[0].Observations {
			require.Equal(t, Invalid, observation.State)
			require.Nil(t, observation.Result)
			require.Nil(t, observation.Agreement)
			require.Equal(t, "mismatch", observation.Bindings[0].State)
		}
		_, err = ParsePreservedReport(marshalReferenceTest(t, report))
		require.NoError(t, err)
	})
}
