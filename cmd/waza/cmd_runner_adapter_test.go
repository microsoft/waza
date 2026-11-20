package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/executiontelemetry"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/provenance"
	"github.com/microsoft/waza/internal/runneradapter"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// publicCLIContract serves adapter calls with the public runner commands
// in-process, exactly as `waza runner ...` would in a container.
type publicCLIContract struct {
	t         *testing.T
	directory string
	service   *runnerunit.Service
	calls     int
}

func (p *publicCLIContract) Call(ctx context.Context, call runneradapter.Call) (runneradapter.Reply, error) {
	p.calls++
	require.Equal(p.t, "runner", call.Command[0])
	root := &cobra.Command{Use: "runner", SilenceUsage: true, SilenceErrors: true}
	revision := func() (string, error) { return strings.Repeat("c", 40), nil }
	root.AddCommand(
		newRunnerSubmitCommand(p.service, artifactmanifest.OpenBackend),
		newRunnerCheckpointCommandWithBackend(p.service, revision, artifactmanifest.OpenBackend),
		newRunnerLineageCommand(artifactmanifest.OpenBackend),
	)
	root.SetArgs(append(slices.Clone(call.Command[1:]), "--store", p.directory))
	root.SetIn(strings.NewReader(call.Input))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&bytes.Buffer{})
	err := root.ExecuteContext(ctx)
	if exit, ok := errors.AsType[*ExitCodeError](err); ok {
		return runneradapter.Reply{ExitCode: exit.Code, Body: output.Bytes()}, nil
	}
	return runneradapter.Reply{Body: output.Bytes()}, err
}

func runAdapterCLI(t *testing.T, contract runneradapter.Contract, descriptor []byte, args ...string) ([]byte, int) {
	t.Helper()
	command := newRunnerAdapterCommandWithContract(func(adapterContractOptions) (runneradapter.Contract, error) { return contract, nil })
	command.SetArgs(args)
	command.SetIn(bytes.NewReader(descriptor))
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	err := command.ExecuteContext(t.Context())
	if err == nil {
		return output.Bytes(), 0
	}
	exit, ok := errors.AsType[*ExitCodeError](err)
	require.True(t, ok, "%v", err)
	return output.Bytes(), exit.Code
}

func adapterDescriptor(t *testing.T, operation string, attempt int, extra string) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	raw = bytes.Replace(bytes.TrimSpace(raw), []byte(`"attempt": 2`), []byte(`"attempt": `+strconv.Itoa(attempt)), 1)
	return []byte(`{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"vendor-neutral/` + operation +
		`","operation":"` + operation + `","request":` + string(raw) + extra + `}`)
}

func dispatchAdapter(t *testing.T, contract runneradapter.Contract, descriptor []byte) runneradapter.Result {
	t.Helper()
	output, exit := runAdapterCLI(t, contract, descriptor, "dispatch")
	result, err := runneradapter.ParseResult(output)
	require.NoError(t, err, "%s", output)
	require.Equal(t, result.ExitCode, exit)
	return *result
}

