package ownedsubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func captureRequest(t *testing.T) *runnercontract.Request {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	request, err := runnercontract.Parse(data)
	require.NoError(t, err)
	return request
}

func captureWire(t *testing.T, request *runnercontract.Request, source *executionattempt.Source) []byte {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	wrapper := runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw}
	if source != nil {
		wrapper.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: source}
	}
	data, err := json.Marshal(wrapper)
	require.NoError(t, err)
	return data
}

func captureBackend(t *testing.T) *artifactmanifest.Local {
	t.Helper()
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	return backend
}

func TestCaptureHooksActualPublicationAndImmutableFailedReuse(t *testing.T) {
	for _, control := range []string{"success", "grader-failure", "canceled"} {
		t.Run(control, func(t *testing.T) {
			backend := captureBackend(t)
			request := captureRequest(t)
			request.Input.Task = strings.Replace(request.Input.Task, "prompt: hello", "prompt: hello private-conversation-control", 1)
			require.Contains(t, request.Input.Task, "private-conversation-control")
			wantCode := 0
			service := runnerunit.NewService(nil)
			if control == "grader-failure" {
				request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- missing-output-control", 1)
				require.Contains(t, request.Input.Task, "missing-output-control")
				wantCode = 1
			}
			if control == "canceled" {
				wantCode = 3
				service = runnerunit.NewService(func(models.Config) (execution.AgentEngine, error) {
					return nil, context.Canceled
				})
			}
			observations := 0
			hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
				Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
					_ executionledger.Reservation, terminal runnerterminal.Envelope) error {
					observations++
					for _, diagnostic := range terminal.Diagnostics {
						require.Contains(t, []string{"", "[REDACTED]"}, diagnostic.Message)
					}
					if terminal.Observation != nil {
						require.Contains(t, []string{"", "[REDACTED]"}, terminal.Observation.ErrorMsg)
					}
					return nil
				},
			})
			require.NoError(t, err)
			result, err := service.Submit(context.Background(), captureWire(t, request, nil), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, result.ExitCode)
			require.NotNil(t, result.Completion)
			require.Empty(t, result.Report)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
			completed, err := ledger.ReadCompleted(context.Background(), source)
			require.NoError(t, err)
			var names []string
			for _, artifact := range completed.Publication.Manifest.Artifacts {
				names = append(names, artifact.Name)
			}
			require.Contains(t, names, "execution-evidence-capture.json")
			require.Contains(t, names, "execution-attempt.json")
			require.NotContains(t, names, "report.json")
			withBytes, err := ledger.ReadCompleted(context.Background(), source, names...)
			require.NoError(t, err)
			require.Equal(t, result.Completion.ReservationDigest, artifactmanifest.Hash(withBytes.Publication.ArtifactBytes["execution-attempt.json"]))
			attempt, err := executionattempt.ParseAttempt(withBytes.Publication.ArtifactBytes["execution-attempt.json"])
			require.NoError(t, err)
			require.Equal(t, result.Reservation.ExecutionID, attempt.ExecutionID)
			require.Equal(t, result.Reservation.Binding, attempt.Binding)
			for name, data := range withBytes.Publication.ArtifactBytes {
				require.NotContains(t, string(data), "private-conversation-control", name)
				require.NotContains(t, string(data), `"prompt"`, name)
				require.NotContains(t, string(data), `"transcript"`, name)
			}
			original := bytes.Clone(completed.Publication.ArtifactBytes["terminal.json"])
			originalOrdinal := result.Terminal.Selection.Attempt
			request.Selection.Attempt++
			reused, err := service.Submit(context.Background(), captureWire(t, request, &source), backend, hooks)
			require.NoError(t, err)
			require.Equal(t, wantCode, reused.ExitCode)
			require.Zero(t, reused.EngineCalls)
			require.Equal(t, 1, observations)
			require.Equal(t, originalOrdinal, reused.Terminal.Selection.Attempt)
			encoded, err := runnerterminal.Encode(*reused.Terminal, result.Reservation.Binding.TerminalBinding())
			require.NoError(t, err)
			require.Equal(t, original, encoded)
		})
	}
}

func TestCaptureHooksRejectConsistentMutableSnapshotAndArtifactChanges(t *testing.T) {
	for _, control := range []string{"category-bytes", "missing-artifact", "duplicate-artifact", "mutable-observation", "attempt-bytes"} {
		t.Run(control, func(t *testing.T) {
			backend := captureBackend(t)
			observed := false
			hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
				Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
					executionledger.Reservation, runnerterminal.Envelope) error {
					observed = true
					return nil
				},
			})
			require.NoError(t, err)
			prepare := hooks.PreparePublication
			hooks.PreparePublication = func(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator,
				source runnerunit.SubmissionSnapshot) (runnerunit.SubmissionPublication, error) {
				if control == "mutable-observation" {
					require.NotNil(t, source.ObservedReport.Observation)
					before, err := json.Marshal(source.ObservedReport.Observation.Usage)
					require.NoError(t, err)
					credits := 987654321.0
					source.ObservedReport.Observation.Usage = &models.UsageStats{AICredits: &credits}
					after, err := json.Marshal(source.ObservedReport.Observation.Usage)
					require.NoError(t, err)
					require.NotEqual(t, before, after)
				}
				publication, err := prepare(ctx, ledger, creator, source)
				if err != nil {
					return publication, err
				}
				switch control {
				case "category-bytes":
					for i := range publication.Artifacts {
						if publication.Artifacts[i].Name != "terminal.json" {
							publication.Artifacts[i].Data = append(publication.Artifacts[i].Data, ' ')
							break
						}
					}
				case "missing-artifact":
					publication.Artifacts = publication.Artifacts[1:]
				case "duplicate-artifact":
					publication.Artifacts[len(publication.Artifacts)-1] = publication.Artifacts[0]
				case "attempt-bytes":
					for i := range publication.Artifacts {
						if publication.Artifacts[i].Name == "execution-attempt.json" {
							publication.Artifacts[i].Data = append(publication.Artifacts[i].Data, ' ')
						}
					}
				}
				return publication, nil
			}
			result, err := runnerunit.NewService(nil).Submit(context.Background(), captureWire(t, captureRequest(t), nil), backend, hooks)
			require.Error(t, err)
			require.False(t, observed, "unapproved bytes must not reach joined terminal observer")
			require.Nil(t, result.Terminal)
			require.Nil(t, result.Completion)
			ledger, err := executionledger.New(backend)
			require.NoError(t, err)
			completion, err := ledger.ReadCompletion(context.Background(), result.Reservation.ExecutionID)
			require.ErrorIs(t, err, artifactmanifest.ErrMissing)
			require.Nil(t, completion)
		})
	}
}

