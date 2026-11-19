package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/evidencestate"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/require"
)

func TestAggregateCLIReportsDegradedEvidenceAndRetryLineage(t *testing.T) {
	directory := t.TempDir()
	store := filepath.Join(directory, "store")
	require.NoError(t, os.Mkdir(store, 0o700))
	backend, err := artifactmanifest.OpenLocal(store)
	require.NoError(t, err)
	original := manifestfixture.PublishWith(t, backend, "../../examples/runner/unit.json", manifestfixture.Options{
		ChangeReport: func(report *runnerunit.Report) { report.Status = "execution_error" },
	})
	retry := manifestfixture.PublishWith(t, backend, "../../examples/runner/unit.json", manifestfixture.Options{
		ChangeReport: manifestfixture.RecordedNonzero(1),
		Withheld: []evidencestate.Category{evidencestate.Conversation, evidencestate.Tool, evidencestate.Grader,
			evidencestate.Reliability, evidencestate.Usage},
		Parent: &executionattempt.Parent{ExecutionID: original.Execution, Relationship: "retry"},
	})
	inspection, err := evidencestate.Inspect(t.Context(), backend, retry.Evidence)
	require.NoError(t, err)
	view := *inspection.Document
	view.Lineage = []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}}
	resumed, err := evidencestate.Publish(t.Context(), backend, view)
	require.NoError(t, err)
	require.NoError(t, backend.Close())

	inputs, err := json.Marshal([]manifestaggregate.Input{
		{Manifest: original.Manifest, Evidence: original.Evidence},
		{Manifest: retry.Manifest, Evidence: resumed.EvidenceSHA256},
	})
	require.NoError(t, err)
	inputPath := filepath.Join(directory, "inputs.json")
	require.NoError(t, os.WriteFile(inputPath, inputs, 0o600))
	output := filepath.Join(directory, "report.json")
	root := newRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs([]string{"aggregate", "--store", store, "--inputs", inputPath, "-o", output})
	require.NoError(t, root.Execute(), "honest degradation is complete membership: %s", stderr.String())

	saved, err := os.ReadFile(output)
	require.NoError(t, err)
	report, err := manifestaggregate.Parse(saved)
	require.NoError(t, err)
	require.Equal(t, "complete", report.State)
	require.NotEqual(t, "complete", report.Usage.State, "redacted usage is never totaled")
	require.Nil(t, report.Usage.Recorded)
	require.Equal(t, map[string]int{"infrastructure_failure": 1, "success": 1}, report.Counts.MemberOutcomes)
	for _, member := range report.Members {
		if member.Manifest != retry.Manifest {
			require.Nil(t, member.Parent)
			require.Empty(t, member.Lineage)
			continue
		}
		require.Equal(t, &executionattempt.Parent{ExecutionID: original.Execution, Relationship: "retry"}, member.Parent)
		require.Equal(t, []evidencestate.Lineage{{Manifest: original.Manifest, Relationship: evidencestate.Resume}}, member.Lineage)
		for _, category := range member.Categories {
			if category.Category != evidencestate.Provenance {
				require.Equal(t, evidencestate.Redacted, category.State, category.Category)
			}
		}
	}
	require.Contains(t, string(saved), `"parent":{"execution_id":"`+original.Execution+`","relationship":"retry"}`)
	require.Contains(t, string(saved), `"lineage":[{"manifest":"`+original.Manifest+`","relationship":"resume"}]`)
}
