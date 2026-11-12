package manifestaggregate

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var mixedIdentityOutput = flag.String("aggregate-mixed-identity-fixture-output", "", "Exclusively save producer-backed mixed identity caller controls")

func mixedIdentityReport(t *testing.T) (*Report, Usage) {
	t.Helper()
	backend, sources, _ := setup(t)
	qualified := publish(t, backend, manifestfixture.RecordedNonzero(1))
	known, err := Build(t.Context(), sources, []Input{qualified}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", known.Usage.State)
	mixed, err := Build(t.Context(), sources, []Input{qualified, {Manifest: qualified.Manifest}}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", mixed.State)
	require.Equal(t, 2, mixed.Counts.Members)
	require.Equal(t, 1, mixed.Counts.VerifiedExecutions)
	require.Empty(t, mixed.Members[0].Execution)
	require.NotEmpty(t, mixed.Members[1].Execution)
	require.Equal(t, "unavailable", mixed.Usage.State)
	require.Nil(t, mixed.Usage.Recorded)
	return mixed, known.Usage
}

func TestMixedIdentityPublicReportFixture(t *testing.T) {
	report, subtotal := mixedIdentityReport(t)
	canonical, err := Encode(*report)
	require.NoError(t, err)
	parsed, err := Parse(canonical)
	require.NoError(t, err)
	require.Equal(t, report, parsed)
	forged := *report
	forged.Usage = subtotal
	invalid, err := json.Marshal(forged)
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(invalid, &value))
	schema, err := reportSchema()
	require.NoError(t, err)
	require.NoError(t, schema.Validate(value), "negative differs only in a schema-valid usage claim")
	if *mixedIdentityOutput != "" {
		fixture, err := json.Marshal(struct {
			Inputs      []Input              `json:"inputs"`
			Canonical   string               `json:"canonical"`
			Forged      string               `json:"forged"`
			Eligibility []eligibilityFixture `json:"eligibility"`
		}{[]Input{report.Members[0].Input, report.Members[1].Input}, string(canonical), string(invalid), declaredUsageFixtures(t)})
		require.NoError(t, err)
		file, err := os.OpenFile(*mixedIdentityOutput, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		require.NoError(t, err)
		written, writeErr := file.Write(fixture)
		closeErr := file.Close()
		require.NoError(t, writeErr)
		require.NoError(t, closeErr)
		require.Equal(t, len(fixture), written)
	}
}

type eligibilityFixture struct {
	Name      string  `json:"name"`
	Inputs    []Input `json:"inputs"`
	Canonical string  `json:"canonical"`
	Forged    string  `json:"forged"`
}

func declaredUsageFixtures(t *testing.T) []eligibilityFixture {
	t.Helper()
	backend, sources, _ := setup(t)
	known := publish(t, backend, manifestfixture.RecordedNonzero(1))
	complete, err := Build(t.Context(), sources, []Input{known}, DefaultLimits())
	require.NoError(t, err)
	require.Equal(t, "complete", complete.Usage.State)
	incomplete := publish(t, backend, func(report *runnerunit.Report) {
		manifestfixture.RecordedNonzero(1)(report)
		report.Observation.Usage.AICredits = nil
	})
	unavailable := publish(t, backend, nil)
	inspection, err := evidencestate.Inspect(t.Context(), backend, known.Evidence)
	require.NoError(t, err)
	document := *inspection.Document
	document.Lineage = []evidencestate.Lineage{{Manifest: unavailable.Manifest, Relationship: evidencestate.Cache}}
	cached, err := evidencestate.Publish(t.Context(), backend, document)
	require.NoError(t, err)
	var fixtures []eligibilityFixture
	for _, control := range []struct {
		name       string
		inputs     []Input
		usageState evidencestate.State
	}{
		{"native incomplete", []Input{incomplete}, evidencestate.Incomplete},
		{"native unavailable", []Input{unavailable}, evidencestate.Unavailable},
		{"compatible present plus cached view", []Input{known, {Manifest: known.Manifest, Evidence: cached.EvidenceSHA256}}, evidencestate.Present},
	} {
		report, err := Build(t.Context(), sources, control.inputs, DefaultLimits())
		require.NoError(t, err)
		require.Equal(t, "complete", report.State)
		require.Equal(t, 1, report.Counts.VerifiedExecutions)
		for _, member := range report.Members {
			require.NotEmpty(t, member.Execution, "this negative must pass the existing identity guard")
			found := false
			for _, category := range member.Categories {
				if category.Category == evidencestate.Usage {
					found = true
					require.Equal(t, control.usageState, category.State)
				}
			}
			require.True(t, found)
		}
		require.Equal(t, "unavailable", report.Usage.State)
		require.Nil(t, report.Usage.Recorded)
		canonical, err := Encode(*report)
		require.NoError(t, err)
		report.Usage = complete.Usage
		forged, err := json.Marshal(report)
		require.NoError(t, err)
		var value any
		require.NoError(t, json.Unmarshal(forged, &value))
		schema, err := reportSchema()
		require.NoError(t, err)
		require.NoError(t, schema.Validate(value))
		fixtures = append(fixtures, eligibilityFixture{control.name, control.inputs, string(canonical), string(forged)})
	}
	recorded := complete.Usage.Recorded
	withoutCredits := *recorded
	withoutCredits.AICredits = nil
	complete.Usage.Recorded = &withoutCredits
	canonical, err := Encode(*complete)
	require.NoError(t, err)
	fixtures = append(fixtures, eligibilityFixture{"optional aggregate credits absent", []Input{known}, string(canonical), ""})
	withoutModels := *recorded
	withoutModels.ModelMetrics = nil
	complete.Usage.Recorded = &withoutModels
	canonical, err = Encode(*complete)
	require.NoError(t, err)
	fixtures = append(fixtures, eligibilityFixture{"optional aggregate attribution absent", []Input{known}, string(canonical), ""})
	zero := publish(t, backend, manifestfixture.RecordedZero)
	zeroReport, err := Build(t.Context(), sources, []Input{zero}, DefaultLimits())
	require.NoError(t, err)
	require.NotNil(t, zeroReport.Usage.Recorded)
	require.NotNil(t, zeroReport.Usage.Recorded.AICredits)
	require.Zero(t, *zeroReport.Usage.Recorded.AICredits)
	canonical, err = Encode(*zeroReport)
	require.NoError(t, err)
	fixtures = append(fixtures, eligibilityFixture{"recorded zero credits", []Input{zero}, string(canonical), ""})
	return fixtures
}

func TestSavedReportRejectsDeclaredUsageExclusions(t *testing.T) {
	for _, fixture := range declaredUsageFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			_, err := Parse([]byte(fixture.Canonical))
			require.NoError(t, err)
			if fixture.Forged == "" {
				return
			}
			_, err = Parse([]byte(fixture.Forged))
			assert.ErrorIs(t, err, ErrInvalid)
			var report Report
			require.NoError(t, json.Unmarshal([]byte(fixture.Forged), &report))
			_, err = Encode(report)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestSavedReportRejectsUnqualifiedUsageCategories(t *testing.T) {
	fixtures := declaredUsageFixtures(t)
	require.Equal(t, "optional aggregate credits absent", fixtures[3].Name)
	for _, change := range []string{"missing category", "cached category", "unverified", "empty count", "withheld declaration"} {
		t.Run(change, func(t *testing.T) {
			report, err := Parse([]byte(fixtures[3].Canonical))
			require.NoError(t, err)
			member := &report.Members[0]
			switch change {
			case "missing category":
				member.Categories = []Category{}
			case "cached category":
				member.Categories[0].Source = evidencestate.Cached
			default:
				for index := range member.Categories {
					category := &member.Categories[index]
					if category.Category != evidencestate.Usage {
						continue
					}
					switch change {
					case "unverified":
						category.Verification = "declared-state-only"
					case "empty count":
						category.ObservedCount = new(int64)
					case "withheld declaration":
						category.DeclaredState = evidencestate.Redacted
					}
				}
			}
			wire, err := json.Marshal(report)
			require.NoError(t, err)
			var value any
			require.NoError(t, json.Unmarshal(wire, &value))
			schema, err := reportSchema()
			require.NoError(t, err)
			require.NoError(t, schema.Validate(value))
			_, err = Parse(wire)
			assert.ErrorIs(t, err, ErrInvalid)
			_, err = Encode(*report)
			assert.ErrorIs(t, err, ErrInvalid)
		})
	}
}

func TestSavedReportRejectsCompleteUsageWithUnknownExecution(t *testing.T) {
	report, subtotal := mixedIdentityReport(t)
	report.Usage = subtotal
	wire, err := json.Marshal(report)
	require.NoError(t, err)
	_, err = Parse(wire)
	assert.ErrorIs(t, err, ErrInvalid, "saved reader must not admit a subset as the complete total")
	_, err = Encode(*report)
	assert.ErrorIs(t, err, ErrInvalid, "writer must apply the same admission rule")
}
