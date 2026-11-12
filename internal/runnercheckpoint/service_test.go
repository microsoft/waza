package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestLargeUnescapedRequestSurvivesSubmitAndBothRecoveryBoundaries(t *testing.T) {
	wrapper, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnercontract.Parse(wrapper.Request)
	if err != nil {
		t.Fatal(err)
	}
	originalTask := request.Input.Task
	request.Input.Task = strings.Replace(originalTask, "prompt: hello", "prompt: "+strings.Repeat("<>&", 270000), 1)
	if request.Input.Task == originalTask {
		t.Fatal("large HTML-character control did not change actual prompt")
	}
	encode := func(value any) []byte {
		t.Helper()
		var buffer bytes.Buffer
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(value); err != nil {
			t.Fatal(err)
		}
		return bytes.TrimSpace(buffer.Bytes())
	}
	wrapper.Request = encode(request)
	wire := encode(wrapper)
	inflated, err := json.Marshal(wrapper)
	if err != nil || len(wire) >= runnerunit.MaxSubmissionBytes || len(inflated) <= runnerunit.MaxSubmissionBytes {
		t.Fatalf("fixture did not straddle only adapter escaping limit: raw=%d escaped=%d %v", len(wire), len(inflated), err)
	}
	if _, err := runnerunit.ParseSubmission(wire); err != nil {
		t.Fatalf("original wire is not admitted: %v", err)
	}
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	trace := func(ctx context.Context, phase string, started time.Time) {
		t.Helper()
		remaining := "none"
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline).String()
		}
		t.Logf("large-request phase=%s elapsed=%s ctx_err=%v deadline_remaining=%s", phase, time.Since(started), ctx.Err(), remaining)
	}
	hooks := func(operation string, started time.Time, stopBefore bool) Hooks {
		return Hooks{Published: func(ctx context.Context, reference Reference) error {
			trace(ctx, operation+"/"+reference.Boundary, started)
			if stopBefore {
				return errors.New("stop before dispatch")
			}
			return nil
		}}
	}
	started := time.Now()
	completed, err := service.Submit(t.Context(), wire, store, hooks("submit", started, false))
	trace(t.Context(), "submit/return", started)
	if err != nil || completed.Submission == nil || completed.Submission.Completion == nil ||
		completed.BeforeDigest == "" || completed.FinalizedDigest == "" || factories != 1 {
		t.Fatalf("accepted prompt failed in checkpoint adapter: %+v %v", completed, err)
	}
	started = time.Now()
	reused, err := service.Resume(t.Context(), wire, store, completed.FinalizedDigest, hooks("finalized-resume", started, false))
	trace(t.Context(), "finalized-resume/return", started)
	if err != nil || reused.Status != "reused" || reused.Submission.EngineCalls != 0 || factories != 1 ||
		reused.Submission.Reservation.ExecutionID != completed.Submission.Reservation.ExecutionID {
		t.Fatalf("large finalized checkpoint was not reused unchanged: %+v %v", reused, err)
	}
	started = time.Now()
	paused, err := service.Submit(t.Context(), wire, store, hooks("before-stop", started, true))
	trace(t.Context(), "before-stop/return", started)
	if err == nil || paused.BeforeDigest == "" || factories != 1 {
		t.Fatalf("before-unit fixture did not stop before engine: %+v %v", paused, err)
	}
	started = time.Now()
	child, err := service.Resume(t.Context(), wire, store, paused.BeforeDigest, hooks("before-resume", started, false))
	trace(t.Context(), "before-resume/return", started)
	if err != nil || child.Submission.Completion == nil || child.Submission.EngineCalls != 1 || factories != 2 ||
		child.Submission.Reservation.ExecutionID == paused.Submission.Reservation.ExecutionID ||
		child.Submission.Reservation.Parent == nil ||
		child.Submission.Reservation.Parent.ExecutionID != paused.Submission.Reservation.ExecutionID {
		t.Fatalf("large before-unit checkpoint failed fresh-child recovery: %+v %v", child, err)
	}
}

func fixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func backend(t *testing.T) *artifactmanifest.Local {
	t.Helper()
	value, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := value.Close(); err != nil {
			t.Error(err)
		}
	})
	return value
}

func adapter(t *testing.T, factory runnerunit.EngineFactory) *Service {
	t.Helper()
	// This is production Submit/adapter evidence with an authored revision,
	// not clean-build metadata or production-CLI interruption evidence.
	service, err := NewService(runnerunit.NewService(factory), strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type checkpointTestCredential struct{}

func (checkpointTestCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "offline", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type finalizedCreatorReadFailure struct {
	mu                   sync.Mutex
	objects              map[string][]byte
	failure              error
	completionReads      int
	failed               bool
	publicationCtxActive bool
}

func (b *finalizedCreatorReadFailure) Do(request *http.Request) (*http.Response, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := strings.TrimPrefix(request.URL.Path, "/container/")
	header := http.Header{"Etag": {`"test"`}}
	response := func(status int, data []byte) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)),
			ContentLength: int64(len(data)), Request: request}, nil
	}
	switch request.Method {
	case http.MethodPut:
		if request.Header.Get("If-None-Match") != "*" {
			return nil, errors.New("missing conditional write")
		}
		if _, exists := b.objects[key]; exists {
			header.Set("X-Ms-Error-Code", "ConditionNotMet")
			return response(http.StatusPreconditionFailed, []byte("<Error><Code>ConditionNotMet</Code></Error>"))
		}
		data, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		b.objects[key] = data
		return response(http.StatusCreated, nil)
	case http.MethodGet:
		if request.URL.Query().Get("comp") == "list" {
			var body strings.Builder
			body.WriteString("<EnumerationResults><Blobs>")
			for name := range b.objects {
				if strings.HasPrefix(name, request.URL.Query().Get("prefix")) {
					body.WriteString("<Blob><Name>" + name + "</Name></Blob>")
				}
			}
			body.WriteString("</Blobs><NextMarker></NextMarker></EnumerationResults>")
			return response(http.StatusOK, []byte(body.String()))
		}
		if b.completionReads >= 2 && strings.HasPrefix(key, "execution-ledger-v1-reservation-") {
			b.failed = true
			_, hasDeadline := request.Context().Deadline()
			b.publicationCtxActive = request.Context().Err() == nil && hasDeadline
			return nil, b.failure
		}
		data, exists := b.objects[key]
		if !exists {
			header.Set("X-Ms-Error-Code", "BlobNotFound")
			return response(http.StatusNotFound, []byte("<Error><Code>BlobNotFound</Code></Error>"))
		}
		if strings.HasPrefix(key, "execution-ledger-v1-completion-") {
			b.completionReads++
		}
		header.Set("Content-Length", strconv.Itoa(len(data)))
		header.Set("X-Ms-Blob-Type", "BlockBlob")
		return response(http.StatusOK, data)
	default:
		return nil, errors.New("unexpected checkpoint fixture request")
	}
}

