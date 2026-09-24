package session

import "time"

// EventType identifies the kind of session event.
type EventType string

const (
	EventSessionStart EventType = "session_start"
	EventSessionEnd   EventType = "session_complete"
	EventTaskStart    EventType = "task_start"
	EventTaskComplete EventType = "task_complete"
	EventRunComplete  EventType = "run_complete"
	EventGraderResult EventType = "grader_result"
	EventError        EventType = "error"
)

// Event is a single timestamped entry in a session log.
type Event struct {
	Timestamp time.Time      `json:"timestamp"`
	Type      EventType      `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
}

// NewEvent creates an event with the current timestamp.
func NewEvent(t EventType, data map[string]any) Event {
	return Event{
		Timestamp: time.Now().UTC(),
		Type:      t,
		Data:      data,
	}
}

// SessionStartData returns event data for a session start.
func SessionStartData(specPath, model, engine string, taskCount int) map[string]any {
	return map[string]any{
		"spec_path":  specPath,
		"model":      model,
		"engine":     engine,
		"task_count": taskCount,
	}
}

// SessionCompleteData returns event data for a session end.
func SessionCompleteData(totalTests, passed, failed, errors int, durationMs int64) map[string]any {
	return map[string]any{
		"total_tests": totalTests,
		"passed":      passed,
		"failed":      failed,
		"errors":      errors,
		"duration_ms": durationMs,
	}
}

// TaskStartData returns event data for a task start.
func TaskStartData(taskName string, taskNum, totalTasks int) map[string]any {
	return map[string]any{
		"task_name":   taskName,
		"task_num":    taskNum,
		"total_tasks": totalTasks,
	}
}

// TaskCompleteData returns event data for a task completion.
func TaskCompleteData(taskName, status string, score float64, durationMs int64) map[string]any {
	return map[string]any{
		"task_name":   taskName,
		"status":      status,
		"score":       score,
		"duration_ms": durationMs,
	}
}

// GraderResultData returns event data for a grader result.
func GraderResultData(graderName, graderType string, passed bool, score float64, feedback string) map[string]any {
	return map[string]any{
		"grader_name": graderName,
		"grader_type": graderType,
		"passed":      passed,
		"score":       score,
		"feedback":    feedback,
	}
}

// ErrorData returns event data for an error.
func ErrorData(message string, details map[string]any) map[string]any {
	d := map[string]any{
		"message": message,
	}
	for k, v := range details {
		d[k] = v
	}
	return d
}
