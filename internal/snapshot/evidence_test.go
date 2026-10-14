package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func capturedWorkspace(t *testing.T, content string) *Snapshot {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("safe workspace capture is explicitly unsupported on this platform")
	}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "output.txt"), []byte(content), 0o600))
	snap, err := Capture(CaptureInput{
		EvalID: "eval", Task: &models.TestCase{TestID: "task"},
		Request:         &execution.ExecutionRequest{Message: "write output"},
		Run:             &models.RunResult{RunNumber: 1, Attempts: 1, Status: models.StatusPassed, WorkspaceDir: root},
		WorkspacePaths:  []string{"output.txt"},
		WorkspaceLimits: WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 1024},
	})
	require.NoError(t, err)
	require.Len(t, snap.WorkspaceFiles, 1)
	require.NoError(t, evidence.Validate(snap.Evidence))
	return snap
}

func TestManifestRecordsDigestOnlyAndUnavailableState(t *testing.T) {
	snap, err := Capture(CaptureInput{
		EvalID: "eval", Task: &models.TestCase{TestID: "task"},
		Run:           &models.RunResult{RunNumber: 1, Attempts: 2, Status: models.StatusError},
		ExecutionMode: "mock", DiagnosticCategory: "grader",
	})
	require.NoError(t, err)
	require.NoError(t, evidence.Validate(snap.Evidence))
	require.Equal(t, 2, snap.Evidence.Origin.AttemptCount)
	require.Equal(t, "not_preserved", snap.Evidence.Origin.PriorAttempts)
	require.Equal(t, "mock", snap.Evidence.Runtime.ExecutionMode)
	require.Equal(t, "unknown", snap.Evidence.Runtime.VerifiedEnforcement)
	require.Equal(t, "grader", snap.Evidence.Diagnostics[0].Category)
	for _, artifact := range snap.Evidence.Artifacts {
		if artifact.ID == "task-config" {
			require.Equal(t, "digest_only", artifact.Availability)
			require.Nil(t, artifact.ContentDigest)
		}
		if artifact.ID == "tool-events" {
			require.Equal(t, "unavailable", artifact.Availability)
		}
		if artifact.ID == "result" {
			require.Equal(t, "partial", artifact.Completeness)
		}
	}
	require.Error(t, VerifyWorkspace(snap, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"}))
	require.Error(t, VerifyWorkspace(&Snapshot{}, models.EvidenceOrigin{}, "", []string{"output.txt"}))
}

func TestPreservedRequiredFilesCanBeVerifiedAndMaterialized(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	origin, identity := snap.Evidence.Origin, snap.Evidence.SHA256
	require.NoError(t, VerifyWorkspace(snap, origin, identity, []string{"output.txt"}))
	materialized, err := MaterializeWorkspace(snap, origin, identity, []string{"output.txt"})
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(materialized.Path(), "output.txt"))
	require.NoError(t, err)
	require.Equal(t, "actual output", string(data))
	dir := materialized.Path()
	require.NoError(t, materialized.Close())
	_, err = os.Stat(dir)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, materialized.Close())
	require.Error(t, VerifyWorkspace(snap, origin, identity, nil))
	require.Error(t, VerifyWorkspace(snap, origin, identity, []string{"missing.txt"}))
	require.Error(t, VerifyWorkspace(snap, origin, identity, []string{"../outside"}))
	require.Error(t, VerifyWorkspace(snap, origin, "wrong", []string{"output.txt"}))
	origin.TaskID = "other-task"
	require.Error(t, VerifyWorkspace(snap, origin, identity, []string{"output.txt"}))
	snap.WorkspaceFiles[0].Content = "tampered"
	require.Error(t, VerifyWorkspace(snap, snap.Evidence.Origin, identity, []string{"output.txt"}))
}

func TestRedactedStateCannotBeMaterializedForExactRegrade(t *testing.T) {
	snap := capturedWorkspace(t, "sensitive@example.com")
	require.True(t, snap.WorkspaceFiles[0].Redacted)
	_, err := MaterializeWorkspace(snap, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"})
	require.ErrorContains(t, err, "redaction")
}

func TestPartialWorkspaceCaptureRemainsExplicit(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	input := CaptureInput{
		EvalID: "eval", Task: &models.TestCase{TestID: "task"},
		Run:             &models.RunResult{RunNumber: 1, Attempts: 1, WorkspaceDir: t.TempDir()},
		WorkspacePaths:  []string{"missing.txt"},
		WorkspaceLimits: WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 1024},
	}
	partial, err := Capture(input)
	require.NoError(t, err)
	require.NoError(t, evidence.Validate(partial.Evidence))
	require.Equal(t, "workspace_partial", partial.Evidence.Diagnostics[0].Code)
	require.Empty(t, partial.WorkspaceFiles)
	require.Error(t, VerifyWorkspace(partial, snap.Evidence.Origin, partial.Evidence.SHA256, []string{"missing.txt"}))
}

