package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func aggregateCLIPublication(t *testing.T) (string, string, artifactmanifest.Manifest, []byte) {
	t.Helper()
	directory, path, manifest, terminal := artifactCLIFixture(t)
	output, err := runArtifactCLI(t, directory, "create", path)
	require.NoError(t, err)
	var declaration struct {
		ManifestSHA256 string `json:"manifest_sha256"`
	}
	require.NoError(t, json.Unmarshal(output, &declaration))
	require.Len(t, declaration.ManifestSHA256, 64)
	_, err = runArtifactCLI(t, directory, "upload", path, "terminal", filepath.Join(directory, "terminal.json"))
	require.NoError(t, err)
	_, err = runArtifactCLI(t, directory, "upload", path, "empty.txt", filepath.Join(directory, "empty.txt"))
	require.NoError(t, err)
	_, err = runArtifactCLI(t, directory, "finalize", declaration.ManifestSHA256)
	require.NoError(t, err)
	return directory, declaration.ManifestSHA256, manifest, terminal
}

func TestAggregateCLIProductionToPersistedAPI(t *testing.T) {
	directory, digest, manifest, _ := aggregateCLIPublication(t)
	storeDirectory := filepath.Join(directory, ".waza-artifacts")
	cmd := newRootCommand()
	found, _, err := cmd.Find([]string{"aggregate"})
	require.NoError(t, err)
	require.False(t, shouldRunUpdateCheck(found, false), "no update/network constructor in offline aggregation")
	for _, file := range []string{".waza.yaml", ".env"} {
		require.NoError(t, os.WriteFile(filepath.Join(directory, file), []byte("invalid sentinel; must not be loaded"), 0o600))
	}
	binary := buildRecoveryCLI(t)
	run := func(digests ...string) ([]byte, error) {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, append([]string{"aggregate", "--store", storeDirectory}, digests...)...)
		command.Dir = directory
		command.Env = append(os.Environ(), "ENABLE_COPILOT_TESTS=false", "NO_COLOR=1",
			"COPILOT_CLI_PATH="+filepath.Join(directory, "must-not-launch-missing-runtime"))
		var stdout bytes.Buffer
		command.Stdout = &stdout
		err := command.Run()
		return stdout.Bytes(), err
	}
	first, err := run(digest)
	require.NoError(t, err)
	second, err := run(digest, digest)
	require.NoError(t, err)
	require.Equal(t, first, second)
	var report manifestaggregate.Report
	require.NoError(t, json.Unmarshal(first, &report))
	require.Len(t, report.Members, 1)
	require.Equal(t, "complete", report.Completion)
	require.Equal(t, manifest.Binding.Identity.Trial, report.Members[0].Logical.Work)
	require.Equal(t, runnerterminal.Success, report.Members[0].Outcome)
	require.NotNil(t, report.Members[0].Verdicts)
	require.Len(t, *report.Members[0].Verdicts, len(manifest.Binding.GraderIDs))
	require.NotContains(t, string(first), directory)
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutes(mux, directory, nil)
	get := func() *httptest.ResponseRecorder {
		body := `{"manifests":["` + digest + `","` + digest + `"]}`
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(body)))
		return response
	}
	response := get()
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, strings.TrimSpace(string(first)), response.Body.String(), "API and production CLI share exact deterministic public report")
	marker := filepath.Join(storeDirectory, "final-"+digest)
	require.NoError(t, os.Remove(marker), "remove only this test's final marker")
	output, err := run(digest)
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, ExitError, exit.ExitCode())
	require.NoError(t, json.Unmarshal(output, &report))
	require.Equal(t, "unfinalized", report.Members[0].State)
	require.Equal(t, "incomplete", report.Completion)
	response = get()
	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), `"state":"unfinalized"`)
}

