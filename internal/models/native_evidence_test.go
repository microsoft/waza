package models_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/assurance"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestNativeResultRejectsAuthoredProfileAtEveryReader(t *testing.T) {
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, nil)
	require.NoError(t, err)
	for _, version := range []string{"1.1", "1.0", "", "1.2", "2.0"} {
		t.Run(version, func(t *testing.T) {
			manifest := input.Manifest()
			manifest.Version = version
			run := models.RunResult{RunNumber: 1, Attempts: 1, Evidence: manifest}
			data, err := json.Marshal(run)
			require.NoError(t, err)
			var decoded models.RunResult
			require.Error(t, json.Unmarshal(data, &decoded))
			require.Equal(t, models.RunResult{}, decoded)
			hidden := strings.TrimSuffix(string(data), "}") + `,"evidence":null}`
			require.Error(t, json.Unmarshal([]byte(hidden), &decoded))
			outcome := models.EvaluationOutcome{RunID: "native-eval", TestOutcomes: []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{run}}}}
			data, err = json.Marshal(outcome)
			require.NoError(t, err)
			_, err = models.ParseEvaluationOutcome(data, "authored-native-result")
			require.Error(t, err)
			hidden = strings.TrimSuffix(string(data), "}") + `,"tasks":[]}`
			_, err = models.ParseEvaluationOutcome([]byte(hidden), "hidden-authored-native-result")
			require.Error(t, err)
			var apiDecoded models.EvaluationOutcome
			require.Error(t, json.Unmarshal(data, &apiDecoded))
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "results.json"), data, 0o600))
			store := webapi.NewFileStore(directory)
			runs, err := store.ListRuns("", "")
			require.NoError(t, err)
			require.Empty(t, runs)
			_, err = store.GetRun("native-eval")
			require.ErrorIs(t, err, webapi.ErrRunNotFound)
			wrapper, err := json.Marshal(models.EvaluationOutcome{BaselineOutcome: &outcome})
			require.NoError(t, err)
			_, err = models.ParseEvaluationOutcome(wrapper, "authored-baseline")
			require.Error(t, err)
		})
	}
}

func TestNativeResultOrdinaryCaptureAndLegacyReaderRegression(t *testing.T) {
	captured, err := snapshot.Capture(snapshot.CaptureInput{
		EvalID: "native", Task: &models.TestCase{TestID: "task"},
		Run: &models.RunResult{RunNumber: 1, Attempts: 1, FinalOutput: "output"},
	})
	require.NoError(t, err)
	for _, manifest := range []*models.EvidenceManifest{nil, captured.Evidence} {
		run := models.RunResult{RunNumber: 1, Attempts: 1, Evidence: manifest, FinalOutput: "output"}
		data, err := json.Marshal(models.EvaluationOutcome{RunID: "native", TestOutcomes: []models.TestOutcome{{TestID: "task", Runs: []models.RunResult{run}}}})
		require.NoError(t, err)
		outcome, err := models.ParseEvaluationOutcome(data, "native")
		require.NoError(t, err)
		require.Equal(t, run, outcome.TestOutcomes[0].Runs[0])
		var apiDecoded models.EvaluationOutcome
		require.NoError(t, json.Unmarshal(data, &apiDecoded))
		directory := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(directory, "results.json"), data, 0o600))
		store := webapi.NewFileStore(directory)
		runs, err := store.ListRuns("", "")
		require.NoError(t, err)
		require.Len(t, runs, 1)
		_, err = store.GetRun("native")
		require.NoError(t, err)
	}
	require.Error(t, models.ValidateNativeEvidenceProfile(nil))
	for _, doc := range []string{"supplied-finite-input", "snapshot"} {
		manifest := *captured.Evidence
		manifest.Artifacts = []models.EvidenceArtifact{{Document: doc}}
		if doc == "snapshot" {
			require.NoError(t, models.ValidateNativeEvidenceProfile(&manifest))
		} else {
			require.Error(t, models.ValidateNativeEvidenceProfile(&manifest))
		}
	}
	_, err = models.ParseEvaluationOutcome([]byte(`{"tasks":[{"runs":[{"evidence":`), "malformed")
	require.Error(t, err)
	manifest := *captured.Evidence
	manifest.Origin.EvalID = "reference:synthetic"
	require.Error(t, models.ValidateNativeEvidenceProfile(&manifest))
	manifest.Origin.EvalID = strings.TrimPrefix(manifest.Origin.EvalID, "reference:")
	require.NoError(t, models.ValidateNativeEvidenceProfile(&manifest))
}

