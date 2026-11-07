package ownedsubmission

import (
	"context"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

func RejectPublic(code string) runnerunit.Submission {
	return runnerunit.Submission{
		Kind: "waza.execution-submission-result", SchemaVersion: "1.0",
		Status: "rejected", ExitCode: 6, Diagnostics: []runnercontract.Diagnostic{publicDiagnostic(code)},
	}
}

func publicDiagnostic(code string) runnercontract.Diagnostic {
	return runnercontract.Diagnostic{
		Code: code, Message: "Submission did not complete. Preserve immutable records and inspect operator diagnostics; unapproved input and source text are withheld.",
	}
}

func FailPublic(code string) runnerunit.Submission {
	result := RejectPublic(code)
	result.Status, result.ExitCode = "submission_error", 2
	return result
}

// ValidateInput is offline. Provider and runtime construction belong to Submit.
func ValidateInput(data []byte) error {
	request, err := runnerunit.ParseSubmission(data)
	if err != nil {
		return err
	}
	if report := runnerunit.Validate(request.Request); report.Status != "ready" {
		return fmt.Errorf("captured submission is not admitted: %s", report.Status)
	}
	return nil
}

// SubmitCaptured admits original metadata independently from report capture.
// A terminal is disclosed only after original publication or source admission.
func SubmitCaptured(ctx context.Context, service *runnerunit.Service, data []byte,
	backend artifactmanifest.Backend) (runnerunit.Submission, error) {
	hooks, err := lifecycleCaptureHooks(time.Now, service)
	if err != nil {
		return RejectPublic("capture_policy_unavailable"), err
	}
	metadataAdmitted, terminalAdmitted := false, false
	admission := hooks.Admission
	hooks.Admission = func(ctx context.Context, binding artifactmanifest.Binding) error {
		if err := admission(ctx, binding); err != nil {
			return err
		}
		metadataAdmitted = true
		return nil
	}
	validate := hooks.ValidatePublication
	hooks.ValidatePublication = func(ctx context.Context, original runnerunit.SubmissionSnapshot,
		publication runnerunit.SubmissionPublication) error {
		if err := validate(ctx, original, publication); err != nil {
			return err
		}
		terminalAdmitted = true
		return nil
	}
	reuse := hooks.ReuseAdmission
	hooks.ReuseAdmission = func(ctx context.Context, original *executionledger.CompletedExecution) error {
		if err := reuse(ctx, original); err != nil {
			return err
		}
		terminalAdmitted = true
		return nil
	}
	result, err := service.Submit(ctx, data, backend, hooks)
	result.Report = nil
	for i, diagnostic := range result.Diagnostics {
		result.Diagnostics[i] = publicDiagnostic(diagnostic.Code)
	}
	if !terminalAdmitted {
		result.Terminal = nil
	}
	if !metadataAdmitted {
		result.Caller, result.Reservation, result.Decision, result.Completion = nil, nil, nil, nil
	}
	return result, err
}

// Publication may have completed before resource release failed. Retain admitted
// original actor facts, but do not report successful completion of this operation.
func CloseFailed(result runnerunit.Submission) runnerunit.Submission {
	result.Status, result.ExitCode, result.Completion = "submission_error", 2, nil
	result.Diagnostics = append(result.Diagnostics, publicDiagnostic("submission_storage_close_failed"))
	return result
}
