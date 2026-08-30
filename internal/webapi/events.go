package webapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const liveEventSchemaVersion = "1.0"

// RunEventType identifies a live progress event in a run SSE stream.
type RunEventType string

const (
	RunEventStarted       RunEventType = "run_started"
	RunEventTaskStarted   RunEventType = "task_started"
	RunEventStepExecuted  RunEventType = "step_executed"
	RunEventTaskCompleted RunEventType = "task_completed"
	RunEventCompleted     RunEventType = "run_completed"
	RunEventFailed        RunEventType = "run_failed"
)

// RunEvent is the JSON payload sent in each Server-Sent Event.
type RunEvent struct {
	SchemaVersion string       `json:"schemaVersion"`
	Sequence      int64        `json:"sequence"`
	RunID         string       `json:"runId"`
	Type          RunEventType `json:"type"`
	Data          RunEventData `json:"data"`
	Timestamp     time.Time    `json:"timestamp"`
}

// RunEventData carries event-specific progress details.
type RunEventData struct {
	Spec           string  `json:"spec,omitempty"`
	Model          string  `json:"model,omitempty"`
	TaskName       string  `json:"taskName,omitempty"`
	Outcome        string  `json:"outcome,omitempty"`
	Score          float64 `json:"score,omitempty"`
	Duration       float64 `json:"duration,omitempty"`
	GraderName     string  `json:"graderName,omitempty"`
	GraderType     string  `json:"graderType,omitempty"`
	Passed         *bool   `json:"passed,omitempty"`
	Message        string  `json:"message,omitempty"`
	TotalTasks     int     `json:"totalTasks,omitempty"`
	CompletedTasks int     `json:"completedTasks,omitempty"`
	PassCount      int     `json:"passCount,omitempty"`
	FailCount      int     `json:"failCount,omitempty"`
	Tokens         int     `json:"tokens,omitempty"`
	Cost           float64 `json:"cost,omitempty"`
}

func runEventsFromDetail(detail *RunDetail) []RunEvent {
	if detail == nil {
		return nil
	}

	base := detail.Timestamp
	if base.IsZero() {
		base = time.Unix(0, 0).UTC()
	}

	var seq int64
	events := make([]RunEvent, 0, 2+(len(detail.Tasks)*3))
	appendEvent := func(t RunEventType, data RunEventData) {
		seq++
		events = append(events, RunEvent{
			SchemaVersion: liveEventSchemaVersion,
			Sequence:      seq,
			RunID:         detail.ID,
			Type:          t,
			Data:          data,
			Timestamp:     base.Add(time.Duration(seq-1) * time.Millisecond),
		})
	}

	appendEvent(RunEventStarted, RunEventData{
		Spec:       detail.Spec,
		Model:      detail.Model,
		TotalTasks: detail.TaskCount,
		PassCount:  detail.PassCount,
		FailCount:  detail.TaskCount - detail.PassCount,
	})

	completed := 0
	for _, task := range detail.Tasks {
		appendEvent(RunEventTaskStarted, RunEventData{
			TaskName:   task.Name,
			TotalTasks: detail.TaskCount,
		})

		graders := append([]GraderResult(nil), task.GraderResults...)
		sort.SliceStable(graders, func(i, j int) bool {
			return graders[i].Name < graders[j].Name
		})
		for _, grader := range graders {
			passed := grader.Passed
			appendEvent(RunEventStepExecuted, RunEventData{
				TaskName:   task.Name,
				GraderName: grader.Name,
				GraderType: grader.Type,
				Passed:     &passed,
				Score:      grader.Score,
				Message:    grader.Message,
			})
		}

		completed++
		appendEvent(RunEventTaskCompleted, RunEventData{
			TaskName:       task.Name,
			Outcome:        task.Outcome,
			Score:          task.Score,
			Duration:       task.Duration,
			TotalTasks:     detail.TaskCount,
			CompletedTasks: completed,
		})
	}

	if !isTerminalRunDetail(detail) {
		return events
	}

	eventType := RunEventCompleted
	if isFailureOutcome(detail.Outcome) {
		eventType = RunEventFailed
	}
	appendEvent(eventType, RunEventData{
		Outcome:    detail.Outcome,
		TotalTasks: detail.TaskCount,
		PassCount:  detail.PassCount,
		FailCount:  detail.TaskCount - detail.PassCount,
		Tokens:     detail.Tokens,
		Cost:       detail.Cost,
		Duration:   detail.Duration,
	})

	return events
}

func isTerminalRunDetail(detail *RunDetail) bool {
	if detail == nil {
		return false
	}
	if isFailureOutcome(detail.Outcome) {
		return true
	}
	outcome := strings.ToLower(detail.Outcome)
	if !strings.HasPrefix(outcome, "pass") {
		return false
	}
	return detail.TaskCount == 0 || len(detail.Tasks) >= detail.TaskCount
}

func isFailureOutcome(outcome string) bool {
	outcome = strings.ToLower(outcome)
	return strings.HasPrefix(outcome, "fail") || strings.HasPrefix(outcome, "error")
}

func filterEventsAfter(events []RunEvent, lastID int64) []RunEvent {
	if lastID <= 0 {
		return events
	}
	for i, event := range events {
		if event.Sequence > lastID {
			return events[i:]
		}
	}
	return nil
}

func lastEventID(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if queryID := r.URL.Query().Get("lastEventId"); queryID != "" {
		raw = queryID
	}
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("invalid Last-Event-ID %q", raw)
	}
	return id, nil
}

func writeSSEHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
}

func writeRunSSEEvent(w io.Writer, event RunEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "id: %d\n", event.Sequence); err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(w, "\n")
	return err
}