// TestRunnerAdapterVendorNeutralEndToEnd drives dispatch, safe retry, reuse,
// checkpoint resume, status and independent aggregation using only adapter
// descriptors and the public runner contract.
func TestRunnerAdapterVendorNeutralEndToEnd(t *testing.T) {
	contract := &publicCLIContract{t: t, directory: t.TempDir(), service: runnerunit.NewService(nil)}

	translated, exit := runAdapterCLI(t, contract, adapterDescriptor(t, "submit", 2, ""), "translate")
	require.Equal(t, 0, exit)
	again, _ := runAdapterCLI(t, contract, adapterDescriptor(t, "submit", 2, ""), "translate")
	require.Equal(t, translated, again, "redelivered descriptors translate to identical bytes")
	require.Zero(t, contract.calls, "translate never dispatches")
	translation, err := runneradapter.ParseTranslation(translated)
	require.NoError(t, err)
	require.NotNil(t, translation.Call)
	var callInput struct {
		Kind    string          `json:"kind"`
		Request json.RawMessage `json:"request"`
	}
	require.NoError(t, json.Unmarshal([]byte(translation.Call.Input), &callInput))
	require.NotEmpty(t, callInput.Request)
	// The provenance hook is a real offline verifier: it checks the Ed25519
	// signature and every bound digest of a published bundle with a trusted key.
	keys := t.TempDir()
	privateKey, publicKey := filepath.Join(keys, "signing.pem"), filepath.Join(keys, "signing.pub.pem")
	_, code := runProvenanceCLI(t, "keygen", "--private-key", privateKey, "--public-key", publicKey)
	require.Zero(t, code)
	bundle := filepath.Join(t.TempDir(), "bundle")
	t.Setenv(signedProvenanceBundleEnv, bundle)
	t.Setenv(signedProvenanceKeyEnv, publicKey)
	signedVerifierArgs := []string{
		"--provenance-verifier", os.Args[0],
		"--provenance-verifier-arg", "-test.run=^TestRunnerAdapterSignedProvenanceVerifierProcessHelper$",
	}

	firstOutput, firstExit := runAdapterCLI(t, contract, adapterDescriptor(t, "submit", 2, ""),
		append([]string{"dispatch"}, signedVerifierArgs...)...)
	first, err := runneradapter.ParseResult(firstOutput)
	require.NoError(t, err, "%s", firstOutput)
	require.Equal(t, first.ExitCode, firstExit)
	require.Equal(t, runneradapter.StatusTerminal, first.Status, "%+v", first.Diagnostics)
	require.Equal(t, "success", first.Outcome)
	require.Equal(t, 1, *first.EngineCalls)
	require.Equal(t, "failed", first.Provenance.State, "an unpublished provenance bundle fails closed")
	require.Equal(t, 1, contract.calls)

	publishSignedProvenance(t, contract.directory, first.ManifestDigest, callInput.Request, privateKey, bundle)

	reuse := `,"policy":{"mode":"reuse","source":{"execution_id":"` + first.ExecutionID + `","manifest_digest":"` + first.ManifestDigest + `"}}`
	reusedOutput, reusedExit := runAdapterCLI(t, contract, adapterDescriptor(t, "submit", 2, reuse),
		append([]string{"dispatch"}, signedVerifierArgs...)...)
	reused, err := runneradapter.ParseResult(reusedOutput)
	require.NoError(t, err, "%s", reusedOutput)
	require.Equal(t, reused.ExitCode, reusedExit)
	require.Equal(t, runneradapter.StatusTerminal, reused.Status, "%+v", reused.Diagnostics)
	require.Equal(t, "reused", reused.Public.Status)
	require.Zero(t, *reused.EngineCalls, "a scheduler-chosen reuse redelivery makes no engine call")
	require.Equal(t, first.ManifestDigest, reused.ManifestDigest)
	require.Equal(t, "verified", reused.Provenance.State, "%+v", reused.Provenance)

	parent := `,"policy":{"mode":"distinct","parent":{"execution_id":"` + first.ExecutionID + `","relationship":"retry"}}`
	retry := dispatchAdapter(t, contract, adapterDescriptor(t, "submit", 3, parent))
	require.Equal(t, runneradapter.StatusTerminal, retry.Status, "%+v", retry.Diagnostics)
	require.NotEqual(t, first.ExecutionID, retry.ExecutionID)
	require.Equal(t, *first.Logical, *retry.Logical, "a retry stays on the same logical work")
	verifySignedProvenanceFailures(t, bundle, keys, *first, retry.ManifestDigest)

	checkpoint := dispatchAdapter(t, contract, adapterDescriptor(t, "checkpoint", 2, ""))
	require.Equal(t, runneradapter.StatusTerminal, checkpoint.Status, "%+v", checkpoint.Diagnostics)
	require.Len(t, checkpoint.CheckpointSHA256, 64)
	resumed := dispatchAdapter(t, contract, adapterDescriptor(t, "resume", 3, `,"checkpoint_sha256":"`+checkpoint.CheckpointSHA256+`"`))
	require.Equal(t, runneradapter.StatusTerminal, resumed.Status, "%+v", resumed.Diagnostics)
	require.Zero(t, *resumed.EngineCalls, "resuming a finalized checkpoint returns the original terminal")
	require.Equal(t, checkpoint.ManifestDigest, resumed.ManifestDigest)

	status := []byte(`{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"vendor-neutral/status","operation":"status",` +
		`"logical":{"run":"` + first.Logical.Run + `","work":"` + first.Logical.Work + `"}}`)
	observed := dispatchAdapter(t, contract, status)
	require.Equal(t, runneradapter.StatusObserved, observed.Status, "%+v", observed.Diagnostics)
	var lineage struct {
		Attempts []json.RawMessage `json:"attempts"`
	}
	require.NoError(t, json.Unmarshal(observed.PublicResult, &lineage))
	require.GreaterOrEqual(t, len(lineage.Attempts), 3)

	invalid := dispatchAdapter(t, contract, adapterDescriptor(t, "submit", 2, `,"priority":"high"`))
	require.Equal(t, runneradapter.StatusRejected, invalid.Status)
	require.False(t, invalid.Dispatched)
	callsBeforeAggregate := contract.calls
	require.Equal(t, 6, callsBeforeAggregate)

	digests := []string{first.ManifestDigest, retry.ManifestDigest, checkpoint.ManifestDigest}
	aggregate := newAggregateCommand()
	var report bytes.Buffer
	aggregate.SetOut(&report)
	aggregate.SetErr(&bytes.Buffer{})
	aggregate.SetArgs(append([]string{"--store", contract.directory}, digests...))
	require.NoError(t, aggregate.ExecuteContext(t.Context()))
	var decoded manifestaggregate.Report
	require.NoError(t, json.Unmarshal(report.Bytes(), &decoded), "%s", report.Bytes())
	require.Len(t, decoded.Members, len(digests), "returned manifests aggregate independently of the adapter")

	verifyAdapterTelemetry(t, contract.directory, first.ManifestDigest, report.Bytes())
}

