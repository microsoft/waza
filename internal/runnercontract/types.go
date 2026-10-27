// Package runnercontract implements the versioned, scheduler-neutral single-unit
// runner boundary. It does not own scheduling, retries, or durable publication.
package runnercontract

import "github.com/microsoft/waza/internal/models"

const (
	RequestVersion  = "1.0"
	ResultVersion   = "1.0"
	IdentityVersion = "sha256-json-v1"
	MaxRequestBytes = 4 << 20
	MaxResultBytes  = 16 << 20
)

type Selection struct {
	Shard   string `json:"shard"`
	Task    string `json:"task"`
	Trial   int    `json:"trial"`
	Attempt int    `json:"attempt"`
}

// Input is a captured source bundle, never a set of paths to discover at runtime.
// The first profile accepts inline inputs only, so HTTP cannot read host files.
type Input struct {
	Shard string `json:"shard"`
	Eval  string `json:"eval"`
	Task  string `json:"task"`
}

type Request struct {
	SchemaVersion        string    `json:"schema_version"`
	Versions             []string  `json:"versions"`
	RequiredCapabilities []string  `json:"required_capabilities"`
	OptionalCapabilities []string  `json:"optional_capabilities"`
	RunKey               string    `json:"run_key"`
	Selection            Selection `json:"selection"`
	Input                Input     `json:"input"`
	TimeoutMS            int       `json:"timeout_ms"`
	AllowPaidExecution   bool      `json:"allow_paid_execution"`
}

type Negotiation struct {
	Version              string   `json:"version,omitempty"`
	RequestedVersions    []string `json:"requested_versions"`
	RequiredCapabilities []string `json:"required_capabilities"`
	OptionalCapabilities []string `json:"optional_capabilities"`
	Available            []string `json:"available_capabilities"`
	Selected             []string `json:"selected_capabilities"`
	UnavailableOptional  []string `json:"unavailable_optional_capabilities"`
}

type Identity struct {
	Version         string `json:"version"`
	CanonicalDigest string `json:"canonical_digest"`
	Run             string `json:"run"`
	Shard           string `json:"shard"`
	Task            string `json:"task"`
	Trial           string `json:"trial"`
	Attempt         string `json:"attempt"`
}

type Outcome string

const (
	Success               Outcome = "success"
	EvaluationFailure     Outcome = "evaluation_failure"
	InfrastructureFailure Outcome = "infrastructure_failure"
	Canceled              Outcome = "canceled"
	Timeout               Outcome = "timeout"
	UnsupportedCapability Outcome = "unsupported_capability"
	InvalidRequest        Outcome = "invalid_request"
)

func (o Outcome) ExitCode() int {
	switch o {
	case Success:
		return 0
	case EvaluationFailure:
		return 1
	case Canceled:
		return 3
	case Timeout:
		return 4
	case UnsupportedCapability:
		return 5
	case InvalidRequest:
		return 6
	default:
		return 2
	}
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Result is independent of historical evaluation result schema versions.
// Nil selection/identity/observation means unavailable, not an empty success.
type Result struct {
	Kind           string            `json:"kind"`
	SchemaVersion  string            `json:"schema_version"`
	Outcome        Outcome           `json:"outcome"`
	ExitCode       int               `json:"exit_code"`
	Boundary       string            `json:"failure_boundary"`
	Negotiation    Negotiation       `json:"negotiation"`
	Requested      *Selection        `json:"requested,omitempty"`
	Resolved       *Selection        `json:"resolved,omitempty"`
	SelectionValid bool              `json:"selection_valid"`
	AdjacentWork   bool              `json:"adjacent_work_selected"`
	Identity       *Identity         `json:"identity,omitempty"`
	Observation    *models.RunResult `json:"observation,omitempty"`
	Diagnostics    []Diagnostic      `json:"diagnostics"`
	EnvelopeValid  bool              `json:"envelope_valid"`
}

func Capabilities() []string {
	return []string{"exact-unit", "identity.sha256-json-v1", "inline.mock-text", "inline.native-text", "terminal-result"}
}

func baseResult() Result {
	return Result{
		Kind:          "waza.runner-result",
		SchemaVersion: ResultVersion, Outcome: InvalidRequest, ExitCode: InvalidRequest.ExitCode(),
		Boundary: "admission", EnvelopeValid: true, Diagnostics: []Diagnostic{},
		Negotiation: Negotiation{
			RequestedVersions: []string{}, RequiredCapabilities: []string{}, OptionalCapabilities: []string{},
			Available: Capabilities(), Selected: []string{}, UnavailableOptional: []string{},
		},
	}
}

func (r *Result) fail(outcome Outcome, boundary, code, message string) {
	r.Outcome, r.ExitCode, r.Boundary = outcome, outcome.ExitCode(), boundary
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: code, Message: message})
}
