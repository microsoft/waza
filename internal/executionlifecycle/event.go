// Package executionlifecycle records owner lifecycle events for one dispatched
// execution attempt and derives read-only lifecycle state from those events and
// the attempt's immutable ledger facts. It never writes ledger records, never
// repairs or replaces evidence and owns no retry policy.
package executionlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version   = "1.0"
	EventKind = "waza.execution-lifecycle-event"
)

type EventType string

const (
	Started            EventType = "started"
	Heartbeat          EventType = "heartbeat"
	CancelRequested    EventType = "cancel_requested"
	CancelAcknowledged EventType = "cancel_acknowledged"
	Forced             EventType = "forced"
	Finished           EventType = "finished"
)

// Cancellation reasons distinguish a caller/operator request from the
// request's own deadline.
const (
	ReasonCaller   = "caller"
	ReasonDeadline = "deadline"
)

// Event is one owner-recorded observation. Times come from the owner's clock;
// they are diagnostics, not authenticated worker liveness.
type Event struct {
	Kind                string                 `json:"kind"`
	SchemaVersion       string                 `json:"schema_version"`
	Event               EventType              `json:"event"`
	At                  time.Time              `json:"at"`
	DeadlineAt          *time.Time             `json:"deadline_at,omitempty"`
	HeartbeatIntervalMS int64                  `json:"heartbeat_interval_ms,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	Outcome             runnerterminal.Outcome `json:"outcome,omitempty"`
}

var schemaFiles = map[string]string{
	"execution-lifecycle-1.0.schema.json": schemas.ExecutionLifecycleSchemaJSON,
	"runner-terminal-1.0.schema.json":     schemas.RunnerTerminalSchemaJSON,
}

var eventSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range schemaFiles {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading lifecycle schema %s: %w", name, err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, fmt.Errorf("registering lifecycle schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/execution-lifecycle-1.0.schema.json")
})

func normalize(event Event) Event {
	event.Kind, event.SchemaVersion = EventKind, Version
	event.At = event.At.UTC()
	if event.DeadlineAt != nil {
		deadline := event.DeadlineAt.UTC()
		event.DeadlineAt = &deadline
	}
	return event
}

// EncodeEvent returns the canonical, schema-valid payload for one event.
func EncodeEvent(event Event) ([]byte, error) {
	data, err := json.Marshal(normalize(event))
	if err != nil {
		return nil, fmt.Errorf("encoding lifecycle event: %w", err)
	}
	if _, err := ParseEvent(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ParseEvent admits only schema-valid, canonical event payloads.
func ParseEvent(data []byte) (*Event, error) {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing lifecycle event: %w", artifactmanifest.ErrInvalid, err)
	}
	schema, err := eventSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: lifecycle event schema: %w", artifactmanifest.ErrInvalid, err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("%w: lifecycle event: %w", artifactmanifest.ErrInvalid, err)
	}
	canonical, err := json.Marshal(normalize(event))
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: lifecycle event is not canonical", artifactmanifest.ErrInvalid)
	}
	return &event, nil
}

// Admit is the diagnostics-storage host gate for lifecycle payloads.
func Admit(_ context.Context, _ artifactmanifest.Scope, payload json.RawMessage) error {
	_, err := ParseEvent(payload)
	return err
}

// Scope binds a lifecycle journal to one dispatched attempt. The dispatch
// decision is unique per execution, so its digest names the invocation.
func Scope(executionID, reservationDigest, dispatchDecision string) (artifactmanifest.Scope, error) {
	digest, ok := strings.CutPrefix(dispatchDecision, "decision-")
	if !ok {
		return artifactmanifest.Scope{}, fmt.Errorf("%w: lifecycle scope requires a dispatch decision", artifactmanifest.ErrInvalid)
	}
	return artifactmanifest.Scope{ExecutionID: executionID, ReservationDigest: reservationDigest, InvocationID: "inv-" + digest}, nil
}
