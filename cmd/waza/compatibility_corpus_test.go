package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityCLIProcessExits(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "waza")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	log, err := build.CombinedOutput()
	require.NoError(t, err, "%s", log)
	root := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1")
	base := filepath.Join(root, "results-1.0.json")
	o, err := models.LoadEvaluationOutcome(base)
	require.NoError(t, err)
	require.Len(t, o.TestOutcomes, 1)
	require.Len(t, o.TestOutcomes[0].Runs, 1)
	o.Digest.SuccessRate = 0
	o.Digest.Succeeded = 0
	o.Digest.Failed = 1
	o.TestOutcomes[0].Status = models.StatusFailed
	o.TestOutcomes[0].Runs[0].Status = models.StatusFailed
	o.TestOutcomes[0].Runs[0].FinalOutput = "not the expected answer"
	o.TestOutcomes[0].Runs[0].Validations["answer"] = models.GraderResults{Name: "answer", Type: models.GraderKindText, Passed: false, Score: 0, Weight: 1}
	bad := writeOutcomeFile(t, dir, "bad.json", o)
	snap, err := snapshot.LoadSnapshotFile(filepath.Join(root, "snapshot-1.0.json"))
	require.NoError(t, err)
	snap.Result.Validations["answer"] = models.GraderResults{Name: "answer", Passed: true, Score: 0, Weight: 1}
	badSnapshot := writeSnapshot(t, dir, "bad-snapshot.json", *snap)
	evalBytes, err := os.ReadFile(filepath.Join(root, "eval-1.0.yaml"))
	require.NoError(t, err)
	taskBytes, err := os.ReadFile(filepath.Join(root, "task.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "task.yaml"), taskBytes, 0o600))
	badEval := filepath.Join(dir, "eval.yaml")
	require.NoError(t, os.WriteFile(badEval, []byte(strings.Replace(string(evalBytes), "contains: [ready]", "contains: [impossible-fixture-answer]", 1)), 0o600))
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "NO_COLOR=1", "TZ=UTC", "LC_ALL=C", "HOME="+dir, "USERPROFILE="+dir, "XDG_CONFIG_HOME="+dir, "WAZA_NO_UPDATE_CHECK=1")
		return cmd
	}
	for _, c := range []struct {
		name string
		args []string
		code int
		json bool
	}{
		{"gate pass", []string{"gate", "--baseline", base, "--current", base, "--format", "json"}, 0, true},
		{"gate regression", []string{"gate", "--baseline", base, "--current", bad, "--golden-must-pass=false", "--format", "json"}, 1, true},
		{"gate golden precedence", []string{"gate", "--baseline", base, "--current", bad, "--format", "json"}, 2, true},
		{"gate configuration", []string{"gate", "--baseline", base, "--current", filepath.Join(dir, "missing.json"), "--format", "json"}, 3, false},
		{"run pass", []string{"run", filepath.Join(root, "eval-1.0.yaml")}, 0, false},
		{"run failure", []string{"run", badEval}, 1, false},
		{"run configuration", []string{"run", filepath.Join(dir, "missing.yaml")}, 2, false},
		{"grade pass", []string{"grade", filepath.Join(root, "eval-1.0.yaml"), "--results", base}, 0, false},
		{"grade reports failed verdict", []string{"grade", filepath.Join(root, "eval-1.0.yaml"), "--results", bad}, 0, true},
		{"replay pass", []string{"replay", filepath.Join(root, "snapshot-1.0.json"), "--json"}, 0, true},
		{"replay inconsistent", []string{"replay", badSnapshot, "--json"}, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd := command(c.args...)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				require.True(t, errors.As(err, &exit), "%v", err)
				code = exit.ExitCode()
			}
			require.Equal(t, c.code, code, "stdout=%s stderr=%s", stdout.String(), stderr.String())
			if c.json {
				var wire map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &wire), "stdout=%s stderr=%s", stdout.String(), stderr.String())
				if c.args[0] == "gate" {
					var reportCode int
					require.NoError(t, json.Unmarshal(wire["exit_code"], &reportCode))
					require.Equal(t, c.code, reportCode)
				}
				if c.args[0] == "grade" {
					require.JSONEq(t, "false", string(wire["passed"]))
				}
			}
		})
	}
	t.Run("run artifacts repeat after narrow normalization", func(t *testing.T) {
		var previous *models.EvaluationOutcome
		for iteration := range 2 {
			resultPath := filepath.Join(t.TempDir(), "outcome.json")
			cmd := command("run", filepath.Join(root, "eval-1.0.yaml"), "--output", resultPath)
			log, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", log)
			outcome, err := models.LoadEvaluationOutcome(resultPath)
			require.NoError(t, err)
			wireBytes, err := os.ReadFile(resultPath)
			require.NoError(t, err)
			expected, err := os.ReadFile(testutil.RepoFile(t, "cmd", "waza", "testdata", "compatibility-run-1.5.json"))
			require.NoError(t, err)
			require.Equal(t, "1.5", outcome.SchemaVersion)
			normalized, err := json.Marshal(normalizeCompatibilityRunWire(t, wireBytes))
			require.NoError(t, err)
			require.JSONEq(t, string(expected), string(normalized), "independent baseline wire oracle must match on every iteration")
			require.NotEmpty(t, outcome.RunID)
			require.False(t, outcome.Timestamp.IsZero())
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			require.Equal(t, "compatibility-task", outcome.TestOutcomes[0].TestID)
			require.Equal(t, models.StatusPassed, outcome.TestOutcomes[0].Status)
			require.NotNil(t, outcome.EvaluationUsage)
			require.Len(t, outcome.EvaluationUsage.Sessions, 1)
			sessionID := outcome.TestOutcomes[0].Runs[0].SessionDigest.SessionID
			require.NotEmpty(t, sessionID)
			require.Equal(t, sessionID, outcome.EvaluationUsage.Sessions[0].SessionID, "validate the ledger reference before normalizing both identities")
			outcome.EvaluationUsage.Sessions[0].SessionID = "normalized-session"
			outcome.RunID = "normalized-run"
			outcome.Timestamp = time.Time{}
			outcome.Digest.DurationMs = 0
			for i := range outcome.TestOutcomes {
				task := &outcome.TestOutcomes[i]
				if task.Stats != nil {
					task.Stats.AvgDurationMs = 0
				}
				for j := range task.Runs {
					run := &task.Runs[j]
					run.SessionDigest.SessionID = "normalized-session"
					run.DurationMs = 0
					run.WorkspaceDir = ""
					for name, verdict := range run.Validations {
						verdict.DurationMs = 0
						run.Validations[name] = verdict
					}
					for k := range run.Checkpoints {
						for name, verdict := range run.Checkpoints[k].Validations {
							verdict.DurationMs = 0
							run.Checkpoints[k].Validations[name] = verdict
						}
					}
				}
			}
			if iteration > 0 {
				require.Equal(t, previous, outcome)
			}
			previous = outcome
		}
	})
}

func compatibilityWireObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	require.True(t, ok, "expected JSON object, got %T", value)
	return object
}

func compatibilityWireArray(t *testing.T, value any) []any {
	t.Helper()
	array, ok := value.([]any)
	require.True(t, ok, "expected JSON array, got %T", value)
	return array
}

func normalizeCompatibilityRunWire(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	normalizeCompatibilityWireField(wire, "eval_id", "normalized-run")
	normalizeCompatibilityWireField(wire, "timestamp", "0001-01-01T00:00:00Z")
	normalizeCompatibilityWireField(compatibilityWireObject(t, wire["summary"]), "duration_ms", float64(0))
	for _, session := range compatibilityWireArray(t, compatibilityWireObject(t, wire["evaluation_usage"])["sessions"]) {
		normalizeCompatibilityWireField(compatibilityWireObject(t, session), "session_id", "normalized-session")
	}
	for _, value := range compatibilityWireArray(t, wire["tasks"]) {
		task := compatibilityWireObject(t, value)
		if stats, exists := task["stats"]; exists {
			normalizeCompatibilityWireField(compatibilityWireObject(t, stats), "avg_duration_ms", float64(0))
		}
		for _, value := range compatibilityWireArray(t, task["runs"]) {
			run := compatibilityWireObject(t, value)
			normalizeCompatibilityWireField(run, "duration_ms", float64(0))
			delete(run, "workspace_dir")
			normalizeCompatibilityWireField(compatibilityWireObject(t, run["session_digest"]), "session_id", "normalized-session")
			for _, verdict := range compatibilityWireObject(t, run["validations"]) {
				normalizeCompatibilityWireField(compatibilityWireObject(t, verdict), "duration_ms", float64(0))
			}
			if checkpoints, exists := run["checkpoints"]; exists {
				for _, value := range compatibilityWireArray(t, checkpoints) {
					for _, verdict := range compatibilityWireObject(t, compatibilityWireObject(t, value)["validations"]) {
						normalizeCompatibilityWireField(compatibilityWireObject(t, verdict), "duration_ms", float64(0))
					}
				}
			}
		}
	}
	return wire
}

func normalizeCompatibilityWireField(object map[string]any, key string, normalized any) {
	switch normalized.(type) {
	case string:
		if _, ok := object[key].(string); ok {
			object[key] = normalized
		}
	case float64:
		if _, ok := object[key].(float64); ok {
			object[key] = normalized
		}
	}
}