func TestSubmitFinalizedCreatorFailureRetainsOriginalDeclarationContext(t *testing.T) {
	failure := errors.New("finalized creator read control")
	fault := &finalizedCreatorReadFailure{objects: map[string][]byte{}, failure: failure}
	store, err := artifactmanifest.NewAzure("https://offline.invalid/container", checkpointTestCredential{}, &container.ClientOptions{
		ClientOptions: azcore.ClientOptions{Transport: fault, Retry: policy.RetryOptions{MaxRetries: -1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	result, err := service.Submit(ctx, fixture(t), store, Hooks{
		Published: func(_ context.Context, reference Reference) error {
			if reference.Boundary == executioncheckpoint.BeforeUnit {
				cancel()
			}
			return nil
		},
	})
	fault.mu.Lock()
	failed, publicationCtxActive := fault.failed, fault.publicationCtxActive
	fault.mu.Unlock()
	if !errors.Is(err, failure) || !errors.Is(ctx.Err(), context.Canceled) ||
		!failed || !publicationCtxActive ||
		result.Status != "checkpoint_error" || result.Submission == nil || result.Submission.Completion == nil {
		t.Fatalf("finalized creator failure did not retain completed submission and separate live publication context: %+v, %v", result, err)
	}
	requestPhase, creatorPhase, ok := strings.Cut(err.Error(), "; creator_publication:")
	if !ok {
		t.Fatalf("creator failure omitted separate request and publication diagnostics: %v", err)
	}
	for _, field := range []string{
		"request_deadline_remaining_at_entry=none",
		"request_entry_ctx_err=<nil>",
		"request_compatibility_ctx_err=<nil>",
	} {
		if !strings.Contains(requestPhase, field) {
			t.Fatalf("original request observation %q was lost or replaced after cancellation: %v", field, err)
		}
	}
	for _, field := range []string{"entry_ctx_err=<nil>", "final_ctx_err=<nil>"} {
		if !strings.Contains(creatorPhase, field) {
			t.Fatalf("live publication context observation %q was lost: %v", field, err)
		}
	}
	_, deadlineField, found := strings.Cut(creatorPhase, "deadline_remaining_at_entry=")
	remainingText, _, _ := strings.Cut(deadlineField, " ")
	remaining, parseErr := time.ParseDuration(remainingText)
	if !found || parseErr != nil || remaining <= 0 || remaining > 10*time.Second {
		t.Fatalf("creator publication deadline is not the independent 10s budget: %q, %v", remainingText, parseErr)
	}
}

func TestSubmitBoundaryHooksAndFinalizedRestorePreserveOriginalTerminal(t *testing.T) {
	for _, control := range []string{"pass", "failed-checks", "canceled"} {
		t.Run(control, func(t *testing.T) {
			store := backend(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wire := fixture(t)
			if control == "failed-checks" {
				changed := bytes.Replace(wire, []byte("- hello"), []byte("- impossible-output-control"), 1)
				if bytes.Equal(changed, wire) {
					t.Fatal("failed-check control did not modify real captured task")
				}
				wire = changed
			}
			factories, terminals := 0, 0
			references := []Reference{}
			service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
				factories++
				if len(references) != 1 || references[0].Boundary != executioncheckpoint.BeforeUnit {
					t.Fatal("factory ran without a published actual before checkpoint")
				}
				return execution.NewMockEngine(config.ModelID), nil
			})
			hooks := Hooks{
				Published: func(ctx context.Context, ref Reference) error {
					checkpoint, err := executioncheckpoint.Load(ctx, store, ref.Digest)
					if err != nil || checkpoint.Boundary != ref.Boundary {
						t.Fatalf("published hook did not reference actual canonical stored checkpoint: %+v, %v", checkpoint, err)
					}
					if ref.Boundary == executioncheckpoint.BeforeUnit && factories != 0 {
						t.Fatal("before checkpoint publication followed engine construction")
					}
					if ref.Boundary == executioncheckpoint.FinalizedUnit {
						ledger, err := executionledger.New(store)
						if err != nil {
							return err
						}
						if _, err := executioncheckpoint.ReadStoredFinalized(ctx, ledger, *checkpoint, checkpoint.Compatibility); err != nil {
							return err
						}
						if terminals != 1 {
							t.Fatal("finalized checkpoint preceded joined terminal callback")
						}
					}
					references = append(references, ref)
					if control == "canceled" && ref.Boundary == executioncheckpoint.BeforeUnit {
						cancel()
					}
					return nil
				},
				Submission: runnerunit.SubmissionHooks{
					Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation, runnerterminal.Envelope) error {
						terminals++
						return nil
					},
				},
			}
			result, err := service.Submit(ctx, wire, store, hooks)
			if err != nil || result.Submission == nil || len(references) != 2 || result.Submission.Completion == nil ||
				result.BeforeDigest != references[0].Digest || result.FinalizedDigest != references[1].Digest {
				t.Fatalf("actual submission did not retain both safe boundaries and completion: %+v, %v", result, err)
			}
			expected := runnerterminal.Success
			if control == "failed-checks" {
				expected = runnerterminal.EvaluationFailure
			}
			if control == "canceled" {
				expected = runnerterminal.Canceled
			}
			if result.Submission.Terminal.Outcome != expected {
				t.Fatalf("submission changed original terminal status: %+v", result.Submission.Terminal)
			}
			sourceTerminal, err := json.Marshal(result.Submission.Terminal)
			if err != nil {
				t.Fatal(err)
			}
			changed := bytes.Replace(wire, []byte(`"attempt":2`), []byte(`"attempt":3`), 1)
			if bytes.Equal(changed, wire) {
				t.Fatal("destination attempt control did not change request")
			}
			previousFactories := factories
			restored, err := service.Resume(context.Background(), changed, store, result.FinalizedDigest, hooks)
			if err != nil || restored.Submission.EngineCalls != 0 || factories != previousFactories || len(references) != 2 ||
				restored.RestoredUnits == nil || *restored.RestoredUnits != 1 ||
				restored.RemainingWork == nil || *restored.RemainingWork != 0 || restored.ExitCode != result.ExitCode {
				t.Fatalf("finalized restore executed or misreported original work: %+v, %v", restored, err)
			}
			terminal, err := json.Marshal(restored.Submission.Terminal)
			if err != nil || !bytes.Equal(terminal, sourceTerminal) ||
				!bytes.Equal(restored.Submission.Report, result.Submission.Report) ||
				restored.Submission.Terminal.Selection.Attempt != 2 {
				t.Fatalf("restore regenerated terminal/report or retagged native ordinal: %s, %v", terminal, err)
			}
			rejected, err := service.Resume(context.Background(), wire, store, result.BeforeDigest, Hooks{})
			if !errors.Is(err, executioncheckpoint.ErrStale) || rejected.Submission != nil ||
				rejected.RestoredUnits != nil || rejected.RemainingWork != nil || factories != previousFactories {
				t.Fatalf("completed original was rerun through its old before checkpoint: %+v, %v", rejected, err)
			}
		})
	}
}

func TestBeforeRestoreCreatesFreshRetryChildAfterInterruptedPublishedBoundary(t *testing.T) {
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	pause := errors.New("controlled embedding host stops after actual before publication")
	original, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return pause },
	})
	if !errors.Is(err, pause) || original.BeforeDigest == "" || original.Submission.Reservation == nil ||
		original.Submission.Completion != nil || factories != 0 {
		t.Fatalf("control did not stop actual Submit before factory with retained source: %+v, %v", original, err)
	}
	restored, err := service.Resume(context.Background(), fixture(t), store, original.BeforeDigest, Hooks{})
	if err != nil || factories != 1 || restored.Submission.EngineCalls != 1 || restored.Submission.Completion == nil ||
		restored.Submission.Reservation.ExecutionID == original.Submission.Reservation.ExecutionID ||
		restored.Submission.Reservation.Parent == nil ||
		*restored.Submission.Reservation.Parent != (executionattempt.Parent{
			ExecutionID: original.Submission.Reservation.ExecutionID, Relationship: "retry",
		}) || restored.RestoredUnits == nil || *restored.RestoredUnits != 0 {
		t.Fatalf("before restore reused ownership or lost exact original retry lineage: %+v, %v", restored, err)
	}
}

