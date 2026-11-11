package manifestaggregate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
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

func TestSavedReportRejectsExecutionIDsBoundToDifferentManifests(t *testing.T) {
	backend, sources, _ := setup(t)
	ctx := t.Context()
	first := publish(t, backend, manifestfixture.RecordedNonzero(1))
	second := publish(t, backend, manifestfixture.RecordedNonzero(2))
	inspection, err := evidencestate.Inspect(ctx, backend, first.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: second.Manifest, Relationship: evidencestate.Aggregation}}
	otherView, err := evidencestate.Publish(ctx, backend, view)
	require.NoError(t, err)
	third := Input{Manifest: first.Manifest, Evidence: otherView.EvidenceSHA256}

	report, err := Build(ctx, sources, []Input{first, second, third}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, 2, report.Counts.VerifiedExecutions)
	var sameManifestExecution string
	sameManifestViews := 0
	for _, member := range report.Members {
		if member.Manifest == first.Manifest && member.Execution != "" {
			if sameManifestExecution != "" {
				require.Equal(t, sameManifestExecution, member.Execution)
			}
			sameManifestExecution = member.Execution
			sameManifestViews++
		}
	}
	require.NotEmpty(t, sameManifestExecution)
	require.Equal(t, 2, sameManifestViews)
	require.Len(t, report.Members, 3)
	require.Equal(t, 3, report.Counts.Members)
	canonical, err := Encode(*report)
	require.NoError(t, err, "multiple sidecar views of one manifest remain valid")
	_, err = Parse(canonical)
	require.NoError(t, err)

	var forged Report
	require.NoError(t, json.Unmarshal(canonical, &forged))
	for index := range forged.Members {
		if forged.Members[index].Manifest == second.Manifest {
			forged.Members[index].Execution = sameManifestExecution
		}
	}
	forged.Counts.VerifiedExecutions = 1
	forged.Usage.Executions = 1
	contradictory, err := json.Marshal(forged)
	require.NoError(t, err)
	_, err = Parse(contradictory)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Encode(forged)
	require.ErrorIs(t, err, ErrInvalid)
}
