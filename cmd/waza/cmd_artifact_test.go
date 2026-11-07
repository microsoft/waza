package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/webapi"
	"github.com/stretchr/testify/require"
)

func TestArtifactDocumentedNodeBuilderToPublication(t *testing.T) {
	for _, ending := range []struct{ name, value string }{{"LF", "\n"}, {"CRLF", "\r\n"}} {
		t.Run(ending.name, func(t *testing.T) {
			directory, manifestPath, declared, terminal := artifactCLIFixture(t)
			doc, err := os.ReadFile("../../site/src/content/docs/guides/immutable-artifacts.mdx")
			require.NoError(t, err)
			document := strings.ReplaceAll(strings.ReplaceAll(string(doc), "\r\n", "\n"), "\n", ending.value)
			require.Contains(t, document, ending.value)
			if ending.name == "CRLF" {
				require.Contains(t, document, "\r\n", "control must actually change source line endings")
			} else {
				require.NotContains(t, document, "\r\n")
			}
			start := "node --input-type=module <<'JS'" + ending.value
			require.Equal(t, 1, strings.Count(document, start))
			_, snippet, found := strings.Cut(document, start)
			require.True(t, found)
			snippet, _, found = strings.Cut(snippet, ending.value+"JS"+ending.value+"```")
			require.True(t, found)
			require.NoError(t, os.Remove(manifestPath), "remove only this invocation's existing declaration")
			runBuilder := func() ([]byte, error) {
				command := exec.Command("node", "--input-type=module", "-e", snippet)
				command.Dir = directory
				return command.CombinedOutput()
			}

			output, err := runBuilder()
			require.NoError(t, err, string(output))
			wire, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			m, err := artifactmanifest.Parse(wire)
			require.NoError(t, err)
			require.Equal(t, declared.Binding, m.Binding)
			require.Len(t, m.Artifacts, 1)
			require.Equal(t, artifactmanifest.Hash(terminal), m.Artifacts[0].SHA256)
			require.Equal(t, int64(len(terminal)), m.Artifacts[0].SizeBytes)
			digest, err := artifactmanifest.Digest(*m)
			require.NoError(t, err)
			require.NotEqual(t, artifactmanifest.Hash(wire), digest, "pretty source bytes are not canonical manifest identity")
			output, err = runArtifactCLI(t, directory, "create", manifestPath)
			require.NoError(t, err)
			require.JSONEq(t, `{"manifest_sha256":"`+digest+`","publication":"declared"}`, string(output))
			_, err = runArtifactCLI(t, directory, "upload", manifestPath, "terminal", filepath.Join(directory, "terminal.json"))
			require.NoError(t, err)
			output, err = runArtifactCLI(t, directory, "finalize", digest)
			require.NoError(t, err)
			var receipt artifactmanifest.Receipt
			require.NoError(t, json.Unmarshal(output, &receipt))
			require.Equal(t, "finalized", receipt.Publication)
			require.Equal(t, "verified", receipt.Integrity)
			_, err = runBuilder()
			require.Error(t, err, "documented builder must not overwrite a competing declaration")
			preserved, err := os.ReadFile(manifestPath)
			require.NoError(t, err)
			require.Equal(t, wire, preserved)
		})
	}
}

