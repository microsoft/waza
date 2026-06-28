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
	"github.com/microsoft/waza/internal/models"
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

// ciEnvVars lists environment variables that indicate a CI/CD environment.
var ciEnvVars = []string{"CI", "GITHUB_ACTIONS", "TF_BUILD", "JENKINS_URL", "CODEBUILD_BUILD_ID"}

// isCI returns true if any common CI environment variable is set.
func isCI() bool {
	for _, v := range ciEnvVars {
		if os.Getenv(v) != "" {
			return true
		}
	}
	return false
}

// getCredentialWithAutoLogin attempts to create DefaultAzureCredential.
// If it fails and the environment is not CI, it runs 'az login' and retries once.
// In CI environments, auto-login is skipped to avoid hanging on interactive prompts.
func getCredentialWithAutoLogin(ctx context.Context) (azcore.TokenCredential, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err == nil {
		return cred, nil
	}

	// In CI environments, skip interactive az login and return a clear message.
	if isCI() {
		return nil, fmt.Errorf("azure credentials not available in CI: set AZURE_CLIENT_ID/AZURE_CLIENT_SECRET/AZURE_TENANT_ID environment variables (original error: %v)", err)
	}

	// If credential creation failed, attempt auto-login.
	// This handles interactive cases where no auth is configured (no env vars, no managed identity, etc.)
	_, _ = fmt.Fprintln(os.Stderr, "Azure credentials not available, attempting 'az login'...")
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
// Optimization: we first attempt a prefix-scoped list using the runID suffix
// pattern (*/{runID}.json) to avoid scanning all blobs. If no match is found,
// we fall back to a full blob scan matching on metadata. The prefix approach is
// O(1) when the blob naming convention is followed; the fallback is O(N) but
// handles legacy or misnamed blobs.
func (abs *AzureBlobStore) Download(ctx context.Context, runID string) (*models.EvaluationOutcome, error) {
	// Fast path: try a direct download using the known blob naming pattern.
	// Blobs are stored as {skill}/{runID}.json, so we can list with suffix match.
	blobSuffix := sanitizePathSegment(runID) + ".json"
	blobPath, err := abs.findBlobBySuffix(ctx, blobSuffix)
	if err != nil {
		return nil, fmt.Errorf("azure blob download: %w", err)
	}

	// Slow path: fall back to full scan matching on metadata if suffix match failed.
	if blobPath == "" {
		blobPath, err = abs.findBlobByMetadata(ctx, runID)
		if err != nil {
			return nil, fmt.Errorf("azure blob download: %w", err)
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

	outcome, err := models.ParseEvaluationOutcome(data, blobPath)
	if err != nil {
		return nil, fmt.Errorf("azure blob download: unmarshaling outcome: %w", err)
	}

	return outcome, nil
}

// findBlobBySuffix lists blobs and returns the first whose name ends with suffix.
// This is faster than a full metadata scan when the blob naming convention is followed.
func (abs *AzureBlobStore) findBlobBySuffix(ctx context.Context, suffix string) (string, error) {
	pager := abs.client.NewListBlobsFlatPager(abs.containerName, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("listing blobs by suffix: %w", err)
		}

		for _, blob := range page.Segment.BlobItems {
			if blob.Name != nil && strings.HasSuffix(*blob.Name, "/"+suffix) {
				return *blob.Name, nil
			}
		}
	}
	return "", nil
}

// findBlobByMetadata lists all blobs and returns the first whose "runid" metadata matches.
// This is the O(N) fallback for blobs that don't follow the naming convention.
func (abs *AzureBlobStore) findBlobByMetadata(ctx context.Context, runID string) (string, error) {
	pager := abs.client.NewListBlobsFlatPager(abs.containerName, nil)

	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("listing blobs by metadata: %w", err)
		}

		for _, blob := range page.Segment.BlobItems {
			if blob.Name == nil || blob.Metadata == nil {
				continue
			}
			if metaRunID, ok := blob.Metadata["runid"]; ok && metaRunID != nil && *metaRunID == runID {
				return *blob.Name, nil
			}
		}
	}
	return "", nil
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
