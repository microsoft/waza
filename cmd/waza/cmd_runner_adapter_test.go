package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runneradapter"
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

	first := dispatchAdapter(t, contract, adapterDescriptor(t, "submit", 2, ""))
	require.Equal(t, runneradapter.StatusTerminal, first.Status, "%+v", first.Diagnostics)
	require.Equal(t, "success", first.Outcome)
	require.Equal(t, 1, *first.EngineCalls)
	require.Equal(t, "not_configured", first.Provenance.State)
	require.Equal(t, 1, contract.calls)

	reuse := `,"policy":{"mode":"reuse","source":{"execution_id":"` + first.ExecutionID + `","manifest_digest":"` + first.ManifestDigest + `"}}`
	reused := dispatchAdapter(t, contract, adapterDescriptor(t, "submit", 2, reuse))
	require.Equal(t, runneradapter.StatusTerminal, reused.Status, "%+v", reused.Diagnostics)
	require.Equal(t, "reused", reused.Public.Status)
	require.Zero(t, *reused.EngineCalls, "a scheduler-chosen reuse redelivery makes no engine call")
	require.Equal(t, first.ManifestDigest, reused.ManifestDigest)

	parent := `,"policy":{"mode":"distinct","parent":{"execution_id":"` + first.ExecutionID + `","relationship":"retry"}}`
	retry := dispatchAdapter(t, contract, adapterDescriptor(t, "submit", 3, parent))
	require.Equal(t, runneradapter.StatusTerminal, retry.Status, "%+v", retry.Diagnostics)
	require.NotEqual(t, first.ExecutionID, retry.ExecutionID)
	require.Equal(t, *first.Logical, *retry.Logical, "a retry stays on the same logical work")

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
