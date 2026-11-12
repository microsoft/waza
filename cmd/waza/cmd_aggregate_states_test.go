package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/require"
)

// runAggregateCLI returns the saved report and command error so evaluated
// incomplete/conflicting reports can be compared with the API's 409 body.
func runAggregateCLI(t *testing.T, directory string, inputs []manifestaggregate.Input) ([]byte, error) {
	t.Helper()
	wire, err := json.Marshal(inputs)
	require.NoError(t, err)
	input := filepath.Join(t.TempDir(), "inputs.json")
	output := filepath.Join(t.TempDir(), "aggregate.json")
	require.NoError(t, os.WriteFile(input, wire, 0o600))
	command := newAggregateCommand()
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"--store", directory, "--inputs", input, "-o", output})
	runErr := command.Execute()
	saved, err := os.ReadFile(output)
	require.NoError(t, err, "every evaluated report must be saved, including incomplete ones")
	require.Equal(t, append(bytes.Clone(saved), '\n'), stdout.Bytes())
	return saved, runErr
}

// publishCanceledMetadataOnly finalizes an authored canceled terminal without
// an execution sidecar, mirroring a canceled source from an earlier result set.
func publishCanceledMetadataOnly(t *testing.T, backend *artifactmanifest.Local, origin string) string {
	t.Helper()
	store := artifactmanifest.NewStore(backend)
	source, err := store.ReadCompleted(t.Context(), origin)
	require.NoError(t, err)
	manifest := source.Manifest
	terminal, err := runnerterminal.Validate(source.ArtifactBytes[manifest.TerminalArtifact], manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	terminal.Outcome = runnerterminal.Canceled
	terminal.ExitCode, _ = terminal.Outcome.ExitCode()
	terminal.FailureBoundary = "shutdown"
	terminal.Diagnostics = []runnerterminal.Diagnostic{{Code: "cancellation", Message: "Authored original cancellation"}}
	terminalBytes, err := runnerterminal.Encode(*terminal, manifest.Binding.TerminalBinding())
	require.NoError(t, err)
	manifest.Artifacts = []artifactmanifest.Artifact{{
		Name: manifest.TerminalArtifact, SHA256: artifactmanifest.Hash(terminalBytes),
		SizeBytes: int64(len(terminalBytes)), MediaType: "application/json",
	}}
	require.NoError(t, store.Put(t.Context(), manifest.Artifacts[0], terminalBytes))
	wire, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	digest, err := store.Create(t.Context(), wire)
	require.NoError(t, err)
	_, err = store.Finalize(t.Context(), digest)
	require.NoError(t, err)
	return digest
}

// publishUnsupportedVersion stores a content-addressed manifest declaring an
// unknown major version, as a newer producer would.
func publishUnsupportedVersion(t *testing.T, backend *artifactmanifest.Local, origin string) string {
	t.Helper()
	wire, err := backend.Get(t.Context(), "manifest-"+origin, artifactmanifest.MaxManifestBytes)
	require.NoError(t, err)
	wire = bytes.Replace(wire, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1)
	digest := artifactmanifest.Hash(wire)
	created, err := backend.Create(t.Context(), "manifest-"+digest, wire)
	require.NoError(t, err)
	require.True(t, created)
	return digest
}

func memberFor(t *testing.T, report *manifestaggregate.Report, input manifestaggregate.Input) manifestaggregate.Member {
	t.Helper()
	index := slices.IndexFunc(report.Members, func(member manifestaggregate.Member) bool { return member.Input == input })
	require.GreaterOrEqual(t, index, 0, "every requested reference must remain an explicit member")
	return report.Members[index]
}

func TestAggregateCLIAndAPIStateMatrix(t *testing.T) {
	directory := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(directory)
	require.NoError(t, err)
	succeeded := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, manifestfixture.RecordedNonzero(1))
	failed := manifestfixture.Publish(t, backend, "../../examples/runner/unit.json", nil, func(report *runnerunit.Report) {
		manifestfixture.RecordedNonzero(2)(report)
		report.Status = "execution_error"
		report.Observation.Status = models.StatusError
		report.Observation.ErrorMsg = "authored restricted failure detail"
	})
	canceled := publishCanceledMetadataOnly(t, backend, succeeded.Manifest)
	unsupported := publishUnsupportedVersion(t, backend, failed.Manifest)
	require.NoError(t, backend.Close())

	good := manifestaggregate.Input{Manifest: succeeded.Manifest, Evidence: succeeded.Evidence}
	bad := manifestaggregate.Input{Manifest: failed.Manifest, Evidence: failed.Evidence}
	cancel := manifestaggregate.Input{Manifest: canceled}
	legacy := manifestaggregate.Input{Manifest: succeeded.Manifest}
	missing := manifestaggregate.Input{Manifest: strings.Repeat("a", 64)}
	conflict := manifestaggregate.Input{Manifest: failed.Manifest, Evidence: succeeded.Evidence}
	newer := manifestaggregate.Input{Manifest: unsupported}

	type expectation struct {
		member     string
		diagnostic string
	}
	for _, control := range []struct {
		name     string
		inputs   []manifestaggregate.Input
		state    string
		outcomes map[string]int
		members  map[manifestaggregate.Input]expectation
	}{
		{
			name: "reordered", inputs: []manifestaggregate.Input{good, bad, cancel, good}, state: "complete",
			outcomes: map[string]int{"success": 1, "infrastructure_failure": 1, string(runnerterminal.Canceled): 1},
			members: map[manifestaggregate.Input]expectation{
				good: {member: "admitted"}, bad: {member: "admitted"},
				cancel: {member: "admitted", diagnostic: "execution_and_usage_unavailable_without_sidecar"},
			},
		},
		{
			name: "missing", inputs: []manifestaggregate.Input{good, missing}, state: "incomplete",
			outcomes: map[string]int{"success": 1},
			members: map[manifestaggregate.Input]expectation{
				good: {member: "admitted"}, missing: {member: "omitted", diagnostic: "missing_source"},
			},
		},
		{
			name: "conflicting", inputs: []manifestaggregate.Input{good, conflict}, state: "conflicting",
			outcomes: map[string]int{"success": 1, "infrastructure_failure": 1},
			members: map[manifestaggregate.Input]expectation{
				good: {member: "admitted"}, conflict: {member: "conflicting", diagnostic: "conflicting_source"},
			},
		},
		{
			name: "mixed-version", inputs: []manifestaggregate.Input{good, legacy, newer}, state: "incomplete",
			outcomes: map[string]int{"success": 2},
			members: map[manifestaggregate.Input]expectation{
				good:   {member: "admitted"},
				legacy: {member: "admitted", diagnostic: "execution_and_usage_unavailable_without_sidecar"},
				newer:  {member: "omitted", diagnostic: "unsupported_or_invalid_profile"},
			},
		},
		{
			name: "mixed", inputs: []manifestaggregate.Input{newer, conflict, missing, cancel, bad, good}, state: "conflicting",
			outcomes: map[string]int{"success": 1, "infrastructure_failure": 2, string(runnerterminal.Canceled): 1},
			members: map[manifestaggregate.Input]expectation{
				good: {member: "admitted"}, bad: {member: "admitted"},
				cancel:   {member: "admitted", diagnostic: "execution_and_usage_unavailable_without_sidecar"},
				missing:  {member: "omitted", diagnostic: "missing_source"},
				conflict: {member: "conflicting", diagnostic: "conflicting_source"},
				newer:    {member: "omitted", diagnostic: "unsupported_or_invalid_profile"},
			},
		},
	} {
		t.Run(control.name, func(t *testing.T) {
			saved, cliErr := runAggregateCLI(t, directory, control.inputs)
			status, body := postAggregateAPI(t, directory, control.inputs)
			require.Equal(t, saved, body, "CLI and API must emit identical canonical reports")
			reversed := slices.Clone(control.inputs)
			slices.Reverse(reversed)
			again, againErr := runAggregateCLI(t, directory, reversed)
			require.Equal(t, saved, again, "input order must not change the CLI aggregate")
			require.Equal(t, cliErr == nil, againErr == nil, "input order must not change the exit status")
			againStatus, againBody := postAggregateAPI(t, directory, reversed)
			require.Equal(t, saved, againBody, "input order must not change the API aggregate")
			require.Equal(t, status, againStatus, "input order must not change the HTTP status")
			report, err := manifestaggregate.Parse(saved)
			require.NoError(t, err)
			require.Equal(t, control.state, report.State)
			if control.state == "complete" {
				require.NoError(t, cliErr)
				require.Equal(t, http.StatusOK, status)
			} else {
				require.Error(t, cliErr, "incomplete or conflicting aggregates must exit nonzero")
				require.Equal(t, http.StatusConflict, status)
				require.Nil(t, report.Usage.Recorded, "a partial subtotal must never be reported")
			}
			require.Equal(t, control.outcomes, report.Counts.MemberOutcomes, "original outcomes remain visible, never upgraded")
			require.Len(t, report.Members, len(control.members))
			for input, expected := range control.members {
				member := memberFor(t, report, input)
				require.Equal(t, expected.member, member.State, input)
				if expected.diagnostic != "" {
					require.Contains(t, member.Diagnostics, expected.diagnostic, input)
				}
			}
			require.NotContains(t, string(saved), "authored restricted failure detail")
			require.NotContains(t, string(saved), "Authored original cancellation")
		})
	}
}
