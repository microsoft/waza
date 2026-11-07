package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercheckpoint"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func TestCheckpointPublicCLICancelsActualBoundedProcessFixtureWithLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
const source = readFileSync("../../web/e2e/checkpoint-cli-process.test.mjs", "utf8");
const start = source.indexOf("function processRequest() {");
const end = source.indexOf("\nasync function killAt(");
assert.ok(start >= 0 && end > start);
const example = JSON.parse(readFileSync("../../examples/runner/unit.json", "utf8"));
const fixture = runInNewContext(source.slice(start, end) + "\nprocessRequest()", { assert, example, structuredClone });
process.stdout.write(JSON.stringify(fixture));
`)
	wire, err := command.Output()
	require.NoError(t, err)
	parsed, err := runnerunit.ParseSubmission(wire)
	require.NoError(t, err)
	require.Greater(t, len(parsed.Request), 1<<20)
	directory := t.TempDir()
	invocation, stop := context.WithCancel(t.Context())
	defer stop()
	checkpoint := newRunnerCheckpointCommandWithRevision(runnerunit.NewService(nil),
		func() (string, error) { return strings.Repeat("c", 40), nil })
	checkpoint.SetArgs([]string{"create", "--store", directory})
	checkpoint.SetIn(bytes.NewReader(wire))
	var output, progress bytes.Buffer
	checkpoint.SetOut(&output)
	checkpoint.SetErr(processDiagnosticWriteFunc(func(data []byte) (int, error) {
		n, err := progress.Write(data)
		if bytes.Contains(progress.Bytes(), []byte("checkpoint before-unit ")) {
			stop()
		}
		return n, err
	}))
	exitErr := checkpoint.ExecuteContext(invocation)
	exit, ok := errors.AsType[*ExitCodeError](exitErr)
	require.True(t, ok, "%v", exitErr)
	require.Equal(t, 3, exit.Code)
	result, err := runnercheckpoint.ParseResult(output.Bytes())
	require.NoError(t, err)
	require.Equal(t, 3, result.ExitCode)
	require.Equal(t, "completed", result.Status)
	require.NotEmpty(t, result.FinalizedDigest)
	require.Equal(t, runnerterminal.Canceled, result.Submission.Terminal.Outcome)
	require.Zero(t, result.Submission.EngineCalls)
	require.Len(t, result.Submission.Reservation.Binding.GraderIDs, 128)
	source := checkpointCLIStoredSource(t, directory, *result, runnerunit.LifecycleCaptureArtifact)
	require.NotEmpty(t, source.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact])
	verifyContext, stopVerification := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopVerification()
	verify := exec.CommandContext(verifyContext, "node", "--input-type=module", "-e", `
import assert from "node:assert/strict";
import { readFileSync, readdirSync } from "node:fs";
import { createHash } from "node:crypto";
import { join } from "node:path";
import { runInNewContext } from "node:vm";
const source = readFileSync("../../web/e2e/checkpoint-cli-process.test.mjs", "utf8");
const start = source.indexOf("function readBoundObject(");
const end = source.indexOf('\ntest("compiled checkpoint CLI');
assert.ok(start >= 0 && end > start);
const result = readFileSync(0, "utf8");
const verify = (result) => runInNewContext(source.slice(start, end) + "\nverifyFinalizedResult(store, JSON.parse(result));",
  { assert, readFileSync, readdirSync, createHash, join, store: process.argv[1], result });
