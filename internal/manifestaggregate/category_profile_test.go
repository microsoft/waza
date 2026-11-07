package manifestaggregate

import (
	"fmt"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/stretchr/testify/require"
)

func TestAggregateNativeProfileRejectsInventedCountsAndUsageAdmission(t *testing.T) {
	for _, name := range []string{"observed-count", "declared-state", "native-tally", "usage-view", "category-order"} {
		t.Run(name, func(t *testing.T) {
			backend := &memoryBackend{objects: map[string][]byte{}}
			origin, sidecar, _ := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, new(float64)))
			report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, sidecar))
			require.NoError(t, err)
			candidate := &report.Evidence.Members[0].Candidates[0]
			require.Len(t, candidate.Categories, 6)
			require.Empty(t, report.Evidence.CategoryStateCounts)
			switch name {
			case "observed-count":
				zero := "0"
				candidate.Categories[0].ObservedCount = &zero
			case "declared-state":
				candidate.Categories[0].DeclaredState = evidencestate.Conflicting
			case "native-tally":
				report.Evidence.CategoryStateCounts[evidencestate.Usage] = map[evidencestate.State]int{evidencestate.Present: 1}
			case "usage-view":
				candidate.Usage.Verification = "source-content-and-recorded-usage-shape-only"
			case "category-order":
				candidate.Categories[0], candidate.Categories[1] = candidate.Categories[1], candidate.Categories[0]
			}
			_, err = Encode(report)
			require.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestAggregateLaterSidecarVerificationCannotReplaceFailedOriginPass(t *testing.T) {
	backend := &memoryBackend{objects: map[string][]byte{}}
	origin, sidecar, _ := evidenceFixture(t, backend, 1, evidencestate.Present, recordedUsage(t, new(float64)))
	reads := 0
	backend.getError = func(key string) error {
		if key == "manifest-"+origin {
			reads++
			if reads == 1 {
				return fmt.Errorf("first origin read: %w", artifactmanifest.ErrMissing)
			}
		}
		return nil
	}
	report, err := Aggregate(t.Context(), backend, evidenceRequest(origin, sidecar))
	require.ErrorIs(t, err, ErrIncomplete)
	require.Equal(t, 2, reads, "the independent sidecar pass saw the now-readable origin")
	require.Equal(t, "missing", report.Members[0].State)
	candidate := report.Evidence.Members[0].Candidates[0]
	require.Equal(t, "unavailable", candidate.State)
	require.Nil(t, candidate.Usage)
	require.Empty(t, candidate.Categories)
	require.Empty(t, candidate.Declarations)
	require.Empty(t, report.Evidence.CategoryStateCounts)
	require.Empty(t, report.Evidence.DeclarationStateCounts)
	require.Equal(t, "origin_admission_withheld", candidate.Diagnostics[0].Code)
	_, err = Encode(report)
	require.NoError(t, err)
}
