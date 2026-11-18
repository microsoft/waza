package executionledger

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/microsoft/waza/internal/executionattempt"
)

// HasDistinctDecision reports whether execution may have begun, not whether an
// engine actually ran. Absence is admitted only from complete discovery; callers
// must separately ensure the original worker has stopped before recovery.
func (l *Ledger) HasDistinctDecision(ctx context.Context, id string) (bool, error) {
	reserved, err := l.ReadReservation(ctx, id)
	if err != nil {
		return false, err
	}
	keys, truncated, err := l.records.ListUnverified(ctx)
	if err != nil {
		return false, fmt.Errorf("discovering execution decisions: %w", err)
	}
	if truncated {
		return false, fmt.Errorf("%w: execution decision discovery is truncated; absence is unavailable", ErrContext)
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, "decision/") {
			continue
		}
		data, err := l.records.ReadUnverified(ctx, key)
		if err != nil {
			return false, fmt.Errorf("reading execution decision: %w", err)
		}
		decision, err := executionattempt.ParseDecision(data)
		if err != nil {
			return false, err
		}
		canonical, err := executionattempt.EncodeDecision(*decision)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(data, canonical) || key != "decision/"+decision.DecisionID {
			return false, fmt.Errorf("%w: execution decision is not canonical or key-bound", ErrContext)
		}
		if decision.Policy != "distinct" || decision.ExecutionID != id {
			continue
		}
		original, actual := reserved.Attempt.Binding, decision.Binding
		if decision.Logical != reserved.Attempt.Logical || original.Identity != actual.Identity ||
			original.Selection != actual.Selection || original.ExecutionMode != actual.ExecutionMode ||
			!slices.Equal(original.GraderIDs, actual.GraderIDs) {
			return false, fmt.Errorf("%w: distinct decision contradicts its original reservation", ErrContext)
		}
		return true, nil
	}
	return false, nil
}
