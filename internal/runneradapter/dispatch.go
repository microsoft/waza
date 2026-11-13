package runneradapter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Adapter result statuses. They classify the public reply without changing
// its semantics: the verbatim public result stays authoritative.
const (
	StatusTerminal       = "terminal"
	StatusRejected       = "rejected"
	StatusOperationError = "operation_error"
	StatusObserved       = "observed"
	StatusContractError  = "contract_error"
)

const (
	CodeContractUnavailable = "contract_unavailable"
	CodeContractViolation   = "contract_violation"

	exitInvalidInput = 6
	exitOperation    = 2
)

// Reply is one public contract response: the process exit code (or the
// X-Waza-Exit-Code header) and the emitted JSON body.
type Reply struct {
	ExitCode int
	Body     []byte
}

// Contract is the portable adapter interface over the public runner contract.
// An implementation performs exactly the given call once; it must not queue,
// place, retry or reinterpret it.
type Contract interface {
	Call(ctx context.Context, call Call) (Reply, error)
}

// Provenance is the optional verification hook outcome for a returned manifest.
type Provenance struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

var provenanceStates = []string{"verified", "failed", "unavailable"}

// ProvenanceVerifier is an optional host hook (for example a provenance
// verifier) invoked with the returned manifest digest and the verbatim public
// result. The adapter only records its answer; it never gates or retries on it.
type ProvenanceVerifier interface {
	VerifyProvenance(ctx context.Context, manifestDigest string, public json.RawMessage) Provenance
}

