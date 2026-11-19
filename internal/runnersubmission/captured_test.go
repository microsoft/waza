package runnersubmission

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionbudget"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
	"github.com/stretchr/testify/require"
)

func request(t *testing.T) *runnercontract.Request {
	t.Helper()
	data, err := os.ReadFile("../../examples/runner/unit.json")
	require.NoError(t, err)
	parsed, err := runnercontract.Parse(data)
	require.NoError(t, err)
	return parsed
}

func wire(t *testing.T, req *runnercontract.Request, policy *runnerunit.SubmissionPolicy) []byte {
	t.Helper()
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	data, err := json.Marshal(runnerunit.SubmissionRequest{Kind: "waza.execution-submission", SchemaVersion: "1.0", Request: raw, Policy: policy})
	require.NoError(t, err)
	return data
}

func localBackend(t *testing.T) (*artifactmanifest.Local, string) {
	t.Helper()
	root := t.TempDir()
	backend, err := artifactmanifest.OpenLocal(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	return backend, root
}

func snapshot(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		files[path] = data
		return err
	}))
	return files
}

func TestSubmitCapturedRetryReuseAndLineage(t *testing.T) {
	ctx := context.Background()
	backend, root := localBackend(t)
	service := runnerunit.NewService(nil)
	req := request(t)

	first, err := SubmitCaptured(ctx, service, wire(t, req, nil), backend)
	require.NoError(t, err)
	require.Equal(t, "completed", first.Status)
	require.Equal(t, 1, first.EngineCalls)
	require.Empty(t, first.Report)
	encoded, err := runnerunit.EncodeSubmissionResult(first)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	retryReq := request(t)
	retryReq.Selection.Attempt++
	retry, err := SubmitCaptured(ctx, service, wire(t, retryReq, &runnerunit.SubmissionPolicy{Mode: "distinct",
		Parent: &executionattempt.Parent{ExecutionID: first.Reservation.ExecutionID, Relationship: "retry"}}), backend)
	require.NoError(t, err)
	require.Equal(t, "completed", retry.Status)
	require.NotEqual(t, first.Reservation.ExecutionID, retry.Reservation.ExecutionID)

	before := snapshot(t, root)
	source := executionattempt.Source{ExecutionID: first.Reservation.ExecutionID, ManifestDigest: first.Completion.ManifestDigest}
	reuseReq := request(t)
	reuseReq.Selection.Attempt++
	reused, err := SubmitCaptured(ctx, service, wire(t, reuseReq, &runnerunit.SubmissionPolicy{Mode: "reuse", Source: &source}), backend)
	require.NoError(t, err, "%+v", reused.Diagnostics)
	require.Equal(t, "reused", reused.Status)
	require.Zero(t, reused.EngineCalls)
	after := snapshot(t, root)
	for path, data := range before {
		require.True(t, bytes.Equal(data, after[path]), "existing record changed: %s", path)
	}

	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	lineage, err := ledger.Lineage(ctx, first.Reservation.Logical, executionledger.LineageOptions{})
	require.NoError(t, err)
	require.Len(t, lineage.Attempts, 2)
	require.Equal(t, first.Reservation.ExecutionID, lineage.Attempts[0].ExecutionID)
	require.Equal(t, retry.Reservation.ExecutionID, lineage.Attempts[1].ExecutionID)
	require.Equal(t, "retry", lineage.Attempts[1].Parent.Relationship)
	require.Equal(t, []string{reused.Decision.DecisionID}, lineage.Attempts[0].ReusedBy)
	require.Empty(t, lineage.Diagnostics)
}

func TestSubmitCapturedConcurrentDuplicatesKeepDistinctEvidence(t *testing.T) {
	ctx := context.Background()
	backend, _ := localBackend(t)
	service := runnerunit.NewService(nil)
	data := wire(t, request(t), nil)
	const writers = 4
	results := make([]runnerunit.Submission, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Go(func() { results[i], errs[i] = SubmitCaptured(ctx, service, data, backend) })
	}
	wg.Wait()
	ids := map[string]bool{}
	for i := range writers {
		require.NoError(t, errs[i])
		require.Equal(t, "completed", results[i].Status)
		ids[results[i].Reservation.ExecutionID] = true
	}
	require.Len(t, ids, writers)

	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	lineage, err := ledger.Lineage(ctx, results[0].Reservation.Logical, executionledger.LineageOptions{})
	require.NoError(t, err)
	require.Len(t, lineage.Attempts, writers)
	for _, attempt := range lineage.Attempts {
		require.Equal(t, "completed", attempt.Status)
	}
	require.Len(t, lineage.Diagnostics, 1)
	require.Equal(t, "concurrent_distinct_attempts", lineage.Diagnostics[0].Code)
	require.Len(t, lineage.Diagnostics[0].ExecutionIDs, writers)
}

func TestSubmitCapturedRejectsInvalidWithoutEcho(t *testing.T) {
	backend, _ := localBackend(t)
	result, err := SubmitCaptured(context.Background(), runnerunit.NewService(nil), []byte(`{"secret-control":true}`), backend)
	require.Error(t, err)
	require.Equal(t, "rejected", result.Status)
	encoded, err := runnerunit.EncodeSubmissionResult(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-control")
	require.Nil(t, result.Reservation)
}

func budgetLimit(value int64) *int64 { return &value }

func TestSubmitCapturedPublishesBudgetEvidence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  executionbudget.Limits
		status  string
		outcome runnerterminal.Outcome
		state   string
	}{
		{"within budget", executionbudget.Limits{WallTimeMS: budgetLimit(60_000), ModelTurns: budgetLimit(1)}, "completed", runnerterminal.Success, executionbudget.StateWithinBudget},
		{"terminated", executionbudget.Limits{ModelTurns: budgetLimit(0)}, "completed", runnerterminal.Canceled, executionbudget.StateTerminated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, _ := localBackend(t)
			req := request(t)
			req.Budgets = &executionbudget.Request{Limits: tc.limits}
			result, err := SubmitCaptured(context.Background(), runnerunit.NewService(nil), wire(t, req, nil), backend)
			require.NoError(t, err, "%+v", result.Diagnostics)
			require.Equal(t, tc.status, result.Status)
			require.NotNil(t, result.Completion, "publication accepted the budgeted terminal")
			require.Equal(t, tc.outcome, result.Terminal.Outcome)
			require.Equal(t, tc.state, result.Terminal.Budget.State)
		})
	}
}

func TestSubmitCapturedReportsUnsupportedBudgetCode(t *testing.T) {
	backend, root := localBackend(t)
	req := request(t)
	req.Budgets = &executionbudget.Request{Limits: executionbudget.Limits{DiskBytes: budgetLimit(1 << 20)}}
	result, err := SubmitCaptured(context.Background(), runnerunit.NewService(nil), wire(t, req, nil), backend)
	require.Error(t, err)
	require.Equal(t, 5, result.ExitCode)
	require.Zero(t, result.EngineCalls)
	require.Nil(t, result.Reservation)
	codes := []string{}
	for _, diagnostic := range result.Diagnostics {
		codes = append(codes, diagnostic.Code)
	}
	require.Contains(t, codes, "unsupported_budget")
	require.Empty(t, snapshot(t, root), "nothing is reserved before the capability rejection")
}
