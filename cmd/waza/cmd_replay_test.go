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

func parseSnapshotForReplay(t *testing.T, snap snapshot.Snapshot) *snapshot.Snapshot {
	t.Helper()
	data, err := json.Marshal(snap)
	require.NoError(t, err)
	parsed, err := snapshot.ParseSnapshot(data, "replay test")
	require.NoError(t, err)
	return parsed
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

func TestReplayHelpStatesUnsupportedLiveMode(t *testing.T) {
	cmd := newReplayCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--help"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "live          Unsupported; returns exit code 2.")
	require.Contains(t, out.String(), "No live replay behavior or delivery date is promised.")
	require.Contains(t, out.String(), "load / parse / snapshot IO / JSON output / unsupported or unknown mode error")
	require.NotContains(t, out.String(), "(planned)")
	require.Contains(t, cmd.Flags().Lookup("mode").Usage, "live (unsupported; exits 2)")
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

func TestReplayModelReplaySequenceDiagnostics(t *testing.T) {
	tests := []struct {
		name      string
		sequences []int
		wantKinds []string
		wantPass  bool
	}{
		{name: "ordered", sequences: []int{1, 2}, wantPass: true},
		{name: "reordered", sequences: []int{2, 1}, wantKinds: []string{"sequence_order"}},
		{name: "duplicate", sequences: []int{1, 1}, wantKinds: []string{"sequence_duplicate", "sequence_gap"}},
		{name: "gap", sequences: []int{1, 3}, wantKinds: []string{"sequence_gap"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := make([]models.ToolEvent, len(tt.sequences))
			for i, sequence := range tt.sequences {
				events[i].Sequence = sequence
			}
			original := append([]models.ToolEvent(nil), events...)
			snap := snapshot.Snapshot{ToolEvents: events}

			report := buildModelReplayReport(&snap, false)
			var gotKinds []string
			for _, issue := range report.Issues {
				gotKinds = append(gotKinds, issue.Kind)
			}
			require.Equal(t, tt.wantKinds, gotKinds)
			require.Equal(t, tt.wantPass, report.Pass)
			require.Equal(t, original, snap.ToolEvents)
		})
	}
}

func TestReplayModelReplayRejectsReorderedSequence(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, false,
		models.ToolEvent{Sequence: 2, Turn: 1, ToolName: "second"},
		models.ToolEvent{Sequence: 1, Turn: 1, ToolName: "first"},
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
	require.Contains(t, out.String(), "sequence_order")
}

func TestReplayStrictStatusConsistencyUsesRecordedFinalValidations(t *testing.T) {
	failedValidation := map[string]models.GraderResults{
		"quality": {Name: "quality", Passed: false, Score: 0, Weight: 1},
	}
	tests := []struct {
		name        string
		status      models.Status
		validations map[string]models.GraderResults
		checkpoints []models.CheckpointOutcome
		strict      bool
		wantKinds   []string
	}{
		{name: "passed with failed validation", status: models.StatusPassed, validations: failedValidation, strict: true, wantKinds: []string{"status_inconsistency"}},
		{name: "passed with failed checkpoint", status: models.StatusPassed, checkpoints: []models.CheckpointOutcome{{AfterTurn: 2, Status: models.StatusFailed}}, strict: true, wantKinds: []string{"status_inconsistency"}},
		{name: "passed with checkpoint error", status: models.StatusPassed, checkpoints: []models.CheckpointOutcome{{AfterTurn: 2, Status: models.StatusError}}, strict: true, wantKinds: []string{"status_inconsistency"}},
		{name: "passed with skipped checkpoint", status: models.StatusPassed, checkpoints: []models.CheckpointOutcome{{AfterTurn: 2, Status: models.StatusSkipped}}, strict: true},
		{name: "missing validation evidence", status: models.StatusPassed, strict: true},
		{name: "failed status with failed validation", status: models.StatusFailed, validations: failedValidation, strict: true},
		{name: "skipped status", status: models.StatusSkipped, validations: failedValidation, strict: true},
		{name: "operational error status", status: models.StatusError, validations: failedValidation, strict: true},
		{name: "non-strict mode", status: models.StatusPassed, validations: failedValidation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snap := snapshot.Snapshot{
				Result: snapshot.SnapshotResult{
					Status:      tt.status,
					Validations: tt.validations,
				},
				Checkpoints: tt.checkpoints,
			}
			report := buildModelReplayReport(parseSnapshotForReplay(t, snap), tt.strict)
			var gotKinds []string
			for _, issue := range report.Issues {
				gotKinds = append(gotKinds, issue.Kind)
				if issue.Kind == "status_inconsistency" && issue.Grader != "" {
					require.Equal(t, "quality", issue.Grader)
				}
			}
			require.Equal(t, tt.wantKinds, gotKinds)
			require.Equal(t, len(tt.wantKinds) == 0, report.Pass)
		})
	}
}

func TestReplayStrictStatusDoesNotInferMissingPassedField(t *testing.T) {
	snap, err := snapshot.ParseSnapshot([]byte(`{"schemaVersion":"1.1","kind":"task-snapshot","result":{"status":"passed","validations":{"quality":{}}}}`), "missing-passed-test")
	require.NoError(t, err)

	report := buildModelReplayReport(snap, true)
	require.True(t, report.Pass)
	require.Empty(t, report.Issues)
}

func TestReplayStrictValidationDoesNotInferMissingScore(t *testing.T) {
	snap, err := snapshot.ParseSnapshot([]byte(`{"schemaVersion":"1.1","kind":"task-snapshot","result":{"status":"passed","validations":{"quality":{"passed":true,"weight":1}}}}`), "missing-score-test")
	require.NoError(t, err)

	report := buildModelReplayReport(snap, true)
	require.True(t, report.Pass)
	require.Empty(t, report.Issues)
}

func TestReplayStrictStatusInconsistencyReturnsDivergence(t *testing.T) {
	dir := t.TempDir()
	snap := makeSnap(models.StatusPassed, false)
	snap.Result.Validations = map[string]models.GraderResults{
		"quality": {Name: "quality", Passed: false, Score: 0, Weight: 1},
	}
	path := writeSnapshot(t, dir, "snap.json", snap)

	cmd := newReplayCommand()
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{path, "--json"})

	err := cmd.Execute()
	require.Error(t, err)
	var exit *ExitCodeError
	require.True(t, errors.As(err, &exit))
	require.Equal(t, 1, exit.Code)
	var report ModelReplayReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.False(t, report.Pass)
	require.Len(t, report.Issues, 1)
	require.Equal(t, "status_inconsistency", report.Issues[0].Kind)
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
