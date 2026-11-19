package executionledger_test

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
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func TestCompletionRejectsAnotherExecutionReservationArtifact(t *testing.T) {
	ctx := t.Context()
	request, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: request,
	})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	calls := 0
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		calls++
		return execution.NewMockEngine(config.ModelID), nil
	})
	original, err := service.Submit(ctx, data, backend, runnerunit.SubmissionHooks{})
	if err != nil || original.Completion == nil || calls != 1 {
		t.Fatalf("actual original execution fixture failed: %+v %v", original, err)
	}
	ledger, err := executionledger.New(backend)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ledger.Reserve(ctx, original.Reservation.Binding, nil)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := ledger.Reservation(other)
	if err != nil || reserved.Attempt.ExecutionID == original.Reservation.ExecutionID {
		t.Fatalf("fixture did not reserve independent execution: %+v %v", reserved, err)
	}
	if completed, err := ledger.Complete(ctx, other, original.Completion.ManifestDigest); completed != nil ||
		!errors.Is(err, executionledger.ErrContext) {
		t.Errorf("different execution adopted original reservation artifact: %+v %v", completed, err)
	}
	completion, err := executionledger.EncodeCompletion(executionledger.Completion{
		Kind: "waza.execution-completion", SchemaVersion: "1.0",
		ExecutionID: reserved.Attempt.ExecutionID, ReservationDigest: reserved.Digest,
		ManifestDigest: original.Completion.ManifestDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an existing inconsistent completion, independently of Complete.
	if _, err := namespace.Create(ctx, "completion/"+reserved.Attempt.ExecutionID, completion); err != nil {
		t.Fatal(err)
	}
	source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: original.Completion.ManifestDigest}
	if completed, err := ledger.ReadCompleted(ctx, source); completed != nil || !errors.Is(err, executionledger.ErrContext) {
		t.Errorf("read admitted contradictory reservation artifact: %+v %v", completed, err)
	}
	data, err = json.Marshal(runnerunit.SubmissionRequest{
		Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: request,
		Policy: &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source},
	})
	if err != nil {
		t.Fatal(err)
	}
	reused, err := service.Submit(ctx, data, backend, runnerunit.SubmissionHooks{})
	if err == nil || reused.Status != "submission_error" || reused.Decision != nil || calls != 1 {
		t.Errorf("production reuse admitted contradictory lineage: %+v calls=%d %v", reused, calls, err)
	}
	source.ExecutionID = original.Reservation.ExecutionID
	if _, err := ledger.ReadCompleted(ctx, source); err != nil {
		t.Fatalf("original immutable completion was damaged: %v", err)
	}
}

