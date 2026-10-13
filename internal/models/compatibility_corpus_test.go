package models

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityArtifactVersions(t *testing.T) {
	root := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1")
	evalBytes, err := os.ReadFile(filepath.Join(root, "eval-1.0.yaml"))
	require.NoError(t, err)
	resultBytes, err := os.ReadFile(filepath.Join(root, "results-1.0.json"))
	require.NoError(t, err)

	for _, version := range []string{"", "1.0", "1.1", "1.4", "1.99", "2.0", "broken", "1.-1"} {
		t.Run("version="+version, func(t *testing.T) {
			versionLine := ""
			if version != "" {
				versionLine = `schemaVersion: "` + version + `"`
			}
			eval := strings.Replace(string(evalBytes), `schemaVersion: "1.0"`, versionLine, 1)
			eval += "\nfuture_field: true\n"
			path := filepath.Join(t.TempDir(), "eval.yaml")
			require.NoError(t, os.WriteFile(path, []byte(eval), 0o600))
			spec, evalErr := LoadEvalSpec(path)

			var wire map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(resultBytes, &wire))
			if version == "" {
				delete(wire, "schemaVersion")
			} else {
				wire["schemaVersion"], err = json.Marshal(version)
				require.NoError(t, err)
			}
			wire["future_field"] = json.RawMessage(`true`)
			data, err := json.Marshal(wire)
			require.NoError(t, err)
			outcome, resultErr := ParseEvaluationOutcome(data, "corpus")
			if version == "2.0" || version == "broken" || version == "1.-1" {
				require.Error(t, evalErr)
				require.Error(t, resultErr)
				if version == "2.0" {
					require.ErrorContains(t, evalErr, "waza migrate")
					require.ErrorContains(t, resultErr, "waza migrate")
				}
				return
			}
			require.NoError(t, evalErr)
			require.NoError(t, resultErr)
			wantVersion := version
			if wantVersion == "" {
				wantVersion = CurrentSchemaVersion
			}
			require.Equal(t, wantVersion, spec.SchemaVersion)
			require.Equal(t, wantVersion, outcome.SchemaVersion)
			require.Equal(t, "compatibility", spec.Name)
			require.Equal(t, "mock", spec.Config.EngineType)
			require.Equal(t, 1, spec.Config.TrialsPerTask)
			require.Equal(t, 30, spec.Config.TimeoutSec)
			require.True(t, spec.Config.ShouldInjectSkillBody())
			require.False(t, spec.Config.ShouldTriggerSkillRouting())
			require.False(t, spec.Config.StopOnError)
			require.False(t, spec.Config.Concurrent)
			require.Nil(t, spec.Config.InjectSkillBody)
			require.Equal(t, TextGraderParameters{Contains: []string{"ready"}}, spec.Graders[0].Parameters)
			require.Equal(t, "compatibility-run", outcome.RunID)
			require.Equal(t, 1.0, outcome.Digest.SuccessRate)
			require.Len(t, outcome.TestOutcomes, 1)
			task := outcome.TestOutcomes[0]
			require.Equal(t, "compatibility-task", task.TestID)
			require.True(t, task.Golden)
			require.Equal(t, StatusPassed, task.Status)
			require.Len(t, task.Runs, 1)
			require.Equal(t, "ready", task.Runs[0].FinalOutput)
			require.True(t, task.Runs[0].Validations["answer"].Passed)
			require.Equal(t, "answer", task.Runs[0].Validations["answer"].Name)
			require.Nil(t, task.Runs[0].SessionDigest.Usage)
			require.Empty(t, task.Runs[0].ToolEvents)
			require.Nil(t, outcome.EvaluationUsage)
		})
	}
	t.Run("unknown fields do not mask invalid types", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "eval.yaml")
		data := strings.Replace(string(evalBytes), "timeout_seconds: 30", "timeout_seconds: [bad]", 1)
		require.NoError(t, os.WriteFile(path, []byte(data+"\nfuture_field: true\n"), 0o600))
		_, err := LoadEvalSpec(path)
		require.Error(t, err)
		data = strings.Replace(string(resultBytes), `"runs_per_test": 1`, `"runs_per_test": "bad"`, 1)
		_, err = ParseEvaluationOutcome([]byte(data), "corpus")
		require.Error(t, err)
	})
}

func TestCompatibilityTaskContract(t *testing.T) {
	path := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "task.yaml")
	tc, err := LoadTestCase(path)
	require.NoError(t, err)
	require.Equal(t, "compatibility-task", tc.TestID)
	require.Equal(t, "Say ready.", tc.Stimulus.Message)
	require.True(t, tc.Golden)
	require.Nil(t, tc.CommandMocks, "omitted mocks inherit")
	require.Equal(t, []string{"Confirm ready."}, tc.Stimulus.FollowUps)
	require.Len(t, tc.Checkpoints, 1)
	require.Equal(t, CheckpointContinue, tc.Checkpoints[0].EffectiveOnFailure())
	require.Equal(t, 1, tc.Checkpoints[0].AfterTurn)
	require.NoError(t, tc.ValidateForExecutor("mock"))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for _, extra := range []string{"schemaVersion: \"1.0\"", "future_field: true"} {
		t.Run(extra, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "task.yaml")
			require.NoError(t, os.WriteFile(path, append(data, []byte("\n"+extra+"\n")...), 0o600))
			var warnings bytes.Buffer
			originalLogger := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
			defer slog.SetDefault(originalLogger)
			loaded, err := LoadTestCase(path)
			require.NoError(t, err, "unknown task fields warn and are ignored; tasks have no version negotiation")
			require.Equal(t, tc, loaded)
			require.Contains(t, warnings.String(), "unknown schema field ignored for same-major compatibility")
			require.Contains(t, warnings.String(), "task YAML")
		})
	}
}

func TestCompatibilityLockIntegrity(t *testing.T) {
	path := testutil.RepoFile(t, "internal", "testdata", "compatibility", "v1", "waza.lock")
	lock, err := LoadLockfile(path)
	require.NoError(t, err)
	require.Len(t, lock.Graders, 1)
	require.False(t, lock.Graders[0].Trusted)
	entry, found := lock.Grader("example.com/compat/graders#answer@v1.0.0")
	require.True(t, found)
	require.Equal(t, "0123456789abcdef0123456789abcdef01234567", entry.Commit)
	for _, name := range []string{"schema", "commit", "digest", "duplicate"} {
		t.Run(name, func(t *testing.T) {
			bad := &Lockfile{SchemaVersion: lock.SchemaVersion, Graders: append([]LockfileGrader(nil), lock.Graders...)}
			switch name {
			case "schema":
				bad.SchemaVersion = 2
			case "commit":
				bad.Graders[0].Commit = "main"
			case "digest":
				bad.Graders[0].Digest = "sha256:bad"
			case "duplicate":
				bad.Graders = append(bad.Graders, bad.Graders[0])
			}
			require.Error(t, bad.Validate())
		})
	}
	first := filepath.Join(t.TempDir(), "first.lock")
	second := filepath.Join(t.TempDir(), "second.lock")
	require.NoError(t, WriteLockfile(first, lock))
	loaded, err := LoadLockfile(first)
	require.NoError(t, err)
	require.NoError(t, WriteLockfile(second, loaded))
	a, err := os.ReadFile(first)
	require.NoError(t, err)
	b, err := os.ReadFile(second)
	require.NoError(t, err)
	require.Equal(t, a, b)
}
