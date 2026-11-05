package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/preflight"
	"github.com/microsoft/waza/internal/registry"
	"github.com/stretchr/testify/require"
)

func writeLockedAssurancePreset(t *testing.T, specPath string, preset []byte) (string, string, *models.Lockfile) {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("WAZA_MODULE_CACHE", cache)
	ref := "github.com/example/assurance/check.yaml@v1"
	commit := strings.Repeat("a", 40)
	module := filepath.Join(cache, "github.com", "example", "assurance", commit)
	require.NoError(t, os.MkdirAll(module, 0o700))
	presetPath := filepath.Join(module, "check.yaml")
	require.NoError(t, os.WriteFile(presetPath, preset, 0o600))
	digest, err := registry.DigestDirectory(module)
	require.NoError(t, err)
	lock := models.NewLockfile()
	lock.UpsertGrader(models.LockfileGrader{
		Ref: ref, Commit: commit, Digest: digest, URL: "https://not-fetched.invalid/assurance.git",
	})
	require.NoError(t, models.WriteLockfile(filepath.Join(filepath.Dir(specPath), models.LockfileName), lock))
	return ref, presetPath, lock
}

func TestAssureLockedPresetNativeAuthoredPass(t *testing.T) {
	fixture := writeCalibrationCLIFixture(t)
	source, err := os.ReadFile(fixture.spec)
	require.NoError(t, err)
	inline := []byte("    type: prompt\n    config:\n      prompt: independent reviewed rubric override\n      rubric: reviewed.md\n      mode: independent")
	text := []byte("    type: text\n    config:\n      contains: ['state: ready']\n      not_contains: [forbidden]")
	require.Contains(t, string(source), string(inline))
	source = bytes.Replace(source, inline, text, 1)
	require.NoError(t, os.WriteFile(fixture.spec, source, 0o600))
	resolved, err := models.LoadEvalSpec(fixture.spec)
	require.NoError(t, err)
	resolvedDigest, err := evidence.JSONDigest(resolved)
	require.NoError(t, err)
	declarationDigest, err := evidence.JSONDigest(resolved.Graders[0])
	require.NoError(t, err)
	ref, _, _ := writeLockedAssurancePreset(t, fixture.spec, []byte("name: cached\ntype: text\nconfig:\n  contains: ['state: ready']\n  not_contains: [forbidden]\n"))
	source = bytes.Replace(source, text, []byte("    ref: '"+ref+"'"), 1)
	require.NoError(t, os.WriteFile(fixture.spec, source, 0o600))
	fixture.document.EvalSourceSHA256 = calibrationCLIHash(source)
	fixture.document.EvalResolvedConfigSHA256 = resolvedDigest.SHA256
	fixture.document.Calibration = nil
	for i := range fixture.document.Cases {
		check := &fixture.document.Cases[i].Checks[0]
		check.GraderDeclarationSHA256 = declarationDigest.SHA256
		check.RubricContentSHA256 = nil
	}
	fixture.save(t)
	inspection := preflight.Inspect(fixture.spec, preflight.Options{})
	require.True(t, inspection.Complete, "%+v", inspection.Diagnostics)
	require.False(t, inspection.Failed(false), "%+v", inspection.Diagnostics)

	cmd := newAssureCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{fixture.spec, "--references", fixture.labels, "--review", fixture.review, "--accept-review-source", "synthetic-source"})
	require.NoError(t, cmd.Execute(), "%s", output.String())
	var report assurance.Report
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Equal(t, assurance.AssessmentPassed, report.State)
	require.True(t, report.Review.Eligible)
	require.Len(t, report.Requirements, 1)
	require.Len(t, report.Requirements[0].Observations, 4)
	for _, observation := range report.Requirements[0].Observations {
		require.Equal(t, assurance.Observed, observation.State)
		require.NotNil(t, observation.Result)
	}
	require.Zero(t, report.Calibration.Executions)
	require.Nil(t, report.Calibration.Usage)
	require.Nil(t, report.Calibration.Credits)
}