func TestPureReuseAdmissionRetainsPublishedFailure(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnercontract.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	originalTask := request.Input.Task
	request.Input.Task = strings.Replace(originalTask, "    - hello\n", "    - impossible-output-control\n", 1)
	if request.Input.Task == originalTask {
		t.Fatal("failure control did not mutate actual task expectation")
	}
	wire, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	factoryCalls := 0
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		factoryCalls++
		return execution.NewMockEngine(config.ModelID), nil
	})
	envelope, binding := service.RunTerminal(ctx, wire)
	if binding == nil || envelope.Outcome != runnerterminal.EvaluationFailure || envelope.Observation == nil ||
		envelope.Observation.Status != "failed" || envelope.Observation.Attempts != request.Selection.Attempt {
		t.Fatalf("production mock failure fixture did not exercise native failure: %+v", envelope)
	}
	terminalBytes, err := runnerterminal.Encode(envelope, binding)
	if err != nil {
		t.Fatal(err)
	}
	attempt := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical: executionattempt.LogicalFrom(*binding.Identity), ExecutionID: "exec-" + strings.Repeat("a", 64),
		Binding: artifactmanifest.Binding{Identity: *binding.Identity, Selection: binding.Selection,
			ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs},
	}
	reservation, err := executionattempt.EncodeAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	manifest := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: attempt.Binding, TerminalArtifact: "terminal.json",
		Artifacts: []artifactmanifest.Artifact{{Name: "terminal.json", SHA256: artifactmanifest.Hash(terminalBytes),
			SizeBytes: int64(len(terminalBytes)), MediaType: "application/json"}},
	}
	manifestBytes, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := backend.Close(); err != nil {
			t.Error(err)
		}
	})
	store := artifactmanifest.NewStore(backend)
	if err := store.Put(ctx, manifest.Artifacts[0], terminalBytes); err != nil {
		t.Fatal(err)
	}
	digest, err := store.Create(ctx, manifestBytes)
	if err != nil {
		t.Fatal(err)
	}
	unfinalized, err := store.Inspect(ctx, digest)
	if err != nil || unfinalized.Publication != "unfinalized" || unfinalized.Integrity != "verified" {
		t.Fatalf("content verification must not imply final publication: %+v, %v", unfinalized, err)
	}
	final, err := store.Finalize(ctx, digest)
	if err != nil || final.Publication != "finalized" || final.Integrity != "verified" {
		t.Fatalf("published recorded failure: %+v, %v", final, err)
	}
	completion := executionledger.Completion{
		Kind: "waza.execution-completion", SchemaVersion: executionledger.Version, ExecutionID: attempt.ExecutionID,
		ReservationDigest: artifactmanifest.Hash(reservation), ManifestDigest: digest,
	}
	completionBytes, err := executionledger.EncodeCompletion(completion)
	if err != nil {
		t.Fatal(err)
	}
	request.Selection.Attempt++
	callerIdentity, err := runneridentity.Derive(request)
	if err != nil {
		t.Fatal(err)
	}
	if executionattempt.LogicalFrom(*callerIdentity) != attempt.Logical || callerIdentity.Attempt == attempt.Binding.Identity.Attempt {
		t.Fatal("caller must share real logical work but differ in native ordinal/identity")
	}
	callerBinding := attempt.Binding
	callerBinding.Identity, callerBinding.Selection = *callerIdentity, request.Selection
	decision := executionattempt.Decision{
		Kind: "waza.duplicate-decision", SchemaVersion: executionattempt.Version,
		DecisionID: "decision-" + strings.Repeat("d", 64), Logical: attempt.Logical, Binding: callerBinding,
		Policy: "reuse", Outcome: "reused",
		Source: &executionattempt.Source{ExecutionID: attempt.ExecutionID, ManifestDigest: digest},
	}
	if err := executionledger.ValidateReuseContext(decision, reservation, completionBytes, manifestBytes); err != nil {
		t.Fatal(err)
	}
	originalTerminal, err := backend.Get(ctx, "blob-"+manifest.Artifacts[0].SHA256, manifest.Artifacts[0].SizeBytes)
	if err != nil || !bytes.Equal(originalTerminal, terminalBytes) {
		t.Fatalf("published terminal was changed: %v", err)
	}
	retained, err := runnerterminal.Validate(originalTerminal, attempt.Binding.TerminalBinding())
	if err != nil || retained.Outcome != runnerterminal.EvaluationFailure || retained.ExitCode != 1 ||
		retained.Observation.Attempts != attempt.Binding.Selection.Attempt {
		t.Fatalf("recorded failure/ordinal changed: %+v, %v", retained, err)
	}
	if _, err := runnerterminal.Validate(originalTerminal, callerBinding.TerminalBinding()); !errors.Is(err, runnerterminal.ErrInvalid) {
		t.Fatalf("source terminal incorrectly retagged to caller ordinal: %v", err)
	}
	t.Run("actual-owner-completion-and-reuse", func(t *testing.T) {
		ledger, err := executionledger.New(backend)
		if err != nil {
			t.Fatal(err)
		}
		creator, err := ledger.Reserve(ctx, attempt.Binding, nil)
		if err != nil {
			t.Fatal(err)
		}
		reserved, err := ledger.Reservation(creator)
		if err != nil {
			t.Fatal(err)
		}
		completed, err := ledger.Complete(ctx, creator, digest)
		if err != nil || completed.Completion.ExecutionID != reserved.Attempt.ExecutionID {
			t.Fatalf("actual owner completion failed: %+v %v", completed, err)
		}
		source := executionattempt.Source{ExecutionID: reserved.Attempt.ExecutionID, ManifestDigest: digest}
		decision, reused, err := ledger.Reuse(ctx, callerBinding, source)
		if err != nil || decision.ExecutionID != "" || decision.Source.ExecutionID != source.ExecutionID ||
			reused.Reservation.Attempt.Binding.Selection.Attempt != attempt.Binding.Selection.Attempt ||
			factoryCalls != 1 || !bytes.Equal(reused.Publication.ArtifactBytes["terminal.json"], terminalBytes) {
			t.Fatalf("actual reuse reran/repaired/retagged source: %+v %+v calls=%d %v", decision, reused, factoryCalls, err)
		}
		retained, err := runnerterminal.Validate(reused.Publication.ArtifactBytes["terminal.json"], attempt.Binding.TerminalBinding())
		if err != nil || retained.Outcome != runnerterminal.EvaluationFailure || retained.ExitCode != 1 {
			t.Fatalf("actual source failure became reuse success: %+v %v", retained, err)
		}
	})
}
