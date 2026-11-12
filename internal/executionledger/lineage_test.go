package executionledger

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
)

type truncatedRecords struct{ records }

func (t truncatedRecords) ListUnverified(ctx context.Context) ([]string, bool, error) {
	keys, _, err := t.records.ListUnverified(ctx)
	return keys, true, err
}

func lineageFor(t *testing.T, ledger *Ledger, binding artifactmanifest.Binding) *Lineage {
	t.Helper()
	lineage, err := ledger.Lineage(context.Background(), executionattempt.LogicalFrom(binding.Identity))
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeLineage(*lineage)
	if err != nil {
		t.Fatalf("lineage wire invalid: %v", err)
	}
	if _, err := ParseLineage(data); err != nil {
		t.Fatal(err)
	}
	return lineage
}

func diagnosticCodes(lineage *Lineage) []string {
	codes := []string{}
	for _, diagnostic := range lineage.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	return codes
}

func TestLineageOrdersRetriesAndRecordsReuseWithoutCollapsing(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	capability, root := reserveFixture(t, ledger, binding)
	if _, err := ledger.Distinct(ctx, capability); err != nil {
		t.Fatal(err)
	}
	digest := ownerPublication(t, ledger, binding, true)
	if _, err := ledger.Complete(ctx, capability, digest); err != nil {
		t.Fatal(err)
	}
	child, err := ledger.Reserve(ctx, binding, &executionattempt.Parent{ExecutionID: root.Attempt.ExecutionID, Relationship: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	childReserved, err := ledger.Reservation(child)
	if err != nil {
		t.Fatal(err)
	}
	caller := binding
	caller.Selection.Attempt++
	caller.Identity.Attempt = "attempt-" + artifactmanifest.Hash([]byte("later-ordinal"))
	caller.Identity.CanonicalDigest = artifactmanifest.Hash([]byte("later-wire"))
	reuse, _, err := ledger.Reuse(ctx, caller, executionattempt.Source{ExecutionID: root.Attempt.ExecutionID, ManifestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}

	other := binding
	other.Identity.Trial = "trial-" + artifactmanifest.Hash([]byte("other-work"))
	if _, err := ledger.Reserve(ctx, other, nil); err != nil {
		t.Fatal(err)
	}

	lineage := lineageFor(t, ledger, binding)
	if len(lineage.Attempts) != 2 {
		t.Fatalf("expected root and retry only: %+v", lineage.Attempts)
	}
	first, second := lineage.Attempts[0], lineage.Attempts[1]
	if first.ExecutionID != root.Attempt.ExecutionID || first.Order != 1 || first.Depth != 0 || first.Status != AttemptCompleted ||
		first.Terminal == nil || first.Terminal.ExitCode != 1 || first.ManifestDigest != digest || first.DispatchDecision == "" ||
		len(first.ReusedBy) != 1 || first.ReusedBy[0] != reuse.DecisionID {
		t.Fatalf("root attempt collapsed or misreported: %+v", first)
	}
	if second.ExecutionID != childReserved.Attempt.ExecutionID || second.Order != 2 || second.Depth != 1 ||
		second.Parent == nil || second.Parent.Relationship != "retry" || second.Status != AttemptReserved {
		t.Fatalf("retry child misreported: %+v", second)
	}
	if len(lineage.Decisions) != 2 {
		t.Fatalf("expected distinct and reuse decisions: %+v", lineage.Decisions)
	}
	for _, decision := range lineage.Decisions {
		if decision.Policy == "reuse" && (decision.Source == nil || decision.Source.ExecutionID != root.Attempt.ExecutionID ||
			decision.NativeAttempt != caller.Selection.Attempt || decision.ExecutionID != "") {
			t.Fatalf("reuse decision retagged source: %+v", decision)
		}
	}
	if len(lineage.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %+v", lineage.Diagnostics)
	}
}

func TestLineageReportsConcurrentAndUnfinishedAttempts(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	first, _ := reserveFixture(t, ledger, binding)
	reserveFixture(t, ledger, binding)
	if _, err := ledger.Distinct(ctx, first); err != nil {
		t.Fatal(err)
	}
	lineage := lineageFor(t, ledger, binding)
	codes := diagnosticCodes(lineage)
	if len(lineage.Attempts) != 2 || len(codes) != 2 || codes[0] != "concurrent_distinct_attempts" || codes[1] != "dispatched_without_completion" {
		t.Fatalf("concurrent writer diagnostics missing: %+v", lineage)
	}
	if len(lineage.Diagnostics[0].ExecutionIDs) != 2 {
		t.Fatalf("concurrent diagnostic must name both attempts: %+v", lineage.Diagnostics[0])
	}
}

func TestLineageFailsClosedOnTruncationAndCorruptRecords(t *testing.T) {
	ctx := context.Background()
	ledger, _, directory, binding := ownerFixture(t)
	capability, _ := reserveFixture(t, ledger, binding)
	logical := executionattempt.LogicalFrom(binding.Identity)

	truncated := *ledger
	truncated.records = truncatedRecords{ledger.records}
	if got, err := truncated.Lineage(ctx, logical); got != nil || !errors.Is(err, ErrContext) {
		t.Fatalf("truncated discovery admitted: %+v %v", got, err)
	}

	digest := ownerPublication(t, ledger, binding, true)
	if _, err := ledger.Complete(ctx, capability, digest); err != nil {
		t.Fatal(err)
	}
	// Tamper with the stored terminal blob: the completion stays recorded but
	// must be surfaced as unverified rather than hidden or repaired.
	tampered := false
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || tampered {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Contains(data, []byte(`"kind":"waza.runner-terminal"`)) {
			tampered = true
			if err := os.Chmod(path, 0o600); err != nil {
				return err
			}
			return os.WriteFile(path, bytes.Replace(data, []byte(`"exit_code":1`), []byte(`"exit_code":0`), 1), 0o600)
		}
		return nil
	})
	if err != nil || !tampered {
		t.Fatalf("terminal blob not found: %v", err)
	}
	lineage := lineageFor(t, ledger, binding)
	if lineage.Attempts[0].Status != AttemptCompletionUnverified || lineage.Attempts[0].Terminal != nil {
		t.Fatalf("tampered completion presented as verified: %+v", lineage.Attempts[0])
	}
	if codes := diagnosticCodes(lineage); len(codes) != 1 || codes[0] != "completion_unverified" {
		t.Fatalf("unverified completion diagnostic missing: %v", codes)
	}
}

func forgeRecord(t *testing.T, ledger *Ledger, key string, data []byte) {
	t.Helper()
	if _, err := ledger.records.Create(context.Background(), key, data); err != nil {
		t.Fatal(err)
	}
}

func TestLineageFailsClosedOnForgedContradictions(t *testing.T) {
	ctx := context.Background()
	t.Run("parent cycle", func(t *testing.T) {
		ledger, _, _, binding := ownerFixture(t)
		_, root := reserveFixture(t, ledger, binding)
		a, b := "exec-"+artifactmanifest.Hash([]byte("a")), "exec-"+artifactmanifest.Hash([]byte("b"))
		for id, parent := range map[string]string{a: b, b: a} {
			attempt := root.Attempt
			attempt.ExecutionID = id
			attempt.Parent = &executionattempt.Parent{ExecutionID: parent, Relationship: "retry"}
			data, err := executionattempt.EncodeAttempt(attempt)
			if err != nil {
				t.Fatal(err)
			}
			forgeRecord(t, ledger, "reservation/"+id, data)
		}
		if got, err := ledger.Lineage(ctx, root.Attempt.Logical); got != nil || !errors.Is(err, ErrContext) {
			t.Fatalf("cyclic parents hidden: %+v %v", got, err)
		}
	})
	t.Run("distinct decision contradicts reservation", func(t *testing.T) {
		ledger, _, _, binding := ownerFixture(t)
		capability, root := reserveFixture(t, ledger, binding)
		decision, err := ledger.Distinct(ctx, capability)
		if err != nil {
			t.Fatal(err)
		}
		forged := *decision
		forged.DecisionID = "decision-" + artifactmanifest.Hash([]byte("forged"))
		forged.Binding.Selection.Attempt++
		data, err := executionattempt.EncodeDecision(forged)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecord(t, ledger, "decision/"+forged.DecisionID, data)
		if got, err := ledger.Lineage(ctx, root.Attempt.Logical); got != nil || !errors.Is(err, ErrContext) {
			t.Fatalf("contradictory dispatch admitted: %+v %v", got, err)
		}
	})
	t.Run("reuse decision without source completion", func(t *testing.T) {
		ledger, _, _, binding := ownerFixture(t)
		_, root := reserveFixture(t, ledger, binding)
		forged := executionattempt.Decision{Kind: "waza.duplicate-decision", SchemaVersion: "1.0",
			DecisionID: "decision-" + artifactmanifest.Hash([]byte("reuse")), Logical: root.Attempt.Logical,
			Binding: root.Attempt.Binding, Policy: "reuse", Outcome: "reused",
			Source: &executionattempt.Source{ExecutionID: root.Attempt.ExecutionID, ManifestDigest: artifactmanifest.Hash([]byte("m"))}}
		data, err := executionattempt.EncodeDecision(forged)
		if err != nil {
			t.Fatal(err)
		}
		forgeRecord(t, ledger, "decision/"+forged.DecisionID, data)
		if got, err := ledger.Lineage(ctx, root.Attempt.Logical); got != nil || !errors.Is(err, ErrContext) {
			t.Fatalf("unrecorded reuse source linked: %+v %v", got, err)
		}
	})
}
