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
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/runnercontract"
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
	lineage, err := ledger.Lineage(ctx, first.Reservation.Logical)
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
	lineage, err := ledger.Lineage(ctx, results[0].Reservation.Logical)
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
