package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// SubmissionHooks are embedding-host callbacks, never caller JSON. Reserved
// runs before factory construction; Terminal runs after joined lifecycle.
// Neither callback implies cancellation acknowledgment or process-wait evidence.
type SubmissionHooks struct {
	Admission           func(context.Context, artifactmanifest.Binding) error
	ReuseAdmission      func(context.Context, *executionledger.CompletedExecution) error
	PreparePublication  func(context.Context, *executionledger.Ledger, executionledger.Creator, SubmissionSnapshot) (SubmissionPublication, error)
	ValidatePublication func(context.Context, SubmissionSnapshot, SubmissionPublication) error
	Factory             EngineFactory
	Reserved            func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error
	Terminal            func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation, runnerterminal.Envelope) error
	Artifacts           func(context.Context, *executionledger.Ledger, executionledger.Creator, SubmissionSnapshot) ([]SubmissionArtifact, error)
}

// SubmissionSnapshot carries detached original evidence after lifecycle return.
// Added artifact bytes share its finalized manifest, not an attestation claim.
type SubmissionSnapshot struct {
	Reservation    executionledger.Reservation
	Terminal       runnerterminal.Envelope
	Report         json.RawMessage
	ObservedReport Report
}

type SubmissionArtifact struct {
	Name      string
	MediaType string
	Data      []byte
}

type SubmissionPublication struct {
	Terminal  runnerterminal.Envelope
	Artifacts []SubmissionArtifact
}

const MaxSubmissionExtraArtifacts = 16

type Submission struct {
	Kind          string                      `json:"kind"`
	SchemaVersion string                      `json:"schema_version"`
	Status        string                      `json:"status"`
	ExitCode      int                         `json:"exit_code"`
	EngineCalls   int                         `json:"engine_calls"`
	Caller        *runneridentity.Identity    `json:"caller,omitempty"`
	Decision      *executionattempt.Decision  `json:"decision,omitempty"`
	Reservation   *executionattempt.Attempt   `json:"reservation,omitempty"`
	Completion    *executionledger.Completion `json:"completion,omitempty"`
	Terminal      *runnerterminal.Envelope    `json:"terminal,omitempty"`
	Report        json.RawMessage             `json:"report,omitempty"`
	Diagnostics   []runnercontract.Diagnostic `json:"diagnostics"`
}

func (s *Submission) fail(code string, err error) {
	s.Status, s.ExitCode = "submission_error", 2
	message := "Submission did not complete this phase; inspect operator diagnostics and preserve existing records."
	if errors.Is(err, execution.ErrOwnedRuntime) {
		message = "Owned native execution rejects ambient custom providers and COPILOT_CLI_PATH overrides; clear them or use the legacy runner."
	}
	// Provider errors remain operator diagnostics, not paths/URLs in the wire.
	s.Diagnostics = append(s.Diagnostics, runnercontract.Diagnostic{Code: code, Message: message})
}

func reportBytes(report Report) ([]byte, error) { return json.Marshal(report) }

var runnerUnitReportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSubmissionSchema("runner-unit-1.0.schema.json")
})

func validateSourceReport(data []byte, source *executionledger.CompletedExecution, terminal *runnerterminal.Envelope) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	schema, err := runnerUnitReportSchema()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("source report schema: %w", err)
	}
	report, err := decodeSubmissionReport(data)
	if err != nil {
		return err
	}
	canonical, err := reportBytes(report)
	if err != nil {
		return err
	}
	binding := source.Reservation.Attempt.Binding
	if !bytes.Equal(canonical, data) || report.Kind != "waza.runner-unit" || report.Version != "1.0" ||
		report.Identity == nil || *report.Identity != binding.Identity ||
		report.Requested == nil || *report.Requested != binding.Selection ||
		report.Resolved == nil || *report.Resolved != binding.Selection ||
		report.Validation != "valid" || report.Compatibility.Status != "compatible" || report.AdjacentWork ||
		report.Calls != terminal.EngineCalls || (report.Observation == nil) != (terminal.Observation == nil) {
		return fmt.Errorf("source report contradicts original terminal/binding or canonical writer")
	}
	switch report.Status {
	case "completed":
		if terminal.Outcome != runnerterminal.Success && terminal.Outcome != runnerterminal.EvaluationFailure &&
			(terminal.Outcome != runnerterminal.InfrastructureFailure || terminal.FailureBoundary != "terminalization") {
			return fmt.Errorf("completed source report contradicts original terminal outcome")
		}
	case "execution_error":
		if terminal.Outcome != runnerterminal.InfrastructureFailure && terminal.Outcome != runnerterminal.Canceled &&
			terminal.Outcome != runnerterminal.Timeout {
			return fmt.Errorf("failed source report contradicts original terminal outcome")
		}
	default:
		return fmt.Errorf("source report has no admitted terminal execution status")
	}
	// Pre-runtime failures may omit the mode; any present mode must match.
	if (report.Mode != "" || report.Observation != nil) && report.Mode != binding.ExecutionMode {
		return fmt.Errorf("source report execution mode contradicts original reservation")
	}
	if report.Observation != nil && (report.Observation.RunNumber != terminal.Observation.RunNumber ||
		report.Observation.Attempts != terminal.Observation.Attempts ||
		string(report.Observation.Status) != terminal.Observation.Status ||
		report.Observation.ErrorMsg != terminal.Observation.ErrorMsg ||
		len(report.Observation.Validations) != len(terminal.Observation.Validations)) {
		return fmt.Errorf("source observation contradicts original native terminal")
	}
	if report.Observation != nil {
		for _, verdict := range terminal.Observation.Validations {
			result, found := report.Observation.Validations[verdict.Identifier]
			if !found || result.Name != verdict.Identifier || result.Passed != verdict.Passed {
				return fmt.Errorf("source grader result contradicts original native terminal")
			}
		}
	}
	return nil
}

