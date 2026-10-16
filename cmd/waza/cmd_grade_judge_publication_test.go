package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

type evidenceSummaryFailure struct{}

func (evidenceSummaryFailure) Write([]byte) (int, error) {
	return 0, io.ErrClosedPipe
}

func TestEvidenceSavedGradeRetainsPaidObservationWhenSummaryCannotBeWritten(t *testing.T) {
	spec, results, original := savedEvidenceFixture(t)
	source, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results, source, 0600))
	output := filepath.Join(t.TempDir(), "graded.json")
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		zero := 0.0
		return &execution.ExecutionResponse{Success: true, SessionID: "observed-judge",
			Usage: &models.UsageStats{InputTokens: 2, OutputTokens: 1, AICredits: &zero}}, err
	})
	err = runGradeWithJudge(t.Context(), evidenceSummaryFailure{}, io.Discard, spec, "task", results, "", "", output, false, "", judge)
	require.ErrorIs(t, err, io.ErrClosedPipe)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	saved, err := models.ParseEvaluationOutcome(data, output)
	require.NoError(t, err)
	require.Len(t, saved.RegradeUsage.Sessions, 1)
	require.Equal(t, "observed-judge", saved.RegradeUsage.Sessions[0].SessionID)
	require.Equal(t, 0.0, *saved.RegradeUsage.Sessions[0].Usage.AICredits)
	require.True(t, saved.TestOutcomes[0].Runs[0].PromptJudging[0].SubmissionObserved)
}

func TestEvidenceSavedGradePublicationFailureDoesNotPrintSuccessfulSummary(t *testing.T) {
	spec, results, original := savedEvidenceFixture(t)
	source, err := json.Marshal(original)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(results, source, 0600))
	var stdout bytes.Buffer
	judge := savedEvidenceJudge(func(request *execution.ExecutionRequest) (*execution.ExecutionResponse, error) {
		_, err := request.Tools[0].Handler(copilot.ToolInvocation{ToolName: request.Tools[0].Name})
		return &execution.ExecutionResponse{Success: true, SessionID: "judge"}, err
	})
	output := filepath.Join(t.TempDir(), "missing", "graded.json")
	err = runGradeWithJudge(t.Context(), &stdout, io.Discard, spec, "task", results, "", "", output, false, "", judge)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to save graded outcome")
	require.Empty(t, stdout.String())
	_, statErr := os.Stat(output)
	require.True(t, errors.Is(statErr, os.ErrNotExist))
}
