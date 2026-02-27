package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/spboyer/waza/internal/models"
)

// AzureBlobStore implements ResultStore using Azure Blob Storage.
// It authenticates using DefaultAzureCredential and supports automatic
// az login fallback if credentials are unavailable.
type AzureBlobStore struct {
	client        *azblob.Client
	containerName string
}

// NewAzureBlobStore creates an Azure Blob Storage-backed ResultStore.
// It uses DefaultAzureCredential for authentication. If credentials are
// unavailable, it attempts to run 'az login' automatically and retries once.
func NewAzureBlobStore(ctx context.Context, accountName, containerName string) (*AzureBlobStore, error) {
	if accountName == "" {
		return nil, fmt.Errorf("azure blob store requires accountName")
	}
	if containerName == "" {
		return nil, fmt.Errorf("azure blob store requires containerName")
	}

	serviceURL := fmt.Sprintf("https://%s.blob.core.windows.net/", accountName)

	// Attempt to create credential with auto-login fallback.
	cred, err := getCredentialWithAutoLogin(ctx)
	if err != nil {
		return nil, fmt.Errorf("azure blob authentication: %w", err)
	}

	client, err := azblob.NewClient(serviceURL, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("creating azure blob client: %w", err)
	}

	return &AzureBlobStore{
		client:        client,
		containerName: containerName,
	}, nil
}

// getCredentialWithAutoLogin attempts to create DefaultAzureCredential.
// If it fails, it runs 'az login' and retries once.
func getCredentialWithAutoLogin(ctx context.Context) (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err == nil {
		return cred, nil
	}

	// If credential creation failed, attempt auto-login.
	// This handles cases where no auth is configured (no env vars, no managed identity, etc.)
	fmt.Fprintln(os.Stderr, "Azure credentials not available, attempting 'az login'...")
	cmd := exec.CommandContext(ctx, "az", "login")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if loginErr := cmd.Run(); loginErr != nil {
		return nil, fmt.Errorf("az login failed: %w (original error: %v)", loginErr, err)
	}

	// Retry credential creation.
	cred, retryErr := azidentity.NewDefaultAzureCredential(nil)
	if retryErr != nil {
		return nil, fmt.Errorf("credentials still unavailable after az login: %w", retryErr)
	}

	return cred, nil
}

// Upload persists an evaluation outcome to Azure Blob Storage.
// Blob path: {skill-name}/{run-id}.json
// Metadata: skill, model, passrate, timestamp, runid
func (abs *AzureBlobStore) Upload(ctx context.Context, outcome *models.EvaluationOutcome) error {
	if outcome.RunID == "" {
		return fmt.Errorf("outcome has empty RunID")
	}

	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return fmt.Errorf("azure blob upload: marshaling outcome: %w", err)
	}

	// Blob path: {skill-name}/{run-id}.json
	blobPath := fmt.Sprintf("%s/%s.json", sanitizePathSegment(outcome.SkillTested), sanitizePathSegment(outcome.RunID))

	// Compute pass rate for metadata.
	passRate := 0.0
	if outcome.Digest.TotalTests > 0 {
		passRate = float64(outcome.Digest.Succeeded) / float64(outcome.Digest.TotalTests) * 100.0
	}

	metadata := map[string]*string{
		"skill":     stringPtr(outcome.SkillTested),
		"model":     stringPtr(outcome.Setup.ModelID),
		"passrate":  stringPtr(fmt.Sprintf("%.2f", passRate)),
		"timestamp": stringPtr(outcome.Timestamp.Format(time.RFC3339)),
		"runid":     stringPtr(outcome.RunID),
	}

	_, err = abs.client.UploadBuffer(ctx, abs.containerName, blobPath, data, &azblob.UploadBufferOptions{
		Metadata: metadata,
	})
	if err != nil {
		return fmt.Errorf("azure blob upload: %w", err)
	}

	return nil
}

// List returns summaries of stored results matching the given options.
// Uses ListBlobsFlat with prefix filtering and reads blob metadata to build
// ResultSummary objects without downloading blobs.
func (abs *AzureBlobStore) List(ctx context.Context, opts ListOptions) ([]ResultSummary, error) {
	var results []ResultSummary

	// Determine prefix for filtering by skill.
	prefix := ""
	if opts.Skill != "" {
		prefix = sanitizePathSegment(opts.Skill) + "/"
	}

	pager := abs.client.NewListBlobsFlatPager(abs.containerName, &azblob.ListBlobsFlatOptions{
		Prefix: &prefix,
	})

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("azure blob list: %w", err)
		}

		for _, blob := range page.Segment.BlobItems {
			if blob.Name == nil || blob.Properties == nil || blob.Metadata == nil {
				continue
			}

			// Parse metadata to build ResultSummary.
			summary, err := abs.blobToResultSummary(blob)
			if err != nil {
				// Skip blobs with invalid metadata.
				continue
			}

			// Apply filters.
			if opts.Model != "" && summary.Model != opts.Model {
				continue
			}
			if !opts.Since.IsZero() && summary.Timestamp.Before(opts.Since) {
				continue
			}

			results = append(results, summary)
		}
	}

	// Sort by timestamp descending (newest first).
	sort.Slice(results, func(i, j int) bool {
		return results[i].Timestamp.After(results[j].Timestamp)
	})

	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}

	return results, nil
}

