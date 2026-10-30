// Package runnerterminal defines bounded, invocation-local terminal envelopes.
// It neither executes work nor attests that caller-supplied evidence was observed.
package runnerterminal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version            = "1.0"
	MaxEnvelopeBytes   = 1 << 20
	MaxDiagnostics     = 16
	MaxMessageBytes    = 4096
	MaxGraders         = 128
	MaxIdentifierBytes = 256
	MaxIdentityBytes   = 2048
)

type Outcome string

const (
	Success               Outcome = "success"
	EvaluationFailure     Outcome = "evaluation_failure"
	InfrastructureFailure Outcome = "infrastructure_failure"
	Canceled              Outcome = "cancelled" //nolint:misspell // Stable external outcome spelling.
	Timeout               Outcome = "timeout"
	UnsupportedCapability Outcome = "unsupported_capability"
	InvalidInput          Outcome = "invalid_input"
)

// ExitCode returns false for an unknown outcome rather than defaulting to success.
func (o Outcome) ExitCode() (int, bool) {
	switch o {
	case Success:
		return 0, true
	case EvaluationFailure:
		return 1, true
	case InfrastructureFailure:
		return 2, true
	case Canceled:
		return 3, true
	case Timeout:
		return 4, true
	case UnsupportedCapability:
		return 5, true
	case InvalidInput:
		return 6, true
	default:
		return 2, false
	}
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Verdict struct {
	Identifier string `json:"identifier"`
	Passed     bool   `json:"passed"`
}

// Observation is a terminal summary, not a replacement for models.RunResult.
// The producer must project actual returned evidence, never configured mock text.
type Observation struct {
	ExecutionMode string                   `json:"execution_mode"`
	EngineCalls   int                      `json:"engine_calls"`
	Selection     runnercontract.Selection `json:"selection"`
	RunNumber     int                      `json:"run_number"`
	Attempts      int                      `json:"attempts"`
	Status        string                   `json:"status"`
	Validations   []Verdict                `json:"validations"`
	ErrorMsg      string                   `json:"error_msg,omitempty"`
}

type Envelope struct {
	Kind            string                    `json:"kind"`
	SchemaVersion   string                    `json:"schema_version"`
	Outcome         Outcome                   `json:"outcome"`
	ExitCode        int                       `json:"exit_code"`
	FailureBoundary string                    `json:"failure_boundary"`
	Validation      string                    `json:"validation"`
	EngineCalls     int                       `json:"engine_calls"`
	Selection       *runnercontract.Selection `json:"selection,omitempty"`
	Identity        *runneridentity.Identity  `json:"identity,omitempty"`
	Observation     *Observation              `json:"observation,omitempty"`
	Diagnostics     []Diagnostic              `json:"diagnostics"`
}

// Binding is supplied only after exact admission and successful identity
// derivation. GraderIDs is the complete admitted set, including explicit [].
// It is comparison context, not a second serialized identity or execution proof.
type Binding struct {
	Selection runnercontract.Selection
	Identity  *runneridentity.Identity
	GraderIDs []string
}

var (
	ErrInvalid  = errors.New("invalid terminal envelope")
	ErrTooLarge = errors.New("terminal envelope exceeds supported bounds")
)

var terminalSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSchema(schemas.RunnerTerminalSchemaJSON)
})

func compileSchema(text string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("reading terminal schema: %w", err)
	}
	if err := compiler.AddResource("runner-terminal.json", doc); err != nil {
		return nil, fmt.Errorf("registering terminal schema: %w", err)
	}
	schema, err := compiler.Compile("runner-terminal.json")
	if err != nil {
		return nil, fmt.Errorf("compiling terminal schema: %w", err)
	}
	return schema, nil
}

// Encode bounds typed inputs before serialization, then applies the same strict
// wire and contextual checks as Validate. Evidence is never truncated or replaced.
func Encode(envelope Envelope, expected *Binding) ([]byte, error) {
	if err := bounds(envelope, expected); err != nil {
		return nil, err
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	data = append(data, '\n')
	if _, err := Validate(data, expected); err != nil {
		return nil, err
	}
	return data, nil
}

// Validate checks original JSON before decoding and compares identity/selection
// against the caller's admitted binding. It does not derive IDs from absent input.
func Validate(data []byte, expected *Binding) (*Envelope, error) {
	if len(data) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: got %d bytes; maximum %d", ErrTooLarge, len(data), MaxEnvelopeBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing: %w", ErrInvalid, err)
	}
	schema, err := terminalSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	if err := bounds(envelope, expected); err != nil {
		return nil, err
	}
	if err := consistent(envelope, expected); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return &envelope, nil
}

func bounded(text string, limit int) error {
	if len(text) > limit {
		return fmt.Errorf("%w: string has %d bytes; maximum %d", ErrTooLarge, len(text), limit)
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("%w: string is not UTF-8", ErrInvalid)
	}
	return nil
}

func selectionBounds(selection runnercontract.Selection) error {
	return errors.Join(bounded(selection.Shard, MaxIdentifierBytes), bounded(selection.Task, MaxIdentifierBytes))
}

