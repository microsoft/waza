package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func adapterCommandInputs(t *testing.T) (string, string, []byte, []byte) {
	t.Helper()
	root := testutil.RepoFile(t, "internal", "runnercompat", "testdata")
	eval, err := os.ReadFile(filepath.Join(root, "eval-1.4.yaml"))
	require.NoError(t, err)
	task, err := os.ReadFile(filepath.Join(root, "task.yaml"))
	require.NoError(t, err)
	directory := isolatedCLITempDir(t)
	require.NoError(t, os.Mkdir(filepath.Join(directory, "tasks"), 0o700))
	evalPath := filepath.Join(directory, "custom-suite.yaml")
	taskPath := filepath.Join(directory, "tasks", "selected.case.yaml")
	require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
	require.NoError(t, os.WriteFile(taskPath, task, 0o600))
	return evalPath, taskPath, eval, task
}

func adapterCommandArgs(eval, task string) []string {
	return []string{"--eval", eval, "--task", task, "--run-key", "frozen-local",
		"--shard", "local", "--task-id", "selected", "--trial", "1", "--attempt", "1"}
}

func TestRunnerAdaptConstructorCapturesExactSources(t *testing.T) {
	evalPath, taskPath, eval, task := adapterCommandInputs(t)
	ambient := isolatedCLITempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(ambient, ".waza.yaml"), []byte("invalid: ["), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(ambient, "skills"), 0o700))
	t.Chdir(ambient)
	cmd := newRunnerAdaptCommand()
	cmd.SetArgs(adapterCommandArgs(evalPath, taskPath))
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.NoError(t, cmd.Execute())
	request, err := runnercontract.Parse(output.Bytes())
	require.NoError(t, err)
	require.Equal(t, string(eval), request.Input.Eval)
	require.Equal(t, string(task), request.Input.Task)
	require.Equal(t, "selected", request.Selection.Task)
	require.Equal(t, 1, request.Selection.Trial)
	require.Equal(t, 1, request.Selection.Attempt)
	require.Equal(t, 30000, request.TimeoutMS)
	require.False(t, request.AllowPaidExecution)
	require.Equal(t, []string{"exact-unit", "inline.mock-text"}, request.RequiredCapabilities)
}

func TestRunnerAdaptConstructorFailuresEmitNoRequest(t *testing.T) {
	evalPath, taskPath, _, task := adapterCommandInputs(t)
	for _, control := range []struct {
		name string
		args []string
	}{
		{"missing flags", nil},
		{"unknown legacy flag", append(adapterCommandArgs(evalPath, taskPath), "--cache")},
		{"zero timeout", append(adapterCommandArgs(evalPath, taskPath), "--timeout-ms", "0")},
		{"selection argument", append(adapterCommandArgs(evalPath, taskPath), "adjacent")},
		{"missing files", adapterCommandArgs(evalPath+".missing", taskPath+".missing")},
		{"directory source", adapterCommandArgs(filepath.Dir(evalPath), taskPath)},
	} {
		t.Run(control.name, func(t *testing.T) {
			cmd := newRunnerAdaptCommand()
			cmd.SetArgs(control.args)
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(io.Discard)
			require.Error(t, cmd.Execute())
			require.Empty(t, output.Bytes(), "failure must not emit a partial runnable request")
		})
	}
	require.NoError(t, os.WriteFile(taskPath, append(task, []byte("enabled: false\n")...), 0o600))
	cmd := newRunnerAdaptCommand()
	cmd.SetArgs(adapterCommandArgs(evalPath, taskPath))
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.ErrorContains(t, cmd.Execute(), "disabled")
	require.Empty(t, output.Bytes())
}

func TestRunnerAdaptConstructorRejectsImplicitFixtures(t *testing.T) {
	evalPath, taskPath, _, _ := adapterCommandInputs(t)
	fixtures := filepath.Join(filepath.Dir(evalPath), "fixtures")
	require.NoError(t, os.Mkdir(fixtures, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(fixtures, "input.txt"), []byte("must not disappear"), 0o600))
	cmd := newRunnerAdaptCommand()
	cmd.SetArgs(adapterCommandArgs(evalPath, taskPath))
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.ErrorContains(t, cmd.Execute(), "default fixture behavior")
	require.Empty(t, output.Bytes())
	data, err := os.ReadFile(filepath.Join(fixtures, "input.txt"))
	require.NoError(t, err)
	require.Equal(t, "must not disappear", string(data))
}

func TestRunnerAdaptConstructorPropagatesWriterFailure(t *testing.T) {
	evalPath, taskPath, _, _ := adapterCommandInputs(t)
	sentinel := errors.New("adapter output unavailable")
	for _, control := range []struct {
		name   string
		writer io.Writer
		err    error
	}{
		{"write error", adapterBrokenWriter{sentinel}, sentinel},
		{"short write", adapterShortWriter{}, io.ErrShortWrite},
	} {
		t.Run(control.name, func(t *testing.T) {
			cmd := newRunnerAdaptCommand()
			cmd.SetArgs(adapterCommandArgs(evalPath, taskPath))
			cmd.SetOut(control.writer)
			require.ErrorIs(t, cmd.Execute(), control.err)
		})
	}
}

