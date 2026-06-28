package snapshot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/models"
)

func TestParseSnapshotRejectsMajorMismatch(t *testing.T) {
	doc := map[string]any{
		"schemaVersion": "9.0",
		"kind":          Kind,
	}
	b, _ := json.Marshal(doc)
	if _, err := ParseSnapshot(b, "test"); err == nil {
		t.Fatalf("expected major-version mismatch error, got nil")
	}
}

func TestParseSnapshotAcceptsHigherMinor(t *testing.T) {
	curMajor, _, _ := parseVersion(CurrentSchemaVersion)
	higher := strings.Replace(CurrentSchemaVersion, ".0", ".99", 1)
	if higher == CurrentSchemaVersion {
		// Pick a high minor regardless of the current MINOR.
		higher = ""
		_ = curMajor
	}
	if higher == "" {
		higher = "1.99"
	}
	doc := map[string]any{
		"schemaVersion": higher,
		"kind":          Kind,
	}
	b, _ := json.Marshal(doc)
	snap, err := ParseSnapshot(b, "test")
	if err != nil {
		t.Fatalf("expected higher-minor acceptance, got %v", err)
	}
	if snap.SchemaVersion != higher {
		t.Fatalf("schemaVersion = %q want %q", snap.SchemaVersion, higher)
	}
}

func TestParseSnapshotRejectsWrongKind(t *testing.T) {
	doc := map[string]any{
		"schemaVersion": CurrentSchemaVersion,
		"kind":          "results",
	}
	b, _ := json.Marshal(doc)
	if _, err := ParseSnapshot(b, "test"); err == nil {
		t.Fatalf("expected kind mismatch error, got nil")
	}
}

func TestDefaultPolicyRedactsKnownSecrets(t *testing.T) {
	p := DefaultPolicy()
	cases := map[string]string{
		"GitHub PAT":    "ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"OpenAI Key":    "sk-proj-ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		"AWS Access":    "AKIAIOSFODNN7EXAMPLE",
		"Bearer Header": "Authorization: Bearer abc123def456ghi789jklmnop",
		"Email":         "user@example.com",
	}
	for label, in := range cases {
		out := p.RedactString(in)
		if out == in {
			t.Errorf("%s: expected redaction of %q, got identical output", label, in)
		}
		if !strings.Contains(out, RedactionPlaceholder) {
			t.Errorf("%s: expected placeholder in output, got %q", label, out)
		}
	}
}

func TestPolicyIsSensitiveKey(t *testing.T) {
	p := DefaultPolicy()
	want := map[string]bool{
		"GITHUB_TOKEN":   true,
		"OPENAI_API_KEY": true,
		"PATH":           false,
		"USER":           false,
	}
	for k, exp := range want {
		got := p.IsSensitiveKey(k)
		if got != exp {
			t.Errorf("IsSensitiveKey(%q) = %v, want %v", k, got, exp)
		}
	}
}

func TestCaptureEnvDefaultDeny(t *testing.T) {
	env := captureEnvFrom([]string{"FOO=bar", "BAZ=qux"}, nil, DefaultPolicy())
	if len(env.Captured) != 0 {
		t.Fatalf("default-deny should capture nothing, got %v", env.Captured)
	}
	// With an empty allow-list we should NOT leak the full host env name
	// list into the snapshot's DeniedKeys field.
	if len(env.DeniedKeys) != 0 {
		t.Fatalf("default-deny should not record DeniedKeys, got %v", env.DeniedKeys)
	}
}

func TestCaptureEnvAllowList(t *testing.T) {
	env := captureEnvFrom([]string{"WAZA_RUN_ID=abc", "WAZA_TRACE=on", "SECRET_TOKEN=ghp_xxxx"}, []string{"WAZA_*"}, DefaultPolicy())
	if _, ok := env.Captured["WAZA_RUN_ID"]; !ok {
		t.Fatalf("expected WAZA_RUN_ID captured, got %v", env.Captured)
	}
	if _, ok := env.Captured["SECRET_TOKEN"]; ok {
		t.Fatalf("SECRET_TOKEN should be denied, got %v", env.Captured)
	}
}

func TestCaptureEnvSensitiveKeyRedacted(t *testing.T) {
	env := captureEnvFrom([]string{"MY_TOKEN=ghp_real_token_value"}, []string{"MY_TOKEN"}, DefaultPolicy())
	val, ok := env.Captured["MY_TOKEN"]
	if !ok {
		t.Fatalf("expected MY_TOKEN to be captured (allow-listed), got %v", env.Captured)
	}
	if val != RedactionPlaceholder {
		t.Fatalf("expected redaction placeholder, got %q", val)
	}
}

func TestHashFixtures(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	digests, err := HashFixtures(root)
	if err != nil {
		t.Fatalf("HashFixtures: %v", err)
	}
	if len(digests) != 2 {
		t.Fatalf("expected 2 digests, got %d", len(digests))
	}
	for _, d := range digests {
		if d.SHA256 == "" {
			t.Errorf("digest missing for %s", d.Path)
		}
	}
}

func TestHashFixturesExcludingSkipsCallerPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	skip := filepath.Join(root, "snapshots")
	if err := os.MkdirAll(skip, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skip, "snap.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	digests, err := HashFixturesExcluding(root, []string{skip})
	if err != nil {
		t.Fatalf("HashFixturesExcluding: %v", err)
	}
	if len(digests) != 1 || digests[0].Path != "a.txt" {
		t.Fatalf("expected only a.txt, got %#v", digests)
	}
}