verify(result);
const conflicting = JSON.parse(result);
conflicting.submission.completion.manifest_digest = "0".repeat(64);
assert.throws(() => verify(JSON.stringify(conflicting)), (error) => error.code === "ERR_ASSERTION");
const missing = JSON.parse(result);
missing.finalized_checkpoint_sha256 = "0".repeat(64);
assert.throws(() => verify(JSON.stringify(missing)), (error) => error.code === "ENOENT");
`, directory)
	verify.Stdin = bytes.NewReader(output.Bytes())
	verification, err := verify.CombinedOutput()
	require.NoError(t, err, "%s", verification)
}

func checkpointCLIStoredSource(t *testing.T, directory string, result runnercheckpoint.Result,
	names ...string,
) *executionledger.CompletedExecution {
	t.Helper()
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeBackend()) }()
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	require.NotNil(t, result.Submission)
	require.NotNil(t, result.Submission.Reservation)
	require.NotNil(t, result.Submission.Completion)
	source, err := ledger.ReadCompleted(t.Context(), executionattempt.Source{
		ExecutionID:    result.Submission.Reservation.ExecutionID,
		ManifestDigest: result.Submission.Completion.ManifestDigest,
	}, names...)
	require.NoError(t, err)
	return source
}

func TestCheckpointPublicCLIActualLifecycleFirstInventoryAndImmutableReuse(t *testing.T) {
	for _, control := range []string{"success", "failed-checks", "canceled"} {
		t.Run(control, func(t *testing.T) {
			wire := checkpointCLIWire(t)
			wantOutcome, wantCode := runnerterminal.Success, 0
			if control == "failed-checks" {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- absent-lifecycle-verdict-control"), 1)
				require.NotEqual(t, wire, changed)
				wire = changed
				wantOutcome, wantCode = runnerterminal.EvaluationFailure, 1
			}
			calls := 0
			factory := func(config models.Config) (execution.AgentEngine, error) {
				calls++
				if control == "canceled" {
					return nil, context.Canceled
				}
				return execution.NewMockEngine(config.ModelID), nil
			}
			if control == "canceled" {
				wantOutcome, wantCode = runnerterminal.Canceled, 3
			}
			revision := strings.Repeat("c", 40)
			baselineDirectory := t.TempDir()
			backend, closeBackend, err := artifactmanifest.OpenBackend(baselineDirectory, "")
			require.NoError(t, err)
			legacy, err := runnercheckpoint.NewService(runnerunit.NewService(factory), revision)
			require.NoError(t, err)
			baseline, err := legacy.SubmitCaptured(t.Context(), wire, backend, runnercheckpoint.Hooks{})
			require.NoError(t, err)
			require.NoError(t, closeBackend())
			require.Equal(t, wantOutcome, baseline.Submission.Terminal.Outcome)
			original := checkpointCLIStoredSource(t, baselineDirectory, baseline)
			names := []string{}
			for _, artifact := range original.Publication.Manifest.Artifacts {
				names = append(names, artifact.Name)
			}
			require.Contains(t, names, "execution-attempt.json")
			require.Contains(t, names, "execution-evidence-capture.json")
			require.NotContains(t, names, runnerunit.LifecycleCaptureArtifact)
			require.NotContains(t, names, "report.json")
			require.Equal(t, 1, calls)
			calls = 0
			service := runnerunit.NewService(factory)
			directory := t.TempDir()
			created, _ := runCheckpointCLI(t, service, func() (string, error) { return revision, nil }, directory, wire, "create")
			require.Equal(t, wantCode, created.ExitCode)
			require.Equal(t, wantOutcome, created.Submission.Terminal.Outcome)
			require.Nil(t, created.Submission.Report)
			require.Equal(t, 1, calls)
			source := checkpointCLIStoredSource(t, directory, created, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json")
			actualNames := []string{}
			for _, artifact := range source.Publication.Manifest.Artifacts {
				actualNames = append(actualNames, artifact.Name)
			}
			require.ElementsMatch(t, append(names, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json"), actualNames)
			var capture runnerunit.LifecycleCapture
			require.NoError(t, json.Unmarshal(source.Publication.ArtifactBytes[runnerunit.LifecycleCaptureArtifact], &capture))
			require.Equal(t, source.Reservation.Bytes, []byte(capture.Reservation))
			require.Equal(t, "mock", capture.OriginalAdmission.ExecutionMode)
			require.False(t, capture.Runtime.OwnedEngineBound)
			require.Equal(t, "unknown", capture.Runtime.ProcessExit)
			require.NotEmpty(t, capture.Deadline)
			require.Len(t, capture.Observations, 2)
			require.Equal(t, *created.Submission.Terminal, *capture.Observations[1].Terminal)
			for i, name := range []string{"lifecycle-observation-001.json", "lifecycle-observation-002.json"} {
				event, err := executionlifecycle.Encode(capture.Observations[i])
				require.NoError(t, err)
				require.Equal(t, event, source.Publication.ArtifactBytes[name])
				require.Equal(t, artifactmanifest.Hash(event), capture.ObservationDigests[i])
			}
			destination := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			require.NotEqual(t, wire, destination)
			reused, _ := runCheckpointCLI(t, service, func() (string, error) { return revision, nil }, directory,
				destination, "resume", created.FinalizedDigest)
			require.Equal(t, wantCode, reused.ExitCode)
			require.Zero(t, reused.Submission.EngineCalls)
			require.Equal(t, created.Submission.Terminal, reused.Submission.Terminal)
			require.Equal(t, created.Submission.Reservation, reused.Submission.Reservation)
			require.Equal(t, 1, calls)
			after := checkpointCLIStoredSource(t, directory, reused, runnerunit.LifecycleCaptureArtifact,
				"lifecycle-observation-001.json", "lifecycle-observation-002.json", "terminal.json")
			require.Equal(t, source.Publication.ArtifactBytes, after.Publication.ArtifactBytes)
		})
	}
}
