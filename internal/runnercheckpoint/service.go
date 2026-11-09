// Package runnercheckpoint connects immutable checkpoints to the single-
// admission runner submission path. It never reconstructs execution ownership.
package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerunit"
)

var ErrPolicy = errors.New("checkpoint operation conflicts with explicit submission policy")

type Reference struct {
	Digest   string `json:"checkpoint_sha256"`
	Boundary string `json:"boundary"`
}

type Hooks struct {
	Submission runnerunit.SubmissionHooks
	Published  func(context.Context, Reference) error
}

type Result struct {
	Kind                     string                          `json:"kind"`
	SchemaVersion            string                          `json:"schema_version"`
	Status                   string                          `json:"status"`
	ExitCode                 int                             `json:"exit_code"`
	Submission               *runnerunit.Submission          `json:"submission,omitempty"`
	Source                   *executioncheckpoint.Checkpoint `json:"source,omitempty"`
	SourceWithheld           bool                            `json:"source_withheld,omitempty"`
	BeforeDigest             string                          `json:"before_checkpoint_sha256,omitempty"`
	FinalizedDigest          string                          `json:"finalized_checkpoint_sha256,omitempty"`
	CompatibilityResult      string                          `json:"compatibility_result"`
	CompatibilityDifferences []string                        `json:"compatibility_differences"`
	RestoredUnits            *int                            `json:"restored_units"`
	RemainingWork            *int                            `json:"remaining_work"`
	Diagnostics              []runnercontract.Diagnostic     `json:"diagnostics"`
}

type Service struct {
	runner   *runnerunit.Service
	revision string
}

// The owning public caller must obtain revision from CurrentRunnerRevision,
// not caller JSON, ambient Git, version strings or a user-supplied flag.
func NewService(runner *runnerunit.Service, revision string) (*Service, error) {
	if runner == nil {
		return nil, fmt.Errorf("checkpoint operations require the owning runner service")
	}
	return &Service{runner: runner, revision: revision}, nil
}

func newResult() Result {
	return Result{
		Kind: "waza.runner-checkpoint-result", SchemaVersion: "1.0", Status: "rejected", ExitCode: 6,
		CompatibilityResult: "unavailable", Diagnostics: []runnercontract.Diagnostic{},
		CompatibilityDifferences: []string{},
	}
}

func (r *Result) fail(code string) {
	r.Diagnostics = append(r.Diagnostics, runnercontract.Diagnostic{
		Code: code, Message: "Checkpoint operation did not complete; preserve source and partial records and inspect operator diagnostics.",
	})
}

// Submit publishes before-unit only in the actual retained-creator Reserved
// callback, and finalized-unit only after Submit's joined, verified completion.
func (s *Service) Submit(ctx context.Context, data []byte, backend artifactmanifest.Backend, hooks Hooks) (Result, error) {
	result := newResult()
	request, err := runnerunit.ParseSubmission(bytes.Clone(data))
	if err != nil {
		result.fail("submission_input_invalid")
		return result, err
	}
	if request.Policy != nil && request.Policy.Mode != "distinct" {
		result.fail("checkpoint_policy_conflict")
		return result, fmt.Errorf("%w: creating a checkpoint requires a new retained creator; use resume for an existing checkpoint", ErrPolicy)
	}
	if _, err := executioncheckpoint.ForRequest(request.Request, s.revision); err != nil {
		result.fail("checkpoint_request_invalid")
		return result, err
	}
	return s.submit(ctx, request, backend, hooks, result, nil)
}