func TestCaptureRoundtrip(t *testing.T) {
	tc := &models.TestCase{
		TestID: "demo",
		Stimulus: models.TaskStimulus{
			Message:   "hello",
			FollowUps: []string{"follow"},
		},
	}
	run := &models.RunResult{
		Status:     models.StatusPassed,
		DurationMs: 12,
		ToolEvents: []models.ToolEvent{
			{Sequence: 1, ToolName: "read_file", Args: map[string]any{"path": "a.txt"}, Result: "ok"},
		},
	}
	in := CaptureInput{
		WazaVersion: "test",
		EvalName:    "demo-eval",
		Skill:       "demo-skill",
		Task:        tc,
		Run:         run,
		Policy:      DefaultPolicy(),
	}
	snap, err := Capture(in)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if snap.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schemaVersion = %q want %q", snap.SchemaVersion, CurrentSchemaVersion)
	}
	if snap.Kind != Kind {
		t.Errorf("kind = %q want %q", snap.Kind, Kind)
	}
	if snap.Task.TestID != "demo" {
		t.Errorf("task.testId = %q", snap.Task.TestID)
	}
	if len(snap.ToolEvents) != 1 {
		t.Errorf("toolEvents = %d want 1", len(snap.ToolEvents))
	}

	// Write to disk, read back, ensure it parses.
	dir := t.TempDir()
	w := NewWriter(dir)
	path, err := w.Write(snap)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := LoadSnapshotFile(path)
	if err != nil {
		t.Fatalf("LoadSnapshotFile: %v", err)
	}
	if loaded.Task.TestID != "demo" {
		t.Errorf("loaded task.testId = %q", loaded.Task.TestID)
	}
}

func TestCompareIdentical(t *testing.T) {
	snap := makeSnapshot(t, []models.ToolEvent{
		{Sequence: 1, ToolName: "a", Args: map[string]any{"k": "v"}},
	})
	r := Compare(snap, snap, true)
	if !r.Match {
		t.Fatalf("expected match, got divergences %+v", r.Divergences)
	}
}

func TestCompareDivergentArgs(t *testing.T) {
	a := makeSnapshot(t, []models.ToolEvent{
		{Sequence: 1, ToolName: "tool", Args: map[string]any{"k": "v"}},
	})
	b := makeSnapshot(t, []models.ToolEvent{
		{Sequence: 1, ToolName: "tool", Args: map[string]any{"k": "other"}},
	})
	r := Compare(a, b, false)
	if r.Match {
		t.Fatalf("expected mismatch")
	}
	if len(r.Divergences) == 0 {
		t.Fatalf("expected at least one divergence")
	}
}

func TestBisectReportsFirstDivergence(t *testing.T) {
	a := makeSnapshot(t, []models.ToolEvent{
		{Sequence: 1, Turn: 1, ToolName: "x"},
		{Sequence: 2, Turn: 2, ToolName: "y"},
		{Sequence: 3, Turn: 3, ToolName: "z"},
	})
	b := makeSnapshot(t, []models.ToolEvent{
		{Sequence: 1, Turn: 1, ToolName: "x"},
		{Sequence: 2, Turn: 2, ToolName: "different"},
		{Sequence: 3, Turn: 3, ToolName: "z"},
	})
	turn, r := Bisect(a, b)
	if r.Match {
		t.Fatalf("expected mismatch")
	}
	if turn != 2 {
		t.Fatalf("expected turn 2, got %d", turn)
	}
}

func TestRedactAnyMapWithSecrets(t *testing.T) {
	p := DefaultPolicy()
	in := map[string]any{
		"prompt": "Use this token: ghp_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"nested": map[string]any{
			"email": "alice@example.com",
		},
	}
	out, _ := p.RedactAny(in).(map[string]any)
	prompt, _ := out["prompt"].(string)
	if !strings.Contains(prompt, RedactionPlaceholder) {
		t.Errorf("prompt not redacted: %v", out["prompt"])
	}
	nested, _ := out["nested"].(map[string]any)
	email, _ := nested["email"].(string)
	if !strings.Contains(email, RedactionPlaceholder) {
		t.Errorf("email not redacted: %v", nested["email"])
	}
}

func TestSnapshotMarshalSetsHeader(t *testing.T) {
	s := Snapshot{
		CreatedAt: time.Now().UTC(),
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"schemaVersion":"1.0"`) {
		t.Errorf("schemaVersion not set: %s", b)
	}
	if !strings.Contains(string(b), `"kind":"task-snapshot"`) {
		t.Errorf("kind not set: %s", b)
	}
}

// makeSnapshot is a small helper that builds a minimal Snapshot fixture
// for compare/bisect tests.
func makeSnapshot(t *testing.T, events []models.ToolEvent) *Snapshot {
	t.Helper()
	return &Snapshot{
		SchemaVersion: CurrentSchemaVersion,
		Kind:          Kind,
		CreatedAt:     time.Now().UTC(),
		Task:          SnapshotTask{TestID: "t"},
		Engine:        SnapshotEngine{Type: "mock", ModelID: "m1"},
		Prompt:        SnapshotPrompt{Message: "hi"},
		ToolEvents:    events,
		Env:           SnapshotEnv{},
		Redaction:     SnapshotRedaction{Policy: "default"},
		Result:        SnapshotResult{Status: models.StatusPassed},
	}
}
