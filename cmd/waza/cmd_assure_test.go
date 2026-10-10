package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func writeAssuranceCommandFixture(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(directory, "fixtures"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "fixtures", "state.txt"), []byte("ready"), 0o600))
	taskPath := filepath.Join(directory, "task.yaml")
	require.NoError(t, os.WriteFile(taskPath, []byte(`id: task
name: Finite state
inputs:
  prompt: Inspect the local state.
requirements:
  - id: state
    category: outcome
    description: The state file exists.
    checks:
      - scope: eval
        grader: check
`), 0o600))
	source := []byte(`schemaVersion: "2.0"
name: assurance-command-test
scenario: local-state
config:
  executor: mock
  model: harness
  trials_per_task: 1
  timeout_seconds: 10
graders:
  - name: check
    type: file
    config:
      must_exist: [state.txt]
metrics:
  - name: accuracy
    weight: 1
    threshold: 1
tasks: [task.yaml]
`)
	specPath := filepath.Join(directory, "eval.yaml")
	require.NoError(t, os.WriteFile(specPath, source, 0o600))
	spec, err := models.LoadEvalSpec(specPath)
	require.NoError(t, err)
	task, err := models.LoadTestCase(taskPath)
	require.NoError(t, err)
	specDigest, err := evidence.JSONDigest(spec)
	require.NoError(t, err)
	taskDigest, err := evidence.JSONDigest(task)
	require.NoError(t, err)
	declDigest, err := evidence.JSONDigest(spec.Graders[0])
	require.NoError(t, err)
	sourceDigest := sha256.Sum256(source)
	document := assurance.ReferenceDocument{
		Kind: assurance.ReferenceKind, SchemaVersion: assurance.ReferenceVersion,
		ID: "unreviewed-candidates", Version: "1",
		EvalSourceSHA256: hex.EncodeToString(sourceDigest[:]), EvalResolvedConfigSHA256: specDigest.SHA256,
		Domains: []assurance.DomainCriteria{{ID: "repository", MinimumCases: 4, MinimumAgreement: 1}},
		Cases: []assurance.ReferenceCase{{
			ID: "good", ScenarioID: "state", Domain: "repository", Classification: assurance.CaseGood,
			TaskID: "task", TaskDeclarationSHA256: taskDigest.SHA256,
			Snapshot: "missing-snapshot.json", ManifestSHA256: strings.Repeat("a", 64),
			Checks: []assurance.ReferenceCheck{{
				RequirementID: "state", Check: models.RequirementCheck{Scope: "eval", Grader: "check"},
				GraderDeclarationSHA256: declDigest.SHA256, ExpectedPassed: new(true),
				Evidence: []models.EvidenceReference{{
					Origin:     models.EvidenceOrigin{EvalID: "fixture", TaskID: "task", RunNumber: 1, AttemptCount: 1, PriorAttempts: "none"},
					ArtifactID: "workspace-file/state.txt",
				}},
			}},
		}},
	}
	data, err := json.Marshal(document)
	require.NoError(t, err)
	labelsPath := filepath.Join(directory, "labels.json")
	require.NoError(t, os.WriteFile(labelsPath, data, 0o600))
	return specPath, labelsPath
}

func TestAssureCommandReportsMissingEvidenceWithoutPassingOrBilling(t *testing.T) {
	specPath, labelsPath := writeAssuranceCommandFixture(t)
	outputPath := filepath.Join(t.TempDir(), "assurance.json")
	for _, calibrate := range []bool{false, true} {
		cmd := newAssureCommand()
		var output, diagnostics bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&diagnostics)
		args := []string{specPath, "--references", labelsPath, "--output", outputPath}
		if calibrate {
			args = append(args, "--calibrate")
		}
		cmd.SetArgs(args)
		err := cmd.Execute()
		var exit *ExitCodeError
		require.True(t, errors.As(err, &exit), "%v", err)
		require.Equal(t, 1, exit.Code)
		var report assurance.Report
		require.NoError(t, json.Unmarshal(output.Bytes(), &report))
		require.Equal(t, assurance.ReportKind, report.Kind)
		require.NotEqual(t, assurance.AssessmentPassed, report.State)
		require.False(t, report.Review.Eligible)
		require.False(t, report.Review.CurrentSourceAccepted)
		require.Nil(t, report.Calibration.Usage)
		require.Nil(t, report.Calibration.Credits)
		require.Zero(t, report.Calibration.Executions)
		require.Len(t, report.Requirements, 1)
		require.Equal(t, assurance.InsufficientEvidence, report.Requirements[0].Observations[0].State)
		require.Nil(t, report.Requirements[0].Observations[0].Result)
		saved, err := os.ReadFile(outputPath)
		require.NoError(t, err)
		require.Equal(t, output.Bytes(), saved)
		if calibrate {
			require.Contains(t, diagnostics.String(), "no paid calls")
		} else {
			require.Empty(t, diagnostics.String())
		}
	}
}

func TestAssureRequiresIndependentReviewSourceSelection(t *testing.T) {
	specPath, labelsPath := writeAssuranceCommandFixture(t)
	cmd := newAssureCommand()
	cmd.SetArgs([]string{specPath, "--references", labelsPath, "--accept-review-source", "source"})
	require.ErrorContains(t, cmd.Execute(), "requires an explicitly supplied --review")
	cmd = newAssureCommand()
	cmd.SetArgs([]string{specPath})
	require.ErrorContains(t, cmd.Execute(), `required flag(s) "references"`)
}

func TestAssureOfflineHelpAndUpdateExemption(t *testing.T) {
	cmd := newAssureCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), "unreviewed labels cannot pass")
	require.Contains(t, output.String(), "zero paid calls")
	require.False(t, shouldRunUpdateCheck(newAssureCommand(), false))
	root := newRootCommand()
	selected, _, err := root.Find([]string{"assure"})
	require.NoError(t, err)
	require.Equal(t, "assure", selected.Name())
}

func TestAssuranceDocumentInputLimitsAndReadErrors(t *testing.T) {
	_, err := readAssuranceDocument(t.Context(), filepath.Join(t.TempDir(), "missing"))
	require.Error(t, err)
	file := filepath.Join(t.TempDir(), "too-large")
	require.NoError(t, os.WriteFile(file, bytes.Repeat([]byte(" "), assurance.MaxLabelBytes+1), 0o600))
	_, err = readAssuranceDocument(t.Context(), file)
	require.ErrorContains(t, err, "byte limit")
	_, err = readAssuranceDocument(t.Context(), t.TempDir())
	require.Error(t, err)
}