func TestLoadAssuranceEvalSpecOfflineAdmission(t *testing.T) {
	for _, name := range []string{"inline", "locked", "override", "missing-lock", "malformed-lock", "missing-entry", "missing-cache", "changed-cache", "bad-digest", "invalid-ref", "malformed-preset", "untrusted-program", "external-schema", "type-conflict", "missing-spec", "canceled"} {
		t.Run(name, func(t *testing.T) {
			specPath, _ := writeAssuranceCommandFixture(t)
			preset := []byte("name: cached\ntype: file\nconfig: {must_exist: [state.txt]}\n")
			if name == "untrusted-program" {
				preset = []byte("name: cached\ntype: program\nconfig: {command: echo}\n")
			}

			if name == "external-schema" {
				preset = []byte("name: cached\ntype: tool_constraint\nconfig:\n  expect_tools:\n    - tool: read\n      args:\n        value:\n          json_schema: {$ref: 'https://not-fetched.invalid/schema'}\n")
			}
			if name == "malformed-preset" {
				preset = []byte("type: [")
			}
			ref, presetPath, lock := writeLockedAssurancePreset(t, specPath, preset)
			source, err := os.ReadFile(specPath)
			require.NoError(t, err)
			inline := []byte("    type: file\n    config:\n      must_exist: [state.txt]")
			replacement := []byte("    ref: '" + ref + "'")
			if name == "invalid-ref" {
				replacement = []byte("    ref: '%'")
				lock.Graders[0].Ref = "%"
				require.NoError(t, models.WriteLockfile(filepath.Join(filepath.Dir(specPath), models.LockfileName), lock))
			}
			if name == "override" {
				replacement = append(replacement, []byte("\n    config:\n      must_not_exist: [forbidden.txt]")...)
			}
			if name == "type-conflict" {
				replacement = append(replacement, []byte("\n    type: text\n    config: {contains: [ready]}")...)
			}
			if name != "inline" {
				source = bytes.Replace(source, inline, replacement, 1)
				require.NoError(t, os.WriteFile(specPath, source, 0o600))
			}
			ctx := t.Context()
			switch name {
			case "missing-lock":
				require.NoError(t, os.Remove(filepath.Join(filepath.Dir(specPath), models.LockfileName)))
			case "malformed-lock":
				require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(specPath), models.LockfileName), []byte("schema_version: 99\n"), 0o600))
			case "missing-entry":
				lock.Graders = nil
				require.NoError(t, models.WriteLockfile(filepath.Join(filepath.Dir(specPath), models.LockfileName), lock))
			case "missing-cache":
				require.NoError(t, os.Remove(presetPath))
				require.NoError(t, os.Remove(filepath.Dir(presetPath)))
			case "changed-cache":
				inspection := preflight.Inspect(specPath, preflight.Options{})
				require.True(t, inspection.Complete, "%+v", inspection.Diagnostics)
				require.False(t, inspection.Failed(false), "%+v", inspection.Diagnostics)
				require.NoError(t, os.WriteFile(presetPath, []byte("different"), 0o600))
			case "bad-digest":
				lock.Graders[0].Digest = "sha256:" + strings.Repeat("b", 64)
				require.NoError(t, models.WriteLockfile(filepath.Join(filepath.Dir(specPath), models.LockfileName), lock))
			case "missing-spec":
				require.NoError(t, os.Remove(specPath))
			case "canceled":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			}
			sourceBefore, sourceErr := os.ReadFile(specPath)
			presetBefore, presetErr := os.ReadFile(presetPath)
			spec, err := loadAssuranceEvalSpec(ctx, specPath)
			sourceAfter, afterSourceErr := os.ReadFile(specPath)
			presetAfter, afterPresetErr := os.ReadFile(presetPath)
			require.Equal(t, sourceBefore, sourceAfter)
			require.Equal(t, presetBefore, presetAfter)
			require.Equal(t, os.IsNotExist(sourceErr), os.IsNotExist(afterSourceErr))
			require.Equal(t, os.IsNotExist(presetErr), os.IsNotExist(afterPresetErr))
			if name != "inline" && name != "locked" && name != "override" {
				require.Error(t, err)
				require.Nil(t, spec)
				if name == "untrusted-program" {
					require.True(t, registry.IsProgramGraderTrustError(err), "%v", err)
				}
				if name == "changed-cache" || name == "bad-digest" {
					require.ErrorContains(t, err, "digest mismatch")
				}
				if name == "canceled" {
					require.ErrorIs(t, err, context.Canceled)
				}
				return
			}
			require.NoError(t, err)
			require.Equal(t, models.GraderKindFile, spec.Graders[0].Kind)
			require.Empty(t, spec.Graders[0].Ref)
			require.Equal(t, "check", spec.Graders[0].Identifier)
			params, ok := spec.Graders[0].Parameters.(models.FileGraderParameters)
			require.True(t, ok)
			require.Equal(t, []string{"state.txt"}, params.MustExist)
			if name == "override" {
				require.Equal(t, []string{"forbidden.txt"}, params.MustNotExist)
			}
			params.MustExist[0] = "mutated-in-memory"
			again, err := loadAssuranceEvalSpec(ctx, specPath)
			require.NoError(t, err)
			original, ok := again.Graders[0].Parameters.(models.FileGraderParameters)
			require.True(t, ok)
			require.Equal(t, []string{"state.txt"}, original.MustExist)
		})
	}
}

