// Package executionlifecycle projects bounded, caller-declared observations.
// It does not authenticate workers, persist state, or authorize retry/reclaim.
package executionlifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version   = "1.0"
	MaxEvents = 128
	MaxBytes  = 1 << 20
)

var (
	ErrInvalid    = errors.New("invalid lifecycle observation")
	ErrContext    = errors.New("lifecycle observation context mismatch")
	ErrTransition = errors.New("invalid lifecycle transition")
)

type Event struct {
	Kind              string                   `json:"kind"`
	SchemaVersion     string                   `json:"schema_version"`
	ExecutionID       string                   `json:"execution_id"`
	ReservationDigest string                   `json:"reservation_digest"`
	Sequence          int                      `json:"sequence"`
	ObservedAt        string                   `json:"observed_at"`
	Event             string                   `json:"event"`
	Handling          string                   `json:"handling,omitempty"`
	Stop              string                   `json:"stop,omitempty"`
	Terminal          *runnerterminal.Envelope `json:"terminal,omitempty"`
}

type Observation struct {
	Sequence   int    `json:"sequence"`
	ObservedAt string `json:"observed_at"`
	Event      string `json:"event"`
	Handling   string `json:"handling,omitempty"`
	Stop       string `json:"stop,omitempty"`
}

type RetryAdvice struct {
	Advice string `json:"advice"`
	Reason string `json:"reason"`
}

type Terminal struct {
	Digest           string                 `json:"digest"`
	Outcome          runnerterminal.Outcome `json:"outcome"`
	ExitCode         int                    `json:"exit_code"`
	FailureBoundary  string                 `json:"failure_boundary"`
	ExecutionVerdict string                 `json:"execution_verdict"`
}

type Snapshot struct {
	Kind              string        `json:"kind"`
	SchemaVersion     string        `json:"schema_version"`
	ExecutionID       string        `json:"execution_id"`
	ReservationDigest string        `json:"reservation_digest"`
	Evidence          string        `json:"evidence"`
	State             string        `json:"state"`
	Cancellation      string        `json:"cancellation"`
	Deadline          string        `json:"deadline,omitempty"`
	DeadlineExpired   bool          `json:"deadline_expired"`
	CompletedUnits    *int          `json:"completed_units,omitempty"`
	Terminal          *Terminal     `json:"terminal,omitempty"`
	Retry             RetryAdvice   `json:"retry"`
	Heartbeat         string        `json:"heartbeat,omitempty"`
	AsOf              string        `json:"as_of,omitempty"`
	HeartbeatAgeMS    *int64        `json:"heartbeat_age_ms,omitempty"`
	SuspectedOrphan   *bool         `json:"suspected_orphan,omitempty"`
	Actions           []string      `json:"actions"`
	Diagnostics       []string      `json:"diagnostics"`
	Observations      []Observation `json:"observations"`
}

var eventSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLifecycle("execution-lifecycle-observation-1.0.schema.json")
})

func compileLifecycle(name string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-lifecycle-observation-1.0.schema.json": schemas.ExecutionLifecycleObservationSchemaJSON,
		"execution-lifecycle-inspection-1.0.schema.json":  schemas.ExecutionLifecycleInspectionSchemaJSON,
		"execution-lifecycle-1.0.schema.json":             schemas.ExecutionLifecycleSchemaJSON,
		"artifact-manifest-1.0.schema.json":               schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":                 schemas.RunnerTerminalSchemaJSON,
	} {
		resource := "https://microsoft.github.io/waza/schemas/" + name
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading lifecycle schema %s: %w", name, err)
		}
		if err := compiler.AddResource(resource, doc); err != nil {
			return nil, fmt.Errorf("registering lifecycle schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/" + name)
}

func timestamp(text string) (time.Time, error) {
	value, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || value.Year() < 1 || value.Location() != time.UTC || value.Format(time.RFC3339Nano) != text {
		return time.Time{}, fmt.Errorf("%w: timestamps must be canonical UTC RFC3339", ErrInvalid)
	}
	return value, nil
}

