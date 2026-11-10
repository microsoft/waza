package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func TestResultRejectsOriginalInvalidReservationUTF8(t *testing.T) {
	result, err := adapter(t, nil).Submit(t.Context(), fixture(t), backend(t), Hooks{
		Published: func(context.Context, Reference) error { return errors.New("stop with reservation") },
	})
	if err == nil || result.Submission == nil || result.Submission.Reservation == nil ||
		result.Submission.Decision != nil || result.Submission.Terminal != nil {
		t.Fatalf("fixture did not produce actual reservation-only result: %+v %v", result, err)
	}
	result.Submission.Reservation.Binding.Selection.Task = "\uFFFD"
	if _, err := EncodeResult(result); err != nil {
		t.Fatalf("valid replacement-character reservation rejected: %v", err)
	}
	result.Submission.Reservation.Binding.Selection.Task = string([]byte{0xff})
	if _, err := executionattempt.EncodeAttempt(*result.Submission.Reservation); err == nil {
		t.Fatal("invalid original reservation control was not selected")
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseResult(normalized); err != nil {
		t.Fatalf("fixture did not demonstrate original reservation normalization: %v", err)
	}
	if raw, err := EncodeResult(result); raw != nil || err == nil {
		t.Fatalf("checkpoint wrapper normalized invalid original reservation: %s %v", raw, err)
	}
}

func TestResultRejectsOriginalInvalidTerminalUTF8(t *testing.T) {
	service := adapter(t, nil)
	wire := fixture(t)
	changed := bytes.Replace(wire, []byte("- hello"), []byte("- impossible-output-control"), 1)
	if bytes.Equal(changed, wire) {
		t.Fatal("terminal error fixture did not change actual grader input")
	}
	result, err := service.Submit(t.Context(), changed, backend(t), Hooks{})
	if err != nil || len(result.Submission.Terminal.Diagnostics) == 0 {
		t.Fatalf("fixture did not produce actual failed-check diagnostics: %+v %v", result, err)
	}
	result.Submission.Terminal.Diagnostics[0].Message = "\uFFFD"
	if _, err := EncodeResult(result); err != nil {
		t.Fatalf("genuine valid replacement character rejected: %v", err)
	}
	result.Submission.Terminal.Diagnostics[0].Message = string([]byte{0xff})
	if _, err := runnerterminal.Encode(*result.Submission.Terminal, result.Submission.Reservation.Binding.TerminalBinding()); err == nil {
		t.Fatal("invalid original terminal control was not selected")
	}
	normalized, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseResult(normalized); err != nil {
		t.Fatalf("fixture did not demonstrate pre-validation UTF-8 normalization: %v", err)
	}
	if data, err := EncodeResult(result); data != nil || err == nil {
		t.Fatalf("checkpoint wrapper repaired invalid original terminal UTF-8: %s %v", data, err)
	}
}

func TestResultCodecAdmitsActualProductionOperationShapes(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	ctx := context.Background()
	completed, err := service.Submit(ctx, fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.Resume(ctx, fixture(t), store, completed.FinalizedDigest, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := service.Resume(ctx, fixture(t), store, completed.BeforeDigest, Hooks{})
	if err == nil {
		t.Fatal("actual old-before control was not stale")
	}
	paused, err := service.Submit(ctx, fixture(t), store, Hooks{
		Published: func(context.Context, Reference) error { return errors.New("controlled publication delivery failure") },
	})
	if err == nil || paused.BeforeDigest == "" {
		t.Fatal("actual callback failure did not retain a partial submission")
	}
	child, err := service.Resume(ctx, fixture(t), store, paused.BeforeDigest, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	publicationFailed, err := service.Submit(ctx, fixture(t), store, Hooks{
		Published: func(_ context.Context, ref Reference) error {
			if ref.Boundary == "finalized-unit" {
				return errors.New("controlled final reference delivery failure")
			}
			return nil
		},
	})
	if err == nil || publicationFailed.Submission.Completion == nil {
		t.Fatal("actual finalized delivery failure did not retain original completion")
	}
	service.revision = strings.Repeat("d", 40)
	incompatible, err := service.Resume(ctx, fixture(t), store, completed.FinalizedDigest, Hooks{})
	if err == nil || len(incompatible.CompatibilityDifferences) == 0 {
		t.Fatal("actual compatibility rejection control did not expose field names")
	}
	results := []Result{completed, reused, stale, paused, child, publicationFailed, incompatible}
	for _, changed := range []Result{reused, child} {
		source := *changed.Source
		source.Compatibility.Work = "trial-" + strings.Repeat("e", 64)
		changed.Source = &source
		if _, err := EncodeResult(changed); err == nil {
			t.Fatal("result codec admitted a different source work for reuse or fresh child")
		}
	}
	for i, original := range results {
		data, err := EncodeResult(original)
		if err != nil {
			t.Fatalf("production result %d (%s) did not encode: %v", i, original.Status, err)
		}
		parsed, err := ParseResult(data)
		if err != nil {
			t.Fatal(err)
		}
		again, err := EncodeResult(*parsed)
		if err != nil || !bytes.Equal(again, data) {
			t.Fatalf("production result %d lost declaration presence or canonical writer output: %v", i, err)
		}
	}
}

func TestResultCodecRejectsNearMissWireAndCoherentSourceSubstitution(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	completed, err := service.Submit(context.Background(), fixture(t), store, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.Resume(context.Background(), fixture(t), store, completed.FinalizedDigest, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeResult(reused)
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []string{"null-progress", "omitted-progress", "string-progress", "wrong-new-calls", "wrong-status", "unknown", "duplicate", "retag-source", "source-work"} {
		t.Run(control, func(t *testing.T) {
			changed := bytes.Clone(data)
			switch control {
			case "null-progress":
				changed = bytes.Replace(changed, []byte(`"restored_units":1`), []byte(`"restored_units":null`), 1)
			case "omitted-progress":
				changed = bytes.Replace(changed, []byte(`"restored_units":1,`), nil, 1)
			case "string-progress":
				changed = bytes.Replace(changed, []byte(`"restored_units":1`), []byte(`"restored_units":"1"`), 1)
			case "wrong-new-calls":
				changed = bytes.Replace(changed, []byte(`"engine_calls":0`), []byte(`"engine_calls":1`), 1)
			case "wrong-status":
				changed = bytes.Replace(changed, []byte(`"status":"reused"`), []byte(`"status":"completed"`), 1)
			case "unknown":
				changed = append([]byte(`{"unadmitted":true,`), changed[1:]...)
			case "duplicate":
				changed = append([]byte(`{"status":"reused",`), changed[1:]...)
			case "retag-source":
				copy := reused
				source := *reused.Source
				source.ReservationDigest = strings.Repeat("e", 64)
				copy.Source = &source
				changed, err = json.Marshal(copy)
				if err != nil {
					t.Fatal(err)
				}
			case "source-work":
				copy := reused
				source := *reused.Source
				source.Compatibility.Work = "trial-" + strings.Repeat("e", 64)
				copy.Source = &source
				changed, err = json.Marshal(copy)
				if err != nil {
					t.Fatal(err)
				}
			}
			if bytes.Equal(changed, data) {
				t.Fatal("negative control did not change actual operation output")
			}
			if parsed, err := ParseResult(changed); parsed != nil || err == nil {
				t.Fatalf("invalid actual operation output was admitted: %+v, %v", parsed, err)
			}
		})
	}
}