func TestNativeResultRejectsHiddenAuthoredJSONAliases(t *testing.T) {
	input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, nil)
	require.NoError(t, err)
	authored, err := json.Marshal(input.Manifest())
	require.NoError(t, err)
	for name, data := range map[string]string{
		"evidence overwritten": `{"evidence":` + string(authored) + `,"Evidence":null}`,
		"uppercase first":      `{"Evidence":null,"evidence":` + string(authored) + `}`,
		"uppercase only":       `{"EVIDENCE":` + string(authored) + `}`,
		"mixed case":           `{"evidence":` + string(authored) + `,"eViDeNcE":null}`,
		"escaped alias":        `{"evidence":` + string(authored) + `,"Evidenc\u0065":null}`,
		"manifest version": `{"evidence":` + strings.Replace(string(authored),
			`"version":"1.1"`, `"version":"1.1","Version":"1.0"`, 1) + `}`,
		"manifest and origin laundered": `{"evidence":` + strings.Replace(
			strings.Replace(string(authored), `"version":"1.1"`, `"version":"1.1","Version":"1.0"`, 1),
			`"eval_id":"reference:subject"`, `"eval_id":"reference:subject","Eval_id":"native-eval"`, 1) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			var run models.RunResult
			require.Error(t, json.Unmarshal([]byte(data), &run))
			require.Equal(t, models.RunResult{}, run)
			result := `{"eval_id":"hidden-authored","tasks":[{"runs":[` + data + `]}]}`
			_, err := models.ParseEvaluationOutcome([]byte(result), "aliased-authored-result")
			require.Error(t, err)
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "results.json"), []byte(result), 0o600))
			store := webapi.NewFileStore(directory)
			runs, err := store.ListRuns("", "")
			require.NoError(t, err)
			require.Empty(t, runs)
			_, err = store.GetRun("hidden-authored")
			require.ErrorIs(t, err, webapi.ErrRunNotFound)
		})
	}
}

func TestNativeJSONKeysPreserveCanonicalAndUnknownFieldCompatibility(t *testing.T) {
	for _, data := range []string{
		`{"evidence":null,"futureField":true,"FutureField":false}`,
		`{"tasks":[{"runs":[{"evidence":null,"validations":{"ANSWER":{"score":1,"details":{"Score":"arbitrary","score":"keys"}}}}]}],"metadata":{"Tasks":"arbitrary keys"}}`,
		`{"baseline_outcome":{"tasks":[{"runs":[{"final_output":"legacy"}]}]}}`,
	} {
		_, err := models.ParseEvaluationOutcome([]byte(data), "canonical-native")
		require.NoError(t, err)
	}
	var run models.RunResult
	require.NoError(t, json.Unmarshal([]byte(`{"evidence":null,"futureField":true}`), &run))
	require.NoError(t, models.ValidateNativeJSONKeys([]byte(`null`), (*models.RunResult)(nil)))
	require.Error(t, models.ValidateNativeJSONKeys([]byte(`{}`), nil))
	for _, data := range []string{
		`{"tasks":[],"Tasks":[]}`,
		`{"ta\u017fks":[]}`,
		`{"tasks":[{"Runs":[]}]}`,
		`{"tasks":[{"runs":[{"Evidence":null}]}]}`,
		`{"baseline_outcome":{"TASKS":[]}}`,
		`{"tasks":[{"runs":[{"validations":{"answer":{"Score":1}}}]}]}`,
	} {
		_, err := models.ParseEvaluationOutcome([]byte(data), "aliased-native")
		require.ErrorContains(t, err, "exact spelling")
	}
	// Failed admission must leave an existing typed destination unchanged.
	run = models.RunResult{FinalOutput: "unchanged"}
	require.ErrorContains(t, json.Unmarshal([]byte(`{"evidence":null,"Evidence":null}`), &run), "exact spelling")
	require.Equal(t, "unchanged", run.FinalOutput)
}

func TestNativeReadersRejectMarkedAuthoredEnvelopeBeforeAssessment(t *testing.T) {
	empty, text := "", "finite authored output"
	for _, output := range []*string{nil, &empty, &text} {
		input, err := assurance.NewAuthoredOutput("subject", "case", "task", 1, output)
		require.NoError(t, err)
		data := input.Document()
		_, classified, err := models.ProbeEvaluationOutcomeSchemaVersion(data)
		require.NoError(t, err)
		require.False(t, classified)
		_, err = models.ParseEvaluationOutcome(data, "authored-envelope")
		require.ErrorContains(t, err, "major 2")
		_, err = snapshot.ParseSnapshot(data, "authored-envelope")
		require.ErrorContains(t, err, `kind "waza.grader-reference-input" is not "task-snapshot"`)
		directory := t.TempDir()
		path := filepath.Join(directory, "authored.json")
		require.NoError(t, os.WriteFile(path, data, 0o600))
		_, err = models.LoadEvaluationOutcome(path)
		require.ErrorContains(t, err, "major 2")
		_, err = snapshot.LoadSnapshotFile(path)
		require.ErrorContains(t, err, `kind "waza.grader-reference-input" is not "task-snapshot"`)
		store := webapi.NewFileStore(directory)
		runs, err := store.ListRuns("", "")
		require.NoError(t, err)
		require.Empty(t, runs)
		_, err = store.GetRun("authored")
		require.ErrorIs(t, err, webapi.ErrRunNotFound)
		summary, err := store.Summary()
		require.NoError(t, err)
		require.Zero(t, summary.TotalRuns)
		require.Zero(t, summary.TotalTasks)
	}
}
