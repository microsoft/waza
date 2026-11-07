package runnercheckpoint

import (
	"context"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/ownedsubmission"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

// RejectPublic creates an operation rejection without exposing an operator
// cause, native input, provider location or unauthenticated source declaration.
func RejectPublic(code string) Result {
	result := newResult()
	result.fail(code)
	return result
}

func (s *Service) SubmitCaptured(ctx context.Context, data []byte, backend artifactmanifest.Backend, observers Hooks) (Result, error) {
	return s.captured(ctx, data, backend, "", observers, false)
}

func (s *Service) ResumeCaptured(ctx context.Context, data []byte, backend artifactmanifest.Backend, digest string, observers Hooks) (Result, error) {
	return s.captured(ctx, data, backend, digest, observers, true)
}

func (s *Service) captured(ctx context.Context, data []byte, backend artifactmanifest.Backend, digest string,
	observers Hooks, resume bool,
) (Result, error) {
	hooks, err := ownedsubmission.CaptureHooks(observers.Submission)
	if err != nil {
		return RejectPublic("checkpoint_capture_policy_invalid"), err
	}
	approved := false
	metadataAdmitted := false
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
		metadataAdmitted = true
		return nil
	}
	reuse := hooks.ReuseAdmission
	hooks.ReuseAdmission = func(ctx context.Context, source *executionledger.CompletedExecution) error {
		if err := reuse(ctx, source); err != nil {
			return err
		}
		approved = true
		metadataAdmitted = true
		return nil
	}
	observers.Submission = hooks
	var result Result
	if resume {
		result, err = s.Resume(ctx, data, backend, digest, observers)
	} else {
		result, err = s.Submit(ctx, data, backend, observers)
	}
	return projectCapturedResult(result, approved, metadataAdmitted), err
}

func projectCapturedResult(result Result, approved, metadataAdmitted bool) Result {
	if result.Source != nil && !approved {
		result.Source = nil
		result.SourceWithheld = true
	}
	result.Diagnostics = publicDiagnostics(result.Diagnostics)
	if result.Submission != nil {
		submission := *result.Submission
		submission.Report = nil
		submission.Diagnostics = publicDiagnostics(submission.Diagnostics)
		if !approved {
			submission.Terminal = nil
		}
		if !metadataAdmitted {
			submission.Caller = nil
			submission.Reservation = nil
			submission.Decision = nil
			submission.Completion = nil
		}
		result.Submission = &submission
	}
	return result
}

func publicDiagnostics(original []runnercontract.Diagnostic) []runnercontract.Diagnostic {
	result := make([]runnercontract.Diagnostic, len(original))
	for i, diagnostic := range original {
		result[i] = runnercontract.Diagnostic{
			Code:    diagnostic.Code,
			Message: "Checkpoint operation did not complete. Preserve source and partial records; inspect operator diagnostics. Unapproved source and invocation text are withheld.",
		}
	}
	return result
}