func TestCaptureHooksUnsafeOriginalReuseIsDeniedWithoutRewriting(t *testing.T) {
	backend := captureBackend(t)
	request := captureRequest(t)
	request.Input.Task = strings.Replace(request.Input.Task, "- hello", "- missing-output-control", 1)
	service := runnerunit.NewService(nil)
	original, err := service.Submit(context.Background(), captureWire(t, request, nil), backend, runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	source := executionattempt.Source{ExecutionID: original.Reservation.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
	hooks, err := CaptureHooks(runnerunit.SubmissionHooks{})
	require.NoError(t, err)
	rejected, err := service.Submit(context.Background(), captureWire(t, request, &source), backend, hooks)
	require.Error(t, err)
	require.Nil(t, rejected.Decision)
	require.Nil(t, rejected.Terminal)
	require.Zero(t, rejected.EngineCalls)
	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	unchanged, err := ledger.ReadCompleted(context.Background(), source)
	require.NoError(t, err)
	encoded, err := runnerterminal.Encode(*original.Terminal, original.Reservation.Binding.TerminalBinding())
	require.NoError(t, err)
	require.Equal(t, encoded, unchanged.Publication.ArtifactBytes["terminal.json"])
}

func TestCaptureHooksPreservesAdmissionDenialAndRejectsCompetingPolicies(t *testing.T) {
	cause := errors.New("operator denial")
	hooks, err := CaptureHooks(runnerunit.SubmissionHooks{Admission: func(context.Context, artifactmanifest.Binding) error { return cause }})
	require.NoError(t, err)
	result, err := runnerunit.NewService(nil).Submit(context.Background(), captureWire(t, captureRequest(t), nil), captureBackend(t), hooks)
	require.ErrorIs(t, err, cause)
	require.Nil(t, result.Reservation)
	require.Zero(t, result.EngineCalls)
	_, err = CaptureHooks(runnerunit.SubmissionHooks{ValidatePublication: func(context.Context, runnerunit.SubmissionSnapshot, runnerunit.SubmissionPublication) error {
		return nil
	}})
	require.Error(t, err)
}

func TestCaptureHooksRejectsRestrictedIdentifiersBeforeReservation(t *testing.T) {
	request := captureRequest(t)
	restricted := "ghp_" + strings.Repeat("A", 36)
	request.Input.Task = strings.Replace(request.Input.Task, "id: selected", "id: "+restricted, 1)
	request.Selection.Task = restricted
	hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
		Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
			t.Fatal("restricted binding reached persisted reservation")
			return nil
		},
	})
	require.NoError(t, err)
	result, err := runnerunit.NewService(nil).Submit(context.Background(), captureWire(t, request, nil), captureBackend(t), hooks)
	require.Error(t, err)
	require.Nil(t, result.Reservation)
	require.Zero(t, result.EngineCalls)
	require.Equal(t, "capture_admission_failed", result.Diagnostics[0].Code)
}

func TestSubmitRejectsValidationWithoutPreparationBeforeReservation(t *testing.T) {
	result, err := runnerunit.NewService(nil).Submit(context.Background(), captureWire(t, captureRequest(t), nil), captureBackend(t),
		runnerunit.SubmissionHooks{ValidatePublication: func(context.Context, runnerunit.SubmissionSnapshot, runnerunit.SubmissionPublication) error {
			t.Fatal("orphan validator must not silently run or be ignored")
			return nil
		}})
	require.Error(t, err)
	require.Nil(t, result.Reservation)
	require.Zero(t, result.EngineCalls)
	require.Equal(t, "publication_policy_invalid", result.Diagnostics[0].Code)
}

func TestCaptureHooksRejectsAdditionalRawArtifactProducer(t *testing.T) {
	hooks, err := CaptureHooks(runnerunit.SubmissionHooks{
		Artifacts: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
			source runnerunit.SubmissionSnapshot) ([]runnerunit.SubmissionArtifact, error) {
			t.Fatal("unvalidated extra producer must not receive or publish raw report")
			return []runnerunit.SubmissionArtifact{{Name: "renamed-raw.json", MediaType: "application/json", Data: source.Report}}, nil
		},
		Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
			executionledger.Reservation, runnerterminal.Envelope) error {
			t.Fatal("rejected extra producer must not reach terminal observer")
			return nil
		},
	})
	require.Error(t, err)
	require.Nil(t, hooks.PreparePublication)
}