type PublicSummary struct {
	Kind          string `json:"kind"`
	SchemaVersion string `json:"schema_version"`
	Status        string `json:"status,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
}

type ResultCall struct {
	Command     []string  `json:"command"`
	HTTP        HTTPRoute `json:"http"`
	InputSHA256 string    `json:"input_sha256,omitempty"`
}

type Result struct {
	Kind             string          `json:"kind"`
	SchemaVersion    string          `json:"schema_version"`
	Adapter          Identity        `json:"adapter"`
	Status           string          `json:"status"`
	ExitCode         int             `json:"exit_code"`
	DescriptorSHA256 string          `json:"descriptor_sha256"`
	WorkID           string          `json:"work_id,omitempty"`
	Operation        string          `json:"operation,omitempty"`
	Dispatched       bool            `json:"dispatched"`
	Call             *ResultCall     `json:"call,omitempty"`
	Public           *PublicSummary  `json:"public,omitempty"`
	Outcome          string          `json:"outcome,omitempty"`
	EngineCalls      *int            `json:"engine_calls,omitempty"`
	ExecutionID      string          `json:"execution_id,omitempty"`
	Logical          *Logical        `json:"logical,omitempty"`
	ManifestDigest   string          `json:"manifest_digest,omitempty"`
	CheckpointSHA256 string          `json:"checkpoint_sha256,omitempty"`
	Provenance       Provenance      `json:"provenance"`
	PublicResult     json.RawMessage `json:"result,omitempty"`
	Diagnostics      []Diagnostic    `json:"diagnostics"`
}

// Dispatch performs the translated call exactly once and maps the reply.
// A rejected translation makes no call. Failures are reported, never retried.
func Dispatch(ctx context.Context, contract Contract, translation Translation, verifier ProvenanceVerifier) Result {
	result := Result{
		Kind: ResultKind, SchemaVersion: SchemaVersion, Adapter: translation.Adapter,
		DescriptorSHA256: translation.DescriptorSHA256, Provenance: Provenance{State: "not_applicable"},
		Diagnostics: []Diagnostic{},
	}
	if translation.Status != "translated" || translation.Call == nil {
		result.Status, result.ExitCode = StatusRejected, exitInvalidInput
		result.Diagnostics = slices.Clone(translation.Diagnostics)
		return result
	}
	result.WorkID, result.Operation, result.Dispatched = translation.WorkID, translation.Operation, true
	result.Call = &ResultCall{Command: translation.Call.Command, HTTP: translation.Call.HTTP, InputSHA256: translation.Call.InputSHA256}
	reply, err := contract.Call(ctx, *translation.Call)
	if err != nil {
		return result.contractError(CodeContractUnavailable, fmt.Sprintf("public contract call did not return a result; the adapter does not retry: %v", err))
	}
	result = MapReply(result, translation.Operation, reply)
	if result.ManifestDigest != "" {
		result.Provenance = Provenance{State: "not_configured"}
		if verifier != nil {
			answer := verifier.VerifyProvenance(ctx, result.ManifestDigest, result.PublicResult)
			if !slices.Contains(provenanceStates, answer.State) {
				answer = Provenance{State: "unavailable", Detail: "provenance hook returned an unknown state"}
			}
			answer.Detail = truncate(answer.Detail)
			result.Provenance = answer
		}
	}
	return result
}

func (r Result) contractError(code, message string) Result {
	r.Status, r.ExitCode = StatusContractError, exitOperation
	r.Public, r.PublicResult, r.Outcome, r.EngineCalls = nil, nil, "", nil
	r.ExecutionID, r.Logical, r.ManifestDigest, r.CheckpointSHA256 = "", nil, "", ""
	r.Diagnostics = append(r.Diagnostics, Diagnostic{Code: code, Message: truncate(message)})
	return r
}

type publicSubmission struct {
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	EngineCalls int    `json:"engine_calls"`
	Reservation *struct {
		ExecutionID string  `json:"execution_id"`
		Logical     Logical `json:"logical"`
	} `json:"reservation"`
	Completion *struct {
		ExecutionID    string `json:"execution_id"`
		ManifestDigest string `json:"manifest_digest"`
	} `json:"completion"`
	Terminal *struct {
		Outcome  string `json:"outcome"`
		ExitCode int    `json:"exit_code"`
	} `json:"terminal"`
}

// inconsistent reports cross-field contradictions the schema cannot express in
// a completed or reused submission.
func (s *publicSubmission) inconsistent() string {
	if s.Reservation != nil && s.Completion != nil && s.Reservation.ExecutionID != s.Completion.ExecutionID {
		return "completion names a different execution from its reservation"
	}
	if s.Terminal != nil && s.Terminal.ExitCode != s.ExitCode {
		return fmt.Sprintf("submission exit code %d contradicts its terminal exit code %d", s.ExitCode, s.Terminal.ExitCode)
	}
	return ""
}

type publicCheckpoint struct {
	SchemaVersion string            `json:"schema_version"`
	Status        string            `json:"status"`
	ExitCode      int               `json:"exit_code"`
	Submission    *publicSubmission `json:"submission"`
	Finalized     string            `json:"finalized_checkpoint_sha256"`
}

var expectedKinds = map[string]string{
	OperationSubmit:     "waza.execution-submission-result",
	OperationCheckpoint: "waza.runner-checkpoint-result",
	OperationResume:     "waza.runner-checkpoint-result",
	OperationStatus:     "waza.execution-lineage",
}

// statusTable maps public statuses to adapter statuses one-to-one.
var statusTable = map[string]string{
	"completed":        StatusTerminal,
	"reused":           StatusTerminal,
	"rejected":         StatusRejected,
	"submission_error": StatusOperationError,
	"checkpoint_error": StatusOperationError,
	"operation_error":  StatusOperationError,
}

// MapReply validates a public reply against its published schema and copies
// status, exit code, outcome and the returned manifest reference. The public
// JSON is retained verbatim. Contradictory or unknown replies are contract errors.
func MapReply(result Result, operation string, reply Reply) Result {
	var head PublicSummary
	if err := json.Unmarshal(reply.Body, &head); err != nil {
		return result.contractError(CodeContractViolation, fmt.Sprintf("public reply is not a JSON object: %v", err))
	}
	if head.Kind != expectedKinds[operation] {
		return result.contractError(CodeContractViolation, fmt.Sprintf("operation %q expects %q, got %q", operation, expectedKinds[operation], head.Kind))
	}
	load := lineageSchema
	switch {
	case head.Kind == expectedKinds[OperationSubmit]:
		load = submissionResultSchema
	case head.Kind == expectedKinds[OperationCheckpoint] && head.SchemaVersion == "1.1":
		load = checkpointErrorSchema
	case head.Kind == expectedKinds[OperationCheckpoint]:
		load = checkpointResultSchema
	}
	if err := validate(reply.Body, load); err != nil {
		return result.contractError(CodeContractViolation, fmt.Sprintf("public reply does not satisfy its published schema: %v", err))
	}
	result.PublicResult = json.RawMessage(reply.Body)
	result.Public = &PublicSummary{Kind: head.Kind, SchemaVersion: head.SchemaVersion, Status: head.Status, ExitCode: head.ExitCode}
	if operation == OperationStatus {
		var lineage struct {
			Logical Logical `json:"logical"`
		}
		if err := json.Unmarshal(reply.Body, &lineage); err != nil {
			return result.contractError(CodeContractViolation, err.Error())
		}
		if reply.ExitCode != 0 {
			return result.contractError(CodeContractViolation, fmt.Sprintf("lineage reply exited %d", reply.ExitCode))
		}
		result.Status, result.ExitCode, result.Logical = StatusObserved, 0, &lineage.Logical
		return result
	}
	var submission *publicSubmission
	if head.Kind == expectedKinds[OperationSubmit] {
		submission = &publicSubmission{}
		if err := json.Unmarshal(reply.Body, submission); err != nil {
			return result.contractError(CodeContractViolation, err.Error())
		}
	} else {
		var checkpoint publicCheckpoint
		if err := json.Unmarshal(reply.Body, &checkpoint); err != nil {
			return result.contractError(CodeContractViolation, err.Error())
		}
		submission, result.CheckpointSHA256 = checkpoint.Submission, checkpoint.Finalized
	}
	if head.ExitCode == nil || *head.ExitCode != reply.ExitCode {
		return result.contractError(CodeContractViolation, fmt.Sprintf("public exit code %d contradicts the emitted result", reply.ExitCode))
	}
	status, known := statusTable[head.Status]
	if !known {
		return result.contractError(CodeContractViolation, fmt.Sprintf("public status %q is not part of the contract", head.Status))
	}
	result.Status, result.ExitCode = status, reply.ExitCode
	if submission != nil && status == StatusTerminal {
		if problem := submission.inconsistent(); problem != "" {
			return result.contractError(CodeContractViolation, problem)
		}
	}
	if submission != nil {
		calls := submission.EngineCalls
		result.EngineCalls = &calls
		if submission.Reservation != nil {
			logical := submission.Reservation.Logical
			result.ExecutionID, result.Logical = submission.Reservation.ExecutionID, &logical
		}
		if submission.Completion != nil {
			result.ExecutionID, result.ManifestDigest = submission.Completion.ExecutionID, submission.Completion.ManifestDigest
		}
		if submission.Terminal != nil {
			result.Outcome = submission.Terminal.Outcome
		}
	}
	if status == StatusTerminal && (result.ManifestDigest == "" || result.Logical == nil || result.Outcome == "") {
		return result.contractError(CodeContractViolation, "terminal public result is missing its completion, reservation or terminal outcome")
	}
	return result
}

// EncodeResult validates the result against its published schema.
func EncodeResult(r Result) ([]byte, error) {
	return encodeValidated(r, resultSchema)
}

// ParseResult strictly validates and decodes an adapter result.
func ParseResult(data []byte) (*Result, error) {
	if err := validate(data, resultSchema); err != nil {
		return nil, fmt.Errorf("adapter result: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ParseTranslation strictly validates and decodes a translation document.
func ParseTranslation(data []byte) (*Translation, error) {
	if err := validate(data, translationSchema); err != nil {
		return nil, fmt.Errorf("adapter translation: %w", err)
	}
	var translation Translation
	if err := json.Unmarshal(data, &translation); err != nil {
		return nil, err
	}
	return &translation, nil
}