func TestArtifactStoredInvalidDeclarationIsCorruptionNotInvalidInput(t *testing.T) {
	directory, _, _, _ := artifactCLIFixture(t)
	backend, err := artifactmanifest.OpenLocal(filepath.Join(directory, ".waza-artifacts"))
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()
	invalid := []byte(`{"kind":"not-a-manifest"}`)
	digest := artifactmanifest.Hash(invalid)
	created, err := backend.Create(context.Background(), "manifest-"+digest, invalid)
	require.NoError(t, err)
	require.True(t, created, "stored malformed control must exist at its actual content hash")
	mux := http.NewServeMux()
	webapi.RegisterManifestRoutes(mux, directory, nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/manifests/"+digest, nil))
	require.Equal(t, http.StatusConflict, recorder.Code)
	var receipt artifactmanifest.Receipt
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
	require.Equal(t, "failed", receipt.Integrity)
	require.Equal(t, "unfinalized", receipt.Publication)
	require.Equal(t, digest, receipt.ManifestSHA256)
	require.Len(t, receipt.Diagnostics, 1)
	require.Equal(t, "corrupt_object", receipt.Diagnostics[0].Code)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/manifests/not-a-digest", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code, "invalid caller input must remain a distinct category")
}

func TestArtifactStoredManifestFailureRetainsFinalMarker(t *testing.T) {
	for _, content := range []string{"corrupted", "removed"} {
		for _, marker := range []string{"matching", "conflicting", "absent"} {
			t.Run(content+"/"+marker, func(t *testing.T) {
				directory, _, m, terminal := artifactCLIFixture(t)
				backend, err := artifactmanifest.OpenLocal(filepath.Join(directory, ".waza-artifacts"))
				require.NoError(t, err)
				defer func() { require.NoError(t, backend.Close()) }()
				store := artifactmanifest.NewStore(backend)
				wire, err := artifactmanifest.Encode(m)
				require.NoError(t, err)
				digest, err := store.Create(context.Background(), wire)
				require.NoError(t, err)
				for _, a := range m.Artifacts {
					var data []byte
					if a.Name == m.TerminalArtifact {
						data = terminal
					}
					require.NoError(t, store.Put(context.Background(), a, data))
				}
				receipt, err := store.Finalize(context.Background(), digest)
				require.NoError(t, err)
				require.Equal(t, "finalized", receipt.Publication)
				require.Equal(t, "verified", receipt.Integrity)
				root, err := os.OpenRoot(filepath.Join(directory, ".waza-artifacts"))
				require.NoError(t, err)
				defer func() { require.NoError(t, root.Close()) }()
				require.NoError(t, root.Chmod("manifest-"+digest, 0o600))
				require.NoError(t, root.Remove("manifest-"+digest))
				if content == "corrupted" {
					require.NoError(t, root.WriteFile("manifest-"+digest, []byte(`{"kind":"corrupted"}`), 0o400))
				}
				if marker != "matching" {
					require.NoError(t, root.Chmod("final-"+digest, 0o600))
					require.NoError(t, root.Remove("final-"+digest))
					if marker == "conflicting" {
						require.NoError(t, root.WriteFile("final-"+digest, []byte(strings.Repeat("f", 64)), 0o400))
					}
				}
				mux := http.NewServeMux()
				webapi.RegisterManifestRoutes(mux, directory, nil)
				recorder := httptest.NewRecorder()
				mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/manifests/"+digest, nil))
				wantStatus, wantPublication := http.StatusConflict, "conflict"
				wantIntegrity := "failed"
				switch marker {
				case "matching":
					wantPublication = "finalized"
				case "absent":
					wantPublication = "unfinalized"
					if content == "removed" {
						wantIntegrity = "unverified"
					}
				}
				if content == "removed" && marker != "conflicting" {
					wantStatus = http.StatusNotFound
				}
				require.Equal(t, wantStatus, recorder.Code)
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &receipt))
				require.Equal(t, wantPublication, receipt.Publication)
				require.Equal(t, wantIntegrity, receipt.Integrity)
				require.NotEmpty(t, receipt.Diagnostics)
				completed, err := store.ReadCompleted(context.Background(), digest)
				require.Error(t, err)
				require.Nil(t, completed, "a retained final marker must never grant invalid source bytes")
			})
		}
	}
}