func TestAssureCalibrateLockedPresetResolvedRequestWithoutExecution(t *testing.T) {
	fixture := writeCalibrationCLIFixture(t)
	preset := []byte("name: cached\ntype: prompt\nconfig:\n  prompt: independent reviewed rubric override\n  rubric: reviewed.md\n  mode: independent\n")
	ref, _, _ := writeLockedAssurancePreset(t, fixture.spec, preset)
	source, err := os.ReadFile(fixture.spec)
	require.NoError(t, err)
	inline := []byte("    type: prompt\n    config:\n      prompt: independent reviewed rubric override\n      rubric: reviewed.md\n      mode: independent")
	require.Contains(t, string(source), string(inline))
	source = bytes.Replace(source, inline, []byte("    ref: '"+ref+"'"), 1)
	require.NoError(t, os.WriteFile(fixture.spec, source, 0o600))
	fixture.document.EvalSourceSHA256 = calibrationCLIHash(source)
	fixture.save(t)
	called := false
	options := calibrationCLIOptions()
	options.Calibrate = func(_ context.Context, request assurance.CalibrateRequest) (*assurance.Report, error) {
		called = true
		require.Empty(t, request.Spec.Graders[0].Ref)
		require.Equal(t, models.GraderKindPrompt, request.Spec.Graders[0].Kind)
		resolved, err := evidence.JSONDigest(request.Spec)
		require.NoError(t, err)
		require.Equal(t, fixture.document.EvalResolvedConfigSHA256, resolved.SHA256)
		require.Equal(t, source, request.EvalSource)
		require.Len(t, request.Tasks, 1)
		return nil, errors.New("synthetic request inspection complete")
	}
	options.EngineFactory = func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
		t.Fatal("request inspection must not initialize any engine")
		return nil, errors.New("engine forbidden")
	}
	cmd := newAssureCalibrateCommandWithOptions(options)
	cmd.SetArgs(fixture.args())
	calibrationCLIExit(t, cmd.Execute(), 2)
	require.True(t, called)
}

func TestAssuranceDirectTypedReferencesRemainUnsupported(t *testing.T) {
	for _, ref := range []string{"github.com/example/assurance/check.yaml@v1", " ", "file:grader.yaml"} {
		t.Run(ref, func(t *testing.T) {
			fixture := writeCalibrationCLIFixture(t)
			spec, err := models.LoadEvalSpec(fixture.spec)
			require.NoError(t, err)
			spec.Graders[0].Ref = ref
			digest, err := evidence.JSONDigest(spec)
			require.NoError(t, err)
			fixture.document.EvalResolvedConfigSHA256 = digest.SHA256
			declaration, err := evidence.JSONDigest(spec.Graders[0])
			require.NoError(t, err)
			for i := range fixture.document.Cases {
				fixture.document.Cases[i].Checks[0].GraderDeclarationSHA256 = declaration.SHA256
			}
			fixture.save(t)
			labels, err := os.ReadFile(fixture.labels)
			require.NoError(t, err)
			references, err := assurance.ParseReferences(labels)
			require.NoError(t, err)
			reviewBytes, err := os.ReadFile(fixture.review)
			require.NoError(t, err)
			review, err := assurance.ParseReview(reviewBytes)
			require.NoError(t, err)
			source, err := os.ReadFile(fixture.spec)
			require.NoError(t, err)
			task, err := models.LoadTestCase(filepath.Join(filepath.Dir(fixture.spec), "task.yaml"))
			require.NoError(t, err)
			root, err := os.OpenRoot(filepath.Dir(fixture.spec))
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, root.Close()) })
			request := assurance.VerifyRequest{
				References: references, Review: review,
				Acceptance: assurance.ReviewSourceAcceptance{SourceID: "synthetic-source", AcceptCurrentDecision: true},
				Now:        time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
				EvalSource: source, Spec: spec, Tasks: map[string]*models.TestCase{task.TestID: task}, SnapshotRoot: root,
			}
			report, err := assurance.Verify(t.Context(), request)
			require.NoError(t, err)
			require.NotEqual(t, assurance.AssessmentPassed, report.State)
			require.Len(t, report.Requirements, 1)
			for _, observation := range report.Requirements[0].Observations {
				require.Equal(t, "unexpanded_grader_reference", observation.Reason)
				require.Equal(t, assurance.NotAssessed, observation.State)
				require.Nil(t, observation.Result)
			}
			request.Calibrate = true
			calibrated, err := assurance.Calibrate(t.Context(), assurance.CalibrateRequest{
				VerifyRequest: request, RubricRoot: root,
				EngineFactory: func(string, func(execution.ExecutionDiagnostic) error) (assurance.CalibrationEngine, error) {
					t.Fatal("direct typed references must not launch an engine")
					return nil, errors.New("engine forbidden")
				},
				PaidCallNotice: func(assurance.CalibrationPlan, int) error {
					t.Fatal("direct typed references must not reach paid admission")
					return errors.New("notice forbidden")
				},
			})
			require.NoError(t, err)
			require.NotEqual(t, assurance.AssessmentPassed, calibrated.State)
			require.Zero(t, calibrated.Calibration.Executions)
			require.Nil(t, calibrated.Calibration.Usage)
			require.Nil(t, calibrated.Calibration.Credits)
			require.Equal(t, ref, spec.Graders[0].Ref)
		})
	}
}