type adapterBrokenWriter struct{ err error }

func (w adapterBrokenWriter) Write([]byte) (int, error) { return 0, w.err }

type adapterShortWriter struct{}

func (adapterShortWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestRunnerAdaptBadAndAlternativeLocalBehaviorControls(t *testing.T) {
	for _, control := range []struct {
		name, from, to string
		fail           bool
		score          float64
	}{
		{"deliberately failed check", "output_contains: [hello]", "output_contains: [missing-marker]", true, 0},
		{"alternative valid prompt", "prompt: hello", "prompt: hello alternative", false, 1},
	} {
		t.Run(control.name, func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			evalPath, taskPath, _, task := adapterCommandInputs(t)
			require.Contains(t, string(task), control.from)
			changed := bytes.Replace(task, []byte(control.from), []byte(control.to), 1)
			require.NotEqual(t, task, changed)
			require.NoError(t, os.WriteFile(taskPath, changed, 0o600))
			t.Chdir(filepath.Dir(evalPath))
			path := filepath.Join(filepath.Dir(evalPath), "legacy.json")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--output", path, "--no-summary"})
			err := cmd.Execute()
			if control.fail {
				var failed *TestFailureError
				require.ErrorAs(t, err, &failed)
			} else {
				require.NoError(t, err)
			}
			outcome, err := models.LoadEvaluationOutcome(path)
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Len(t, run.Validations, 5)
			require.Equal(t, control.score, run.Validations["_output_contains"].Score)
			require.Equal(t, !control.fail, run.Validations["_output_contains"].Passed)
			adapt := newRunnerAdaptCommand()
			adapt.SetArgs(adapterCommandArgs(evalPath, taskPath))
			var captured bytes.Buffer
			adapt.SetOut(&captured)
			require.NoError(t, adapt.Execute(), "failed checks are not invalid configurations")
			request, err := runnercontract.Parse(captured.Bytes())
			require.NoError(t, err)
			require.Equal(t, string(changed), request.Input.Task)
			require.Contains(t, request.Input.Task, control.to)
		})
	}
}

func TestRunnerAdaptHistoricalLocalCommandFrozenOracle(t *testing.T) {
	oraclePath := testutil.RepoFile(t, "internal", "runnercompat", "testdata", "local-observation.original")
	oracle, err := os.ReadFile(oraclePath)
	require.NoError(t, err)
	for _, version := range []string{"", "1.0", "1.4", "1.99", "2.0"} {
		for _, lineEnding := range []string{"LF", "CRLF"} {
			t.Run(version+"/"+lineEnding, func(t *testing.T) {
				resetRunGlobals()
				t.Cleanup(resetRunGlobals)
				evalPath, taskPath, eval, task := adapterCommandInputs(t)
				header := ""
				if version != "" {
					header = "schemaVersion: \"" + version + "\"\n"
				}
				if version == "2.0" {
					header += "scenario: compatibility\n"
				}
				eval = bytes.Replace(eval, []byte("schemaVersion: \"1.4\"\n"), []byte(header), 1)
				if lineEnding == "CRLF" {
					eval = bytes.ReplaceAll(eval, []byte("\n"), []byte("\r\n"))
					task = bytes.ReplaceAll(task, []byte("\n"), []byte("\r\n"))
					require.Contains(t, string(task), "\r\n")
				}
				require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
				require.NoError(t, os.WriteFile(taskPath, task, 0o600))
				t.Chdir(filepath.Dir(evalPath))
				outputPath := filepath.Join(filepath.Dir(evalPath), "legacy.json")
				cmd := newRootCommand()
				cmd.SetArgs([]string{"run", evalPath, "--output", outputPath, "--no-summary"})
				require.NoError(t, cmd.Execute())
				data, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				outcome, err := models.ParseEvaluationOutcome(data, outputPath)
				require.NoError(t, err)
				require.Equal(t, "1.7", outcome.SchemaVersion)
				require.Equal(t, "adapter-frozen", outcome.BenchName)
				require.Len(t, outcome.TestOutcomes, 1)
				require.Equal(t, "selected", outcome.TestOutcomes[0].TestID)
				require.Equal(t, "Selected task", outcome.TestOutcomes[0].DisplayName)
				require.Len(t, outcome.TestOutcomes[0].Runs, 1)
				require.JSONEq(t, string(oracle), string(adapterObservationProjection(t, data)))
				local := newRootCommand()
				local.SetArgs([]string{"run", evalPath, "--runner-compat", "--output", outputPath, "--no-summary"})
				require.NoError(t, local.Execute())
				adapted, err := os.ReadFile(outputPath)
				require.NoError(t, err)
				require.JSONEq(t, string(oracle), string(adapterObservationProjection(t, adapted)))
				require.JSONEq(t, string(adapterOutcomeProjection(t, data)), string(adapterOutcomeProjection(t, adapted)))
				adapt := newRootCommand()
				adapt.SetArgs(append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...))
				var captured bytes.Buffer
				adapt.SetOut(&captured)
				require.NoError(t, adapt.Execute())
				request, err := runnercontract.Parse(captured.Bytes())
				require.NoError(t, err)
				require.Equal(t, string(eval), request.Input.Eval)
				require.Equal(t, string(task), request.Input.Task)
			})
		}
	}
}

