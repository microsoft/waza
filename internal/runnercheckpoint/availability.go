package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	ResumeFreshRetryChild       = "fresh_retry_child"
	ResumeReuseOriginalTerminal = "reuse_original_terminal"
)

// AvailabilitySource is the minimum checkpoint declaration disclosed after
// compatibility and the capture policy admit the original source.
type AvailabilitySource struct {
	Boundary    string `json:"boundary"`
	CreatedAt   string `json:"created_at"`
	ExecutionID string `json:"execution_id"`
}

// Availability is a read-only resume preflight. It never reserves, dispatches,
// reuses or repairs, so a later resume can still fail if records change.
type Availability struct {
	Kind                     string                      `json:"kind"`
	SchemaVersion            string                      `json:"schema_version"`
	Checkpoint               string                      `json:"checkpoint_sha256,omitempty"`
	Status                   string                      `json:"status"`
	ExitCode                 int                         `json:"exit_code"`
	Source                   *AvailabilitySource         `json:"source,omitempty"`
	CompatibilityResult      string                      `json:"compatibility_result"`
	CompatibilityDifferences []string                    `json:"compatibility_differences"`
	ResumeAction             string                      `json:"resume_action,omitempty"`
	RestoredUnits            *int                        `json:"restored_units"`
	RemainingWork            *int                        `json:"remaining_work"`
	Diagnostics              []runnercontract.Diagnostic `json:"diagnostics"`
}

// availabilityGuidance holds fixed operator guidance; it never echoes captured
// input, provider locations or underlying causes.
var availabilityGuidance = map[string]string{
	"checkpoint_input_unavailable":         "The captured submission could not be read from input within the size limit. Provide one submission on stdin or in the request body.",
	"checkpoint_flags_conflict":            "Use either --store or --azure-container-url, not both.",
	"checkpoint_flags_invalid":             "The command flags are invalid. See waza runner checkpoint availability --help.",
	"checkpoint_arguments_invalid":         "Pass exactly one checkpoint digest.",
	"checkpoint_input_invalid":             "The captured submission is not valid execution-submission JSON. Recapture it; nothing was read from storage.",
	"checkpoint_digest_invalid":            "The checkpoint digest must be 64 lowercase hex characters from a create result.",
	"checkpoint_request_invalid":           "The captured request is not admissible (for example its attempt exceeds max_attempts) or could not be bound to a compatibility declaration. Correct or recapture it with this build; resume rejects it the same way.",
	"checkpoint_not_found":                 "No checkpoint blob with this digest exists in the selected store. Check --store or --azure-container-url and the digest.",
	"checkpoint_corrupt":                   "The stored checkpoint differs from its digest or is not canonical. Preserve it for investigation; do not edit or resume it.",
	"checkpoint_incompatible":              "The checkpoint was created by a different build or for different work, policy or consent. Rerun as new work or use the original build; implicit cross-version resume is not supported.",
	"checkpoint_policy_conflict":           "The captured submission policy contradicts this boundary. before-unit needs a distinct retry of the original; finalized-unit needs reuse of its exact source. Omit the policy or correct it.",
	"checkpoint_stale":                     "The original attempt was dispatched or already completed, so this before-unit checkpoint cannot safely start a retry. Use the finalized checkpoint or inspect lineage before starting separate work.",
	"checkpoint_before_source_rejected":    "The original reservation or its dispatch/completion state could not be verified. Preserve the records and inspect operator logs before any retry.",
	"checkpoint_finalized_source_rejected": "The original finalized completion, manifest or artifacts could not be verified. Preserve the records; the original terminal cannot be reused.",
	"checkpoint_capture_policy_rejected":   "The original source contains restricted identifiers or diagnostic text, so the capture policy will not reuse or disclose it.",
	"checkpoint_boundary_invalid":          "The checkpoint names an unsupported boundary.",
	"checkpoint_runtime_rejected":          "This environment would not admit the owned runtime for the retry (for example COPILOT_CLI_PATH or an ambient custom provider is set). Clear those settings and check again, or use the legacy runner.",
	ErrorCodeBuildUnavailable:              "This build has no verifiable runner revision, so compatibility cannot be checked. Use a release or VCS-stamped build.",
	ErrorCodeRunnerUnavailable:             "The runner service is unavailable.",
	ErrorCodeStorageUnavailable:            "The immutable store could not be opened or read. Check the store location, permissions and credentials, then retry the check.",
	ErrorCodeStorageCloseFailed:            "The immutable store did not close cleanly; discard this availability answer and retry.",
	"checkpoint_availability_interrupted":  "The availability check was canceled or timed out before it finished. Retry it.",
}

