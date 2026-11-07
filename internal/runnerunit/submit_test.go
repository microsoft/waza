package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func submissionWire(t *testing.T, request runnercontract.Request, policy *SubmissionPolicy) []byte {
	t.Helper()
	data, err := json.Marshal(SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: wire(t, request), Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func submissionBackend(t *testing.T) *artifactmanifest.Local {
	t.Helper()
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	return backend
}

func TestSubmitReservesBeforeFactoryAndRetainsObservedFailureOnReuse(t *testing.T) {
	ctx := context.Background()
	backend := submissionBackend(t)
	request := fixture(t)
	old := request.Input.Task
	request.Input.Task = strings.Replace(old, "    - hello\n", "    - impossible-output-control\n", 1)
	if request.Input.Task == old {
		t.Fatal("failed-check fixture did not mutate actual captured input")
	}
	factoryCalls, reservedCalls, terminalCalls := 0, 0, 0
	var original executionledger.Reservation
	service := NewService(func(config models.Config) (execution.AgentEngine, error) {
		factoryCalls++
		if reservedCalls != 1 {
			t.Fatal("runtime constructed before admitted exclusive reservation")
		}
		return execution.NewMockEngine(config.ModelID), nil
	})
	hooks := SubmissionHooks{
		Reserved: func(ctx context.Context, ledger *executionledger.Ledger, creator executionledger.Creator, reservation executionledger.Reservation) error {
			reservedCalls++
			if err := ledger.ValidateCreator(ctx, creator); err != nil {
				return err
			}
			var err error
			snapshot, err := ledger.Reservation(creator)
			if err != nil {
				return err
			}
			original = *snapshot
			reservation.Bytes[0] = 'X'
			reservation.Attempt.Binding.Identity.Run = "callback-mutated"
			return nil
		},
		Terminal: func(_ context.Context, _ *executionledger.Ledger, _ executionledger.Creator, reservation executionledger.Reservation, envelope runnerterminal.Envelope) error {
			terminalCalls++
			if factoryCalls != 1 || envelope.Outcome != runnerterminal.EvaluationFailure {
				t.Fatal("joined terminal hook did not receive actual failed evaluation")
			}
			reservation.Bytes[0] = 'Y'
			envelope.Observation.Status = "passed"
			envelope.Identity.Run = "callback-mutated"
			return nil
		},
	}
	result, err := service.Submit(ctx, submissionWire(t, request, nil), backend, hooks)
	if err != nil || result.Status != "completed" || result.ExitCode != 1 || result.EngineCalls != 1 ||
		factoryCalls != 1 || reservedCalls != 1 || terminalCalls != 1 || result.Completion == nil ||
		result.Reservation.ExecutionID != original.Attempt.ExecutionID || result.Terminal.Observation.Status != "failed" {
		t.Fatalf("actual failed submission lost source/ownership/status: %+v factories=%d hooks=%d/%d %v", result, factoryCalls, reservedCalls, terminalCalls, err)
	}
	source := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	originalReport, originalTerminal := bytes.Clone(result.Report), *result.Terminal
	request.Selection.Attempt++
	reused, err := service.Submit(ctx, submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, hooks)
	if err != nil || reused.Status != "reused" || reused.ExitCode != 1 || reused.EngineCalls != 0 ||
		factoryCalls != 1 || reservedCalls != 1 || terminalCalls != 1 || reused.Decision.ExecutionID != "" ||
		reused.Decision.Source.ExecutionID != source.ExecutionID || reused.Terminal.Selection.Attempt != originalTerminal.Selection.Attempt ||
		reused.Caller.Attempt == originalTerminal.Identity.Attempt || !bytes.Equal(reused.Report, originalReport) {
		t.Fatalf("explicit source reuse reran/retagged/rebilled original: %+v %v", reused, err)
	}
	another, err := service.Submit(ctx, submissionWire(t, request, &SubmissionPolicy{Mode: "distinct"}), backend, SubmissionHooks{})
	if err != nil || another.Reservation.ExecutionID == source.ExecutionID || factoryCalls != 2 {
		t.Fatalf("default distinct execution became implicit reuse: %+v %v", another, err)
	}
}

func TestSubmitCancellationAfterReservationRetainsTerminalWithoutRuntime(t *testing.T) {
	backend := submissionBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factoryCalls := 0
	service := NewService(func(models.Config) (execution.AgentEngine, error) {
		factoryCalls++
		return nil, errors.New("runtime must not be constructed")
	})
	hooks := SubmissionHooks{Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
		cancel()
		return nil
	}}
	result, err := service.Submit(ctx, submissionWire(t, fixture(t), nil), backend, hooks)
	if err != nil || result.ExitCode != 3 || result.Status != "completed" || result.Completion == nil ||
		result.Terminal.Outcome != runnerterminal.Canceled || factoryCalls != 0 || result.EngineCalls != 0 {
		t.Fatalf("owned cancellation became success/error/reexecution: %+v factories=%d %v", result, factoryCalls, err)
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	source, err := ledger.ReadCompleted(context.Background(), executionattempt.Source{
		ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest})
	if err != nil || source.Completion.ExecutionID != result.Reservation.ExecutionID {
		t.Fatalf("canceled source was not actually finalized/completed: %+v %v", source, err)
	}
	reference := executionattempt.Source{ExecutionID: result.Reservation.ExecutionID, ManifestDigest: result.Completion.ManifestDigest}
	reused, err := service.Submit(context.Background(), submissionWire(t, fixture(t), &SubmissionPolicy{Mode: "reuse", Source: &reference}), backend, hooks)
	if err != nil || reused.Status != "reused" || reused.ExitCode != 3 || reused.Terminal.Outcome != runnerterminal.Canceled ||
		factoryCalls != 0 || !bytes.Equal(reused.Report, result.Report) {
		t.Fatalf("actual canceled source was repaired or rerun: %+v %v", reused, err)
	}
}

func TestSubmitErrorsDoNotProduceCompletionOrConstructRuntime(t *testing.T) {
	for _, kind := range []string{"bad-wrapper", "invalid-selection", "backend-unavailable", "observer-error", "unfinished-source", "native-override"} {
		t.Run(kind, func(t *testing.T) {
			backend := submissionBackend(t)
			var supplied artifactmanifest.Backend = backend
			factoryCalls := 0
			service := NewService(func(models.Config) (execution.AgentEngine, error) {
				factoryCalls++
				return nil, errors.New("runtime must not be constructed")
			})
			request := fixture(t)
			hooks := SubmissionHooks{}
			var policy *SubmissionPolicy
			switch kind {
			case "invalid-selection":
				request.Selection.Attempt = 99
			case "backend-unavailable":
				supplied = nil
			case "observer-error":
				hooks.Reserved = func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
					return errors.New("observer control failure")
				}
			case "unfinished-source":
				policy = &SubmissionPolicy{Mode: "reuse", Source: &executionattempt.Source{
					ExecutionID: "exec-" + strings.Repeat("f", 64), ManifestDigest: artifactmanifest.Hash(nil)}}
			case "native-override":
				request.RequiredCapabilities = []string{"exact-unit", "inline.copilot-text"}
				request.Input.Eval = strings.Replace(request.Input.Eval, "executor: mock", "executor: copilot-sdk", 1)
				request.Input.Eval = strings.Replace(request.Input.Eval, "model: mock-model", "model: owned-control-model", 1)
				request.AllowPaidExecution = true
				t.Setenv("COPILOT_CLI_PATH", "uncontrolled-native")
			}
			data := submissionWire(t, request, policy)
			if kind == "bad-wrapper" {
				data = []byte(`{"kind":"waza.execution-submission","schema_version":"1.0","request":null}`)
			}
			result, err := service.Submit(context.Background(), data, supplied, hooks)
			if err == nil || result.ExitCode == 0 || result.Completion != nil || factoryCalls != 0 || result.EngineCalls != 0 {
				t.Fatalf("failed submission constructed runtime or completed: %+v factories=%d %v", result, factoryCalls, err)
			}
		})
	}
}

func TestSubmissionCodecPreservesOriginalStrictWire(t *testing.T) {
	base := submissionWire(t, fixture(t), nil)
	for _, data := range [][]byte{
		bytes.Replace(base, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":null`), 1),
		bytes.Replace(base, []byte(`"request":`), []byte(`"unknown":true,"request":`), 1),
		append(bytes.Clone(base[:len(base)-1]), []byte(`,"policy":null}`)...),
		append(bytes.Clone(base[:len(base)-1]), []byte(`,"policy":{"mode":"reuse","source":null}}`)...),
		append(bytes.Clone(base[:len(base)-1]), []byte(`,"policy":{"mode":"distinct","parent":null}}`)...),
		bytes.Replace(base, []byte(`"trial":2`), []byte(`"trial":2.5`), 1),
	} {
		if bytes.Equal(data, base) || !json.Valid(data) {
			t.Fatal("invalid-wire control did not mutate a valid JSON object")
		}
		if got, err := ParseSubmission(data); got != nil || err == nil {
			t.Fatalf("invalid original wire repaired into valid wrapper: %+v %v", got, err)
		}
	}
	if parsed, err := ParseSubmission(base); err != nil || parsed.Policy != nil {
		t.Fatalf("omitted policy default profile changed: %+v %v", parsed, err)
	}
	if _, err := ParseSubmission(wire(t, fixture(t))); err == nil {
		t.Fatal("legacy request silently became a submission wrapper")
	}
}

func TestSubmitRejectsFinalizedReportContradictingOriginalVerdicts(t *testing.T) {
	ctx := context.Background()
	backend := submissionBackend(t)
	request := fixture(t)
	before := request.Input.Task
	request.Input.Task = strings.Replace(before, "    - hello\n", "    - impossible-output-control\n", 1)
	if request.Input.Task == before {
		t.Fatal("actual failed-input control was not selected")
	}
	service := NewService(nil)
	original, err := service.Submit(ctx, submissionWire(t, request, nil), backend, SubmissionHooks{})
	if err != nil || original.ExitCode != 1 || original.Terminal.Observation == nil {
		t.Fatalf("actual source did not produce failed observation: %+v %v", original, err)
	}
	var report Report
	if err := json.Unmarshal(original.Report, &report); err != nil {
		t.Fatal(err)
	}
	mutated := false
	for name, result := range report.Observation.Validations {
		if !result.Passed {
			result.Passed = true
			report.Observation.Validations[name] = result
			mutated = true
			break
		}
	}
	if !mutated {
		t.Fatal("source lacked a failing verdict to contradict")
	}
	fullReport, err := reportBytes(report)
	if err != nil {
		t.Fatal(err)
	}
	source := publishSubmissionReport(t, backend, original, fullReport)
	assertContradictoryReportRejected(t, backend, request, source)
}

func TestSubmitRejectsCompletedReportForOriginalCanceledTerminal(t *testing.T) {
	backend := submissionBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := NewService(nil)
	hooks := SubmissionHooks{Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
		cancel()
		return nil
	}}
	request := fixture(t)
	original, err := service.Submit(ctx, submissionWire(t, request, nil), backend, hooks)
	if err != nil || original.Terminal.Outcome != runnerterminal.Canceled || original.Terminal.Observation != nil {
		t.Fatalf("actual cancellation control did not select no-observation terminal: %+v %v", original, err)
	}
	var report Report
	if err := json.Unmarshal(original.Report, &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "execution_error" {
		t.Fatalf("unexpected original canceled report status %q", report.Status)
	}
	report.Status = "completed"
	fullReport, err := reportBytes(report)
	if err != nil {
		t.Fatal(err)
	}
	source := publishSubmissionReport(t, backend, original, fullReport)
	assertContradictoryReportRejected(t, backend, request, source)
}

func publishSubmissionReport(t *testing.T, backend artifactmanifest.Backend, original Submission, fullReport []byte) executionattempt.Source {
	t.Helper()
	ctx := context.Background()
	binding := original.Reservation.Binding
	terminalData, err := runnerterminal.Encode(*original.Terminal, binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	creator, err := ledger.Reserve(ctx, binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := artifactmanifest.NewStore(backend)
	manifest := artifactmanifest.Manifest{Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: binding, TerminalArtifact: "terminal.json", Artifacts: []artifactmanifest.Artifact{}}
	for _, object := range []struct {
		name string
		data []byte
	}{{"terminal.json", terminalData}, {"report.json", fullReport}} {
		artifact := artifactmanifest.Artifact{Name: object.name, SHA256: artifactmanifest.Hash(object.data),
			SizeBytes: int64(len(object.data)), MediaType: "application/json"}
		if err := store.Put(ctx, artifact, object.data); err != nil {
			t.Fatal(err)
		}
		manifest.Artifacts = append(manifest.Artifacts, artifact)
	}
	manifestData, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.Create(ctx, manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finalize(ctx, digest); err != nil {
		t.Fatal(err)
	}
	completed, err := ledger.Complete(ctx, creator, digest, "terminal.json", "report.json")
	if err != nil {
		t.Fatal(err)
	}
	return executionattempt.Source{ExecutionID: completed.Completion.ExecutionID, ManifestDigest: digest}
}

func assertContradictoryReportRejected(t *testing.T, backend artifactmanifest.Backend, request runnercontract.Request, source executionattempt.Source) {
	t.Helper()
	factories := 0
	reuser := NewService(func(models.Config) (execution.AgentEngine, error) {
		factories++
		return nil, errors.New("reuse must not construct runtime")
	})
	result, err := reuser.Submit(context.Background(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, SubmissionHooks{})
	if err == nil || result.Status != "submission_error" || result.Decision != nil || factories != 0 ||
		len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "source_report_invalid" {
		t.Fatalf("finalized but contradictory source became actionable reuse: %+v %v", result, err)
	}
}
