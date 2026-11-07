package runnerunit

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
)

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
	for _, control := range []string{"reuse-new-calls", "retag-terminal", "reservation-hash", "manifest-source", "caller-work", "distinct-id"} {
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
			}
			mutated, err := json.Marshal(result)
			if err != nil || bytes.Equal(mutated, data) {
				t.Fatal("invalid output control did not mutate original receipt")
			}
			if parsed, err := ParseSubmissionResult(mutated); parsed != nil || err == nil {
				t.Fatalf("inconsistent original/source context admitted: %+v %v", parsed, err)
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
