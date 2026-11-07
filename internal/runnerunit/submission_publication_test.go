package runnerunit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func TestSubmitPublicationPolicyPrecedesObserversAndNeverTransformsReuse(t *testing.T) {
	backend := submissionBackend(t)
	request := fixture(t)
	before := request.Input.Task
	request.Input.Task = strings.Replace(before, "    - hello\n", "    - impossible-output-control\n", 1)
	if request.Input.Task == before {
		t.Fatal("actual failed-check input control was not selected")
	}
	prepared, observed := 0, 0
	hooks := SubmissionHooks{
		PreparePublication: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, source SubmissionSnapshot) (SubmissionPublication, error) {
			prepared++
			if source.Terminal.Outcome != runnerterminal.EvaluationFailure || len(source.Report) == 0 || source.ObservedReport.Observation == nil {
				t.Fatal("publication policy did not receive the one original invocation")
			}
			source.Terminal.Diagnostics[0].Message = "[REDACTED]"
			data, err := runnerterminal.Encode(source.Terminal, source.Reservation.Attempt.Binding.TerminalBinding())
			if err != nil {
				return SubmissionPublication{}, err
			}
			return SubmissionPublication{Terminal: source.Terminal, Artifacts: []SubmissionArtifact{{
				Name: "terminal.json", MediaType: "application/json", Data: data,
			}}}, nil
		},
		Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, _ executionledger.Reservation, envelope runnerterminal.Envelope) error {
			observed++
			if prepared != 1 || envelope.Diagnostics[0].Message != "[REDACTED]" || envelope.Outcome != runnerterminal.EvaluationFailure {
				t.Fatal("terminal observer saw raw text or changed original outcome")
			}
			return nil
		},
	}
	service := NewService(nil)
	result, err := service.Submit(context.Background(), submissionWire(t, request, nil), backend, hooks)
	if err != nil || result.ExitCode != 1 || result.Terminal.Diagnostics[0].Message != "[REDACTED]" || len(result.Report) != 0 {
		t.Fatalf("first-publication policy failed or leaked raw report: %+v %v", result, err)
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	published, err := ledger.ReadCompleted(context.Background(), source)
	if err != nil || len(published.Publication.Manifest.Artifacts) != 1 {
		t.Fatalf("raw or duplicate terminal artifact was published: %+v %v", published, err)
	}
	original := bytes.Clone(published.Publication.ArtifactBytes["terminal.json"])
	request.Selection.Attempt++
	reused, err := service.Submit(context.Background(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, hooks)
	if err != nil || prepared != 1 || observed != 1 || reused.ExitCode != 1 || reused.EngineCalls != 0 || len(reused.Report) != 0 {
		t.Fatalf("reuse transformed source or invoked observer: %+v %v", reused, err)
	}
	reusedBytes, err := runnerterminal.Encode(*reused.Terminal, result.Reservation.Binding.TerminalBinding())
	if err != nil || !bytes.Equal(reusedBytes, original) {
		t.Fatalf("reused source bytes were retagged: %v", err)
	}
}

func TestSubmitRejectsPublicationPolicyNativeChangesAndRawArtifact(t *testing.T) {
	for _, control := range []string{"policy-error", "native-change", "raw-report"} {
		t.Run(control, func(t *testing.T) {
			backend := submissionBackend(t)
			hooks := SubmissionHooks{PreparePublication: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, source SubmissionSnapshot) (SubmissionPublication, error) {
				if control == "policy-error" {
					return SubmissionPublication{}, errors.New("privacy policy control failure")
				}
				if control == "native-change" {
					source.Terminal.Identity.Run = "run-" + strings.Repeat("0", 64)
				}
				result := SubmissionPublication{Terminal: source.Terminal}
				if control == "raw-report" {
					result.Artifacts = []SubmissionArtifact{{Name: "report.json", MediaType: "application/json", Data: source.Report}}
				}
				return result, nil
			}}
			result, err := NewService(nil).Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, hooks)
			if err == nil || result.Status != "submission_error" || result.ExitCode != 2 || result.Completion != nil || len(result.Report) != 0 {
				t.Fatalf("invalid publication policy completed or disclosed raw report: %+v %v", result, err)
			}
			if control != "raw-report" && result.Terminal != nil {
				t.Fatal("failed terminal privacy policy exposed unapproved original text")
			}
		})
	}
}

func TestSubmitSourceDisclosureDenialPrecedesReuseDecision(t *testing.T) {
	backend := submissionBackend(t)
	service := NewService(nil)
	request := fixture(t)
	result, err := service.Submit(context.Background(), submissionWire(t, request, nil), backend, SubmissionHooks{})
	if err != nil {
		t.Fatal(err)
	}
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	calls := 0
	hooks := SubmissionHooks{ReuseAdmission: func(context.Context, *executionledger.CompletedExecution) error {
		calls++
		return errors.New("original source disclosure is denied")
	}}
	rejected, err := service.Submit(context.Background(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, hooks)
	if err == nil || calls != 1 || rejected.Decision != nil || rejected.Terminal != nil || rejected.EngineCalls != 0 ||
		len(rejected.Diagnostics) != 1 || rejected.Diagnostics[0].Code != "source_disclosure_denied" {
		t.Fatalf("source disclosure denial became actionable reuse: %+v %v", rejected, err)
	}
}
