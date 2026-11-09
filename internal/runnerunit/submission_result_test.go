package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func TestSubmissionResultRejectsOriginalInvalidTerminalUTF8(t *testing.T) {
	request := fixture(t)
	originalTask := request.Input.Task
	request.Input.Task = strings.Replace(originalTask, "- hello", "- impossible-output-control", 1)
	if originalTask == request.Input.Task {
		t.Fatal("terminal error fixture did not change actual grader input")
	}
	result, err := NewService(nil).Submit(t.Context(), submissionWire(t, request, nil), submissionBackend(t), SubmissionHooks{})
	if err != nil || len(result.Terminal.Diagnostics) == 0 {
		t.Fatalf("fixture did not produce actual failed-check diagnostics: %+v %v", result, err)
	}
	result.Terminal.Diagnostics[0].Message = "\uFFFD"
	if _, err := EncodeSubmissionResult(result); err != nil {
		t.Fatalf("genuine valid replacement character rejected: %v", err)
	}
	result.Terminal.Diagnostics[0].Message = string([]byte{0xff})
	if _, err := runnerterminal.Encode(*result.Terminal, result.Reservation.Binding.TerminalBinding()); err == nil {
		t.Fatal("invalid original terminal control was not selected")
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseSubmissionResult(normalized); err != nil {
		t.Fatalf("fixture did not demonstrate pre-validation UTF-8 normalization: %v", err)
	}
	if data, err := EncodeSubmissionResult(result); data != nil || err == nil {
		t.Fatalf("wrapper repaired invalid original terminal UTF-8: %s %v", data, err)
	}
}

func TestSubmissionResultCodecChecksActualProducerAndSourceContext(t *testing.T) {
	backend := submissionBackend(t)
	service := NewService(nil)
	request := fixture(t)
	completed, err := service.Submit(context.Background(), submissionWire(t, request, nil), backend, SubmissionHooks{})
	if err != nil {
		t.Fatal(err)
	}
	completedBytes, err := EncodeSubmissionResult(completed)
	if err != nil {
		t.Fatalf("actual completed producer violates output profile: %v", err)
	}
	source := executionattempt.Source{ExecutionID: completed.Reservation.ExecutionID, ManifestDigest: completed.Completion.ManifestDigest}
	request.Selection.Attempt++
	reused, err := service.Submit(context.Background(), submissionWire(t, request, &SubmissionPolicy{Mode: "reuse", Source: &source}), backend, SubmissionHooks{})
	if err != nil {
		t.Fatal(err)
	}
	reusedBytes, err := EncodeSubmissionResult(reused)
	if err != nil {
		t.Fatalf("actual alternate-ordinal reuse violates output profile: %v", err)
	}
	for _, data := range [][]byte{completedBytes, reusedBytes} {
		parsed, err := ParseSubmissionResult(data)
		if err != nil || parsed.Completion == nil || parsed.Terminal == nil {
			t.Fatalf("actual source declaration lost on strict read: %+v %v", parsed, err)
		}
	}
	for _, control := range []string{"reuse-new-calls", "retag-terminal", "reservation-hash", "manifest-source", "caller-work", "distinct-id", "original-binding-run", "original-binding-work", "caller-mode", "caller-graders", "caller-task"} {
		t.Run(control, func(t *testing.T) {
			data := reusedBytes
			if control == "distinct-id" {
				data = completedBytes
			}
			result, err := ParseSubmissionResult(data)
			if err != nil {
				t.Fatal(err)
			}
			switch control {
			case "reuse-new-calls":
				result.EngineCalls = 1
			case "retag-terminal":
				result.Terminal.Selection.Attempt++
			case "reservation-hash":
				result.Completion.ReservationDigest = strings.Repeat("0", 64)
			case "manifest-source":
				result.Decision.Source.ManifestDigest = strings.Repeat("0", 64)
			case "caller-work":
				result.Caller.Trial = "trial-" + strings.Repeat("0", 64)
			case "distinct-id":
				result.Decision.ExecutionID = "exec-" + strings.Repeat("0", 64)
			case "original-binding-run":
				result.Reservation.Binding.Identity.Run = "run-" + strings.Repeat("0", 64)
				result.Terminal.Identity.Run = result.Reservation.Binding.Identity.Run
			case "original-binding-work":
				result.Reservation.Binding.Identity.Trial = "trial-" + strings.Repeat("0", 64)
				result.Terminal.Identity.Trial = result.Reservation.Binding.Identity.Trial
			case "caller-mode":
				result.Decision.Binding.ExecutionMode = "copilot"
			case "caller-graders":
				result.Decision.Binding.GraderIDs = []string{"different-grader"}
			case "caller-task":
				result.Decision.Binding.Selection.Task = "different-task"
			}
			mutated, err := json.Marshal(result)
			if err != nil || bytes.Equal(mutated, data) {
				t.Fatal("invalid output control did not mutate original receipt")
			}
			parsed, err := ParseSubmissionResult(mutated)
			if parsed != nil || err == nil {
				t.Fatalf("inconsistent original/source context admitted: %+v %v", parsed, err)
			}
			if (control == "original-binding-run" || control == "original-binding-work") &&
				!strings.Contains(err.Error(), "logical run/work contradicts original binding") {
				t.Fatalf("original binding control rejected for another reason: %v", err)
			}
		})
	}
	reused.Report = nil
	if _, err := EncodeSubmissionResult(reused); err != nil {
		t.Fatalf("unavailable historical report was invented or required: %v", err)
	}
}

func TestSubmissionResultCodecPreservesRejectedAndCanceledPhases(t *testing.T) {
	backend := submissionBackend(t)
	service := NewService(nil)
	rejected, err := service.Submit(context.Background(), []byte(`{}`), backend, SubmissionHooks{})
	if err == nil {
		t.Fatal("invalid input did not select rejection")
	}
	if _, err := EncodeSubmissionResult(rejected); err != nil {
		t.Fatalf("actual rejection violates output profile: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled, err := service.Submit(ctx, submissionWire(t, fixture(t), nil), backend, SubmissionHooks{
		Reserved: func(context.Context, *executionledger.Ledger, executionledger.Creator, executionledger.Reservation) error {
			cancel()
			return nil
		},
	})
	if err != nil || canceled.ExitCode != 3 || canceled.EngineCalls != 0 {
		t.Fatalf("canceled producer control failed: %+v %v", canceled, err)
	}
	if _, err := EncodeSubmissionResult(canceled); err != nil {
		t.Fatalf("zero-call canceled source violates profile: %v", err)
	}
}

func TestSubmissionResultCodecRejectsOriginalWireRepair(t *testing.T) {
	backend := submissionBackend(t)
	result, err := NewService(nil).Submit(context.Background(), submissionWire(t, fixture(t), nil), backend, SubmissionHooks{})
	if err != nil {
		t.Fatal(err)
	}
	base, err := EncodeSubmissionResult(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		bytes.Replace(base, []byte(`"engine_calls":1`), []byte(`"engine_calls":1.5`), 1),
		bytes.Replace(base, []byte(`"engine_calls":1`), []byte(`"engine_calls":null`), 1),
		bytes.Replace(base, []byte(`"engine_calls":1`), []byte(`"engine_calls":false`), 1),
		bytes.Replace(base, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"1.0","schema_version":"1.0"`), 1),
		bytes.Replace(base, []byte(`"caller":{`), []byte(`"unknown":true,"caller":{`), 1),
	} {
		if bytes.Equal(data, base) || !json.Valid(data) {
			t.Fatal("strict original output control did not select mutated valid JSON")
		}
		if parsed, err := ParseSubmissionResult(data); parsed != nil || err == nil {
			t.Fatalf("invalid original output repaired: %+v %v", parsed, err)
		}
	}
}