func TestAggregateCLIInputAndOutputFailures(t *testing.T) {
	directory, digest, _, _ := aggregateCLIPublication(t)
	for _, value := range []string{"../secret", strings.Repeat("A", 64)} {
		cmd := newAggregateCommand()
		cmd.SetArgs([]string{"--store", filepath.Join(directory, "missing-store"), value})
		require.ErrorIs(t, cmd.Execute(), manifestaggregate.ErrInvalid, "reject before opening provider")
	}
	cmd := newAggregateCommand()
	cmd.SetArgs([]string{"--store", filepath.Join(directory, ".waza-artifacts"), digest})
	cmd.SetOut(rejectAggregateOutput{})
	require.ErrorIs(t, cmd.Execute(), io.ErrClosedPipe)
	missing := strings.Repeat("f", 64)
	cmd = newAggregateCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--store", filepath.Join(directory, ".waza-artifacts"), digest, missing})
	require.ErrorIs(t, cmd.Execute(), manifestaggregate.ErrIncomplete)
	var report manifestaggregate.Report
	require.NoError(t, json.Unmarshal(output.Bytes(), &report))
	require.Len(t, report.Members, 2)
	require.Equal(t, 1, report.StateCounts["missing"])
	require.Equal(t, 1, report.StateCounts["success"])
}

type rejectAggregateOutput struct{}

func (rejectAggregateOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAggregateCLIReorderedActualFailureSet(t *testing.T) {
	directory, first, _, _ := aggregateCLIPublication(t)
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	original := request.Input.Task
	request.Input.Task = strings.Replace(original, "- hello", "- must-not-appear", 1)
	require.NotEqual(t, original, request.Input.Task)
	request.Selection.Trial = 1
	data, err = json.Marshal(request)
	require.NoError(t, err)
	service := runnerunit.NewService(func(c models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(c.ModelID), nil
	})
	envelope, binding := service.RunTerminal(context.Background(), data)
	require.NotNil(t, binding)
	require.Equal(t, runnerterminal.EvaluationFailure, envelope.Outcome, "actual mock production grader must fail")
	terminal, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	manifest := artifactmanifest.Manifest{Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding:          artifactmanifest.Binding{Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs},
		TerminalArtifact: "failed-terminal", Artifacts: []artifactmanifest.Artifact{{
			Name: "failed-terminal", SHA256: artifactmanifest.Hash(terminal), SizeBytes: int64(len(terminal)), MediaType: "application/json",
		}},
	}
	manifestData, err := artifactmanifest.Encode(manifest)
	require.NoError(t, err)
	path := filepath.Join(directory, "failed-manifest.json")
	terminalPath := filepath.Join(directory, "failed-terminal.json")
	require.NoError(t, os.WriteFile(path, manifestData, 0o600))
	require.NoError(t, os.WriteFile(terminalPath, terminal, 0o600))
	output, err := runArtifactCLI(t, directory, "create", path)
	require.NoError(t, err)
	var declared struct {
		Digest string `json:"manifest_sha256"`
	}
	require.NoError(t, json.Unmarshal(output, &declared))
	_, err = runArtifactCLI(t, directory, "upload", path, "failed-terminal", terminalPath)
	require.NoError(t, err)
	_, err = runArtifactCLI(t, directory, "finalize", declared.Digest)
	require.NoError(t, err)
	binary := buildRecoveryCLI(t)
	run := func(digests ...string) []byte {
		command := exec.CommandContext(t.Context(), binary, append([]string{"aggregate", "--store", filepath.Join(directory, ".waza-artifacts")}, digests...)...)
		command.Dir = directory
		command.Env = append(os.Environ(), "COPILOT_CLI_PATH="+filepath.Join(directory, "no-runtime"), "NO_COLOR=1")
		var output bytes.Buffer
		command.Stdout = &output
		var exit *exec.ExitError
		require.ErrorAs(t, command.Run(), &exit)
		require.Equal(t, ExitTestFailed, exit.ExitCode())
		return output.Bytes()
	}
	firstOutput := run(first, declared.Digest)
	require.Equal(t, firstOutput, run(declared.Digest, first, first))
	var report manifestaggregate.Report
	require.NoError(t, json.Unmarshal(firstOutput, &report))
	require.Equal(t, "complete", report.Completion)
	require.Equal(t, map[string]int{"success": 1, "evaluation_failure": 1}, report.StateCounts)
	require.Len(t, report.Members, 2)
	for _, member := range report.Members {
		if member.Outcome == runnerterminal.EvaluationFailure {
			require.NotNil(t, member.Verdicts)
			require.Equal(t, []runnerterminal.Verdict{{Identifier: "_output_contains", Passed: false}}, *member.Verdicts)
		}
	}
}

type forbiddenAggregateReader struct{ calls int }

func (r *forbiddenAggregateReader) Aggregate(context.Context, manifestaggregate.Request) (manifestaggregate.Report, error) {
	r.calls++
	return manifestaggregate.Report{}, errors.New("should not be reached")
}

func TestAggregateAPIRejectsOriginalWireBeforeReads(t *testing.T) {
	reader := &forbiddenAggregateReader{}
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutesWithReader(mux, reader)
	for _, body := range []string{`{}`, `{"manifests":null}`, `{"manifests":[]}`, `{"manifests":123}`,
		`{"manifests":["` + strings.Repeat("a", 64) + `"],"store":"../secret"}`,
		`{"manifests":["` + strings.Repeat("a", 64) + `"],"manifests":["` + strings.Repeat("b", 64) + `"]}`,
		strings.Repeat(" ", 16385)} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
		require.NotContains(t, recorder.Body.String(), "../secret")
	}
	require.Zero(t, reader.calls)
}