// Download retrieves a single evaluation outcome by run ID.
// It attempts to download {skill}/{run-id}.json. If not found and skill is
// not known, it lists all blobs to find a match by run ID.
func (abs *AzureBlobStore) Download(ctx context.Context, runID string) (*models.EvaluationOutcome, error) {
	// First, try listing all blobs to find the one matching the run ID.
	// This is necessary because we don't know the skill name from just the run ID.
	pager := abs.client.NewListBlobsFlatPager(abs.containerName, nil)

	var blobPath string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("azure blob download: listing blobs: %w", err)
		}

		for _, blob := range page.Segment.BlobItems {
			if blob.Name == nil || blob.Metadata == nil {
				continue
			}

			if metaRunID, ok := blob.Metadata["runid"]; ok && metaRunID != nil && *metaRunID == runID {
				blobPath = *blob.Name
				break
			}
		}

		if blobPath != "" {
			break
		}
	}

	if blobPath == "" {
		return nil, ErrNotFound
	}

	// Download the blob.
	resp, err := abs.client.DownloadStream(ctx, abs.containerName, blobPath, nil)
	if err != nil {
		return nil, fmt.Errorf("azure blob download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("azure blob download: reading blob: %w", err)
	}

	var outcome models.EvaluationOutcome
	if err := json.Unmarshal(data, &outcome); err != nil {
		return nil, fmt.Errorf("azure blob download: unmarshaling outcome: %w", err)
	}

	return &outcome, nil
}

// Compare downloads two runs and produces a comparison report with deltas.
func (abs *AzureBlobStore) Compare(ctx context.Context, runID1, runID2 string) (*ComparisonReport, error) {
	o1, err := abs.Download(ctx, runID1)
	if err != nil {
		return nil, fmt.Errorf("downloading run %s: %w", runID1, err)
	}
	o2, err := abs.Download(ctx, runID2)
	if err != nil {
		return nil, fmt.Errorf("downloading run %s: %w", runID2, err)
	}

	s1 := abs.outcomeToResultSummary(o1)
	s2 := abs.outcomeToResultSummary(o2)

	report := &ComparisonReport{
		Run1:       s1,
		Run2:       s2,
		PassDelta:  s2.PassRate - s1.PassRate,
		ScoreDelta: o2.Digest.AggregateScore - o1.Digest.AggregateScore,
		Metrics:    buildMetricDeltas(o1, o2),
	}

	return report, nil
}

// blobToResultSummary converts a blob item to a ResultSummary using metadata.
func (abs *AzureBlobStore) blobToResultSummary(blob *container.BlobItem) (ResultSummary, error) {
	metadata := blob.Metadata
	if metadata == nil {
		return ResultSummary{}, fmt.Errorf("blob has no metadata")
	}

	runID := getMetadata(metadata, "runid")
	skill := getMetadata(metadata, "skill")
	model := getMetadata(metadata, "model")
	passRateStr := getMetadata(metadata, "passrate")
	timestampStr := getMetadata(metadata, "timestamp")

	if runID == "" || timestampStr == "" {
		return ResultSummary{}, fmt.Errorf("missing required metadata")
	}

	timestamp, err := time.Parse(time.RFC3339, timestampStr)
	if err != nil {
		return ResultSummary{}, fmt.Errorf("parsing timestamp: %w", err)
	}

	passRate := 0.0
	if passRateStr != "" {
		_, _ = fmt.Sscanf(passRateStr, "%f", &passRate)
	}

	blobPath := ""
	if blob.Name != nil {
		blobPath = *blob.Name
	}

	return ResultSummary{
		RunID:     runID,
		Skill:     skill,
		Model:     model,
		Timestamp: timestamp,
		PassRate:  passRate,
		BlobPath:  blobPath,
	}, nil
}

// outcomeToResultSummary converts an EvaluationOutcome to a ResultSummary.
func (abs *AzureBlobStore) outcomeToResultSummary(o *models.EvaluationOutcome) ResultSummary {
	passRate := 0.0
	if o.Digest.TotalTests > 0 {
		passRate = float64(o.Digest.Succeeded) / float64(o.Digest.TotalTests) * 100.0
	}

	blobPath := fmt.Sprintf("%s/%s.json", sanitizePathSegment(o.SkillTested), sanitizePathSegment(o.RunID))

	return ResultSummary{
		RunID:     o.RunID,
		Skill:     o.SkillTested,
		Model:     o.Setup.ModelID,
		Timestamp: o.Timestamp,
		PassRate:  passRate,
		BlobPath:  blobPath,
	}
}

// sanitizePathSegment removes characters unsafe for blob paths.
func sanitizePathSegment(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(s)
}

// stringPtr returns a pointer to a string value.
func stringPtr(s string) *string {
	return &s
}

// getMetadata retrieves a metadata value by key, returning empty string if not found.
func getMetadata(metadata map[string]*string, key string) string {
	if val, ok := metadata[key]; ok && val != nil {
		return *val
	}
	return ""
}

// Ensure AzureBlobStore satisfies ResultStore at compile time.
var _ ResultStore = (*AzureBlobStore)(nil)
