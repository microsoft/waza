package snapshot

import (
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestCaptureOriginPreservesValidatedLineage(t *testing.T) {
	for _, tt := range []struct {
		attempts int
		prior    string
	}{{0, "unknown"}, {1, "none"}, {3, "not_preserved"}} {
		in := CaptureInput{EvalID: "eval", Task: &models.TestCase{TestID: "task"},
			Run: &models.RunResult{RunNumber: 2, Attempts: tt.attempts}}
		origin, err := CaptureOrigin(in)
		require.NoError(t, err)
		require.Equal(t, models.EvidenceOrigin{EvalID: "eval", TaskID: "task",
			RunNumber: 2, AttemptCount: tt.attempts, PriorAttempts: tt.prior}, origin)
		snap, err := Capture(in)
		require.NoError(t, err)
		require.Equal(t, snap.Evidence.Origin, origin)
	}
}

func TestCaptureOriginRejectsUnavailableOrUnsafeIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*CaptureInput)
		want   string
	}{
		{"missing-run", func(in *CaptureInput) { in.Run = nil }, "requires Run"},
		{"missing-task", func(in *CaptureInput) { in.Task = nil }, "requires Task"},
		{"empty-eval", func(in *CaptureInput) { in.EvalID = "" }, "nonempty eval ID"},
		{"empty-task", func(in *CaptureInput) { in.Task.TestID = "" }, "nonempty task ID"},
		{"sensitive-eval", func(in *CaptureInput) { in.EvalID = "identity@example.com" }, "eval ID requires redaction"},
		{"sensitive-task", func(in *CaptureInput) { in.Task.TestID = "identity@example.com" }, "task ID requires redaction"},
		{"invalid-policy", func(in *CaptureInput) {
			in.Policy = &Policy{Rules: []RedactionRule{{Name: "rule", Pattern: "["}}}
		}, "invalid redaction policy"},
		{"sensitive-rule-name", func(in *CaptureInput) {
			in.Policy = DefaultPolicy()
			in.Policy.Rules = append(in.Policy.Rules, RedactionRule{Name: "identity@example.com", Pattern: "marker"})
		}, "redaction rule name requires redaction"},
		{"custom-rejection", func(in *CaptureInput) {
			in.Policy = &Policy{Rules: []RedactionRule{{Name: "identity", Pattern: "task"}}}
		}, "task ID requires redaction"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := CaptureInput{EvalID: "eval", Task: &models.TestCase{TestID: "task"},
				Run: &models.RunResult{RunNumber: 2, Attempts: 3}}
			tt.change(&in)
			origin, err := CaptureOrigin(in)
			require.ErrorContains(t, err, tt.want)
			require.NotContains(t, err.Error(), "identity@example.com")
			require.Empty(t, origin.EvalID)
			require.Empty(t, origin.TaskID)
			if in.Run != nil {
				require.Equal(t, 2, origin.RunNumber)
				require.Equal(t, 3, origin.AttemptCount)
				require.Equal(t, "not_preserved", origin.PriorAttempts)
			} else {
				require.Equal(t, models.EvidenceOrigin{PriorAttempts: "unknown"}, origin)
			}
		})
	}
}

func TestCaptureOriginClonesSharedPolicy(t *testing.T) {
	policy := DefaultPolicy()
	policy.RedactString("existing@example.com")
	count, rules := policy.MatchCount(), policy.MatchedRules()
	in := CaptureInput{EvalID: "eval", Task: &models.TestCase{TestID: "task"},
		Run: &models.RunResult{RunNumber: 1, Attempts: 1}, Policy: policy}
	var wg sync.WaitGroup
	results := make(chan error, 16)
	for range 16 {
		wg.Go(func() {
			_, err := CaptureOrigin(in)
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	require.Equal(t, count, policy.MatchCount())
	require.Equal(t, rules, policy.MatchedRules())
}

func TestOrdinaryCaptureRetainsOptionalIdentityCompatibility(t *testing.T) {
	snap, err := Capture(CaptureInput{Task: &models.TestCase{}, Run: &models.RunResult{}})
	require.NoError(t, err)
	require.Empty(t, snap.Evidence.Origin.EvalID)
	require.Empty(t, snap.Evidence.Origin.TaskID)
	require.Equal(t, "unknown", snap.Evidence.Origin.PriorAttempts)
}