func TestSubmissionJoinedTerminalFailureRetainsBeforeButNeverFinalizedCheckpoint(t *testing.T) {
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	writeFailure := errors.New("controlled joined terminal write failure")
	result, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
				executionledger.Reservation, runnerterminal.Envelope,
			) error {
				return writeFailure
			},
		},
	})
	if !errors.Is(err, writeFailure) || factories != 1 || result.Submission.EngineCalls != 1 ||
		result.BeforeDigest == "" || result.FinalizedDigest != "" || result.Submission.Completion != nil ||
		result.Status != "submission_error" || result.RemainingWork != nil {
		t.Fatalf("joined terminal failure became finalized evidence or success: %+v, %v", result, err)
	}

}

func TestResumeRejectsPresentInvalidSourceAndChangedCompatibilityBeforeSubmission(t *testing.T) {
	for _, control := range []string{"corrupt", "missing-publication", "runner-revision", "model", "timeout", "conflicting-parent"} {
		t.Run(control, func(t *testing.T) {
			store := backend(t)
			factories := 0
			service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
				factories++
				return execution.NewMockEngine(config.ModelID), nil
			})
			paused, err := service.Submit(context.Background(), fixture(t), store, Hooks{
				Published: func(context.Context, Reference) error { return errors.New("stop after stored before boundary") },
			})
			if err == nil || paused.BeforeDigest == "" || paused.Submission.Reservation == nil {
				t.Fatalf("negative control did not retain an actual before boundary: %+v, %v", paused, err)
			}
			wire := fixture(t)
			switch control {
			case "runner-revision":
				service.revision = strings.Repeat("d", 40)
			case "model":
				wire = bytes.Replace(wire, []byte("model: mock-model"), []byte("model: changed-mock-model"), 1)
				if bytes.Equal(wire, fixture(t)) {
					t.Fatal("model control did not change captured native config")
				}
			case "timeout":
				wire = bytes.Replace(wire, []byte(`"timeout_ms":60000`), []byte(`"timeout_ms":60001`), 1)
				if bytes.Equal(wire, fixture(t)) {
					t.Fatal("timeout control did not change actual native request")
				}
			case "conflicting-parent":
				request, err := runnerunit.ParseSubmission(wire)
				if err != nil {
					t.Fatal(err)
				}
				request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct", Parent: &executionattempt.Parent{
					ExecutionID: paused.Submission.Reservation.ExecutionID, Relationship: "branch",
				}}
				wire, err = json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
			default:
				checkpoint, err := executioncheckpoint.Load(context.Background(), store, paused.BeforeDigest)
				if err != nil {
					t.Fatal(err)
				}
				record, err := executionledger.EncodeCompletion(executionledger.Completion{
					Kind: "waza.execution-completion", SchemaVersion: "1.0", ExecutionID: checkpoint.ExecutionID,
					ReservationDigest: checkpoint.ReservationDigest, ManifestDigest: strings.Repeat("e", 64),
				})
				if err != nil {
					t.Fatal(err)
				}
				if control == "corrupt" {
					record = []byte("actual-corrupt-completion-control")
				}
				namespace, err := artifactmanifest.NewNamespace(store, artifactmanifest.ExecutionLedgerNamespaceV1)
				if err != nil {
					t.Fatal(err)
				}
				if created, err := namespace.Create(context.Background(), "completion/"+checkpoint.ExecutionID, record); !created || err != nil {
					t.Fatalf("present completion control was not written: %t, %v", created, err)
				}
			}
			rejected, err := service.Resume(context.Background(), wire, store, paused.BeforeDigest, Hooks{})
			if err == nil || factories != 0 || rejected.Submission != nil ||
				rejected.RestoredUnits != nil || rejected.RemainingWork != nil {
				t.Fatalf("invalid resume reached actual submission or fabricated progress: %+v, %v", rejected, err)
			}
			if control == "missing-publication" && !errors.Is(err, artifactmanifest.ErrMissing) {
				t.Fatalf("missing-publication control did not reach real source evidence read: %v", err)
			}
			if control == "runner-revision" || control == "model" || control == "timeout" {
				field := "work"
				if control == "runner-revision" {
					field = "runner_revision"
				}
				if rejected.CompatibilityResult != "incompatible" || len(rejected.CompatibilityDifferences) != 1 ||
					rejected.CompatibilityDifferences[0] != field {
					t.Fatalf("actual differing compatibility field was hidden or included captured values: %+v", rejected)
				}
			}
		})
	}
}