func newAvailability(digest string) Availability {
	return Availability{
		Kind: "waza.runner-checkpoint-availability", SchemaVersion: "1.0", Checkpoint: digest,
		Status: "rejected", ExitCode: 6, CompatibilityResult: "unavailable",
		CompatibilityDifferences: []string{}, Diagnostics: []runnercontract.Diagnostic{},
	}
}

func (a Availability) with(status, code string) Availability {
	a.Status = status
	a.ExitCode = map[string]int{"available": 0, "operation_error": 2}[status]
	if status == "unavailable" || status == "rejected" {
		a.ExitCode = 6
	}
	if status == "rejected" || status == "operation_error" {
		a.Source, a.ResumeAction, a.CompatibilityResult = nil, "", "unavailable"
		a.CompatibilityDifferences = []string{}
	}
	a.RestoredUnits, a.RemainingWork = nil, nil
	message, ok := availabilityGuidance[code]
	if !ok {
		message = "The availability request was not admitted. Check the request transport and operator logs, then retry."
	}
	a.Diagnostics = append(a.Diagnostics, runnercontract.Diagnostic{Code: code, Message: message})
	return a
}

// AvailabilityRejected reports invalid caller input before storage is opened.
func AvailabilityRejected(digest, code string) Availability {
	if executioncheckpoint.ValidateDigest(digest) != nil {
		digest = ""
	}
	return newAvailability(digest).with("rejected", code)
}

// AvailabilityOperationError reports infrastructure failure; it is not a
// statement that the checkpoint is unavailable.
func AvailabilityOperationError(digest, code string) Availability {
	if executioncheckpoint.ValidateDigest(digest) != nil {
		digest = ""
	}
	return newAvailability(digest).with("operation_error", code)
}