func TestPrivateStatePublicationDoesNotOverwriteAndUsesRestrictiveModes(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	root := filepath.Join(t.TempDir(), "evidence")
	writer := NewWriter(root)
	first, err := writer.Write(snap)
	require.NoError(t, err)
	second, err := writer.Write(snap)
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	info, err := os.Stat(first)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	loaded, err := LoadSnapshotFile(first)
	require.NoError(t, err)
	require.NoError(t, VerifyWorkspace(loaded, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"}))
	require.NoError(t, os.Chmod(root, 0o755))
	_, err = writer.Write(snap)
	require.ErrorContains(t, err, "0700")
}

func TestSnapshotPreservesExactJSONNumbers(t *testing.T) {
	snap, err := Capture(CaptureInput{
		Task: &models.TestCase{TestID: "task"}, Run: &models.RunResult{
			ToolEvents: []models.ToolEvent{{Args: json.RawMessage(`{"count":9007199254740993}`)}},
		},
	})
	require.NoError(t, err)
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	loaded, err := ParseSnapshot(data, "memory")
	require.NoError(t, err)
	args, ok := loaded.ToolEvents[0].Args.(map[string]any)
	require.True(t, ok)
	require.Equal(t, json.Number("9007199254740993"), args["count"])
	require.NoError(t, evidence.Validate(loaded.Evidence))
}

func TestRequiredStateUsesActualRawFileMetadataAndLocator(t *testing.T) {
	snap := capturedWorkspace(t, "actual output")
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	var document map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &document))
	var files []map[string]any
	require.NoError(t, json.Unmarshal(document["workspaceFiles"], &files))
	files[0]["unhashed"] = "extra metadata"
	document["workspaceFiles"], err = json.Marshal(files)
	require.NoError(t, err)
	data, err = json.Marshal(document)
	require.NoError(t, err)
	loaded, err := ParseSnapshot(data, "memory")
	require.NoError(t, err)
	require.Error(t, VerifyWorkspace(loaded, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"}))
	for i := range snap.Evidence.Artifacts {
		if snap.Evidence.Artifacts[i].ID == "workspace-file/output.txt" {
			snap.Evidence.Artifacts[i].Pointer = "/workspaceFiles/99"
		}
	}
	require.NoError(t, evidence.Seal(snap.Evidence))
	require.ErrorContains(t, VerifyWorkspace(snap, snap.Evidence.Origin, snap.Evidence.SHA256, []string{"output.txt"}), "locator")
}

func TestSensitiveEnvironmentMatchesAreAccountedWithoutCapturingUnlistedValues(t *testing.T) {
	t.Setenv("WAZA_TEST_TOKEN", "unrecognized-secret")
	snap, err := Capture(CaptureInput{
		Task: &models.TestCase{TestID: "task"}, Run: &models.RunResult{},
		EnvAllowList: []string{"WAZA_TEST_TOKEN"},
	})
	require.NoError(t, err)
	require.Equal(t, RedactionPlaceholder, snap.Env.Captured["WAZA_TEST_TOKEN"])
	require.Contains(t, snap.Evidence.Redaction.AppliedRules, "sensitive_key")
	require.Positive(t, snap.Evidence.Redaction.MatchCount)
	require.Len(t, snap.Env.Captured, 1)
}

func TestPartialCaptureKeepsAllSafeFilesAndEveryUnavailableRequest(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("selected-file capture is explicitly unsupported on this platform")
	}
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprintf("captured-%d", count), func(t *testing.T) {
			root := t.TempDir()
			paths := []string{"missing.txt", "sensitive@example.com"}
			for i := range count {
				name := fmt.Sprintf("output%d.txt", i)
				require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("actual output"), 0o600))
				paths = append(paths, name)
			}
			snap, err := Capture(CaptureInput{
				EvalID: "eval", Task: &models.TestCase{TestID: "task"},
				Run:            &models.RunResult{RunNumber: 1, Attempts: 1, WorkspaceDir: root},
				WorkspacePaths: paths, WorkspaceLimits: WorkspaceLimits{MaxFileBytes: 1024, MaxTotalBytes: 4096},
			})
			require.NoError(t, err)
			require.NoError(t, evidence.Validate(snap.Evidence))
			require.Len(t, snap.WorkspaceFiles, count)
			missing := 0
			for _, artifact := range snap.Evidence.Artifacts {
				if strings.HasPrefix(artifact.ID, "workspace-request/") {
					missing++
				}
			}
			require.Equal(t, 2, missing)
			require.Equal(t, "workspace_partial", snap.Evidence.Diagnostics[0].Code)
			data, err := json.Marshal(snap.Evidence)
			require.NoError(t, err)
			require.NotContains(t, string(data), "sensitive@example.com")
		})
	}
}