const (
	signedProvenanceBundleEnv = "WAZA_TEST_SIGNED_PROVENANCE_BUNDLE"
	signedProvenanceKeyEnv    = "WAZA_TEST_SIGNED_PROVENANCE_PUBLIC_KEY"
)

// publishSignedProvenance signs the stored execution with the public
// `waza runner provenance generate` command, as a producer would.
func publishSignedProvenance(t *testing.T, directory, manifestDigest string, request []byte, privateKey, bundle string) {
	t.Helper()
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeBackend()) }()
	store := artifactmanifest.NewStore(backend)
	declared, err := store.ReadCompleted(t.Context(), manifestDigest)
	require.NoError(t, err)
	var names []string
	for _, artifact := range declared.Manifest.Artifacts {
		names = append(names, artifact.Name)
	}
	completed, err := store.ReadCompleted(t.Context(), manifestDigest, names...)
	require.NoError(t, err)
	require.Equal(t, manifestDigest, artifactmanifest.Hash(completed.ManifestBytes))

	inputs := t.TempDir()
	artifacts := filepath.Join(inputs, "artifacts")
	require.NoError(t, os.Mkdir(artifacts, 0o755))
	for name, data := range completed.ArtifactBytes {
		require.NoError(t, os.WriteFile(filepath.Join(artifacts, name), data, 0o644))
	}
	files := map[string][]byte{"request.json": request, "manifest.json": completed.ManifestBytes, "policy.json": []byte("{\"policy\":\"adapter-e2e\"}\n")}
	for name, data := range files {
		require.NoError(t, os.WriteFile(filepath.Join(inputs, name), data, 0o644))
	}
	cmd := newRunnerCommand()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"provenance", "generate",
		"--request", filepath.Join(inputs, "request.json"), "--manifest", filepath.Join(inputs, "manifest.json"),
		"--artifacts", artifacts, "--policy", filepath.Join(inputs, "policy.json"),
		"--image-reference", "ghcr.io/microsoft/waza-runner:e2e", "--image-digest", "sha256:" + strings.Repeat("e", 64),
		"--waza-version", "0.42.0", "--signing-key", privateKey, "--out", bundle})
	require.NoError(t, cmd.Execute(), "stdout: %s\nstderr: %s", stdout.String(), stderr.String())
}