// Parse validates original wire types, presence, duplicate keys and Unicode.
// A parsed declaration carries no ownership or observation authority.
func Parse(data []byte) (*Event, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing: %w", ErrInvalid, err)
	}
	schema, err := eventSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling lifecycle schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var event Event
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	if _, err := timestamp(event.ObservedAt); err != nil {
		return nil, err
	}
	return &event, nil
}

func Encode(event Event) ([]byte, error) {
	if err := validStrings(event); err != nil {
		return nil, err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

func validStrings(event Event) error {
	texts := []string{event.Kind, event.SchemaVersion, event.ExecutionID, event.ReservationDigest,
		event.ObservedAt, event.Event, event.Handling, event.Stop}
	if terminal := event.Terminal; terminal != nil {
		texts = append(texts, terminal.Kind, terminal.SchemaVersion, string(terminal.Outcome), terminal.FailureBoundary, terminal.Validation)
		if identity := terminal.Identity; identity != nil {
			texts = append(texts, identity.Version, identity.CanonicalDigest, identity.Run,
				identity.Shard, identity.Task, identity.Trial, identity.Attempt)
		}
		if selection := terminal.Selection; selection != nil {
			texts = append(texts, selection.Shard, selection.Task)
		}
		for _, diagnostic := range terminal.Diagnostics {
			texts = append(texts, diagnostic.Code, diagnostic.Message)
		}
		if observation := terminal.Observation; observation != nil {
			texts = append(texts, observation.ExecutionMode, observation.Selection.Shard, observation.Selection.Task,
				observation.Status, observation.ErrorMsg)
			for _, verdict := range observation.Validations {
				texts = append(texts, verdict.Identifier)
			}
		}
	}
	for _, text := range texts {
		if !utf8.ValidString(text) {
			return fmt.Errorf("%w: string is not UTF-8", ErrInvalid)
		}
	}
	return nil
}

// Projection is invocation-local and not concurrently mutable. New requires
// independently retained admission context, not context inferred from sidecars.
// No read or successful comparison reconstructs an attempt creator capability.
type Projection struct {
	value   Snapshot
	binding runnerterminal.Binding
	last    time.Time
}

func New(reservation []byte, original runnerterminal.Binding, deadline string) (*Projection, error) {
	attempt, err := executionattempt.ParseAttempt(reservation)
	if err != nil {
		return nil, fmt.Errorf("%w: reservation: %w", ErrContext, err)
	}
	canonical, err := executionattempt.EncodeAttempt(*attempt)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(canonical, reservation) || original.Identity == nil ||
		original.GraderIDs == nil || attempt.Binding.Identity != *original.Identity ||
		attempt.Binding.Selection != original.Selection || attempt.Binding.ExecutionMode != original.ExecutionMode {
		return nil, fmt.Errorf("%w: reservation differs from canonical original admission", ErrContext)
	}
	graders := slices.Clone(original.GraderIDs)
	slices.Sort(graders)
	if !slices.Equal(graders, attempt.Binding.GraderIDs) {
		return nil, fmt.Errorf("%w: original grader set differs", ErrContext)
	}
	if deadline != "" {
		if _, err := timestamp(deadline); err != nil {
			return nil, err
		}
	}
	identity := *original.Identity
	original.Identity, original.GraderIDs = &identity, graders
	return &Projection{
		binding: original,
		value: Snapshot{
			Kind: "waza.execution-lifecycle", SchemaVersion: Version,
			ExecutionID: attempt.ExecutionID, ReservationDigest: artifactmanifest.Hash(reservation),
			Evidence: "caller-declared-unverified", State: "admitted", Cancellation: "none",
			Deadline: deadline, Retry: RetryAdvice{"unknown", "terminal-unavailable"},
			Actions: []string{}, Diagnostics: []string{}, Observations: []Observation{},
		},
	}, nil
}

// Apply commits only a valid next observation. Sequence must be contiguous;
// timestamps may tie but cannot go backwards. Terminal evidence is absorbing.
func (p *Projection) Apply(event Event) error {
	data, err := Encode(event)
	if err != nil {
		return err
	}
	owned, err := Parse(data)
	if err != nil {
		return err
	}
	event = *owned
	if event.ExecutionID != p.value.ExecutionID || event.ReservationDigest != p.value.ReservationDigest {
		return fmt.Errorf("%w: observation retags original reservation", ErrContext)
	}
	at, err := timestamp(event.ObservedAt)
	if err != nil {
		return err
	}
	if p.value.Terminal != nil || event.Sequence != len(p.value.Observations)+1 ||
		at.Before(p.last) || len(p.value.Observations) >= MaxEvents {
		return fmt.Errorf("%w: terminal, unordered or exhausted observation sequence", ErrTransition)
	}
	if len(p.value.Observations) == MaxEvents-1 && event.Event != "terminal_recorded" {
		return fmt.Errorf("%w: final observation slot is reserved for terminal evidence", ErrTransition)
	}
	next := p.value
	switch event.Event {
	case "started":
		if next.State != "admitted" {
			return fmt.Errorf("%w: started requires admitted state", ErrTransition)
		}
		next.State = "running"
		zero := 0
		next.CompletedUnits = &zero
	case "heartbeat":
		if next.State != "running" {
			return fmt.Errorf("%w: heartbeat requires running state", ErrTransition)
		}
		next.Heartbeat = event.ObservedAt
	case "cancellation_requested":
		if next.Cancellation != "none" || next.State == "stopped-without-terminal" {
			return fmt.Errorf("%w: cancellation already requested or process stopped", ErrTransition)
		}
		next.Cancellation = "requested"
	case "cancellation_acknowledged":
		if next.Cancellation != "requested" && next.Cancellation != "force_requested" {
			return fmt.Errorf("%w: acknowledgement requires an outstanding request", ErrTransition)
		}
		// A late acknowledgement must not erase an outstanding force request.
		if next.Cancellation == "requested" {
			next.Cancellation = "handling_acknowledged"
		}
	case "force_requested":
		if next.Cancellation != "requested" && next.Cancellation != "handling_acknowledged" {
			return fmt.Errorf("%w: force requires cancellation request", ErrTransition)
		}
		next.Cancellation = "force_requested"
	case "stop_observed":
		if next.Cancellation != "force_requested" {
			return fmt.Errorf("%w: stop observation requires force request", ErrTransition)
		}
		next.Cancellation, next.State = "stop_observed", "stopped-without-terminal"
		next.CompletedUnits = nil
	case "deadline_expired":
		if next.Deadline == "" || next.DeadlineExpired || next.State == "stopped-without-terminal" {
			return fmt.Errorf("%w: expiry requires an unexpired configured deadline", ErrTransition)
		}
		deadline, err := timestamp(next.Deadline)
		if err != nil {
			return err
		}
		if at.Before(deadline) {
			return fmt.Errorf("%w: deadline has not elapsed", ErrTransition)
		}
		next.DeadlineExpired = true
	case "terminal_recorded":
		terminalBytes, err := runnerterminal.Encode(*event.Terminal, &p.binding)
		if err != nil {
			return fmt.Errorf("%w: terminal: %w", ErrContext, err)
		}
		advice, err := retryAdvice(event.Terminal.Outcome)
		if err != nil {
			return err
		}
		verdict := "unavailable"
		next.CompletedUnits = nil
		if observation := event.Terminal.Observation; observation != nil {
			verdict = observation.Status
			units := 0
			if verdict == "passed" || verdict == "failed" {
				units = 1
			}
			next.CompletedUnits = &units
		}
		next.Terminal = &Terminal{
			Digest: artifactmanifest.Hash(terminalBytes), Outcome: event.Terminal.Outcome,
			ExitCode: event.Terminal.ExitCode, FailureBoundary: event.Terminal.FailureBoundary,
			ExecutionVerdict: verdict,
		}
		next.State, next.Retry = "terminal-recorded", advice
		if next.DeadlineExpired && event.Terminal.Outcome != runnerterminal.Timeout {
			next.Diagnostics = append(slices.Clone(next.Diagnostics), "deadline-observation-differs-from-terminal-outcome")
		}
	}
	next.Observations = append(slices.Clone(next.Observations), Observation{
		Sequence: event.Sequence, ObservedAt: event.ObservedAt, Event: event.Event, Handling: event.Handling, Stop: event.Stop,
	})
	p.value, p.last = next, at
	return nil
}

func retryAdvice(outcome runnerterminal.Outcome) (RetryAdvice, error) {
	switch outcome {
	case runnerterminal.Success:
		return RetryAdvice{"none", "completed-success"}, nil
	case runnerterminal.EvaluationFailure:
		return RetryAdvice{"none", "completed-check-failure-not-transient-advice"}, nil
	case runnerterminal.InvalidInput, runnerterminal.UnsupportedCapability:
		return RetryAdvice{"none", "change-request-before-independent-admission"}, nil
	case runnerterminal.InfrastructureFailure, runnerterminal.Canceled, runnerterminal.Timeout:
		return RetryAdvice{"conditional-fresh-attempt", "requires-independent-admission-and-side-effect-review"}, nil
	default:
		return RetryAdvice{}, fmt.Errorf("%w: unknown terminal outcome %q", ErrInvalid, outcome)
	}
}

// Snapshot derives diagnostic age only from an observed heartbeat and explicit
// as-of/threshold. Missing heartbeat/as-of remains unknown, never zero/fresh.
// An expired observation suspects an orphan; it cannot authorize reservation reuse.
func (p *Projection) Snapshot(asOf string, staleAfter time.Duration) (Snapshot, error) {
	if staleAfter <= 0 {
		return Snapshot{}, fmt.Errorf("%w: stale threshold must be positive", ErrInvalid)
	}
	out := p.value
	out.Observations, out.Diagnostics = slices.Clone(out.Observations), slices.Clone(out.Diagnostics)
	if out.CompletedUnits != nil {
		units := *out.CompletedUnits
		out.CompletedUnits = &units
	}
	if out.Terminal != nil {
		terminal := *out.Terminal
		out.Terminal = &terminal
	}
	if asOf != "" {
		at, err := timestamp(asOf)
		if err != nil {
			return Snapshot{}, err
		}
		if at.Before(p.last) {
			return Snapshot{}, fmt.Errorf("%w: as-of predates observations", ErrInvalid)
		}
		out.AsOf = asOf
		if out.Heartbeat != "" {
			heartbeat, err := timestamp(out.Heartbeat)
			if err != nil {
				return Snapshot{}, err
			}
			age := at.Sub(heartbeat)
			if !heartbeat.Add(age).Equal(at) {
				return Snapshot{}, fmt.Errorf("%w: heartbeat age exceeds duration range", ErrInvalid)
			}
			ms, suspected := age.Milliseconds(), age >= staleAfter && out.Terminal == nil && out.State != "stopped-without-terminal"
			out.HeartbeatAgeMS, out.SuspectedOrphan = &ms, &suspected
		}
	}
	out.Actions = []string{"inspect-original-evidence"}
	if out.Terminal == nil && out.State != "stopped-without-terminal" && out.Cancellation == "none" {
		out.Actions = append(out.Actions, "request-cancellation")
	}
	if out.SuspectedOrphan != nil && *out.SuspectedOrphan {
		out.Actions = append(out.Actions, "investigate-worker-no-reclaim")
	}
	if out.Retry.Advice == "conditional-fresh-attempt" {
		out.Actions = append(out.Actions, "review-independent-fresh-attempt")
	}
	data, err := json.Marshal(out)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: encoding projection: %w", ErrInvalid, err)
	}
	if len(data) > MaxBytes {
		return Snapshot{}, fmt.Errorf("%w: projection exceeds %d bytes", ErrInvalid, MaxBytes)
	}
	if err := validateSnapshot(data); err != nil {
		return Snapshot{}, err
	}
	return out, nil
}

var snapshotSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileLifecycle("execution-lifecycle-1.0.schema.json")
})

func validateSnapshot(data []byte) error {
	schema, err := snapshotSchema()
	if err != nil {
		return fmt.Errorf("compiling lifecycle projection schema: %w", err)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("%w: parsing projection: %w", ErrInvalid, err)
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("%w: projection schema: %w", ErrInvalid, err)
	}
	return nil
}
