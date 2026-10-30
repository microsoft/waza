package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRunnerCompatPreservesObservedExecutionErrorArtifact(t *testing.T) {
	evalPath, _, _, _ := adapterCommandInputs(t)
	directory := filepath.Dir(evalPath)
	t.Chdir(directory)
	blocked := filepath.Join(directory, "not-a-temporary-directory")
	require.NoError(t, os.WriteFile(blocked, []byte("owned failure control"), 0o600))
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, blocked)
	}
	var before *models.EvaluationOutcome
	leafPattern := regexp.MustCompile(`waza-mock-[0-9]+`)
	for _, adapted := range []bool{false, true} {
		resetRunGlobals()
		t.Cleanup(resetRunGlobals)
		output := filepath.Join(directory, "before.json")
		args := []string{"run", evalPath, "--output", output, "--no-summary"}
		if adapted {
			output = filepath.Join(directory, "after.json")
			args = []string{"run", evalPath, "--runner-compat", "--output", output, "--no-summary"}
		}
		cmd := newRootCommand()
		cmd.SetArgs(args)
		cmd.SetErr(io.Discard)
		var failed *TestFailureError
		require.ErrorAs(t, cmd.Execute(), &failed, "observed execution errors retain the local exit-1 policy")
		outcome, err := models.LoadEvaluationOutcome(output)
		require.NoError(t, err, "an observed error must still be saved")
		require.Len(t, outcome.TestOutcomes, 1)
		task := &outcome.TestOutcomes[0]
		require.Equal(t, models.StatusFailed, task.Status)
		require.Len(t, task.Runs, 1)
		run := &task.Runs[0]
		require.Equal(t, models.StatusError, run.Status)
		require.Contains(t, run.ErrorMsg, "failed to create mock workspace")
		require.Contains(t, run.ErrorMsg, blocked)
		require.Equal(t, 1, run.Attempts)
		require.Nil(t, run.Validations)
		require.Empty(t, run.WorkspaceDir)
		require.Empty(t, run.SessionDigest.SessionID)
		require.Equal(t, []models.EvaluationSessionUsage{{}}, outcome.EvaluationUsage.Sessions,
			"native usage scope retains an anonymous invocation without inventing a session or usage")
		require.Nil(t, outcome.Digest.Usage)
		require.Equal(t, 1, outcome.Digest.Failed)
		require.Equal(t, 0, outcome.Digest.Errors)
		require.Equal(t, 1, task.Stats.TotalRuns)
		require.NotNil(t, run.FailureArtifacts)
		artifacts := run.FailureArtifacts
		require.Equal(t, run.ErrorMsg, artifacts.StdErr)
		require.Len(t, artifacts.ErrorPatterns, 1)
		require.Contains(t, artifacts.ErrorPatterns[0], strings.ToLower(blocked))
		require.Contains(t, artifacts.TriageSummary, run.ErrorMsg)
		require.False(t, artifacts.CapturedAt.IsZero())
		require.Len(t, leafPattern.FindAllString(run.ErrorMsg, -1), 1)
		// Normalize only the independently allocated leaf and timestamps.
		run.ErrorMsg = leafPattern.ReplaceAllString(run.ErrorMsg, "waza-mock-VOLATILE")
		artifacts.StdErr = leafPattern.ReplaceAllString(artifacts.StdErr, "waza-mock-VOLATILE")
		artifacts.ErrorPatterns[0] = leafPattern.ReplaceAllString(artifacts.ErrorPatterns[0], "waza-mock-VOLATILE")
		artifacts.TriageSummary = leafPattern.ReplaceAllString(artifacts.TriageSummary, "waza-mock-VOLATILE")
		artifacts.CapturedAt = time.Time{}
		run.DurationMs = 0
		task.Stats.AvgDurationMs = 0
		outcome.Digest.DurationMs = 0
		if !adapted {
			before = outcome
		} else {
			outcome.RunID, outcome.Timestamp = before.RunID, before.Timestamp
			require.Equal(t, before, outcome, "preserve the complete native artifact, including absent evidence")
		}
	}
}

func TestRunnerCompatPropagatesOutputWriterFailures(t *testing.T) {
	for _, executionFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved confirmation", true: "execution diagnostic"}[executionFailure], func(t *testing.T) {
			evalPath, _, _, _ := adapterCommandInputs(t)
			directory := filepath.Dir(evalPath)
			t.Chdir(directory)
			if executionFailure {
				blocked := filepath.Join(directory, "not-a-temporary-directory")
				require.NoError(t, os.WriteFile(blocked, nil, 0o600))
				for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
					t.Setenv(key, blocked)
				}
			}
			output := filepath.Join(directory, "result.json")
			sentinel := errors.New("local stdout unavailable")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--runner-compat", "--output", output})
			cmd.SetOut(adapterBrokenWriter{sentinel})
			cmd.SetErr(io.Discard)
			require.ErrorIs(t, cmd.Execute(), sentinel)
			_, err := os.Stat(output)
			if executionFailure {
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err, "failed confirmation does not undo a saved artifact")
			}
		})
	}
}