// verifySignedProvenanceFailures drives the adapter's process hook with a
// tampered terminal outcome, an untrusted key and a substituted manifest.
func verifySignedProvenanceFailures(t *testing.T, bundle, keys string, first runneradapter.Result, otherManifest string) {
	t.Helper()
	verifier := commandProvenanceVerifier{path: os.Args[0], args: []string{"-test.run=^TestRunnerAdapterSignedProvenanceVerifierProcessHelper$"}}
	verify := func(manifestDigest string) runneradapter.Provenance {
		return verifier.VerifyProvenance(t.Context(), manifestDigest, first.PublicResult)
	}
	require.Equal(t, "verified", verify(first.ManifestDigest).State)
	require.NotEqual(t, first.ManifestDigest, otherManifest)
	require.Equal(t, "failed", verify(otherManifest).State, "a valid bundle for another execution must not verify this manifest")

	otherPrivate, otherPublic := filepath.Join(keys, "other.pem"), filepath.Join(keys, "other.pub.pem")
	_, code := runProvenanceCLI(t, "keygen", "--private-key", otherPrivate, "--public-key", otherPublic)
	require.Zero(t, code)
	t.Setenv(signedProvenanceKeyEnv, otherPublic)
	require.Equal(t, "failed", verify(first.ManifestDigest).State, "a bundle signed by an untrusted key fails")
	t.Setenv(signedProvenanceKeyEnv, filepath.Join(keys, "signing.pub.pem"))

	tampered := filepath.Join(t.TempDir(), "tampered")
	require.NoError(t, copyProvenanceFixture(bundle, filepath.Join(keys, "signing.pub.pem"), tampered))
	terminalPath := filepath.Join(tampered, "bundle", "artifacts", "objects", "terminal.json")
	terminal, err := os.ReadFile(terminalPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(terminalPath, append(terminal, ' '), 0o644))
	t.Setenv(signedProvenanceBundleEnv, filepath.Join(tampered, "bundle"))
	require.Equal(t, "failed", verify(first.ManifestDigest).State, "a tampered terminal outcome fails")
	t.Setenv(signedProvenanceBundleEnv, bundle)
	require.Equal(t, "verified", verify(first.ManifestDigest).State, "the untouched bundle still verifies")
}