func TestBeforeResumeAcceptsExplicitExactRetryParent(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	paused, err := service.Submit(context.Background(), fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return errors.New("stop after before publication") },
	})
	if err == nil || paused.BeforeDigest == "" {
		t.Fatal("explicit-parent control did not retain a before checkpoint")
	}
	request, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct", Parent: &executionattempt.Parent{
		ExecutionID: paused.Submission.Reservation.ExecutionID, Relationship: "retry",
	}}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Resume(context.Background(), wire, store, paused.BeforeDigest, Hooks{})
	if err != nil || result.Submission == nil || result.Submission.Reservation.Parent == nil ||
		*result.Submission.Reservation.Parent != *request.Policy.Parent || result.Submission.EngineCalls != 1 {
		t.Fatalf("valid explicit original retry parent was rejected or changed: %+v, %v", result, err)
	}
}

func TestFrozenRequestAndFinalizedPublicationCallbackFailure(t *testing.T) {
	store := backend(t)
	wire := fixture(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	callbackFailure := errors.New("actual stored finalized reference could not be delivered")
	result, err := service.Submit(context.Background(), wire, store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Reserved: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator,
				reservation executionledger.Reservation,
			) error {
				wire[0] = 'X'
				reservation.Bytes[0] = 'Y'
				reservation.Attempt.Binding.Identity.Run = "mutated-embedding-copy"
				return nil
			},
		},
		Published: func(_ context.Context, ref Reference) error {
			if ref.Boundary == executioncheckpoint.FinalizedUnit {
				return callbackFailure
			}
			return nil
		},
	})
	if !errors.Is(err, callbackFailure) || factories != 1 || result.Status != "checkpoint_error" ||
		result.ExitCode != 2 || result.Submission.Status != "completed" ||
		result.Submission.Completion == nil || result.FinalizedDigest == "" ||
		result.Submission.Terminal.Outcome != runnerterminal.Success || result.RemainingWork != nil {
		t.Fatalf("frozen request changed or delivery failure rewrote original completion: %+v, %v", result, err)
	}
	checkpoint, err := executioncheckpoint.Load(context.Background(), store, result.FinalizedDigest)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := executionledger.New(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executioncheckpoint.ReadStoredFinalized(context.Background(), ledger, *checkpoint, checkpoint.Compatibility); err != nil {
		t.Fatalf("delivery failure destroyed actual finalized source evidence: %v", err)
	}
}

func TestFinalizedRestoreHonorsExplicitExactSourceAndRejectsDistinctPolicy(t *testing.T) {
	store := backend(t)
	factories := 0
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnerunit.ParseSubmission(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "distinct"}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := service.Resume(context.Background(), wire, store, completed.FinalizedDigest, Hooks{})
	if !errors.Is(err, ErrPolicy) || factories != 1 || rejected.Submission != nil {
		t.Fatalf("explicit distinct policy was silently converted to source reuse: %+v, %v", rejected, err)
	}
	request.Policy = &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
		ExecutionID:    completed.Submission.Reservation.ExecutionID,
		ManifestDigest: completed.Submission.Completion.ManifestDigest,
	}}
	wire, err = json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.Resume(context.Background(), wire, store, completed.FinalizedDigest, Hooks{})
	if err != nil || factories != 1 || reused.Submission.EngineCalls != 0 ||
		*reused.Submission.Decision.Source != *request.Policy.Source {
		t.Fatalf("valid explicit original source was rejected or changed: %+v, %v", reused, err)
	}
	if _, err := NewService(nil, strings.Repeat("c", 40)); err == nil {
		t.Fatal("missing owning runner was silently substituted")
	}
}