func bounds(e Envelope, expected *Binding) error {
	for _, field := range []string{e.Kind, e.SchemaVersion, string(e.Outcome), e.FailureBoundary, e.Validation} {
		if err := bounded(field, 64); err != nil {
			return err
		}
	}
	if len(e.Diagnostics) > MaxDiagnostics {
		return ErrTooLarge
	}
	for _, identity := range []*runneridentity.Identity{e.Identity, bindingIdentity(expected)} {
		if identity != nil {
			for _, value := range []string{identity.Version, identity.CanonicalDigest, identity.Run,
				identity.Shard, identity.Task, identity.Trial, identity.Attempt} {
				if err := bounded(value, MaxIdentityBytes/7); err != nil {
					return err
				}
			}
		}
	}
	for _, diagnostic := range e.Diagnostics {
		if err := errors.Join(bounded(diagnostic.Code, 64), bounded(diagnostic.Message, MaxMessageBytes)); err != nil {
			return err
		}
	}
	if e.Selection != nil {
		if err := selectionBounds(*e.Selection); err != nil {
			return err
		}
	}
	if o := e.Observation; o != nil {
		if len(o.Validations) > MaxGraders {
			return ErrTooLarge
		}
		if err := errors.Join(selectionBounds(o.Selection), bounded(o.ExecutionMode, 64),
			bounded(o.Status, 64), bounded(o.ErrorMsg, MaxMessageBytes)); err != nil {
			return err
		}
		for _, verdict := range o.Validations {
			if err := bounded(verdict.Identifier, MaxIdentifierBytes); err != nil {
				return err
			}
		}
	}
	if expected != nil && e.Observation != nil {
		if len(expected.GraderIDs) > MaxGraders {
			return ErrTooLarge
		}
		if err := selectionBounds(expected.Selection); err != nil {
			return err
		}
		for _, id := range expected.GraderIDs {
			if err := bounded(id, MaxIdentifierBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func consistent(e Envelope, expected *Binding) error {
	if expected == nil {
		if e.Selection != nil || e.Identity != nil || e.Observation != nil || e.EngineCalls != 0 {
			return errors.New("unadmitted envelope contains identity, selection, or observation")
		}
		return nil
	}
	if e.Validation != "valid" || e.Selection == nil || *e.Selection != expected.Selection {
		return errors.New("selection does not match admitted work")
	}
	if e.Identity == nil || expected.Identity == nil || *e.Identity != *expected.Identity {
		return errors.New("identity does not match admitted work")
	}
	o := e.Observation
	if o == nil {
		return nil
	}
	ids := make(map[string]bool, len(expected.GraderIDs))
	if expected.GraderIDs == nil {
		return errors.New("admitted grader set is unavailable")
	}
	for _, id := range expected.GraderIDs {
		if id == "" || ids[id] {
			return errors.New("admitted grader identifiers are empty or duplicated")
		}
		ids[id] = true
	}
	if e.EngineCalls != o.EngineCalls {
		return errors.New("observation contradicts invocation engine call count")
	}
	if o.Selection != expected.Selection || o.RunNumber != expected.Selection.Trial || o.Attempts != expected.Selection.Attempt {
		return errors.New("observation does not match admitted selection")
	}
	seen := make(map[string]bool, len(o.Validations))
	failed := false
	for _, verdict := range o.Validations {
		if !ids[verdict.Identifier] || seen[verdict.Identifier] {
			return errors.New("observation contains an unexpected or duplicate grader")
		}
		seen[verdict.Identifier] = true
		failed = failed || !verdict.Passed
	}
	if o.Status != "error" {
		if len(seen) != len(ids) {
			return errors.New("completed observation lacks admitted grader results")
		}
		if (o.Status == "failed") != failed {
			return errors.New("observation status contradicts grader results")
		}
	}
	return nil
}

func bindingIdentity(binding *Binding) *runneridentity.Identity {
	if binding == nil {
		return nil
	}
	return binding.Identity
}

// Failure classifies a boundary error without guessing from its message.
// Deadline expiration takes precedence over cancellation within a joined cause.
func Failure(boundary string, err error) Envelope {
	outcome := InfrastructureFailure
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		outcome = Timeout
	case errors.Is(err, context.Canceled):
		outcome = Canceled
	}
	return NewFailure(outcome, boundary, "not_checked", string(outcome), err.Error())
}

func NewFailure(outcome Outcome, boundary, validation, code, message string) Envelope {
	exit, _ := outcome.ExitCode()
	if !utf8.ValidString(message) {
		message = strings.ToValidUTF8(message, "\uFFFD") + " [invalid UTF-8 replaced in diagnostic]"
	}
	if len(message) > MaxMessageBytes {
		message = fmt.Sprintf("%.900s [diagnostic truncated; original failure detail exceeds terminal bounds]", message)
	}
	return Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: Version, Outcome: outcome, ExitCode: exit,
		FailureBoundary: boundary, Validation: validation, Diagnostics: []Diagnostic{{Code: code, Message: message}},
	}
}
