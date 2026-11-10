package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestRunnerSubmissionInputRetainsWrapperLimit(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	padded := append(bytes.Clone(wrapper), bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes-len(wrapper))...)
	require.Len(t, padded, runnerunit.MaxSubmissionBytes)
	for _, control := range []struct {
		name string
		data []byte
		size int
	}{
		{"at-limit", padded, runnerunit.MaxSubmissionBytes},
		{"over-limit", append(bytes.Clone(padded), ' ', ' '), runnerunit.MaxSubmissionBytes + 1},
	} {
		t.Run(control.name, func(t *testing.T) {
			data, err := readRunnerInputLimit(t.Context(), bytes.NewReader(control.data), runnerunit.MaxSubmissionBytes)
			require.NoError(t, err)
			require.Equal(t, control.data[:control.size], data)
			_, err = runnerunit.ParseSubmission(data)
			if control.name == "at-limit" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	legacy, err := readRunnerInput(t.Context(), bytes.NewReader(padded))
	require.NoError(t, err)
	require.Equal(t, padded[:runnercontract.MaxRequestBytes+1], legacy)
}

func TestRunnerSubmissionInheritedInputChild(t *testing.T) {
	if os.Getenv("WAZA_TEST_SUBMISSION_INPUT") != "1" {
		return
	}
	data, err := readRunnerInputLimit(t.Context(), os.Stdin, runnerunit.MaxSubmissionBytes)
	require.NoError(t, err)
	_, parseErr := runnerunit.ParseSubmission(data)
	require.NoError(t, json.NewEncoder(os.Stdout).Encode(struct {
		Bytes    int    `json:"bytes"`
		SHA256   string `json:"sha256"`
		Admitted bool   `json:"admitted"`
	}{len(data), fmt.Sprintf("%x", sha256.Sum256(data)), parseErr == nil}))
}

func TestRunnerSubmissionInheritedInputRetainsWrapperLimit(t *testing.T) {
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	wrapper, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw,
	})
	require.NoError(t, err)
	padded := append(bytes.Clone(wrapper), bytes.Repeat([]byte(" "), runnerunit.MaxSubmissionBytes-len(wrapper))...)
	for _, overLimit := range []bool{false, true} {
		t.Run(fmt.Sprint(overLimit), func(t *testing.T) {
			payload := bytes.Clone(padded)
			expectedSize := len(payload)
			if overLimit {
				payload = append(payload, ' ', ' ')
				expectedSize++
			}
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRunnerSubmissionInheritedInputChild$", "-test.count=1")
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "WAZA_TEST_SUBMISSION_INPUT=") {
					child.Env = append(child.Env, entry)
				}
			}
			child.Env = append(child.Env, "WAZA_TEST_SUBMISSION_INPUT=1")
			child.Stdin = bytes.NewReader(payload)
			var stderr bytes.Buffer
			child.Stderr = &stderr
			output, err := child.Output()
			require.NoError(t, err, "%s", stderr.String())
			var observed struct {
				Bytes    int    `json:"bytes"`
				SHA256   string `json:"sha256"`
				Admitted bool   `json:"admitted"`
			}
			require.NoError(t, json.NewDecoder(bytes.NewReader(output)).Decode(&observed), "%s", output)
			require.Equal(t, expectedSize, observed.Bytes)
			require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(payload[:expectedSize])), observed.SHA256)
			require.Equal(t, !overLimit, observed.Admitted)
		})
	}
}

func TestRunnerSubmissionInputCancellationAndReadFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := &submissionInputFailure{}
	data, err := readRunnerInputLimit(ctx, reader, runnerunit.MaxSubmissionBytes)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, data)
	require.Zero(t, reader.calls, "canceled admission must not start reading")

	data, err = readRunnerInputLimit(t.Context(), reader, runnerunit.MaxSubmissionBytes)
	require.ErrorIs(t, err, errSubmissionInput)
	require.Nil(t, data)
	require.Equal(t, 1, reader.calls)
}

var errSubmissionInput = errors.New("controlled submission input failure")

type submissionInputFailure struct{ calls int }

func (r *submissionInputFailure) Read([]byte) (int, error) {
	r.calls++
	return 0, errSubmissionInput
}
