package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spboyer/waza/internal/models"
)

// LocalStore implements ResultStore using the local filesystem.
// JSON result files are stored in a flat directory structure.
type LocalStore struct {
	dir string

	mu     sync.RWMutex
	cache  map[string]*models.EvaluationOutcome
	loaded bool
}

// NewLocalStore creates a LocalStore that reads/writes results in dir.
func NewLocalStore(dir string) *LocalStore {
	return &LocalStore{
		dir:   dir,
		cache: make(map[string]*models.EvaluationOutcome),
	}
}

// Upload writes an evaluation outcome as a JSON file to the local results directory.
func (ls *LocalStore) Upload(_ context.Context, outcome *models.EvaluationOutcome) error {
	if outcome.RunID == "" {
		return fmt.Errorf("outcome has empty RunID")
	}

	if err := os.MkdirAll(ls.dir, 0o755); err != nil {
		return fmt.Errorf("creating results directory: %w", err)
	}

	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling outcome: %w", err)
	}

	filename := sanitizeFilename(outcome.RunID) + ".json"
	path := filepath.Join(ls.dir, filename)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing result file: %w", err)
	}

	// Update cache.
	ls.mu.Lock()
	ls.cache[outcome.RunID] = outcome
	ls.mu.Unlock()

	return nil
}

// List returns summaries of stored results matching the given options.
func (ls *LocalStore) List(_ context.Context, opts ListOptions) ([]ResultSummary, error) {
	if err := ls.ensureLoaded(); err != nil {
		return nil, err
	}

	ls.mu.RLock()
	defer ls.mu.RUnlock()

	var results []ResultSummary
	for _, o := range ls.cache {
		if !matchesFilter(o, opts) {
			continue
		}
		results = append(results, outcomeToResultSummary(o, ls.dir))
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
func (ls *LocalStore) Download(_ context.Context, runID string) (*models.EvaluationOutcome, error) {
	if err := ls.ensureLoaded(); err != nil {
		return nil, err
	}

	ls.mu.RLock()
	defer ls.mu.RUnlock()

	o, ok := ls.cache[runID]
	if !ok {
		return nil, ErrNotFound
	}
	return o, nil
}

// Compare downloads two runs and produces a comparison report with deltas.
func (ls *LocalStore) Compare(ctx context.Context, runID1, runID2 string) (*ComparisonReport, error) {
	o1, err := ls.Download(ctx, runID1)
	if err != nil {
		return nil, fmt.Errorf("downloading run %s: %w", runID1, err)
	}
	o2, err := ls.Download(ctx, runID2)
	if err != nil {
		return nil, fmt.Errorf("downloading run %s: %w", runID2, err)
	}

	s1 := outcomeToResultSummary(o1, ls.dir)
	s2 := outcomeToResultSummary(o2, ls.dir)

	report := &ComparisonReport{
		Run1:       s1,
		Run2:       s2,
		PassDelta:  s2.PassRate - s1.PassRate,
		ScoreDelta: o2.Digest.AggregateScore - o1.Digest.AggregateScore,
		Metrics:    buildMetricDeltas(o1, o2),
	}

	return report, nil
}

// load reads all JSON result files from the configured directory.
func (ls *LocalStore) load() error {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	ls.cache = make(map[string]*models.EvaluationOutcome)

	if ls.dir == "" {
		ls.loaded = true
		return nil
	}

	if _, err := os.Stat(ls.dir); err != nil {
		if os.IsNotExist(err) {
			ls.loaded = true
			return nil
		}
		return err
	}

	err := filepath.WalkDir(ls.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var outcome models.EvaluationOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			return nil
		}

		// Skip non-EvaluationOutcome JSON files.
		if outcome.BenchName == "" && outcome.Digest.TotalTests == 0 {
			return nil
		}

		if outcome.RunID == "" {
			relPath, relErr := filepath.Rel(ls.dir, path)
			if relErr != nil {
				relPath = d.Name()
			}
			outcome.RunID = strings.TrimSuffix(filepath.ToSlash(relPath), ".json")
		}

		ls.cache[outcome.RunID] = &outcome
		return nil
	})

	if err != nil {
		return err
	}

	ls.loaded = true
	return nil
}

// ensureLoaded triggers a load if data hasn't been read yet.
func (ls *LocalStore) ensureLoaded() error {
	ls.mu.RLock()
	if ls.loaded {
		ls.mu.RUnlock()
		return nil
	}
	ls.mu.RUnlock()
	return ls.load()
}

// outcomeToResultSummary converts an EvaluationOutcome to a ResultSummary.
func outcomeToResultSummary(o *models.EvaluationOutcome, dir string) ResultSummary {
	passRate := 0.0
	if o.Digest.TotalTests > 0 {
		passRate = float64(o.Digest.Succeeded) / float64(o.Digest.TotalTests) * 100.0
	}
	return ResultSummary{
		RunID:     o.RunID,
		Skill:     o.SkillTested,
		Model:     o.Setup.ModelID,
		Timestamp: o.Timestamp,
		PassRate:  passRate,
		BlobPath:  filepath.Join(dir, sanitizeFilename(o.RunID)+".json"),
	}
}

// matchesFilter checks if an outcome matches the list filter options.
func matchesFilter(o *models.EvaluationOutcome, opts ListOptions) bool {
	if opts.Skill != "" && o.SkillTested != opts.Skill {
		return false
	}
	if opts.Model != "" && o.Setup.ModelID != opts.Model {
		return false
	}
	if !opts.Since.IsZero() && o.Timestamp.Before(opts.Since) {
		return false
	}
	return true
}

// buildMetricDeltas computes per-metric deltas between two outcomes.
func buildMetricDeltas(o1, o2 *models.EvaluationOutcome) map[string]MetricDelta {
	deltas := make(map[string]MetricDelta)

	// Collect all metric names from both runs.
	names := make(map[string]struct{})
	for k := range o1.Measures {
		names[k] = struct{}{}
	}
	for k := range o2.Measures {
		names[k] = struct{}{}
	}

	for name := range names {
		v1 := o1.Measures[name].Value
		v2 := o2.Measures[name].Value
		deltas[name] = MetricDelta{
			Name:   name,
			Value1: v1,
			Value2: v2,
			Delta:  math.Round((v2-v1)*1000) / 1000,
		}
	}

	return deltas
}

// sanitizeFilename replaces path separators and other unsafe characters
// so the run ID can be used as a filename.
func sanitizeFilename(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(id)
}

// Ensure LocalStore satisfies ResultStore at compile time.
var _ ResultStore = (*LocalStore)(nil)