// Resume admits the original source, then either reserves a fresh retry child
// or explicitly reuses the verified original terminal without engine calls.
func (s *Service) Resume(ctx context.Context, data []byte, backend artifactmanifest.Backend, digest string, hooks Hooks) (Result, error) {
	result := newResult()
	request, err := runnerunit.ParseSubmission(bytes.Clone(data))
	if err != nil {
		result.fail("submission_input_invalid")
		return result, err
	}
	current, err := executioncheckpoint.ForRequest(request.Request, s.revision)
	if err != nil {
		result.fail("checkpoint_request_invalid")
		return result, err
	}
	checkpoint, err := executioncheckpoint.Load(ctx, backend, digest)
	if err != nil {
		result.fail("checkpoint_source_unavailable")
		return result, err
	}
	result.Source = checkpoint
	if err := executioncheckpoint.ValidateCompatibility(*checkpoint, current); err != nil {
		result.CompatibilityResult = "incompatible"
		if mismatch, ok := errors.AsType[*executioncheckpoint.CompatibilityError](err); ok {
			result.CompatibilityDifferences = append(result.CompatibilityDifferences, mismatch.Fields...)
		}
		result.fail("checkpoint_incompatible")
		return result, err
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		result.fail("checkpoint_ledger_unavailable")
		return result, err
	}
	switch checkpoint.Boundary {
	case executioncheckpoint.BeforeUnit:
		if _, err := executioncheckpoint.ReadBefore(ctx, ledger, *checkpoint, current); err != nil {
			result.fail("checkpoint_before_source_rejected")
			return result, err
		}
		parent := executionattempt.Parent{ExecutionID: checkpoint.ExecutionID, Relationship: "retry"}
		if request.Policy != nil && (request.Policy.Mode != "distinct" ||
			(request.Policy.Parent != nil && *request.Policy.Parent != parent)) {
			result.fail("checkpoint_policy_conflict")
			return result, fmt.Errorf("%w: before-unit requires a fresh distinct retry child of its original reservation", ErrPolicy)
		}
		request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct", Parent: &parent}
		checkSource := func(ctx context.Context, owner *executionledger.Ledger) error {
			_, err := executioncheckpoint.ReadBefore(ctx, owner, *checkpoint, current)
			return err
		}
		return s.submit(ctx, request, backend, hooks, result, checkSource)
	case executioncheckpoint.FinalizedUnit:
		original, err := executioncheckpoint.ReadStoredFinalized(ctx, ledger, *checkpoint, current)
		if err != nil {
			result.fail("checkpoint_finalized_source_rejected")
			return result, err
		}
		source := executionattempt.Source{ExecutionID: checkpoint.ExecutionID, ManifestDigest: checkpoint.ManifestDigest}
		if request.Policy != nil && (request.Policy.Mode != "reuse" || request.Policy.Source == nil ||
			*request.Policy.Source != source) {
			result.fail("checkpoint_policy_conflict")
			return result, fmt.Errorf("%w: finalized-unit must reuse its exact original source, not execute a distinct unit", ErrPolicy)
		}
		request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source}
		wire, err := json.Marshal(request)
		if err != nil {
			result.fail("submission_encoding_failed")
			return result, err
		}
		submissionHooks := hooks.Submission
		submissionHooks.ReuseAdmission = func(ctx context.Context, source *executionledger.CompletedExecution) error {
			if err := executioncheckpoint.ValidateContext(*checkpoint, source.Reservation.Attempt.Binding,
				source.Reservation.Bytes, source.CompletionBytes, source.Publication.ManifestBytes); err != nil {
				return err
			}
			if hooks.Submission.ReuseAdmission != nil {
				return hooks.Submission.ReuseAdmission(ctx, source)
			}
			return nil
		}
		submission, err := s.runner.Submit(ctx, wire, backend, submissionHooks)
		result.Submission, result.Status, result.ExitCode = &submission, submission.Status, submission.ExitCode
		if err != nil {
			result.fail("checkpoint_reuse_failed")
			return result, err
		}
		result.CompatibilityResult = "compatible"
		restored, remaining := 1, 0
		result.RestoredUnits, result.RemainingWork = &restored, &remaining
		reservation, reservationErr := executionattempt.EncodeAttempt(*submission.Reservation)
		completion, completionErr := executionledger.EncodeCompletion(*submission.Completion)
		if err := errors.Join(reservationErr, completionErr); err != nil ||
			!bytes.Equal(reservation, original.Reservation.Bytes) || !bytes.Equal(completion, original.CompletionBytes) {
			result.Status, result.ExitCode = "submission_error", 2
			result.Submission.Status, result.Submission.ExitCode = result.Status, result.ExitCode
			result.CompatibilityResult = "unavailable"
			result.RestoredUnits, result.RemainingWork = nil, nil
			result.fail("checkpoint_returned_source_invalid")
			return result, fmt.Errorf("returned reuse differs from original admitted sidecars: %w", errors.Join(executioncheckpoint.ErrContext, err))
		}
		return result, nil
	default:
		result.fail("checkpoint_boundary_invalid")
		return result, fmt.Errorf("%w: unsupported checkpoint boundary", executioncheckpoint.ErrInvalid)
	}
}

func (s *Service) submit(ctx context.Context, request *runnerunit.SubmissionRequest,
	backend artifactmanifest.Backend, hooks Hooks, result Result,
	checkSource func(context.Context, *executionledger.Ledger) error,
) (Result, error) {
	original := bytes.Clone(request.Request)
	wire, err := json.Marshal(request)
	if err != nil {
		result.fail("submission_encoding_failed")
		return result, err
	}
	var owner *executionledger.Ledger
	var creator executionledger.Creator
	submissionHooks := hooks.Submission
	submissionHooks.Reserved = func(ctx context.Context, ledger *executionledger.Ledger,
		capability executionledger.Creator, reservation executionledger.Reservation,
	) error {
		owner, creator = ledger, capability
		if hooks.Submission.Reserved != nil {
			if err := hooks.Submission.Reserved(ctx, ledger, capability, reservation); err != nil {
				return err
			}
		}
		if checkSource != nil {
			if err := checkSource(ctx, ledger); err != nil {
				return err
			}
		}
		digest, err := executioncheckpoint.PublishBefore(ctx, backend, ledger, capability, original, s.revision)
		result.BeforeDigest = digest
		if err != nil {
			return err
		}
		if hooks.Published != nil {
			if err := hooks.Published(ctx, Reference{Digest: digest, Boundary: executioncheckpoint.BeforeUnit}); err != nil {
				return err
			}
		}
		return nil
	}
	submission, err := s.runner.Submit(ctx, wire, backend, submissionHooks)
	result.Submission, result.Status, result.ExitCode = &submission, submission.Status, submission.ExitCode
	if err != nil {
		result.fail("checkpoint_submission_failed")
		return result, err
	}
	if owner == nil || submission.Completion == nil {
		result.Status, result.ExitCode = "checkpoint_error", 2
		result.fail("checkpoint_completion_missing")
		return result, fmt.Errorf("completed checkpoint submission did not retain its creator and completion")
	}
	publicationContext, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer stop()
	digest, err := executioncheckpoint.PublishFinalized(publicationContext, backend, owner, creator, original,
		s.revision, submission.Completion.ManifestDigest)
	result.FinalizedDigest = digest
	if err == nil && hooks.Published != nil {
		err = hooks.Published(publicationContext, Reference{Digest: digest, Boundary: executioncheckpoint.FinalizedUnit})
	}
	if err != nil {
		result.Status, result.ExitCode = "checkpoint_error", 2
		result.fail("checkpoint_finalized_publication_failed")
		return result, err
	}
	result.CompatibilityResult = "compatible"
	restored, remaining := 0, 0
	result.RestoredUnits, result.RemainingWork = &restored, &remaining
	return result, nil
}
