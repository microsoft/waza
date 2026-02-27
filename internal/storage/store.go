// Package storage defines the ResultStore interface for persisting and
// retrieving evaluation outcomes. Implementations include a local filesystem
// adapter and (planned) an Azure Blob Storage adapter.
package storage

import (
	"context"
	"errors"
	"time"

	"github.com/spboyer/waza/internal/models"
	"github.com/spboyer/waza/internal/projectconfig"
)

// ErrNotFound is returned when a requested run ID does not exist.
var ErrNotFound = errors.New("result not found")

// ResultStore abstracts how evaluation outcomes are persisted and queried.
// All methods accept a context.Context for cancellation and deadline support,
// which is required for cloud-backed implementations.
type ResultStore interface {
	// Upload persists an evaluation outcome.
	Upload(ctx context.Context, outcome *models.EvaluationOutcome) error
	// List returns summaries matching the given options.
	List(ctx context.Context, opts ListOptions) ([]ResultSummary, error)
	// Download retrieves a single evaluation outcome by run ID.
	Download(ctx context.Context, runID string) (*models.EvaluationOutcome, error)
	// Compare downloads two runs and produces a comparison report.
	Compare(ctx context.Context, runID1, runID2 string) (*ComparisonReport, error)
}

// ListOptions controls filtering and pagination for List.
type ListOptions struct {
	Skill string
	Model string
	Since time.Time
	Limit int
}

// ResultSummary is a lightweight representation of a stored evaluation run,
// suitable for listing without loading the full outcome.
type ResultSummary struct {
	RunID     string    `json:"run_id"`
	Skill     string    `json:"skill"`
	Model     string    `json:"model"`
	Timestamp time.Time `json:"timestamp"`
	PassRate  float64   `json:"pass_rate"`
	BlobPath  string    `json:"blob_path"`
}

// ComparisonReport holds the result of comparing two evaluation runs.
type ComparisonReport struct {
	Run1       ResultSummary
	Run2       ResultSummary
	PassDelta  float64
	ScoreDelta float64
	Metrics    map[string]MetricDelta
}

// MetricDelta captures the difference for a single metric between two runs.
type MetricDelta struct {
	Name   string
	Value1 float64
	Value2 float64
	Delta  float64
}

// NewStore creates a ResultStore based on project configuration.
// If storage is configured with provider "azure-blob" and enabled, it returns
// an AzureBlobStore using DefaultAzureCredential.
// Otherwise it returns a LocalStore backed by localDir.
func NewStore(cfg *projectconfig.StorageConfig, localDir string) (ResultStore, error) {
	if cfg != nil && cfg.Enabled && cfg.Provider == "azure-blob" {
		return NewAzureBlobStore(context.Background(), cfg.AccountName, cfg.ContainerName)
	}
	return NewLocalStore(localDir), nil
}