func artifactCLIFixture(t *testing.T) (string, string, artifactmanifest.Manifest, []byte) {
	t.Helper()
	directory := t.TempDir()
	storeDir := filepath.Join(directory, ".waza-artifacts")
	require.NoError(t, os.Mkdir(storeDir, 0o700))
	request, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	service := runnerunit.NewService(func(c models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(c.ModelID), nil
	})
	envelope, binding := service.RunTerminal(context.Background(), request)
	require.NotNil(t, binding, "actual mock runner must supply the production binding")
	require.Equal(t, runnerterminal.Success, envelope.Outcome)
	terminal, err := runnerterminal.Encode(envelope, binding)
	require.NoError(t, err)
	m := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: "1.0",
		Binding:          artifactmanifest.Binding{Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs},
		TerminalArtifact: "terminal",
		Artifacts: []artifactmanifest.Artifact{
			{Name: "terminal", SHA256: artifactmanifest.Hash(terminal), SizeBytes: int64(len(terminal)), MediaType: "application/json"},
			{Name: "empty.txt", SHA256: artifactmanifest.Hash(nil), SizeBytes: 0, MediaType: "text/plain"},
		},
	}
	manifestPath := filepath.Join(directory, "manifest.json")
	wire, err := artifactmanifest.Encode(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(manifestPath, wire, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "terminal.json"), terminal, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "empty.txt"), nil, 0o600))
	return directory, manifestPath, m, terminal
}

func runArtifactCLI(t *testing.T, directory string, args ...string) ([]byte, error) {
	t.Helper()
	cmd := newRootCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(append(append([]string{"--no-update-check", "artifact"}, args...), "--store", filepath.Join(directory, ".waza-artifacts")))
	err := cmd.Execute()
	return output.Bytes(), err
}

func TestArtifactCLIToPersistedNativeAPI(t *testing.T) {
	directory, manifestPath, m, terminal := artifactCLIFixture(t)
	digest, err := artifactmanifest.Digest(m)
	require.NoError(t, err)
	registered, _, err := newRootCommand().Find([]string{"artifact", "create"})
	require.NoError(t, err)
	require.Equal(t, "create", registered.Name())
	mux := http.NewServeMux()
	webapi.RegisterManifestRoutes(mux, directory, nil)
	server := httptest.NewServer(mux)
	defer server.Close()
	getReceipt := func(status int) artifactmanifest.Receipt {
		t.Helper()
		response, err := server.Client().Get(server.URL + "/api/v1/manifests/" + digest)
		require.NoError(t, err)
		defer func() { require.NoError(t, response.Body.Close()) }()
		require.Equal(t, status, response.StatusCode)
		var r artifactmanifest.Receipt
		require.NoError(t, json.NewDecoder(response.Body).Decode(&r))
		serialized, err := json.Marshal(r)
		require.NoError(t, err)
		require.NotContains(t, string(serialized), directory)
		return r
	}
	r := getReceipt(http.StatusNotFound)
	require.Equal(t, "unverified", r.Integrity)
	for range 2 {
		output, err := runArtifactCLI(t, directory, "create", manifestPath)
		require.NoError(t, err)
		require.JSONEq(t, `{"manifest_sha256":"`+digest+`","publication":"declared"}`, string(output))
	}
	output, err := runArtifactCLI(t, directory, "finalize", digest)
	require.ErrorIs(t, err, artifactmanifest.ErrMissing)
	require.NoError(t, json.Unmarshal(output, &r))
	require.Equal(t, "failed", r.Integrity)
	require.NotEqual(t, "finalized", r.Publication)
	require.Len(t, r.Artifacts, 2, "must inspect every missing artifact")
	require.Len(t, r.Diagnostics, 2)
	// A partial file is rejected before publication; restoring original bytes is
	// the explicit interrupted-upload recovery path.
	path := filepath.Join(directory, "terminal.json")
	require.NoError(t, os.WriteFile(path, terminal[:len(terminal)-1], 0o600))
	_, err = runArtifactCLI(t, directory, "upload", manifestPath, "terminal", path)
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
	require.NoError(t, os.WriteFile(path, terminal, 0o600))
	for _, name := range []string{"terminal", "empty.txt"} {
		pathName := name
		if name == "terminal" {
			pathName = "terminal.json"
		}
		for range 2 {
			_, err := runArtifactCLI(t, directory, "upload", manifestPath, name, filepath.Join(directory, pathName))
			require.NoError(t, err)
		}
	}
	r = getReceipt(http.StatusOK)
	require.Equal(t, "verified", r.Integrity)
	require.Equal(t, "unfinalized", r.Publication)
	require.Equal(t, int64(0), r.Artifacts[0].SizeBytes)
	for range 2 {
		output, err = runArtifactCLI(t, directory, "finalize", digest)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(output, &r))
		require.Equal(t, "finalized", r.Publication)
		require.Equal(t, "verified", r.Integrity)
		require.Equal(t, "content-and-declared-context-only", r.Verification)
	}
	r = getReceipt(http.StatusOK)
	require.Equal(t, "finalized", r.Publication)
	// The actual persisted pointer is discoverable independently of legacy
	// result enumeration. It is deliberately unverified until inspected.
	response, err := server.Client().Get(server.URL + "/api/v1/manifests")
	require.NoError(t, err)
	var catalog artifactmanifest.Catalog
	require.NoError(t, json.NewDecoder(response.Body).Decode(&catalog))
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	require.Equal(t, []string{digest}, catalog.ManifestSHA256)
	require.Equal(t, "unverified-pointers-only", catalog.Verification)
	require.False(t, catalog.Truncated)
	// Conflicting finalization never rewrites the persisted marker.
	marker := filepath.Join(directory, ".waza-artifacts", "final-"+digest)
	conflict := strings.Repeat("b", 64)
	require.NoError(t, os.Chmod(marker, 0o600), "intentional privileged corruption control")
	require.NoError(t, os.WriteFile(marker, []byte(conflict), 0o600))
	output, err = runArtifactCLI(t, directory, "finalize", digest)
	require.ErrorIs(t, err, artifactmanifest.ErrConflict)
	require.NoError(t, json.Unmarshal(output, &r))
	require.Equal(t, "conflict", r.Publication)
	r = getReceipt(http.StatusConflict)
	require.Equal(t, "failed", r.Integrity)
	saved, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, conflict, string(saved))
	require.NoError(t, os.WriteFile(marker, []byte(digest), 0o600))
	corrupt := append([]byte(nil), terminal...)
	corrupt[0] ^= 1
	blob := filepath.Join(directory, ".waza-artifacts", "blob-"+m.Artifacts[0].SHA256)
	require.NoError(t, os.Chmod(blob, 0o600), "intentional privileged corruption control")
	require.NoError(t, os.WriteFile(blob, corrupt, 0o600))
	r = getReceipt(http.StatusConflict)
	require.Equal(t, "finalized", r.Publication)
	require.Equal(t, "failed", r.Integrity, "a prior receipt must not hide current corruption")
	_, err = runArtifactCLI(t, directory, "inspect", digest)
	require.ErrorIs(t, err, artifactmanifest.ErrCorrupt)
}

