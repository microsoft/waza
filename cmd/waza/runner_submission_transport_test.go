package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRunnerSubmissionTransportRetainsFullWrapperLimit(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	padded := append(bytes.Clone(wrapper), bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes-len(wrapper))...)
	require.Len(t, padded, runnerunit.MaxSubmissionBytes)
	data, err := readRunnerInputLimit(context.Background(), bytes.NewReader(padded), runnerunit.MaxSubmissionBytes)
	require.NoError(t, err)
	require.Equal(t, padded, data)
	_, err = runnerunit.ParseSubmission(data)
	require.NoError(t, err)
	tooLarge := append(bytes.Clone(padded), ' ')
	data, err = readRunnerInputLimit(context.Background(), bytes.NewReader(tooLarge), runnerunit.MaxSubmissionBytes)
	require.NoError(t, err)
	require.Len(t, data, runnerunit.MaxSubmissionBytes+1)
	_, err = runnerunit.ParseSubmission(data)
	require.Error(t, err)
	legacy, err := readRunnerInput(context.Background(), bytes.NewReader(padded))
	require.NoError(t, err)
	require.Len(t, legacy, runnercontract.MaxRequestBytes+1)
}

func TestRunnerSubmissionTransportEmitsOneValidatedSourceResult(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	result, err := runnerunit.NewService(nil).Submit(context.Background(), wrapper, backend, runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	command := &cobra.Command{}
	var output bytes.Buffer
	command.SetOut(&output)
	require.NoError(t, emitSubmission(command, result))
	parsed, err := runnerunit.ParseSubmissionResult(output.Bytes())
	require.NoError(t, err)
	require.Equal(t, result.Reservation.ExecutionID, parsed.Completion.ExecutionID)
	require.Empty(t, parsed.Report)
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	require.NoError(t, decoder.Decode(new(json.RawMessage)))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)

	output.Reset()
	command.SetOut(failingSubmissionWriter{})
	err = emitSubmission(command, result)
	exit, ok := errors.AsType[*ExitCodeError](err)
	require.True(t, ok)
	require.Equal(t, 2, exit.Code)
	command.SetOut(shortSubmissionWriter{})
	err = emitSubmission(command, result)
	require.ErrorIs(t, err, io.ErrShortWrite)
	command.SetOut(&output)
	result.EngineCalls = 9
	require.Error(t, emitSubmission(command, result))
	require.Empty(t, output.Bytes(), "invalid original output must not be partially emitted")
}

type failingSubmissionWriter struct{}

func (failingSubmissionWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

type shortSubmissionWriter struct{}

func (shortSubmissionWriter) Write(data []byte) (int, error) { return len(data) - 1, nil }

func TestRunnerOperationCLIFramingAndStrictCallerBoundary(t *testing.T) {
	cmd := &cobra.Command{}
	var output bytes.Buffer
	cmd.SetOut(&output)
	for _, suffix := range []string{"", "\n", "\n\n", "\r\n \t\n"} {
		output.Reset()
		require.NoError(t, emitRunnerOperationJSON(cmd, []byte("{\"recorded\":0}"+suffix), 0, "completed"))
		require.Equal(t, "{\"recorded\":0}\n", output.String())
	}
	output.Reset()
	require.Error(t, emitRunnerOperationJSON(cmd, []byte("{"), 0, "completed"))
	require.Empty(t, output.Bytes())
}
