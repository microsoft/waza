// Package runnersubmission is the public protected entry point for generic
// owned submissions. It composes the evidence owner's capture policy with
// runnerunit.Service.Submit and projects only capture-approved fields.
package runnersubmission

import (
	"context"
	"fmt"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

const DiagnosticMessage = "Submission did not complete. Preserve existing records and inspect operator diagnostics. Unapproved source and invocation text are withheld."

// Public diagnostic codes for failures outside runnerunit.Submit.
const (
	CodeInputUnavailable   = "submission_input_unavailable"
	CodeInputInvalid       = "submission_input_invalid"
	CodeFlagsInvalid       = "submission_flags_invalid"
	CodeStorageUnavailable = "storage_unavailable"
	CodeStorageClose       = "storage_close_failed"
	CodeCapturePolicy      = "capture_policy_invalid"
)

func base(status string, exitCode int, code string) runnerunit.Submission {
	return runnerunit.Submission{Kind: "waza.execution-submission-result", SchemaVersion: "1.0",
		Status: status, ExitCode: exitCode,
		Diagnostics: []runnercontract.Diagnostic{{Code: code, Message: DiagnosticMessage}}}
}

// Rejected reports invalid public input without echoing it.
func Rejected(code string) runnerunit.Submission { return base("rejected", 6, code) }

// OperationError reports a host or storage failure before admission. Operator
// causes must be logged separately and are never placed in the public record.
func OperationError(code string) runnerunit.Submission { return base("submission_error", 2, code) }

// SubmitCaptured runs one distinct or reuse submission against the operator
// backend. Distinct submissions always reserve a fresh execution ID; reuse
// admits only a verified finalized source and makes zero engine calls.
func SubmitCaptured(ctx context.Context, runner *runnerunit.Service, data []byte, backend artifactmanifest.Backend) (runnerunit.Submission, error) {
	if runner == nil {
		return OperationError(CodeCapturePolicy), fmt.Errorf("submission requires the owning runner service")
	}
	hooks, err := ownedsubmission.CaptureHooks(runnerunit.SubmissionHooks{})
	if err != nil {
		return OperationError(CodeCapturePolicy), err
	}
	approved, metadataAdmitted := false, false
	admission := hooks.Admission
	hooks.Admission = func(ctx context.Context, binding artifactmanifest.Binding) error {
		if err := admission(ctx, binding); err != nil {
			return err
		}
		metadataAdmitted = true
		return nil
	}
	validate := hooks.ValidatePublication
	hooks.ValidatePublication = func(ctx context.Context, original runnerunit.SubmissionSnapshot, publication runnerunit.SubmissionPublication) error {
		if err := validate(ctx, original, publication); err != nil {
			return err
		}
		approved = true
		return nil
	}
	reuse := hooks.ReuseAdmission
	hooks.ReuseAdmission = func(ctx context.Context, source *executionledger.CompletedExecution) error {
		if err := reuse(ctx, source); err != nil {
			return err
		}
		approved = true
		return nil
	}
	result, err := runner.Submit(ctx, data, backend, hooks)
	return project(result, approved, metadataAdmitted), err
}

func project(result runnerunit.Submission, approved, metadataAdmitted bool) runnerunit.Submission {
	result.Report = nil
	diagnostics := make([]runnercontract.Diagnostic, len(result.Diagnostics))
	for i, diagnostic := range result.Diagnostics {
		diagnostics[i] = runnercontract.Diagnostic{Code: diagnostic.Code, Message: DiagnosticMessage}
	}
	result.Diagnostics = diagnostics
	if !approved {
		result.Terminal = nil
		result.Completion = nil
	}
	if !metadataAdmitted {
		result.Caller, result.Reservation, result.Decision, result.Completion = nil, nil, nil, nil
	}
	return result
}