type artifactOutputFailure struct{}

func (artifactOutputFailure) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestArtifactCLIAdmissionAndOutputFailures(t *testing.T) {
	directory, path, _, _ := artifactCLIFixture(t)
	for _, args := range [][]string{
		{"create", path, "--store", filepath.Join(directory, "absent")},
		{"inspect", "../private/path", "--store", filepath.Join(directory, ".waza-artifacts")},
		{"upload", path, "undeclared", path, "--store", filepath.Join(directory, ".waza-artifacts")},
		{"create", path, "--store", directory, "--azure-container-url", "https://offline.invalid/container"},
		{"create"},
	} {
		cmd := newArtifactCommand()
		cmd.SetArgs(args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		require.Error(t, cmd.Execute())
	}
	cmd := newArtifactCommand()
	cmd.SetArgs([]string{"create", path, "--store", filepath.Join(directory, ".waza-artifacts")})
	cmd.SetOut(artifactOutputFailure{})
	cmd.SetErr(io.Discard)
	require.ErrorIs(t, cmd.Execute(), io.ErrClosedPipe, "output failure cannot become successful CLI exit")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd = newArtifactCommand()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"create", path, "--store", filepath.Join(directory, ".waza-artifacts")})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	require.ErrorIs(t, cmd.Execute(), context.Canceled)
}
