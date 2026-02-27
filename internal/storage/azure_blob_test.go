package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/spboyer/waza/internal/models"
)

// ========================================
// MOCK BLOB CLIENT INTERFACE
// ========================================

// These tests use a mock blob client to avoid Azure dependencies.
// Once azure_blob.go is implemented, update the implementation to use these mocks.

type mockBlobClient struct {
	blobs       map[string][]byte            // blob path -> content
	metadata    map[string]map[string]string // blob path -> metadata
	uploadErr   error
	downloadErr error
	listErr     error
}

func newMockBlobClient() *mockBlobClient {
	return &mockBlobClient{
		blobs:    make(map[string][]byte),
		metadata: make(map[string]map[string]string),
	}
}

func (m *mockBlobClient) Upload(ctx context.Context, path string, data []byte, metadata map[string]string) error {
	if m.uploadErr != nil {
		return m.uploadErr
	}
	m.blobs[path] = data
	m.metadata[path] = metadata
	return nil
}

func (m *mockBlobClient) Download(ctx context.Context, path string) ([]byte, error) {
	if m.downloadErr != nil {
		return nil, m.downloadErr
	}
	data, ok := m.blobs[path]
	if !ok {
		return nil, ErrNotFound
	}
	return data, nil
}

func (m *mockBlobClient) List(ctx context.Context, prefix string) ([]blobItem, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var items []blobItem
	for path, meta := range m.metadata {
		items = append(items, blobItem{
			Path:     path,
			Metadata: meta,
		})
	}
	return items, nil
}

type blobItem struct {
	Path     string
	Metadata map[string]string
}

// ========================================
// AZURE BLOB STORE TESTS
// ========================================

// Note: These tests assume AzureBlobStore will be implemented with a similar interface.
// Adjust once the actual implementation exists.

func makeAzureOutcome(runID, skill, model string, passed, total int) *models.EvaluationOutcome {
	return &models.EvaluationOutcome{
		RunID:       runID,
		SkillTested: skill,
		BenchName:   "test-bench",
		Timestamp:   time.Date(2026, 2, 27, 12, 0, 0, 0, time.UTC),
		Setup:       models.OutcomeSetup{ModelID: model},
		Digest: models.OutcomeDigest{
			TotalTests:     total,
			Succeeded:      passed,
			Failed:         total - passed,
			AggregateScore: 0.85,
		},
		Measures: map[string]models.MeasureResult{
			"accuracy": {Value: 0.9},
		},
	}
}

func TestAzureBlobStore_Upload_Serialization(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()
	outcome := makeAzureOutcome("azure-run-1", "skill-x", "gpt-4o", 8, 10)

	// Expected blob path construction: {skill}/{timestamp}/{runID}.json
	expectedPath := fmt.Sprintf("skill-x/2026-02-27/%s.json", outcome.RunID)

	// Simulate Upload logic
	data, err := json.Marshal(outcome)
	if err != nil {
		t.Fatal(err)
	}

	metadata := map[string]string{
		"skill":     outcome.SkillTested,
		"model":     outcome.Setup.ModelID,
		"timestamp": outcome.Timestamp.Format(time.RFC3339),
	}

	if err := mock.Upload(context.Background(), expectedPath, data, metadata); err != nil {
		t.Fatalf("Upload() error: %v", err)
	}

	// Verify blob was stored with correct metadata
	if _, ok := mock.blobs[expectedPath]; !ok {
		t.Errorf("expected blob at path %s", expectedPath)
	}

	if mock.metadata[expectedPath]["skill"] != "skill-x" {
		t.Errorf("metadata skill = %q, want skill-x", mock.metadata[expectedPath]["skill"])
	}
}

func TestAzureBlobStore_Download_Deserialization(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()
	outcome := makeAzureOutcome("azure-run-2", "skill-y", "claude-sonnet", 9, 10)

	data, _ := json.Marshal(outcome)
	path := "skill-y/2026-02-27/azure-run-2.json"
	_ = mock.Upload(context.Background(), path, data, nil)

	// Download and deserialize
	downloaded, err := mock.Download(context.Background(), path)
	if err != nil {
		t.Fatalf("Download() error: %v", err)
	}

	var got models.EvaluationOutcome
	if err := json.Unmarshal(downloaded, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if got.RunID != "azure-run-2" {
		t.Errorf("RunID = %q, want azure-run-2", got.RunID)
	}
	if got.SkillTested != "skill-y" {
		t.Errorf("SkillTested = %q, want skill-y", got.SkillTested)
	}
}

func TestAzureBlobStore_List_MetadataFiltering(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()
	ctx := context.Background()

	// Upload multiple outcomes with different metadata
	outcomes := []*models.EvaluationOutcome{
		makeAzureOutcome("run-1", "skill-a", "gpt-4o", 5, 10),
		makeAzureOutcome("run-2", "skill-a", "claude-sonnet", 8, 10),
		makeAzureOutcome("run-3", "skill-b", "gpt-4o", 9, 10),
	}

	for i, o := range outcomes {
		data, _ := json.Marshal(o)
		path := fmt.Sprintf("%s/2026-02-27/run-%d.json", o.SkillTested, i+1)
		metadata := map[string]string{
			"skill": o.SkillTested,
			"model": o.Setup.ModelID,
		}
		_ = mock.Upload(ctx, path, data, metadata)
	}

	// List all
	items, err := mock.List(ctx, "")
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("List() returned %d items, want 3", len(items))
	}

	// Filter by skill (would need to be implemented in actual AzureBlobStore)
	skillACount := 0
	for _, item := range items {
		if item.Metadata["skill"] == "skill-a" {
			skillACount++
		}
	}
	if skillACount != 2 {
		t.Errorf("skill-a count = %d, want 2", skillACount)
	}
}