func TestRunnerCompatPublicFailedAndAlternative(t *testing.T) {
	for _, control := range []struct {
		name, from, to string
		failed         bool
	}{
		{"failed", "output_contains: [hello]", "output_contains: [missing-marker]", true},
		{"alternative", "prompt: hello", "prompt: hello alternative", false},
		{"null", "prompt: hello", "prompt: hello\nunused: null", false},
	} {
		t.Run(control.name, func(t *testing.T) {
			evalPath, taskPath, _, task := adapterCommandInputs(t)
			t.Chdir(filepath.Dir(evalPath))
			require.Contains(t, string(task), control.from)
			changed := bytes.Replace(task, []byte(control.from), []byte(control.to), 1)
			require.NotEqual(t, task, changed)
			require.NoError(t, os.WriteFile(taskPath, changed, 0o600))
			path := filepath.Join(filepath.Dir(evalPath), "adapted.json")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--runner-compat", "--output", path})
			err := cmd.Execute()
			if control.name == "null" {
				require.ErrorContains(t, err, "remove --runner-compat", "unknown null fields need actionable migration, not silent dropping")
				_, err = os.Stat(path)
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			if control.failed {
				var failed *TestFailureError
				require.ErrorAs(t, err, &failed)
			} else {
				require.NoError(t, err)
			}
			outcome, err := models.LoadEvaluationOutcome(path)
			require.NoError(t, err)
			require.Equal(t, "1.7", outcome.SchemaVersion)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			run := outcome.TestOutcomes[0].Runs[0]
			require.Equal(t, !control.failed, run.Validations["_output_contains"].Passed)
			require.Equal(t, 1, run.Attempts)
			require.Nil(t, outcome.Digest.Usage)
		})
	}
}

func TestRunnerCompatRejectsUnsupportedInvocationBeforeExecution(t *testing.T) {
	for _, flag := range []string{"--cache", "--no-cache", "--parallel=false", "--trials=1", "--no-skills", "--skip-graders", "--model=mock-model", "--reporter=json", "--output-dir=results", "--snapshot=snapshot"} {
		t.Run(flag, func(t *testing.T) {
			evalPath, _, _, _ := adapterCommandInputs(t)
			t.Chdir(filepath.Dir(evalPath))
			path := filepath.Join(filepath.Dir(evalPath), "forbidden.json")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--runner-compat", "--output", path, flag})
			cmd.SetErr(io.Discard)
			require.ErrorContains(t, cmd.Execute(), "remove --runner-compat")
			_, err := os.Stat(path)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestRunnerCompatRejectsAmbientAndAdjacentInputs(t *testing.T) {
	for _, name := range []string{".waza.yaml", "trigger_tests.yaml", "fixtures"} {
		t.Run(name, func(t *testing.T) {
			evalPath, _, _, _ := adapterCommandInputs(t)
			directory := filepath.Dir(evalPath)
			t.Chdir(directory)
			if name == "fixtures" {
				require.NoError(t, os.Mkdir(filepath.Join(directory, name), 0o700))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte("invalid: ["), 0o600))
			}
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", evalPath, "--runner-compat"})
			cmd.SetErr(io.Discard)
			require.Error(t, cmd.Execute())
		})
	}
}

func TestRunnerAdaptRootRemainsOffline(t *testing.T) {
	evalPath, taskPath, _, _ := adapterCommandInputs(t)
	ambient := isolatedCLITempDir(t)
	require.NoError(t, os.WriteFile(filepath.Join(ambient, ".waza.yaml"), []byte("invalid: ["), 0o600))
	t.Chdir(ambient)
	t.Setenv("WAZA_NO_UPDATE_CHECK", "")
	previous := http.DefaultTransport
	http.DefaultTransport = adapterDenyTransport{t}
	t.Cleanup(func() { http.DefaultTransport = previous })
	cmd := newRootCommand()
	cmd.SetArgs(append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...))
	var output bytes.Buffer
	cmd.SetOut(&output)
	require.NoError(t, cmd.Execute())
	require.Contains(t, output.String(), `"schema_version":"1.0"`)
	found, _, err := cmd.Find([]string{"runner", "adapt"})
	require.NoError(t, err)
	require.False(t, shouldRunUpdateCheck(found, false))
}

type adapterDenyTransport struct{ t *testing.T }

func (d adapterDenyTransport) RoundTrip(*http.Request) (*http.Response, error) {
	d.t.Error("offline adaptation attempted HTTP")
	return nil, errors.New("HTTP forbidden")
}
