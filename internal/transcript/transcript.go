package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/spboyer/waza/internal/models"
)

// sanitize replaces characters that are unsafe in filenames.
var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sanitizeName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = unsafeChars.ReplaceAllString(s, "")
	if s == "" {
		s = "unnamed"
	}
	return s
}

// Filename returns the transcript filename for a task.
func Filename(taskName string, ts time.Time) string {
	return fmt.Sprintf("%s-%s.json", sanitizeName(taskName), ts.Format("20060102-150405"))
}

// Write serializes a TaskTranscript and writes it to dir.
func Write(dir string, t *models.TaskTranscript) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create transcript dir: %w", err)
	}

	name := Filename(t.TaskName, t.StartedAt)
	path := filepath.Join(dir, name)

	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal transcript: %w", err)
	}

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write transcript: %w", err)
	}

	return path, nil
}

// BuildFromSessionEvents converts a slice of Copilot session events
// into TranscriptEvents.
func BuildFromSessionEvents(events []copilot.SessionEvent) []models.TranscriptEvent {
	entries := make([]models.TranscriptEvent, 0, len(events))
	for _, evt := range events {
		entries = append(entries, models.TranscriptEvent{SessionEvent: evt})
	}
	return entries
}

// BuildTaskTranscript constructs a TaskTranscript from run results.
func BuildTaskTranscript(tc *models.TestCase, outcome models.TestOutcome, startTime time.Time) *models.TaskTranscript {
	var totalDurationMs int64
	var allEntries []models.TranscriptEvent
	allValidations := make(map[string]models.GraderResults)
	var finalOutput string
	var session models.SessionDigest
	var errMsg string

	for _, run := range outcome.Runs {
		totalDurationMs += run.DurationMs
		allEntries = append(allEntries, run.Transcript...)
		for k, v := range run.Validations {
			allValidations[k] = v
		}
		finalOutput = run.FinalOutput
		session = run.SessionDigest
		if run.ErrorMsg != "" {
			errMsg = run.ErrorMsg
		}
	}

	endTime := startTime.Add(time.Duration(totalDurationMs) * time.Millisecond)

	return &models.TaskTranscript{
		TaskID:      tc.TestID,
		TaskName:    tc.DisplayName,
		Status:      outcome.Status,
		StartedAt:   startTime,
		CompletedAt: endTime,
		DurationMs:  totalDurationMs,
		Prompt:      tc.Stimulus.Message,
		FinalOutput: finalOutput,
		Transcript:  allEntries,
		Validations: allValidations,
		Session:     session,
		ErrorMsg:    errMsg,
	}
}
