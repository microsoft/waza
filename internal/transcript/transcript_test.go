package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/utils"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"Hello World", "hello-world"},
		{"task/with/slashes", "taskwithslashes"},
		{"special@chars!", "specialchars"},
		{"", "unnamed"},
		{"  spaces  ", "spaces"},
		{"Mixed-Case_Test", "mixed-case_test"},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			got := sanitizeName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFilename(t *testing.T) {
	ts := time.Date(2025, 6, 15, 14, 30, 45, 0, time.UTC)
	got := Filename("My Task", ts)
	want := "my-task-20250615-143045.json"
	if got != want {
		t.Errorf("Filename() = %q, want %q", got, want)
	}
}

func TestWrite(t *testing.T) {
	dir := t.TempDir()

	tr := &models.TaskTranscript{
		TaskID:      "test-1",
		TaskName:    "Explain Code",
		Status:      models.StatusPassed,
		StartedAt:   time.Date(2025, 6, 15, 14, 0, 0, 0, time.UTC),
		CompletedAt: time.Date(2025, 6, 15, 14, 0, 1, 0, time.UTC),
		DurationMs:  1000,
		Prompt:      "Explain this function",
		FinalOutput: "This function does X",
		Transcript: []models.TranscriptEvent{
			{
				SessionEvent: copilot.SessionEvent{
					Data: copilot.Data{Content: utils.Ptr("Explain this function")},
				},
			},
			{
				SessionEvent: copilot.SessionEvent{
					Data: copilot.Data{Content: utils.Ptr("This function does X")},
				},
			},
		},
		Validations: map[string]models.GraderResults{
			"contains-check": {
				Name:   "contains-check",
				Type:   models.GraderKindText,
				Score:  1.0,
				Passed: true,
			},
		},
		Session: models.SessionDigest{
			TotalTurns:    2,
			ToolCallCount: 0,
			TokensIn:      50,
			TokensOut:     100,
			TokensTotal:   150,
		},
	}

	path, err := Write(dir, tr)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			t.Fatal("transcript file was not created")
		}
		t.Fatalf("Stat() error: %v", err)
	}

	// Verify content is valid JSON that round-trips
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error: %v", err)
	}

	var decoded models.TaskTranscript
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if decoded.TaskID != "test-1" {
		t.Errorf("TaskID = %q, want %q", decoded.TaskID, "test-1")
	}
	if decoded.Status != models.StatusPassed {
		t.Errorf("Status = %q, want %q", decoded.Status, models.StatusPassed)
	}
	if decoded.DurationMs != 1000 {
		t.Errorf("DurationMs = %d, want %d", decoded.DurationMs, 1000)
	}
	if len(decoded.Transcript) != 2 {
		t.Errorf("len(Transcript) = %d, want %d", len(decoded.Transcript), 2)
	}
	if decoded.Prompt != "Explain this function" {
		t.Errorf("Prompt = %q, want %q", decoded.Prompt, "Explain this function")
	}
	if decoded.Session.TokensTotal != 150 {
		t.Errorf("Session.TokensTotal = %d, want %d", decoded.Session.TokensTotal, 150)
	}
}

func TestWrite_CreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "dir")

	tr := &models.TaskTranscript{
		TaskID:    "test-2",
		TaskName:  "Nested Dir Test",
		Status:    models.StatusPassed,
		StartedAt: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	}

	path, err := Write(dir, tr)
	if err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			t.Fatal("transcript file was not created in nested dir")
		}
		t.Fatalf("failed to stat transcript file: %v", err)
	}
}

func TestBuildTaskTranscript(t *testing.T) {
	tc := &models.TestCase{
		TestID:      "tc-1",
		DisplayName: "Code Explain",
		Stimulus: models.TestStimulus{
			Message: "Explain this code",
		},
	}

	outcome := models.TestOutcome{
		TestID:      "tc-1",
		DisplayName: "Code Explain",
		Status:      models.StatusPassed,
		Runs: []models.RunResult{
			{
				RunNumber:  1,
				Status:     models.StatusPassed,
				DurationMs: 500,
				Transcript: []models.TranscriptEvent{
					{SessionEvent: copilot.SessionEvent{Data: copilot.Data{Content: utils.Ptr("Explain this code")}}},
					{SessionEvent: copilot.SessionEvent{Data: copilot.Data{Content: utils.Ptr("Sure, this code...")}}},
				},
				Validations: map[string]models.GraderResults{
					"check": {Name: "check", Score: 1.0, Passed: true},
				},
				SessionDigest: models.SessionDigest{TotalTurns: 2, TokensTotal: 100},
				FinalOutput:   "Sure, this code...",
			},
		},
	}

	start := time.Now()
	result := BuildTaskTranscript(tc, outcome, start)

	if result.TaskID != "tc-1" {
		t.Errorf("TaskID = %q, want %q", result.TaskID, "tc-1")
	}
	if result.TaskName != "Code Explain" {
		t.Errorf("TaskName = %q, want %q", result.TaskName, "Code Explain")
	}
	if result.Status != models.StatusPassed {
		t.Errorf("Status = %q, want %q", result.Status, models.StatusPassed)
	}
	if result.Prompt != "Explain this code" {
		t.Errorf("Prompt = %q, want %q", result.Prompt, "Explain this code")
	}
	if result.DurationMs != 500 {
		t.Errorf("DurationMs = %d, want %d", result.DurationMs, 500)
	}
	if len(result.Transcript) != 2 {
		t.Errorf("len(Transcript) = %d, want %d", len(result.Transcript), 2)
	}
	if result.FinalOutput != "Sure, this code..." {
		t.Errorf("FinalOutput = %q, want %q", result.FinalOutput, "Sure, this code...")
	}
}