// Availability applies the same admission, compatibility, source and capture
// checks as ResumeCaptured without creating any record or engine call.
func (s *Service) Availability(ctx context.Context, data []byte, backend artifactmanifest.Backend, digest string) (Availability, error) {
	if err := executioncheckpoint.ValidateDigest(digest); err != nil {
		return AvailabilityRejected("", "checkpoint_digest_invalid"), err
	}
	result := newAvailability(digest)
	// Input causes can quote captured input, so only a fixed error leaves here.
	request, err := runnerunit.ParseSubmission(bytes.Clone(data))
	if err != nil {
		return result.with("rejected", "checkpoint_input_invalid"), ErrAvailabilityInput
	}
	caller, ok := runnerunit.AdmittedBinding(request.Request)
	if !ok {
		return result.with("rejected", "checkpoint_request_invalid"), ErrAvailabilityInput
	}
	declaration, err := executioncheckpoint.DeclareRequestWithContext(ctx, request.Request, s.revision)
	if err != nil {
		if interrupted(ctx, err) {
			return result.with("operation_error", "checkpoint_availability_interrupted"), err
		}
		return result.with("rejected", "checkpoint_request_invalid"), ErrAvailabilityInput
	}
	current := declaration.Compatibility()
	checkpoint, err := executioncheckpoint.Load(ctx, backend, digest)
	switch {
	case err == nil:
	case interrupted(ctx, err):
		return result.with("operation_error", "checkpoint_availability_interrupted"), err
	case providerFailure(err):
		return result.with("operation_error", ErrorCodeStorageUnavailable), err
	case errors.Is(err, artifactmanifest.ErrMissing):
		return result.with("unavailable", "checkpoint_not_found"), err
	case errors.Is(err, artifactmanifest.ErrCorrupt):
		return result.with("unavailable", "checkpoint_corrupt"), err
	default:
		return result.with("operation_error", ErrorCodeStorageUnavailable), err
	}
	if err := executioncheckpoint.ValidateCompatibility(*checkpoint, current); err != nil {
		result.CompatibilityResult = "incompatible"
		if mismatch, ok := errors.AsType[*executioncheckpoint.CompatibilityError](err); ok {
			result.CompatibilityDifferences = append(result.CompatibilityDifferences, mismatch.Fields...)
		}
		return result.with("unavailable", "checkpoint_incompatible"), err
	}
	result.CompatibilityResult = "compatible"
	ledger, err := executionledger.New(backend)
	if err != nil {
		return result.with("operation_error", ErrorCodeStorageUnavailable), err
	}
	restored, remaining := 0, 1
	switch checkpoint.Boundary {
	case executioncheckpoint.BeforeUnit:
		reserved, err := executioncheckpoint.ReadBefore(ctx, ledger, *checkpoint, current)
		switch {
		case err == nil:
		case interrupted(ctx, err):
			return result.with("operation_error", "checkpoint_availability_interrupted"), err
		case errors.Is(err, executioncheckpoint.ErrStale):
			return result.with("unavailable", "checkpoint_stale"), err
		case operational(err):
			return result.with("operation_error", ErrorCodeStorageUnavailable), err
		default:
			return result.with("unavailable", "checkpoint_before_source_rejected"), err
		}
		parent := executionattempt.Parent{ExecutionID: checkpoint.ExecutionID, Relationship: "retry"}
		if request.Policy != nil && (request.Policy.Mode != "distinct" ||
			(request.Policy.Parent != nil && *request.Policy.Parent != parent)) {
			return result.with("unavailable", "checkpoint_policy_conflict"), ErrPolicy
		}
		// Resume admits the caller's runtime and reserves the retry child
		// through the same ancestry admission.
		if err := execution.ValidateOwnedRuntime(caller.ExecutionMode); err != nil {
			return result.with("unavailable", "checkpoint_runtime_rejected"), err
		}
		if err := ledger.AdmitParent(ctx, reserved.Attempt.Binding, parent); err != nil {
			switch {
			case interrupted(ctx, err):
				return result.with("operation_error", "checkpoint_availability_interrupted"), err
			case operational(err):
				return result.with("operation_error", ErrorCodeStorageUnavailable), err
			default:
				return result.with("unavailable", "checkpoint_before_source_rejected"), err
			}
		}
		// The retry child persists the caller's binding, so admit both.
		for _, binding := range []artifactmanifest.Binding{reserved.Attempt.Binding, caller} {
			if err := evidencestate.ValidateCaptureBinding(binding); err != nil {
				return result.with("unavailable", "checkpoint_capture_policy_rejected"), err
			}
		}
		result.ResumeAction = ResumeFreshRetryChild
	case executioncheckpoint.FinalizedUnit:
		if _, err := executioncheckpoint.ReadStoredFinalized(ctx, ledger, *checkpoint, current); err != nil {
			return result.with(finalizedSourceFailure(ctx, err)), err
		}
		source := executionattempt.Source{ExecutionID: checkpoint.ExecutionID, ManifestDigest: checkpoint.ManifestDigest}
		if request.Policy != nil && (request.Policy.Mode != "reuse" || request.Policy.Source == nil ||
			*request.Policy.Source != source) {
			return result.with("unavailable", "checkpoint_policy_conflict"), ErrPolicy
		}
		// Resume admits the same terminal and report.json before reusing them.
		original, _, stage, err := runnerunit.VerifyReuseSource(ctx, ledger, source)
		if err != nil {
			if stage != "source_unavailable" && !interrupted(ctx, err) {
				return result.with("unavailable", "checkpoint_finalized_source_rejected"), err
			}
			return result.with(finalizedSourceFailure(ctx, err)), err
		}
		// The source is reread, so bind the exact bytes used below to the
		// checkpoint as Resume's ReuseAdmission does.
		if err := executioncheckpoint.ValidateContext(*checkpoint, original.Reservation.Attempt.Binding,
			original.Reservation.Bytes, original.CompletionBytes, original.Publication.ManifestBytes); err != nil {
			return result.with("unavailable", "checkpoint_finalized_source_rejected"), err
		}
		// Resume's reuse decision admits the caller against the source binding.
		if err := executionledger.ValidateReuseCaller(caller, original.Reservation.Attempt.Binding); err != nil {
			return result.with("unavailable", "checkpoint_finalized_source_rejected"), err
		}
		if err := ownedsubmission.ValidateSourceDisclosure(original); err != nil {
			return result.with("unavailable", "checkpoint_capture_policy_rejected"), err
		}
		result.ResumeAction = ResumeReuseOriginalTerminal
		restored, remaining = 1, 0
	default:
		return result.with("unavailable", "checkpoint_boundary_invalid"), fmt.Errorf("%w: unsupported checkpoint boundary", executioncheckpoint.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		result.ResumeAction = ""
		return result.with("operation_error", "checkpoint_availability_interrupted"), err
	}
	result.Status, result.ExitCode = "available", 0
	result.Source = &AvailabilitySource{Boundary: checkpoint.Boundary, CreatedAt: checkpoint.CreatedAt, ExecutionID: checkpoint.ExecutionID}
	result.RestoredUnits, result.RemainingWork = &restored, &remaining
	return result, nil
}

// ErrAvailabilityInput replaces input admission causes, which may quote the
// captured submission, before they can reach operator logs.
var ErrAvailabilityInput = errors.New("captured submission was not admitted for checkpoint availability")

func finalizedSourceFailure(ctx context.Context, err error) (string, string) {
	switch {
	case interrupted(ctx, err):
		return "operation_error", "checkpoint_availability_interrupted"
	case operational(err):
		return "operation_error", ErrorCodeStorageUnavailable
	default:
		return "unavailable", "checkpoint_finalized_source_rejected"
	}
}

// operational separates provider read failures from stale, missing or invalid
// source evidence. Evidence sentinels may wrap a provider failure, so a
// filesystem, network or Azure response cause always wins.
func operational(err error) bool {
	if providerFailure(err) {
		return true
	}
	for _, evidence := range []error{
		artifactmanifest.ErrMissing, artifactmanifest.ErrCorrupt, artifactmanifest.ErrConflict, artifactmanifest.ErrInvalid,
		executionattempt.ErrInvalid, executionledger.ErrInvalid, executionledger.ErrContext,
		executioncheckpoint.ErrInvalid, executioncheckpoint.ErrContext, executioncheckpoint.ErrIncompatible, executioncheckpoint.ErrStale,
	} {
		if errors.Is(err, evidence) {
			return false
		}
	}
	return true
}

// providerFailure recognizes filesystem, network, Azure response and
// truncated-transfer causes, which may be joined with an evidence sentinel.
func providerFailure(err error) bool {
	if _, ok := errors.AsType[*fs.PathError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return true
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}

func interrupted(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

var availabilitySchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"runner-checkpoint-availability-1.0.schema.json": schemas.RunnerCheckpointAvailabilitySchemaJSON,
		"execution-attempt-1.0.schema.json":              schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":              schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":                schemas.RunnerTerminalSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading availability schema %s: %w", name, err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, fmt.Errorf("registering availability schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/runner-checkpoint-availability-1.0.schema.json")
})

// EncodeAvailability validates the wire against its published schema.
func EncodeAvailability(result Availability) ([]byte, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encoding checkpoint availability: %w", err)
	}
	if _, err := ParseAvailability(data); err != nil {
		return nil, err
	}
	return data, nil
}

func ParseAvailability(data []byte) (*Availability, error) {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing checkpoint availability: %w", err)
	}
	schema, err := availabilitySchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("checkpoint availability schema: %w", err)
	}
	var result Availability
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decoding checkpoint availability: %w", err)
	}
	return &result, nil
}
