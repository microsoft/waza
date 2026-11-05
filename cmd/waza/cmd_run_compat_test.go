package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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
			for _, mode := range []string{"error", "short", "complete"} {
				t.Run(mode, func(t *testing.T) {
					resetRunGlobals()
					t.Cleanup(resetRunGlobals)
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
					writer := &adapterMessageWriter{target: "Results saved to:", mode: mode, err: sentinel}
					if executionFailure {
						writer.target = "[ERROR]"
					}
					cmd.SetOut(writer)
					cmd.SetErr(io.Discard)
					err := cmd.Execute()
					require.Equal(t, 1, writer.matches, "only the selected production message exercises this writer")
					if mode == "complete" {
						if executionFailure {
							var failed *TestFailureError
							require.ErrorAs(t, err, &failed)
						} else {
							require.NoError(t, err)
						}
						require.Contains(t, writer.String(), " BENCHMARK RESULTS\n")
					} else {
						want := sentinel
						if mode == "short" {
							want = io.ErrShortWrite
						}
						require.ErrorIs(t, err, want)
						var failed *TestFailureError
						require.False(t, errors.As(err, &failed), "output loss must not use exit 1")
						var exit *ExitCodeError
						require.False(t, errors.As(err, &exit), "main must retain its default exit 2")
					}
					if executionFailure && mode != "complete" {
						_, statErr := os.Stat(output)
						require.ErrorIs(t, statErr, os.ErrNotExist)
						require.NotContains(t, writer.String(), " BENCHMARK RESULTS\n")
					} else {
						outcome, loadErr := models.LoadEvaluationOutcome(output)
						require.NoError(t, loadErr, "failed confirmation does not undo a saved artifact")
						require.Len(t, outcome.TestOutcomes, 1)
						require.Len(t, outcome.TestOutcomes[0].Runs, 1)
						if executionFailure {
							require.Equal(t, models.StatusError, outcome.TestOutcomes[0].Runs[0].Status)
						} else {
							require.Equal(t, 1, outcome.Digest.Succeeded)
						}
					}
				})
			}
		})
	}
}

type adapterMessageWriter struct {
	bytes.Buffer
	target, mode string
	err          error
	matches      int
}

func (w *adapterMessageWriter) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(w.target)) {
		w.matches++
		switch w.mode {
		case "error":
			return 0, w.err
		case "short":
			return w.Buffer.Write(p[:len(p)/2])
		}
	}
	return w.Buffer.Write(p)
}

func (w *adapterMessageWriter) WriteString(p string) (int, error) {
	return w.Write([]byte(p))
}

func TestRunnerCompatRequiresPathInsteadOfSkillName(t *testing.T) {
	evalPath, _, eval, _ := adapterCommandInputs(t)
	t.Chdir(filepath.Dir(evalPath))
	require.NoError(t, os.WriteFile("suite", eval, 0o600))
	for _, control := range []struct {
		operand string
		reject  bool
	}{
		{"suite", true},
		{"./suite", false},
		{evalPath, false},
		{"custom-suite.yaml", false},
	} {
		t.Run(control.operand, func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			output := filepath.Join(t.TempDir(), "outcome.json")
			cmd := newRootCommand()
			cmd.SetArgs([]string{"run", control.operand, "--runner-compat", "--output", output})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			err := cmd.Execute()
			if control.reject {
				require.ErrorContains(t, err, `not skill name "suite"`)
				require.ErrorContains(t, err, "./suite")
				_, err = os.Stat(output)
				require.ErrorIs(t, err, os.ErrNotExist)
				paths, resolveErr := resolveSpecPaths([]string{control.operand})
				require.Error(t, resolveErr, "ordinary run must not select the same-named local file")
				require.Empty(t, paths)
				return
			}
			require.NoError(t, err)
			outcome, err := models.LoadEvaluationOutcome(output)
			require.NoError(t, err)
			require.Equal(t, 1, outcome.Digest.Succeeded)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			require.Equal(t, "Mock response for: hello\nTask: Selected task\nDescription: Frozen local mock/text control",
				outcome.TestOutcomes[0].Runs[0].FinalOutput)
		})
	}
}

func TestRunSingleModelNilCommandSummary(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(fmt.Sprintf("closed=%t", closed), func(t *testing.T) {
			resetRunGlobals()
			t.Cleanup(resetRunGlobals)
			evalPath, _, _, _ := adapterCommandInputs(t)
			t.Chdir(filepath.Dir(evalPath))
			spec, err := models.LoadEvalSpec(evalPath)
			require.NoError(t, err)
			stdout, err := os.CreateTemp(t.TempDir(), "summary-")
			require.NoError(t, err)
			t.Cleanup(func() {
				if !closed {
					require.NoError(t, stdout.Close())
				}
			})
			if closed {
				require.NoError(t, stdout.Close())
			}
			original := os.Stdout
			os.Stdout = stdout
			t.Cleanup(func() { os.Stdout = original })

			outcome, err := runSingleModel(nil, spec, evalPath, nil)
			if closed {
				require.ErrorIs(t, err, os.ErrClosed)
				require.ErrorContains(t, err, "printing benchmark summary:")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, outcome)
			require.Equal(t, 1, outcome.Digest.Succeeded)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			require.True(t, outcome.TestOutcomes[0].Runs[0].Validations["_output_contains"].Passed)
			output, err := os.ReadFile(stdout.Name())
			require.NoError(t, err)
			require.Contains(t, string(output), " BENCHMARK RESULTS\n")
			require.Contains(t, string(output), "Succeeded:      1\n")
			require.Contains(t, string(output), "  ✓ Selected task [passed]\n")
		})
	}
}