type unavailableAggregateBackend struct{}

func (unavailableAggregateBackend) Create(context.Context, string, []byte) (bool, error) {
	return false, errors.New("unexpected write")
}
func (unavailableAggregateBackend) Get(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("private operator storage error")
}

type unavailableAggregateReader struct{}

func (unavailableAggregateReader) Aggregate(ctx context.Context, request manifestaggregate.Request) (manifestaggregate.Report, error) {
	return manifestaggregate.Aggregate(ctx, unavailableAggregateBackend{}, request)
}

func TestAggregateAPIReadUnavailableIs503NotMembershipConflict(t *testing.T) {
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutesWithReader(mux, unavailableAggregateReader{})
	recorder := httptest.NewRecorder()
	body := `{"manifests":["` + strings.Repeat("a", 64) + `"]}`
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(body)))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	var report manifestaggregate.Report
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &report))
	require.Equal(t, "incomplete", report.Completion)
	require.Equal(t, "unavailable", report.Members[0].State)
	require.Equal(t, map[string]int{"unavailable": 1}, report.StateCounts)
	require.NotContains(t, recorder.Body.String(), "private operator")
}

func TestAggregateExplicitProviderDoesNotFallBackToValidLocalStore(t *testing.T) {
	directory, digest, _, _ := aggregateCLIPublication(t)
	cmd := newAggregateCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--store", filepath.Join(directory, ".waza-artifacts"), "--azure-container-url", "http://invalid/container", digest})
	require.Error(t, cmd.Execute(), "explicit store and Azure flags are mutually exclusive")
	require.Empty(t, output.String())
	cmd = newAggregateCommand()
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--azure-container-url", "http://invalid/container", digest})
	require.Error(t, cmd.Execute(), "invalid explicit Azure URL must fail before reading local defaults")
	require.Empty(t, output.String())
	mux := http.NewServeMux()
	webapi.RegisterAggregateRoutes(mux, directory, &projectconfig.StorageConfig{
		Enabled: true, Provider: "azure-blob", AccountName: "invalid account", ContainerName: "container",
	})
	recorder := httptest.NewRecorder()
	body := `{"manifests":["` + digest + `"]}`
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/aggregates", strings.NewReader(body)))
	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.NotContains(t, recorder.Body.String(), `"completion":"complete"`)
	require.NotContains(t, recorder.Body.String(), "invalid account")
}