func TestCompatibilityRunOracleRejectsDeterministicDrift(t *testing.T) {
	data, err := os.ReadFile(testutil.RepoFile(t, "cmd", "waza", "testdata", "compatibility-run-1.4.json"))
	require.NoError(t, err)
	expected := normalizeCompatibilityRunWire(t, data)
	for _, name := range []string{"schema version", "prompt", "checkpoint", "config", "billing", "score", "output", "wire shape", "null instead of empty", "zero instead of omitted", "cardinality",
		"missing run ID", "missing timestamp", "missing summary duration", "missing run duration", "missing verdict duration", "missing checkpoint duration", "missing stats duration", "missing digest ID", "missing ledger ID", "wrong duration type"} {
		t.Run(name, func(t *testing.T) {
			var wire map[string]any
			require.NoError(t, json.Unmarshal(data, &wire))
			task := compatibilityWireObject(t, compatibilityWireArray(t, wire["tasks"])[0])
			run := compatibilityWireObject(t, compatibilityWireArray(t, task["runs"])[0])
			switch name {
			case "schema version":
				wire["schemaVersion"] = "1.5"
			case "prompt":
				run["prompt"] = "different prompt"
			case "checkpoint":
				compatibilityWireObject(t, compatibilityWireArray(t, run["checkpoints"])[0])["after_turn"] = float64(2)
			case "config":
				compatibilityWireObject(t, wire["config"])["timeout_sec"] = float64(60)
			case "billing":
				session := compatibilityWireArray(t, compatibilityWireObject(t, wire["evaluation_usage"])["sessions"])[0]
				compatibilityWireObject(t, session)["usage"] = map[string]any{"ai_credits": float64(5)}
			case "score":
				compatibilityWireObject(t, wire["summary"])["aggregate_score"] = float64(0)
			case "output":
				run["final_output"] = "different output"
			case "wire shape":
				delete(run, "prompt")
			case "null instead of empty":
				compatibilityWireObject(t, run["session_digest"])["tools_used"] = nil
			case "zero instead of omitted":
				compatibilityWireObject(t, run["session_digest"])["usage"] = map[string]any{"ai_credits": float64(0)}
			case "cardinality":
				wire["tasks"] = append(compatibilityWireArray(t, wire["tasks"]), task)
			case "missing run ID":
				delete(wire, "eval_id")
			case "missing timestamp":
				delete(wire, "timestamp")
			case "missing summary duration":
				delete(compatibilityWireObject(t, wire["summary"]), "duration_ms")
			case "missing run duration":
				delete(run, "duration_ms")
			case "missing verdict duration":
				delete(compatibilityWireObject(t, compatibilityWireObject(t, run["validations"])["answer"]), "duration_ms")
			case "missing checkpoint duration":
				checkpoint := compatibilityWireObject(t, compatibilityWireArray(t, run["checkpoints"])[0])
				delete(compatibilityWireObject(t, compatibilityWireObject(t, checkpoint["validations"])["intermediate"]), "duration_ms")
			case "missing stats duration":
				delete(compatibilityWireObject(t, task["stats"]), "avg_duration_ms")
			case "missing digest ID":
				delete(compatibilityWireObject(t, run["session_digest"]), "session_id")
			case "missing ledger ID":
				session := compatibilityWireArray(t, compatibilityWireObject(t, wire["evaluation_usage"])["sessions"])[0]
				delete(compatibilityWireObject(t, session), "session_id")
			case "wrong duration type":
				run["duration_ms"] = "0"
			}

			mutated, err := json.Marshal(wire)
			require.NoError(t, err)
			require.NotEqual(t, expected, normalizeCompatibilityRunWire(t, mutated), "normalization must not erase deterministic contract drift")
		})
	}
}

func TestCompatibilityResultWriterOracleDiffersOnlyByVersion(t *testing.T) {
	legacyBytes, err := os.ReadFile(testutil.RepoFile(t, "cmd", "waza", "testdata", "compatibility-run-1.4.json"))
	require.NoError(t, err)
	currentBytes, err := os.ReadFile(testutil.RepoFile(t, "cmd", "waza", "testdata", "compatibility-run-1.5.json"))
	require.NoError(t, err)
	var legacy, current map[string]any
	require.NoError(t, json.Unmarshal(legacyBytes, &legacy))
	require.NoError(t, json.Unmarshal(currentBytes, &current))
	require.Equal(t, "1.4", legacy["schemaVersion"])
	require.Equal(t, "1.5", current["schemaVersion"])
	require.Equal(t, models.CurrentResultSchemaVersion, current["schemaVersion"])
	legacy["schemaVersion"] = "1.5"
	require.Equal(t, legacy, current, "only the explicitly changed writer version may differ from the frozen legacy oracle")
}