func adapterOutcomeProjection(t *testing.T, data []byte) []byte {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	delete(raw, "eval_id")
	delete(raw, "timestamp")
	var summary map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["summary"], &summary))
	delete(summary, "duration_ms")
	encoded, err := json.Marshal(summary)
	require.NoError(t, err)
	raw["summary"] = encoded
	var tasks []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["tasks"], &tasks))
	require.Len(t, tasks, 1)
	require.Contains(t, tasks[0], "stats")
	var stats map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(tasks[0]["stats"], &stats))
	require.NotEmpty(t, stats)
	require.Contains(t, stats, "total_runs")
	delete(stats, "avg_duration_ms")
	encoded, err = json.Marshal(stats)
	require.NoError(t, err)
	tasks[0]["stats"] = encoded
	tasks[0]["runs"] = append(append([]byte("["), adapterObservationProjection(t, data)...), ']')
	encoded, err = json.Marshal(tasks)
	require.NoError(t, err)
	raw["tasks"] = encoded
	var usage struct {
		Sessions []map[string]json.RawMessage `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(raw["evaluation_usage"], &usage))
	require.Len(t, usage.Sessions, 1)
	var original struct {
		Tasks []struct {
			Runs []struct {
				Session struct {
					ID string `json:"session_id"`
				} `json:"session_digest"`
			} `json:"runs"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &original))
	var sessionID string
	require.NoError(t, json.Unmarshal(usage.Sessions[0]["session_id"], &sessionID))
	require.NotEmpty(t, sessionID)
	require.Equal(t, original.Tasks[0].Runs[0].Session.ID, sessionID)
	delete(usage.Sessions[0], "session_id")
	encoded, err = json.Marshal(usage)
	require.NoError(t, err)
	raw["evaluation_usage"] = encoded
	encoded, err = json.Marshal(raw)
	require.NoError(t, err)
	return encoded
}

func adapterObservationProjection(t *testing.T, data []byte) []byte {
	t.Helper()
	var raw struct {
		Tasks []struct {
			Runs []map[string]json.RawMessage `json:"runs"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Len(t, raw.Tasks, 1)
	require.Len(t, raw.Tasks[0].Runs, 1)
	run := raw.Tasks[0].Runs[0]
	delete(run, "duration_ms")
	delete(run, "workspace_dir")
	var session map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(run["session_digest"], &session))
	require.NotEmpty(t, session["session_id"])
	delete(session, "session_id")
	encoded, err := json.Marshal(session)
	require.NoError(t, err)
	run["session_digest"] = encoded
	var checks map[string]map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(run["validations"], &checks))
	require.Len(t, checks, 5)
	for _, check := range checks {
		delete(check, "duration_ms")
	}
	encoded, err = json.Marshal(checks)
	require.NoError(t, err)
	run["validations"] = encoded
	encoded, err = json.Marshal(run)
	require.NoError(t, err)
	return encoded
}

func TestRunnerAdaptFrozenOracleProjectionDetectsSemanticDrift(t *testing.T) {
	evalPath, _, _, _ := adapterCommandInputs(t)
	resetRunGlobals()
	t.Cleanup(resetRunGlobals)
	t.Chdir(filepath.Dir(evalPath))
	outputPath := filepath.Join(filepath.Dir(evalPath), "legacy.json")
	cmd := newRootCommand()
	cmd.SetArgs([]string{"run", evalPath, "--output", outputPath, "--no-summary"})
	require.NoError(t, cmd.Execute())
	data, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	original := adapterObservationProjection(t, data)
	for _, mutation := range []struct{ from, to string }{
		{`"prompt": "hello"`, `"prompt": "different"`},
		{`"score": 1`, `"score": 0.12345678901234567`},
		{`"passed": true`, `"passed": false`},
		{`"contains": null`, `"contains": []`},
		{`"attempts": 1`, `"attempts": 2`},
		{`"Mock response for: hello\n`, `"Mock response for: wrong\n`},
	} {
		require.Contains(t, string(data), mutation.from)
		changed := strings.Replace(string(data), mutation.from, mutation.to, 1)
		require.NotEqual(t, string(data), changed)
		require.NotEqual(t, string(original), string(adapterObservationProjection(t, []byte(changed))))
	}
}

func TestRunnerAdaptFrozenOracleChecksum(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoFile(t, "internal", "runnercompat", "testdata", "local-observation.original"))
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	require.Equal(t, "68d40e6e2b6bd5a408f2c3070efb9e51e06f564c6e26fae4234b2b2a7765a372", hex.EncodeToString(sum[:]))
}
