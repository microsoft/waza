package executionbudget

import (
	"fmt"
	"slices"
)

// Clone returns a deep copy of the report.
func (r *Report) Clone() *Report {
	if r == nil {
		return nil
	}
	copied := *r
	copied.Resources = make([]Usage, len(r.Resources))
	for i, row := range r.Resources {
		row.Limit, row.Used, row.Remaining = clonePtr(row.Limit), clonePtr(row.Used), clonePtr(row.Remaining)
		row.Exceeded = clonePtr(row.Exceeded)
		copied.Resources[i] = row
	}
	if r.Termination != nil {
		termination := *r.Termination
		copied.Termination = &termination
	}
	return &copied
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// TerminationOutcome is the terminal outcome an enforced termination produces.
func TerminationOutcome(resource Resource) string {
	if resource == WallTime {
		return "timeout"
	}
	return "cancelled" //nolint:misspell // wire value
}

// CheckTerminal verifies that a terminal budget report is internally
// consistent and agrees with the enclosing terminal outcome. The JSON schema
// checks shape only; these are the cross-field rules readers must enforce.
func (r *Report) CheckTerminal(outcome string) error {
	seen := map[Resource]Usage{}
	anyExceeded, anyUsed := false, false
	for _, row := range r.Resources {
		if !slices.Contains(Resources, row.Resource) {
			return fmt.Errorf("budget report lists unknown resource %q", row.Resource)
		}
		if _, duplicate := seen[row.Resource]; duplicate {
			return fmt.Errorf("budget report lists %s more than once", row.Resource)
		}
		seen[row.Resource] = row
		if err := r.checkRow(row); err != nil {
			return fmt.Errorf("budget report %s: %w", row.Resource, err)
		}
		anyUsed = anyUsed || row.Used != nil
		anyExceeded = anyExceeded || (row.Exceeded != nil && *row.Exceeded)
	}
	if len(seen) != len(Resources) {
		return fmt.Errorf("budget report must list each of the %d resources exactly once", len(Resources))
	}
	if r.Termination != nil {
		row := seen[r.Termination.Resource]
		if row.Enforcement != Enforced || row.Limit == nil || *row.Limit != r.Termination.Limit {
			return fmt.Errorf("budget termination %s does not match an enforced limit", r.Termination.Resource)
		}
		if r.Termination.Observed < r.Termination.Limit ||
			(r.Termination.Resource != WallTime && r.Termination.Observed == r.Termination.Limit) {
			return fmt.Errorf("budget termination %s observed %d does not exceed limit %d",
				r.Termination.Resource, r.Termination.Observed, r.Termination.Limit)
		}
		if err := r.Termination.checkUsage(row.Used); err != nil {
			return err
		}
	}
	switch r.State {
	case StateRejectedUnsupported:
		if outcome != "unsupported_capability" || anyUsed || r.Termination != nil || r.Unsupported != PolicyReject {
			return fmt.Errorf("budget state %s requires an unexecuted unsupported_capability outcome under the reject policy", r.State)
		}
	case StateNotStarted:
		if anyUsed || r.Termination != nil {
			return fmt.Errorf("budget state %s cannot carry usage or a termination", r.State)
		}
		if outcome == "success" || outcome == "evaluation_failure" {
			return fmt.Errorf("budget state %s contradicts completed outcome %s", r.State, outcome)
		}
	case StateWithinBudget:
		if anyExceeded || r.Termination != nil {
			return fmt.Errorf("budget state %s contradicts an exceeded resource or termination", r.State)
		}
	case StateExceeded:
		if !anyExceeded || r.Termination != nil {
			return fmt.Errorf("budget state %s requires an exceeded resource and no termination", r.State)
		}
	case StateTerminated:
		if r.Termination == nil {
			return fmt.Errorf("budget state %s requires a termination", r.State)
		}
		if want := TerminationOutcome(r.Termination.Resource); outcome != want {
			return fmt.Errorf("budget termination by %s requires outcome %s, not %s", r.Termination.Resource, want, outcome)
		}
	default:
		return fmt.Errorf("unknown budget state %q", r.State)
	}
	return nil
}

// checkUsage requires measured usage for the terminating resource. Counted
// usage includes the triggering start, so it is at least the observation. Wall
// time is reported as elapsed whole milliseconds, which may round below a
// deadline the meter records as observed == limit.
func (t *Termination) checkUsage(used *int64) error {
	if used == nil {
		return fmt.Errorf("budget termination %s has no measured usage", t.Resource)
	}
	consistent := *used >= t.Observed
	if t.Resource == WallTime {
		consistent = *used == t.Observed || (*used < t.Limit && t.Observed == t.Limit)
	}
	if !consistent {
		return fmt.Errorf("budget termination %s observed %d contradicts used %d", t.Resource, t.Observed, *used)
	}
	return nil
}

func (r *Report) checkRow(row Usage) error {
	if row.Used == nil {
		if row.Remaining != nil || row.Exceeded != nil {
			return fmt.Errorf("remaining/exceeded require measured usage")
		}
		return nil
	}
	if row.Measurement != Measured {
		return fmt.Errorf("usage is reported for an unmeasured resource")
	}
	if row.Limit == nil {
		if row.Remaining != nil || row.Exceeded != nil {
			return fmt.Errorf("remaining/exceeded require a limit")
		}
		return nil
	}
	if row.Remaining == nil || row.Exceeded == nil {
		return fmt.Errorf("a limited, measured resource must report remaining and exceeded")
	}
	if *row.Remaining != max(*row.Limit-*row.Used, 0) {
		return fmt.Errorf("remaining %d is not limit %d minus used %d", *row.Remaining, *row.Limit, *row.Used)
	}
	terminatedHere := r.Termination != nil && r.Termination.Resource == row.Resource
	if over := *row.Used > *row.Limit; *row.Exceeded != (over || terminatedHere) {
		return fmt.Errorf("exceeded=%t contradicts used %d and limit %d", *row.Exceeded, *row.Used, *row.Limit)
	}
	return nil
}
