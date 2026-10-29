package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func adapterProcess(t *testing.T, binary, directory string, input []byte, code int, args ...string) ([]byte, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = directory
	cmd.Stdin = bytes.NewReader(input)
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, ctx.Err(), "bounded CLI helper did not return: %v; stderr: %s", err, stderr.String())
	if code == 0 {
		require.NoError(t, err, "%s", stderr.String())
	} else {
		exit, ok := errors.AsType[*exec.ExitError](err)
		require.True(t, ok, "%v: %s", err, stderr.String())
		require.Equal(t, code, exit.ExitCode(), "%s", stderr.String())
	}
	return stdout.Bytes(), stderr.String()
}

func assertAdaptedAdmission(t *testing.T, binary, directory string, data []byte) {
	t.Helper()
	report := runnerunit.Validate(data)
	require.Equal(t, "ready", report.Status, "%+v", report)
	output, _ := adapterProcess(t, binary, directory, data, 0, "runner", "validate")
	var receipt runnerunit.Report
	require.NoError(t, json.Unmarshal(output, &receipt))
	require.Equal(t, "ready", receipt.Status)
	require.NotNil(t, receipt.Identity)
	output, _ = adapterProcess(t, binary, directory, data, 0, "runner", "run")
	var terminal runnerterminal.Envelope
	require.NoError(t, json.Unmarshal(output, &terminal))
	require.Equal(t, runnerterminal.Success, terminal.Outcome)
	require.Equal(t, 1, terminal.EngineCalls)
	require.NotNil(t, terminal.Observation)
	require.Equal(t, 1, terminal.Observation.Attempts)
}

