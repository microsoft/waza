package executionledger

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
)

func TestBeforeDispatchUsesOnlyFreshRetainedCreator(t *testing.T) {
	ledger, _, _, binding := ownerFixture(t)
	creator, _ := reserveFixture(t, ledger, binding)
	foreign, _, _, _ := ownerFixture(t)
	for _, control := range []struct {
		ledger  *Ledger
		creator Creator
	}{
		{ledger, Creator{}}, {foreign, creator}, {nil, creator},
	} {
		called := false
		err := control.ledger.PublishBeforeDispatch(t.Context(), control.creator, func(context.Context) error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrCapability) || called {
			t.Fatalf("invalid creator admitted publication: called=%v %v", called, err)
		}
	}
	ledger.records = truncatedDecisionRecords{records: ledger.records}
	copy := creator
	err := ledger.PublishBeforeDispatch(t.Context(), copy, func(context.Context) error {
		if creator.state.mu.TryLock() {
			creator.state.mu.Unlock()
			t.Fatal("publication does not retain the shared creator gate")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("fresh retained creator depended on global discovery: %v", err)
	}
}

func TestBeforeDispatchRejectsEveryPriorMutationAttempt(t *testing.T) {
	for _, operation := range []string{"distinct", "complete"} {
		for _, outcome := range []string{"success", "failed", "unknown"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				ledger, _, _, binding := ownerFixture(t)
				creator, _ := reserveFixture(t, ledger, binding)
				copy := creator
				digest := ownerPublication(t, ledger, binding, true)
				original := ledger.records
				injected := errors.New("provider response unavailable")
				creates := 0
				ledger.records = recordFault{records: original, create: func(ctx context.Context, key string, data []byte) (bool, error) {
					creates++
					if outcome == "failed" {
						return false, injected
					}
					created, err := original.Create(ctx, key, data)
					if outcome == "unknown" {
						return created, injected
					}
					return created, err
				}}
				var err error
				if operation == "distinct" {
					_, err = ledger.Distinct(t.Context(), creator)
				} else {
					_, err = ledger.Complete(t.Context(), creator, digest)
				}
				if creates != 1 || (err == nil) != (outcome == "success") {
					t.Fatalf("mutation control did not reach provider: creates=%d %v", creates, err)
				}
				ledger.records = original
				called := false
				err = ledger.PublishBeforeDispatch(t.Context(), copy, func(context.Context) error {
					called = true
					return nil
				})
				if err == nil || called {
					t.Fatalf("prior %s/%s admitted before publication: called=%v %v", operation, outcome, called, err)
				}
			})
		}
	}
}

func TestBeforeDispatchRequiresExactCompletionAbsence(t *testing.T) {
	denied := errors.New("completion read denied")
	for _, readErr := range []error{denied, errors.Join(artifactmanifest.ErrMissing, denied)} {
		ledger, _, _, binding := ownerFixture(t)
		creator, _ := reserveFixture(t, ledger, binding)
		original := ledger.records
		ledger.records = recordFault{records: original, read: func(ctx context.Context, key string) ([]byte, error) {
			if strings.HasPrefix(key, "completion/") {
				return nil, readErr
			}
			return original.ReadUnverified(ctx, key)
		}}
		called := false
		err := ledger.PublishBeforeDispatch(t.Context(), creator, func(context.Context) error {
			called = true
			return nil
		})
		if called || !errors.Is(err, denied) {
			t.Fatalf("unresolved completion became absence: called=%v %v", called, err)
		}
	}
}
