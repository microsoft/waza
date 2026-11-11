package manifestaggregate

import (
	"encoding/json"
	"flag"
	"os"
	"testing"

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
			Inputs    []Input `json:"inputs"`
			Canonical string  `json:"canonical"`
			Forged    string  `json:"forged"`
		}{[]Input{report.Members[0].Input, report.Members[1].Input}, string(canonical), string(invalid)})
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