func TestRunSummaryConfiguredWriter(t *testing.T) {
	for _, adapted := range []bool{false, true} {
		for _, control := range []struct {
			name       string
			outputFile bool
			noSummary  bool
		}{
			{name: "console"},
			{name: "file", outputFile: true},
			{name: "no-combined-summary", noSummary: true},
			{name: "file-no-combined-summary", outputFile: true, noSummary: true},
		} {
			t.Run(fmt.Sprintf("compat=%t/%s", adapted, control.name), func(t *testing.T) {
				resetRunGlobals()
				t.Cleanup(resetRunGlobals)
				evalPath, _, _, _ := adapterCommandInputs(t)
				t.Chdir(filepath.Dir(evalPath))
				args := []string{"run", evalPath, "--no-update-check"}
				if adapted {
					args = append(args, "--runner-compat")
				}
				output := filepath.Join(filepath.Dir(evalPath), "result.json")
				if control.outputFile {
					args = append(args, "--output", output)
				}
				if control.noSummary {
					args = append(args, "--no-summary")
				}
				var summary bytes.Buffer
				cmd := newRootCommand()
				cmd.SetArgs(args)
				cmd.SetOut(&summary)
				cmd.SetErr(io.Discard)
				require.NoError(t, cmd.Execute())
				require.Contains(t, summary.String(), " BENCHMARK RESULTS\n")
				require.Contains(t, summary.String(), "Total Tests:    1\n")
				require.Contains(t, summary.String(), "Succeeded:      1\n")
				require.Contains(t, summary.String(), "Failed:         0\n")
				require.Contains(t, summary.String(), " PER-TASK BREAKDOWN\n")
				require.Contains(t, summary.String(), "  ✓ Selected task [passed]\n")
				require.NotContains(t, summary.String(), "[ERROR]")
				if control.outputFile {
					outcome, err := models.LoadEvaluationOutcome(output)
					require.NoError(t, err)
					require.Equal(t, 1, outcome.Digest.Succeeded)
					require.Len(t, outcome.TestOutcomes, 1)
					require.Len(t, outcome.TestOutcomes[0].Runs, 1)
					require.True(t, outcome.TestOutcomes[0].Runs[0].Validations["_output_contains"].Passed)
				}
			})
		}
	}
}

func TestRunSummaryWriterFailures(t *testing.T) {
	sentinel := errors.New("summary sink unavailable")
	for _, adapted := range []bool{false, true} {
		for _, outputFile := range []bool{false, true} {
			for _, control := range []struct {
				name   string
				writer io.Writer
				err    error
			}{
				{"rejected", adapterBrokenWriter{sentinel}, sentinel},
				{"partial", adapterPartialWriter{sentinel}, sentinel},
				{"short", adapterPartialWriter{}, io.ErrShortWrite},
			} {
				t.Run(fmt.Sprintf("compat=%t/file=%t/%s", adapted, outputFile, control.name), func(t *testing.T) {
					resetRunGlobals()
					t.Cleanup(resetRunGlobals)
					evalPath, _, _, _ := adapterCommandInputs(t)
					t.Chdir(filepath.Dir(evalPath))
					args := []string{"run", evalPath, "--no-update-check"}
					if adapted {
						args = append(args, "--runner-compat")
					}
					output := filepath.Join(filepath.Dir(evalPath), "result.json")
					if outputFile {
						args = append(args, "--output", output)
					}
					cmd := newRootCommand()
					cmd.SetArgs(args)
					cmd.SetOut(control.writer)
					cmd.SetErr(io.Discard)
					err := cmd.Execute()
					require.ErrorIs(t, err, control.err)
					require.ErrorContains(t, err, "summary")
					var failed *TestFailureError
					require.False(t, errors.As(err, &failed), "output failures must not use the test-failure exit-1 policy")
					var exit *ExitCodeError
					require.False(t, errors.As(err, &exit), "output errors retain main's default exit-2 policy")
					if outputFile {
						_, err := os.Stat(output)
						require.ErrorIs(t, err, os.ErrNotExist, "summary failure occurs before saving the requested file")
					}
				})
			}
		}
	}
}

func TestRunnerCompatSummaryFailureProcessExit(t *testing.T) {
	if evalPath := os.Getenv("WAZA_TEST_SUMMARY_FAILURE_EVAL"); evalPath != "" {
		readOnly, err := os.Open(evalPath)
		require.NoError(t, err)
		os.Stdout = readOnly
		os.Args = []string{"waza", "run", evalPath, "--runner-compat", "--no-update-check"}
		main()
		return
	}
	evalPath, _, _, _ := adapterCommandInputs(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable, "-test.run=^TestRunnerCompatSummaryFailureProcessExit$")
	cmd.Dir = filepath.Dir(evalPath)
	// main calls os.Exit, so let the parent's owned fixture clean child test storage.
	cache := isolatedCLITempDir(t)
	cmd.Env = append(os.Environ(), "WAZA_TEST_SUMMARY_FAILURE_EVAL="+evalPath,
		"HOME="+cache, "XDG_CACHE_HOME="+cache, "LocalAppData="+cache)
	output, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, ExitError, exit.ExitCode(), string(output))
	require.Contains(t, string(output), "printing local runner summary:")
}

type adapterPartialWriter struct{ err error }

func (w adapterPartialWriter) Write(p []byte) (int, error) {
	return len(p) / 2, w.err
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
			require.Equal(t, "1.8", outcome.SchemaVersion)
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