// TestRunnerAdapterSignedProvenanceVerifierProcessHelper is a real provenance
// hook: it runs `waza runner provenance verify` against a trusted public key
// and answers verified only when the signed manifest digest is the one the
// adapter returned.
func TestRunnerAdapterSignedProvenanceVerifierProcessHelper(t *testing.T) {
	bundle, publicKey := os.Getenv(signedProvenanceBundleEnv), os.Getenv(signedProvenanceKeyEnv)
	if bundle == "" || publicKey == "" {
		return
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, provenanceVerifierMaxIO+1))
	if err != nil || len(data) > provenanceVerifierMaxIO {
		os.Exit(2)
	}
	var request struct {
		ManifestDigest string          `json:"manifest_digest"`
		PublicResult   json.RawMessage `json:"public_result"`
	}
	if json.Unmarshal(data, &request) != nil || request.ManifestDigest == "" {
		os.Exit(2)
	}
	output, code := runProvenanceCLI(t, "verify", "--bundle", bundle, "--public-key", publicKey)
	var report provenance.Report
	if code != 0 || json.Unmarshal([]byte(output), &report) != nil || report.Status != "verified" ||
		report.Predicate == nil || report.Predicate.Artifacts.Manifest.SHA256 != request.ManifestDigest {
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"state":"verified"}`)
	os.Exit(0)
}

func verifyAdapterTelemetry(t *testing.T, directory, manifestDigest string, aggregate []byte) {
	t.Helper()
	backend, closeBackend, err := artifactmanifest.OpenBackend(directory, "")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeBackend()) })
	completed, err := artifactmanifest.NewStore(backend).ReadCompleted(
		t.Context(), manifestDigest, "terminal.json", executiontelemetry.AttemptArtifact,
	)
	require.NoError(t, err)
	terminalBytes := completed.ArtifactBytes[completed.Manifest.TerminalArtifact]
	attemptBytes := completed.ArtifactBytes[executiontelemetry.AttemptArtifact]
	require.NotEmpty(t, terminalBytes)
	require.NotEmpty(t, attemptBytes)
	envelope, err := runnerterminal.Validate(terminalBytes, completed.Manifest.Binding.TerminalBinding())
	require.NoError(t, err)

	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	lineage, err := ledger.Lineage(t.Context(), executionattempt.LogicalFrom(*envelope.Identity), executionledger.LineageOptions{})
	require.NoError(t, err)
	lineageBytes, err := executionledger.EncodeLineage(*lineage)
	require.NoError(t, err)
	report, err := json.Marshal(map[string]any{"tasks": []any{map[string]any{
		"test_id": envelope.Selection.Task,
		"runs": []any{map[string]any{
			"run_number": envelope.Observation.RunNumber, "prompt": "SECRET-ADAPTER-PROMPT",
			"final_output": "SECRET-ADAPTER-OUTPUT", "attempts": envelope.Observation.Attempts,
			"status": envelope.Observation.Status, "error_msg": envelope.Observation.ErrorMsg,
			"validations": adapterVerdicts(envelope),
			"session_digest": map[string]any{"tool_calls": []any{
				map[string]any{"id": "secret-call", "name": "bash", "arguments": map[string]any{"command": "SECRET-ADAPTER-ARGS"},
					"result": map[string]any{"content": "SECRET-ADAPTER-RESULT"}, "success": true},
			}},
		}},
	}}})
	require.NoError(t, err)
	source, err := executiontelemetry.Load(executiontelemetry.Inputs{
		Terminal: terminalBytes, Manifest: completed.ManifestBytes, Attempt: attemptBytes,
		Lineage: lineageBytes, Report: report, Aggregate: aggregate, SchedulerWorkID: "queue/job-42",
	})
	require.NoError(t, err)
	bundle := executiontelemetry.Build(source, executiontelemetry.Options{
		Policy: executiontelemetry.PayloadExclude, ServiceVersion: "qualification",
		Now: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
	})
	require.Equal(t, envelope.Identity.Run, bundle.Report.Correlation.Run)
	require.Equal(t, envelope.Identity.Trial, bundle.Report.Correlation.Work)
	require.Equal(t, "queue/job-42", bundle.Report.Correlation.SchedulerWorkID)
	var correlatedManifests []string
	for _, attempt := range bundle.Report.Correlation.Attempts {
		correlatedManifests = append(correlatedManifests, attempt.ManifestSHA256)
	}
	require.Contains(t, correlatedManifests, manifestDigest)
	require.NotNil(t, bundle.Report.Correlation.Aggregation)
	signals, err := json.Marshal(bundle)
	require.NoError(t, err)
	for _, secret := range []string{
		"SECRET-ADAPTER-PROMPT", "SECRET-ADAPTER-OUTPUT",
		"SECRET-ADAPTER-ARGS", "SECRET-ADAPTER-RESULT",
	} {
		require.NotContains(t, string(signals), secret)
	}
}

func adapterVerdicts(envelope *runnerterminal.Envelope) map[string]any {
	validations := make(map[string]any, len(envelope.Observation.Validations))
	for _, validation := range envelope.Observation.Validations {
		validations[validation.Identifier] = map[string]any{"name": validation.Identifier, "passed": validation.Passed}
	}
	return validations
}

func TestRunnerAdapterCLIRejectsWithoutDispatch(t *testing.T) {
	contract := &publicCLIContract{t: t, directory: t.TempDir(), service: runnerunit.NewService(nil)}
	output, exit := runAdapterCLI(t, contract, []byte(`{"kind":"waza.adapter-work-descriptor"`), "translate")
	require.Equal(t, 6, exit)
	translation, err := runneradapter.ParseTranslation(output)
	require.NoError(t, err)
	require.Equal(t, "rejected", translation.Status)
	_, exit = runAdapterCLI(t, contract, adapterDescriptor(t, "submit", 2, ""), "dispatch", "--endpoint", "http://127.0.0.1:1", "--store", "x")
	require.Equal(t, 6, exit)
	require.Zero(t, contract.calls)
}

// TestRunnerAdapterConfiguredProvenanceVerifier covers only the hook process
// protocol with a stub answer; signed verification is exercised end to end by
// TestRunnerAdapterVendorNeutralEndToEnd.
func TestRunnerAdapterConfiguredProvenanceVerifier(t *testing.T) {
	t.Setenv("WAZA_TEST_PROVENANCE_VERIFIER", "1")
	contract := &publicCLIContract{t: t, directory: t.TempDir(), service: runnerunit.NewService(nil)}
	command := newRunnerAdapterCommandWithContract(func(adapterContractOptions) (runneradapter.Contract, error) {
		return contract, nil
	})
	command.SetArgs([]string{
		"dispatch", "--store", contract.directory,
		"--provenance-verifier", os.Args[0],
		"--provenance-verifier-arg", "-test.run=TestRunnerAdapterProvenanceVerifierProcessHelper",
	})
	command.SetIn(bytes.NewReader(adapterDescriptor(t, "submit", 2, "")))
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	require.NoError(t, command.ExecuteContext(t.Context()))
	result, err := runneradapter.ParseResult(output.Bytes())
	require.NoError(t, err, "%s", output.Bytes())
	require.Equal(t, "verified", result.Provenance.State)
	require.NotEmpty(t, result.ManifestDigest)
	require.Equal(t, 1, contract.calls, "verifier execution does not redispatch runner work")
}

func TestCommandProvenanceVerifierBoundsOutputAndPipeDrain(t *testing.T) {
	var output boundedVerifierOutput
	written, err := output.Write(make([]byte, provenanceVerifierMaxIO))
	require.NoError(t, err)
	require.Equal(t, provenanceVerifierMaxIO, written)
	written, err = output.Write([]byte("x"))
	require.Error(t, err)
	require.Zero(t, written)
	require.Len(t, output.Bytes(), provenanceVerifierMaxIO)

	t.Setenv("WAZA_TEST_PROVENANCE_VERIFIER", "1")
	t.Setenv("WAZA_TEST_VERIFIER_HOLD_PIPE", "1")
	verifier := commandProvenanceVerifier{
		path:      os.Args[0],
		args:      []string{"-test.run=TestRunnerAdapterProvenanceVerifierProcessHelper"},
		waitDelay: 25 * time.Millisecond,
	}
	result := verifier.VerifyProvenance(t.Context(), strings.Repeat("a", 64), json.RawMessage(`{}`))
	require.Equal(t, "unavailable", result.State)
	require.Contains(t, result.Detail, "output drain exceeded")
}

func TestRunnerAdapterProvenanceVerifierProcessHelper(t *testing.T) {
	if os.Getenv("WAZA_TEST_PROVENANCE_VERIFIER") != "1" {
		return
	}
	if os.Getenv("WAZA_TEST_VERIFIER_HOLD_PIPE_CHILD") == "1" {
		time.Sleep(250 * time.Millisecond)
		os.Exit(0)
	}
	if os.Getenv("WAZA_TEST_VERIFIER_HOLD_PIPE") == "1" {
		child := exec.Command(os.Args[0], "-test.run=TestRunnerAdapterProvenanceVerifierProcessHelper")
		child.Env = append(os.Environ(), "WAZA_TEST_VERIFIER_HOLD_PIPE_CHILD=1")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, provenanceVerifierMaxIO+1))
	if err != nil || len(data) > provenanceVerifierMaxIO {
		os.Exit(2)
	}
	var request struct {
		ManifestDigest string          `json:"manifest_digest"`
		PublicResult   json.RawMessage `json:"public_result"`
	}
	if json.Unmarshal(data, &request) != nil || request.ManifestDigest == "" || len(request.PublicResult) == 0 {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(os.Stdout, `{"state":"verified"}`)
	os.Exit(0)
}
