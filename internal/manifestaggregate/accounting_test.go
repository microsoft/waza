package manifestaggregate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSavedReportAccountingAndWireBoundary(t *testing.T) {
	_, sources, _ := setup(t)
	report, err := Build(t.Context(), sources, []Input{{Manifest: strings.Repeat("a", 64)}}, DefaultLimits())
	require.ErrorIs(t, err, ErrIncomplete)
	canonical, err := Encode(*report)
	require.NoError(t, err)
	for _, control := range []struct {
		name   string
		change func(*Report)
		valid  bool
	}{
		{"valid incomplete", func(*Report) {}, true},
		{"omitted disguised complete", func(r *Report) { r.State = "complete" }, false},
		{"usage execution count differs", func(r *Report) { r.Usage.Executions = 1 }, false},
		{"conflicting priority retained", func(r *Report) {
			r.State, r.Members[0].State = "conflicting", "conflicting"
			r.Counts.MemberStates = map[string]int{"conflicting": 1}
		}, true},
		{"conflicting disguised incomplete", func(r *Report) {
			r.Members[0].State = "conflicting"
			r.Counts.MemberStates = map[string]int{"conflicting": 1}
		}, false},
		{"incomplete cannot claim complete usage", func(r *Report) {
			r.Members[0].Execution = "exec-" + strings.Repeat("b", 64)
			r.Counts.VerifiedExecutions, r.Usage.Executions = 1, 1
			r.Usage.State, r.Usage.Recorded, r.Usage.Diagnostics = "complete", &models.UsageStats{}, []string{}
		}, false},
	} {
		t.Run(control.name, func(t *testing.T) {
			var changed Report
			require.NoError(t, json.Unmarshal(canonical, &changed))
			control.change(&changed)
			wire, err := json.Marshal(changed)
			require.NoError(t, err)
			_, err = Parse(wire)
			if control.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalid)
			}
		})
	}
	exact := append([]byte(strings.Repeat(" ", MaxReportBytes-len(canonical))), canonical...)
	require.Len(t, exact, MaxReportBytes)
	_, err = Parse(exact)
	require.NoError(t, err)
	_, err = Parse(append(exact, ' '))
	require.ErrorIs(t, err, ErrInvalid)
}
