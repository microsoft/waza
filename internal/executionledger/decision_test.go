package executionledger

import (
	"context"
	"testing"
)

func TestDistinctDecisionSeparatesReservedFromPossiblyExecuted(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	creator, reserved := reserveFixture(t, ledger, binding)
	if started, err := ledger.HasDistinctDecision(ctx, reserved.Attempt.ExecutionID); err != nil || started {
		t.Fatalf("reservation alone became execution evidence: %v %v", started, err)
	}
	if _, err := ledger.Distinct(ctx, creator); err != nil {
		t.Fatal(err)
	}
	if started, err := ledger.HasDistinctDecision(ctx, reserved.Attempt.ExecutionID); err != nil || !started {
		t.Fatalf("persisted dispatch decision was ignored: %v %v", started, err)
	}
	_, independent := reserveFixture(t, ledger, binding)
	if started, err := ledger.HasDistinctDecision(ctx, independent.Attempt.ExecutionID); err != nil || started {
		t.Fatalf("other execution's decision marked an independent reservation: %v %v", started, err)
	}
}

func TestDistinctDecisionDiscoveryRejectsIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	ledger, _, _, binding := ownerFixture(t)
	_, reserved := reserveFixture(t, ledger, binding)
	original := ledger.records
	ledger.records = truncatedDecisionRecords{records: original}
	if started, err := ledger.HasDistinctDecision(ctx, reserved.Attempt.ExecutionID); err == nil || started {
		t.Fatalf("truncated discovery proved absence: %v %v", started, err)
	}
}

type truncatedDecisionRecords struct{ records }

func (r truncatedDecisionRecords) ListUnverified(context.Context) ([]string, bool, error) {
	return []string{}, true, nil
}
