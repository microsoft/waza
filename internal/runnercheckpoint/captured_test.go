package runnercheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executioncheckpoint"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestCapturedRestoreWithholdsUnapprovedSourceWithoutLosingCompatibility(t *testing.T) {
	store := backend(t)
	factories := 0
	runner := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factories++
		return execution.NewMockEngine(config.ModelID), nil
	})
	original, err := NewService(runner, strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	created, err := original.SubmitCaptured(context.Background(), fixture(t), store, Hooks{})
	if err != nil || created.FinalizedDigest == "" {
		t.Fatalf("actual captured source did not finalize: %+v, %v", created, err)
	}
	current, err := NewService(runner, strings.Repeat("d", 40))
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := current.ResumeCaptured(context.Background(), fixture(t), store, created.FinalizedDigest, Hooks{})
	if !errors.Is(err, executioncheckpoint.ErrIncompatible) || factories != 1 ||
		rejected.Source != nil || !rejected.SourceWithheld || rejected.CompatibilityResult != "incompatible" ||
		len(rejected.CompatibilityDifferences) != 1 || rejected.CompatibilityDifferences[0] != "runner_revision" ||
		rejected.RestoredUnits != nil || rejected.RemainingWork != nil {
		t.Fatalf("public projection exposed source or changed known compatibility/progress: %+v, %v", rejected, err)
	}
	data, err := EncodeResult(rejected)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"source":`)) || !bytes.Contains(data, []byte(`"source_withheld":true`)) {
		t.Fatalf("source withholding was not explicit in actual encoded result: %s", data)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(data, []byte(`"source_withheld":true`), []byte(`"source_withheld":false`), 1),
		bytes.Replace(data, []byte(`"source_withheld":true`), []byte(`"source_withheld":null`), 1),
		bytes.Replace(data, []byte(`"source_withheld":true`), []byte(`"source_withheld":"true"`), 1),
		bytes.Replace(data, []byte(`"source_withheld":true`), []byte(`"source_withheld":1`), 1),
		bytes.Replace(data, []byte(`"source_withheld":true`), []byte(`"source_withheld":{}`), 1),
		bytes.Replace(data, []byte(`"source_withheld":true,`), nil, 1),
	} {
		if bytes.Equal(invalid, data) {
			t.Fatal("near-miss did not change actual withholding field")
		}
		if _, err := ParseResult(invalid); err == nil {
			t.Fatalf("invalid withholding became a valid incompatible response: %s", invalid)
		}
	}
	reused, err := original.ResumeCaptured(context.Background(), fixture(t), store, created.FinalizedDigest, Hooks{})
	if err != nil || reused.Source == nil || reused.SourceWithheld ||
		factories != 1 || reused.Submission.EngineCalls != 0 {
		t.Fatalf("approved original reuse was hidden or rerun: %+v, %v", reused, err)
	}
	contradiction := reused
	contradiction.SourceWithheld = true
	if _, err := EncodeResult(contradiction); err == nil {
		t.Fatal("withholding marker accepted simultaneous source or successful restore")
	}
	completedWithheld := created
	completedWithheld.SourceWithheld = true
	if _, err := EncodeResult(completedWithheld); err == nil {
		t.Fatal("withholding marker accepted successful creation without any source")
	}
}

func TestCapturedProjectionWithholdsOperatorErrorsAndHistoricalReports(t *testing.T) {
	store := backend(t)
	service := adapter(t, func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	operator := "private-operator-path-and-conversation-control"
	failed, err := service.SubmitCaptured(context.Background(), fixture(t), store, Hooks{
		Submission: runnerunit.SubmissionHooks{
			Terminal: func(context.Context, *executionledger.Ledger, executionledger.Creator,
				executionledger.Reservation, runnerterminal.Envelope,
			) error {
				return errors.New(operator)
			},
		},
	})
	if err == nil || failed.Submission == nil || failed.Submission.Completion != nil {
		t.Fatalf("joined error control did not fail actual publication: %+v, %v", failed, err)
	}
	data, err := EncodeResult(failed)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(operator)) {
		t.Fatalf("public operation serialized operator text: %s", data)
	}
	legacy := *failed.Submission
	legacy.Report = json.RawMessage(`{"private":"historical-report-control"}`)
	original := failed
	original.Submission = &legacy
	projected := projectCapturedResult(original, true, true)
	if len(projected.Submission.Report) != 0 || len(original.Submission.Report) == 0 {
		t.Fatal("public projection exposed report or mutated original source evidence")
	}
}