// Submit is additive: legacy Run/RunTerminal and request schemas are unchanged.
// Backend is operator-configured, never selected by submission JSON.
func (s *Service) Submit(ctx context.Context, data []byte, backend artifactmanifest.Backend, hooks SubmissionHooks) (result Submission, failure error) {
	result = Submission{Kind: "waza.execution-submission-result", SchemaVersion: "1.0",
		Status: "rejected", ExitCode: 6, Diagnostics: []runnercontract.Diagnostic{}}
	if hooks.ValidatePublication != nil && hooks.PreparePublication == nil {
		err := fmt.Errorf("publication content validation requires a preparation policy")
		result.fail("publication_policy_invalid", err)
		return result, err
	}
	request, err := ParseSubmission(data)
	if err != nil {
		result.Diagnostics = []runnercontract.Diagnostic{{
			Code: "invalid_submission", Message: "Submission wrapper is invalid; compare the original input with the execution-submission schema.",
		}}
		return result, err
	}
	report, input := admit(request.Request)
	if input == nil {
		envelope, _ := terminal(report)
		result.Terminal, result.ExitCode = &envelope, envelope.ExitCode
		return result, fmt.Errorf("execution submission request was rejected")
	}
	result.Caller = report.Identity
	ctx, stop := context.WithTimeout(ctx, time.Duration(input.request.TimeoutMS)*time.Millisecond)
	defer stop()
	binding := artifactmanifest.Binding{Identity: *report.binding.Identity, Selection: report.binding.Selection,
		ExecutionMode: report.binding.ExecutionMode, GraderIDs: report.binding.GraderIDs}
	if hooks.Admission != nil {
		callbackBinding := binding
		callbackBinding.GraderIDs = slices.Clone(binding.GraderIDs)
		if err := hooks.Admission(ctx, callbackBinding); err != nil {
			result.fail("capture_admission_failed", err)
			return result, err
		}
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		result.fail("ledger_unavailable", err)
		return result, err
	}
	policy := SubmissionPolicy{Mode: "distinct"}
	if request.Policy != nil {
		policy = *request.Policy
	}
	if policy.Mode == "reuse" {
		// First verify the actual source. Optional full report is captured only
		// when declared; missing historical billing remains unavailable.
		source, err := ledger.ReadCompleted(ctx, *policy.Source)
		if err != nil {
			result.fail("source_unavailable", err)
			return result, err
		}
		names := []string{source.Publication.Manifest.TerminalArtifact}
		for _, artifact := range source.Publication.Manifest.Artifacts {
			if artifact.Name == "report.json" && artifact.Name != source.Publication.Manifest.TerminalArtifact {
				names = append(names, artifact.Name)
			}
		}
		// Verification and report admission precede the actionable decision.
		source, err = ledger.ReadCompleted(ctx, *policy.Source, names...)
		if err != nil {
			result.fail("source_unavailable", err)
			return result, err
		}
		envelope, err := runnerterminal.Validate(source.Publication.ArtifactBytes[source.Publication.Manifest.TerminalArtifact],
			source.Reservation.Attempt.Binding.TerminalBinding())
		if err != nil {
			result.fail("source_terminal_invalid", err)
			return result, err
		}
		if raw, found := source.Publication.ArtifactBytes["report.json"]; found && source.Publication.Manifest.TerminalArtifact != "report.json" {
			if err := validateSourceReport(raw, source, envelope); err != nil {
				result.fail("source_report_invalid", err)
				return result, err
			}
		}
		reservationBytes, completionBytes := bytes.Clone(source.Reservation.Bytes), bytes.Clone(source.CompletionBytes)
		if hooks.ReuseAdmission != nil {
			if err := hooks.ReuseAdmission(ctx, source); err != nil {
				result.fail("source_disclosure_denied", err)
				return result, err
			}
		}
		decision, verified, err := ledger.ReusePinned(ctx, binding, *policy.Source, reservationBytes, completionBytes, names...)
		if err != nil {
			result.fail("reuse_not_admitted", err)
			return result, err
		}
		// The decision rechecks current source bytes. Do not mix earlier reads.
		envelope, err = runnerterminal.Validate(verified.Publication.ArtifactBytes[verified.Publication.Manifest.TerminalArtifact],
			verified.Reservation.Attempt.Binding.TerminalBinding())
		if err != nil {
			result.fail("source_terminal_invalid", err)
			return result, err
		}
		result.Status, result.ExitCode = "reused", envelope.ExitCode
		result.Decision, result.Completion, result.Terminal = decision, &verified.Completion, envelope
		result.Reservation = &verified.Reservation.Attempt
		return result, nil
	}
	creator, err := ledger.Reserve(ctx, binding, policy.Parent)
	if err != nil {
		result.fail("reservation_failed", err)
		return result, err
	}
	reserved, err := ledger.Reservation(creator)
	if err != nil {
		result.fail("creator_invalid", err)
		return result, err
	}
	result.Reservation = &reserved.Attempt
	if hooks.Reserved != nil {
		callbackReservation, err := ledger.Reservation(creator)
		if err != nil {
			result.fail("creator_invalid", err)
			return result, err
		}
		if err := hooks.Reserved(ctx, ledger, creator, *callbackReservation); err != nil {
			result.fail("reserved_observer_failed", err)
			return result, err
		}
	}
	controlContext, controlStop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer controlStop()
	if err := ledger.ValidateCreator(controlContext, creator); err != nil {
		result.fail("creator_invalid", err)
		return result, err
	}
	if ctx.Err() == nil {
		if err := execution.ValidateOwnedRuntime(binding.ExecutionMode); err != nil {
			result.fail("owned_runtime_rejected", err)
			return result, err
		}
	}
	decision, err := ledger.Distinct(controlContext, creator)
	if err != nil {
		result.fail("decision_failed", err)
		return result, err
	}
	result.Decision = decision
	executor := s
	if hooks.Factory != nil {
		executor = NewService(hooks.Factory)
	}
	report = executor.executeAdmitted(ctx, report, input, false)
	envelope, terminalBinding := terminal(report)
	result.Terminal, result.EngineCalls = &envelope, report.Calls
	// Lifecycle is already joined. Bounded publication may retain a canceled
	// terminal without treating cancellation as successful execution.
	publicationContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	terminalData, err := runnerterminal.Encode(envelope, terminalBinding)
	if err != nil {
		result.fail("terminal_encoding_failed", err)
		return result, err
	}
	var fullReport []byte
	if hooks.PreparePublication != nil || hooks.Artifacts != nil {
		fullReport, err = reportBytes(report)
		if err != nil {
			result.fail("report_encoding_failed", err)
			return result, err
		}
	}
	preparedArtifacts := []SubmissionArtifact{}
	if hooks.PreparePublication != nil {
		snapshot, err := submissionSnapshot(ledger, creator, report, fullReport, terminalData, terminalBinding)
		if err != nil {
			result.Terminal = nil
			result.fail("report_snapshot_failed", err)
			return result, err
		}
		prepared, err := hooks.PreparePublication(publicationContext, ledger, creator, snapshot)
		if err != nil {
			result.Terminal = nil
			result.fail("publication_policy_failed", err)
			return result, err
		}
		projected, err := validatePublicationTerminal(envelope, prepared.Terminal, terminalBinding)
		if err != nil {
			result.Terminal = nil
			result.fail("publication_terminal_invalid", err)
			return result, err
		}
		if hooks.ValidatePublication != nil {
			originalSnapshot, err := submissionSnapshot(ledger, creator, report, fullReport, terminalData, terminalBinding)
			if err != nil {
				result.Terminal = nil
				result.fail("report_snapshot_failed", err)
				return result, err
			}
			if err := hooks.ValidatePublication(publicationContext, originalSnapshot, prepared); err != nil {
				result.Terminal = nil
				result.fail("publication_content_invalid", err)
				return result, err
			}
		}
		envelope = *projected
		result.Terminal = &envelope
		terminalData, err = runnerterminal.Encode(envelope, terminalBinding)
		if err != nil {
			result.Terminal = nil
			result.fail("terminal_encoding_failed", err)
			return result, err
		}
		preparedArtifacts = prepared.Artifacts
	}
	if hooks.Terminal != nil {
		callbackReservation, err := ledger.Reservation(creator)
		if err != nil {
			result.fail("creator_invalid", err)
			return result, err
		}
		callbackTerminal, err := runnerterminal.Validate(terminalData, terminalBinding)
		if err != nil {
			result.fail("terminal_encoding_failed", err)
			return result, err
		}
		if err := hooks.Terminal(publicationContext, ledger, creator, *callbackReservation, *callbackTerminal); err != nil {
			result.fail("terminal_observer_failed", err)
			return result, err
		}
	}
	objects := []SubmissionArtifact{
		{Name: "terminal.json", MediaType: "application/json", Data: terminalData},
		{Name: "execution-attempt.json", MediaType: "application/json", Data: bytes.Clone(reserved.Bytes)},
	}
	extra := preparedArtifacts
	if hooks.Artifacts != nil {
		callbackReservation, err := ledger.Reservation(creator)
		if err != nil {
			result.fail("creator_invalid", err)
			return result, err
		}
		callbackTerminal, err := runnerterminal.Validate(terminalData, terminalBinding)
		if err != nil {
			result.fail("terminal_encoding_failed", err)
			return result, err
		}
		observedReport, err := cloneSubmissionReport(report, fullReport)
		if err != nil {
			result.fail("report_snapshot_failed", err)
			return result, err
		}
		additional, err := hooks.Artifacts(publicationContext, ledger, creator, SubmissionSnapshot{
			Reservation: *callbackReservation, Terminal: *callbackTerminal, Report: bytes.Clone(fullReport),
			ObservedReport: observedReport,
		})
		if err != nil {
			result.fail("artifact_observer_failed", err)
			return result, err
		}
		extra = append(extra, additional...)
	}
	{
		if len(extra) > MaxSubmissionExtraArtifacts {
			err := fmt.Errorf("submission artifact observer exceeds %d extra artifacts", MaxSubmissionExtraArtifacts)
			result.fail("artifact_observer_invalid", err)
			return result, err
		}
		totalBytes := int64(len(terminalData) + len(reserved.Bytes))
		for _, artifact := range extra {
			if artifact.Name == "execution-attempt.json" {
				if artifact.MediaType == "application/json" && bytes.Equal(artifact.Data, reserved.Bytes) {
					continue
				}
				err := fmt.Errorf("publication artifacts contradict original execution reservation")
				result.fail("artifact_observer_invalid", err)
				return result, err
			}
			if artifact.Name == "terminal.json" {
				if artifact.MediaType == "application/json" && bytes.Equal(artifact.Data, terminalData) {
					continue
				}
				err := fmt.Errorf("publication artifacts contradict protected original terminal")
				result.fail("artifact_observer_invalid", err)
				return result, err
			}
			if artifact.Name == "report.json" {
				err := fmt.Errorf("raw report publication requires category-scoped capture instead")
				result.fail("artifact_observer_invalid", err)
				return result, err
			}
			totalBytes += int64(len(artifact.Data))
			if len(artifact.Data) > artifactmanifest.MaxArtifactBytes || totalBytes > artifactmanifest.MaxTotalBytes {
				err := fmt.Errorf("submission artifact observer exceeds artifact byte bounds")
				result.fail("artifact_observer_invalid", err)
				return result, err
			}
			artifact.Data = bytes.Clone(artifact.Data)
			objects = append(objects, artifact)
		}
	}
	manifest := artifactmanifest.Manifest{Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: binding, TerminalArtifact: "terminal.json", Artifacts: []artifactmanifest.Artifact{}}
	store := artifactmanifest.NewStore(backend)
	for _, object := range objects {
		artifact := artifactmanifest.Artifact{Name: object.Name, SHA256: artifactmanifest.Hash(object.Data),
			SizeBytes: int64(len(object.Data)), MediaType: object.MediaType}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	manifestData, err := artifactmanifest.Encode(manifest)
	if err != nil {
		result.fail("manifest_encoding_failed", err)
		return result, err
	}
	for i, object := range objects {
		if err := store.Put(publicationContext, manifest.Artifacts[i], object.Data); err != nil {
			result.fail("artifact_publication_failed", err)
			return result, err
		}
	}
	digest, err := store.Create(publicationContext, manifestData)
	if err != nil {
		result.fail("manifest_publication_failed", err)
		return result, err
	}
	if _, err := store.Finalize(publicationContext, digest); err != nil {
		result.fail("manifest_finalization_failed", err)
		return result, err
	}
	completed, err := ledger.Complete(publicationContext, creator, digest, "terminal.json")
	if err != nil {
		result.fail("completion_failed", err)
		return result, err
	}
	result.Status, result.ExitCode, result.Completion = "completed", envelope.ExitCode, &completed.Completion
	return result, nil
}
