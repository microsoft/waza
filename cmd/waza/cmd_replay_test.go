package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/snapshot"
	"github.com/stretchr/testify/require"
)

func writeSnapshot(t *testing.T, dir, name string, snap snapshot.Snapshot) string {
	t.Helper()
	path := filepath.Join(dir, name)
	b, err := json.MarshalIndent(snap, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, b, 0o644))
	return path
}

// makeSnap returns a minimal-but-valid 1.0 snapshot for testing.
func makeSnap(status models.Status, withInconsistentGrader bool, events ...models.ToolEvent) snapshot.Snapshot {
	s := snapshot.Snapshot{
		SchemaVersion: snapshot.CurrentSchemaVersion,
		Kind:          snapshot.Kind,
		WazaVersion:   "test",
		CreatedAt:     time.Now().UTC(),
		Task:          snapshot.SnapshotTask{TestID: "demo"},
		ToolEvents:    events,
		Result: snapshot.SnapshotResult{
			Status:      status,
			Validations: map[string]models.GraderResults{},
		},
		Env: snapshot.SnapshotEnv{AllowList: []string{}, Captured: map[string]string{}},
	}
	if withInconsistentGrader {
		s.Result.Validations["bad"] = models.GraderResults{
			Passed: true, Score: 0, Weight: 1,
		}
	} else {
		s.Result.Validations["good"] = models.GraderResults{
			Passed: true, Score: 1, Weight: 1,
		}
	}
	return s
}

func TestReplayModelReplayPasses(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "read_file"},
		models.ToolEvent{Sequence: 2, Turn: 1, ToolName: "write_file"},
	)
	path := writeSnapshot(t, dir, "snap.json", snap)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{path})

	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "OK model-replay")
}

func TestReplayModelReplayDetectsInconsistency(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, true,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "x"},
	)
	path := writeSnapshot(t, dir, "snap.json", snap)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{path})

	err := cmd.Execute()
	require.Error(t, err)
	var exit *ExitCodeError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 1, exit.Code)
}

func TestReplayJSONOutput(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "x"},
	)
	path := writeSnapshot(t, dir, "snap.json", snap)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json", path})
	require.NoError(t, cmd.Execute())

	var report ModelReplayReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.True(t, report.Pass)
	require.Equal(t, 1, report.ToolEvents)
	require.Equal(t, snapshot.ModeModelReplay, report.Mode)
}

func TestReplayBisectDetectsDivergence(t *testing.T) {
	dir := t.TempDir()
	a := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "x"},
		models.ToolEvent{Sequence: 2, Turn: 1, ToolName: "y"},
	)
	b := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "x"},
		models.ToolEvent{Sequence: 2, Turn: 1, ToolName: "z"},
	)
	pa := writeSnapshot(t, dir, "a.json", a)
	pb := writeSnapshot(t, dir, "b.json", b)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--bisect", pb, pa})
	err := cmd.Execute()
	require.Error(t, err)
	var exit *ExitCodeError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 1, exit.Code)
	require.Contains(t, out.String(), "DIVERGENCE")
}

func TestReplayBisectMatch(t *testing.T) {
	dir := t.TempDir()
	a := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "x"},
	)
	pa := writeSnapshot(t, dir, "a.json", a)
	pb := writeSnapshot(t, dir, "b.json", a)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--bisect", pb, pa})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "OK snapshots match")
}

func TestReplayRejectsIncompatibleMajor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"schemaVersion":"2.0","kind":"task-snapshot"}`), 0o644))

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{path})
	err := cmd.Execute()
	require.Error(t, err)
	var exit *ExitCodeError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 2, exit.Code)
}

func TestReplayLiveModeNotImplemented(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, false)
	path := writeSnapshot(t, dir, "snap.json", snap)

	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--mode", "live", path})
	err := cmd.Execute()
	require.Error(t, err)
	var exit *ExitCodeError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 2, exit.Code)
}