func TestAzureBlobStore_Compare_DeltaCalculation(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()
	ctx := context.Background()

	o1 := makeAzureOutcome("run-1", "skill-x", "gpt-4o", 6, 10)
	o1.Digest.AggregateScore = 0.6
	o2 := makeAzureOutcome("run-2", "skill-x", "gpt-4o", 9, 10)
	o2.Digest.AggregateScore = 0.9

	for i, o := range []*models.EvaluationOutcome{o1, o2} {
		data, _ := json.Marshal(o)
		path := fmt.Sprintf("skill-x/2026-02-27/run-%d.json", i+1)
		_ = mock.Upload(ctx, path, data, nil)
	}

	// Compare logic (would be implemented in AzureBlobStore.Compare)
	data1, _ := mock.Download(ctx, "skill-x/2026-02-27/run-1.json")
	data2, _ := mock.Download(ctx, "skill-x/2026-02-27/run-2.json")

	var downloaded1, downloaded2 models.EvaluationOutcome
	_ = json.Unmarshal(data1, &downloaded1)
	_ = json.Unmarshal(data2, &downloaded2)

	scoreDelta := downloaded2.Digest.AggregateScore - downloaded1.Digest.AggregateScore
	if scoreDelta != 0.3 {
		t.Errorf("score delta = %v, want 0.3", scoreDelta)
	}
}

func TestAzureBlobStore_AuthFailure_AzLoginRetry(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()

	// Simulate auth error
	mock.uploadErr = errors.New("authentication failed: No valid credentials")

	outcome := makeAzureOutcome("run-1", "skill-x", "gpt-4o", 5, 10)
	data, _ := json.Marshal(outcome)

	err := mock.Upload(context.Background(), "test-path.json", data, nil)
	if err == nil {
		t.Fatal("Upload() should error on auth failure")
	}

	// In actual implementation, this should trigger `az login` retry flow
	// and potentially switch to local storage as fallback
	if !errors.Is(err, mock.uploadErr) {
		t.Errorf("expected auth error, got: %v", err)
	}
}

func TestAzureBlobStore_NetworkError_Handling(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()

	// Simulate network error
	mock.downloadErr = errors.New("network timeout")

	_, err := mock.Download(context.Background(), "some-path.json")
	if err == nil {
		t.Fatal("Download() should error on network failure")
	}

	// Should handle gracefully, possibly with retry logic
	if !errors.Is(err, mock.downloadErr) {
		t.Errorf("expected network error, got: %v", err)
	}
}

func TestAzureBlobStore_GracefulDegradation(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	// Test that when Azure storage is unavailable, system falls back to local storage
	// This would be tested in the actual NewStore factory function

	mock := newMockBlobClient()
	mock.listErr = errors.New("service unavailable")

	_, err := mock.List(context.Background(), "")
	if err == nil {
		t.Fatal("List() should error when service unavailable")
	}

	// Implementation should detect this and potentially:
	// 1. Fall back to local storage
	// 2. Log the error
	// 3. Continue execution without crashing
}

func TestAzureBlobStore_BlobPathConstruction(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	// Test various blob path construction scenarios
	tests := []struct {
		skill     string
		timestamp time.Time
		runID     string
		want      string
	}{
		{
			skill:     "code-explainer",
			timestamp: time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC),
			runID:     "run-123",
			want:      "code-explainer/2026-03-15/run-123.json",
		},
		{
			skill:     "skill-with-dashes",
			timestamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			runID:     "run-abc-def",
			want:      "skill-with-dashes/2026-01-01/run-abc-def.json",
		},
	}

	for _, tt := range tests {
		// This would be the actual path construction logic in AzureBlobStore
		got := fmt.Sprintf("%s/%s/%s.json",
			tt.skill,
			tt.timestamp.Format("2006-01-02"),
			tt.runID,
		)
		if got != tt.want {
			t.Errorf("path = %q, want %q", got, tt.want)
		}
	}
}

func TestAzureBlobStore_SpecialCharactersInSkillName(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()

	// Skill names with special characters should be sanitized for blob paths
	outcome := makeAzureOutcome("run-1", "skill/with:special\\chars", "gpt-4o", 5, 10)

	// Expected: special characters should be URL-encoded or sanitized
	// Actual implementation will determine the encoding strategy
	data, _ := json.Marshal(outcome)

	// This test verifies the implementation handles special characters
	// The exact path format will depend on the implementation
	err := mock.Upload(context.Background(), "skill-with-special-chars/2026-02-27/run-1.json", data, nil)
	if err != nil {
		t.Fatalf("Upload() error: %v", err)
	}
}

func TestAzureBlobStore_ContextCancellation(t *testing.T) {
	t.Skip("Skip until azure_blob.go is implemented")

	mock := newMockBlobClient()

	// Create a canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	outcome := makeAzureOutcome("run-1", "skill-x", "gpt-4o", 5, 10)
	data, _ := json.Marshal(outcome)

	// Upload should respect context cancellation
	// The actual implementation should check ctx.Err() before operations
	err := mock.Upload(ctx, "test-path.json", data, nil)

	// In a real implementation with context checking, this would return context.Canceled
	// For now, the mock doesn't check context, so we just verify it handles it gracefully
	_ = err
}