func TestRunnerAdapterAdmissionCompiledProcess(t *testing.T) {
	binary := buildRecoveryCLI(t)
	t.Run("absolute task cannot select an external file", func(t *testing.T) {
		evalPath, taskPath, eval, task := adapterCommandInputs(t)
		directory := filepath.Dir(evalPath)
		external := filepath.Join(isolatedCLITempDir(t), "external.yaml")
		require.NoError(t, os.WriteFile(external, task, 0o600))
		eval = bytes.ReplaceAll(eval, []byte("tasks/selected.case.yaml"), []byte(filepath.ToSlash(external)))
		require.Contains(t, string(eval), filepath.ToSlash(external))
		require.NoError(t, os.WriteFile(evalPath, eval, 0o600))
		nativeTask := filepath.Join(directory, external)
		require.NotEqual(t, external, nativeTask, "native filepath.Join does not select the absolute entry directly")
		artifact := filepath.Join(directory, "native.json")
		adapterProcess(t, binary, directory, nil, 2, "run", evalPath, "--output", artifact, "--no-summary")
		_, err := os.Stat(artifact)
		require.ErrorIs(t, err, os.ErrNotExist)
		// A joined drive-qualified path is not a creatable alternative on Windows.
		if filepath.VolumeName(external) == "" {
			require.NoError(t, os.MkdirAll(filepath.Dir(nativeTask), 0o700))
			nativeBytes := bytes.Replace(task, []byte("prompt: hello"), []byte("prompt: hello native-selected"), 1)
			require.NotEqual(t, task, nativeBytes)
			require.NoError(t, os.WriteFile(nativeTask, nativeBytes, 0o600))
			adapterProcess(t, binary, directory, nil, 0, "run", evalPath, "--output", artifact, "--no-summary")
			outcome, err := models.LoadEvaluationOutcome(artifact)
			require.NoError(t, err)
			require.Len(t, outcome.TestOutcomes, 1)
			require.Len(t, outcome.TestOutcomes[0].Runs, 1)
			require.Equal(t, "hello native-selected", outcome.TestOutcomes[0].Runs[0].Prompt)
		}
		for _, selected := range []string{external, nativeTask, taskPath} {
			output, diagnostic := adapterProcess(t, binary, directory, nil, 2,
				append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, selected)...)...)
			require.Empty(t, output)
			require.Contains(t, diagnostic, "relative")
		}
		compatArtifact := filepath.Join(directory, "compat.json")
		_, diagnostic := adapterProcess(t, binary, directory, nil, 2, "run", evalPath,
			"--runner-compat", "--output", compatArtifact, "--no-summary")
		require.Contains(t, diagnostic, "relative")
		_, err = os.Stat(compatArtifact)
		require.ErrorIs(t, err, os.ErrNotExist)
	})
	t.Run("relative custom file selects the same native task", func(t *testing.T) {
		evalPath, taskPath, _, _ := adapterCommandInputs(t)
		directory := filepath.Dir(evalPath)
		var native []byte
		for _, compat := range []bool{false, true} {
			artifact := filepath.Join(directory, "result.json")
			args := []string{"run", evalPath, "--output", artifact, "--no-summary"}
			if compat {
				args = append(args, "--runner-compat")
			}
			adapterProcess(t, binary, directory, nil, 0, args...)
			data, err := os.ReadFile(artifact)
			require.NoError(t, err)
			if !compat {
				native = adapterOutcomeProjection(t, data)
			} else {
				require.JSONEq(t, string(native), string(adapterOutcomeProjection(t, data)))
			}
		}
		output, _ := adapterProcess(t, binary, directory, nil, 0,
			append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...)...)
		assertAdaptedAdmission(t, binary, directory, output)
	})
	t.Run("prompt admission and alternative valid control", func(t *testing.T) {
		for _, prompt := range []string{"inspect ./file", "hello alternative"} {
			evalPath, taskPath, _, task := adapterCommandInputs(t)
			directory := filepath.Dir(evalPath)
			task = bytes.Replace(task, []byte("prompt: hello"), []byte("prompt: "+prompt), 1)
			require.Contains(t, string(task), "prompt: "+prompt)
			require.NoError(t, os.WriteFile(taskPath, task, 0o600))
			code := 0
			if prompt == "inspect ./file" {
				code = 2
			}
			output, diagnostic := adapterProcess(t, binary, directory, nil, code,
				append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...)...)
			if code != 0 {
				require.Empty(t, output)
				require.Contains(t, diagnostic, "no workspace files were loaded")
				artifact := filepath.Join(directory, "unsupported.json")
				adapterProcess(t, binary, directory, nil, 2, "run", evalPath, "--runner-compat", "--output", artifact)
				_, err := os.Stat(artifact)
				require.ErrorIs(t, err, os.ErrNotExist)
			} else {
				assertAdaptedAdmission(t, binary, directory, output)
			}
		}
	})
	t.Run("emitted byte boundaries round trip", func(t *testing.T) {
		evalPath, taskPath, _, task := adapterCommandInputs(t)
		directory := filepath.Dir(evalPath)
		initial, _ := adapterProcess(t, binary, directory, nil, 0,
			append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...)...)
		var request runnercontract.Request
		require.NoError(t, json.Unmarshal(initial, &request))
		compact, err := json.Marshal(request)
		require.NoError(t, err)
		for _, delta := range []int{-2, -1, 0, 1} {
			target := runnercontract.MaxRequestBytes + delta
			// A YAML comment grows the encoded request by exactly its ASCII length.
			padding := target - len(compact) - 1
			require.Positive(t, padding)
			padded := append(bytes.Clone(task), []byte("#"+strings.Repeat("x", padding))...)
			request.Input.Task = string(padded)
			wire, err := json.Marshal(request)
			require.NoError(t, err)
			require.Len(t, wire, target, "setup must exercise the encoded JSON limit")
			require.NoError(t, os.WriteFile(taskPath, padded, 0o600))
			code := 0
			if delta > 0 {
				code = 2
			}
			output, diagnostic := adapterProcess(t, binary, directory, nil, code,
				append([]string{"runner", "adapt"}, adapterCommandArgs(evalPath, taskPath)...)...)
			if code != 0 {
				require.Empty(t, output)
				require.Contains(t, diagnostic, "exceeds")
				continue
			}
			want := target + 1
			if target == runnercontract.MaxRequestBytes {
				want = target
			}
			require.Len(t, output, want)
			require.LessOrEqual(t, len(output), runnercontract.MaxRequestBytes)
			assertAdaptedAdmission(t, binary, directory, output)
		}
		oversize := append(bytes.Clone(compact), bytes.Repeat([]byte(" "), runnercontract.MaxRequestBytes-len(compact)+1)...)
		output, _ := adapterProcess(t, binary, directory, oversize, 6, "runner", "validate")
		var rejected runnerunit.Report
		require.NoError(t, json.Unmarshal(output, &rejected))
		require.Equal(t, "rejected", rejected.Status)
		output, _ = adapterProcess(t, binary, directory, oversize, 6, "runner", "run")
		var terminal runnerterminal.Envelope
		require.NoError(t, json.Unmarshal(output, &terminal))
		require.Equal(t, runnerterminal.InvalidInput, terminal.Outcome)
		require.Zero(t, terminal.EngineCalls)
	})
}
