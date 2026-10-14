package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/models"
)

// LocalStore implements ResultStore using the local filesystem.
// JSON result files are stored in a flat directory structure.
type LocalStore struct {
	dir string

	mu     sync.RWMutex
	cache  map[string]*models.EvaluationOutcome
	files  map[string]struct{}
	loaded bool
}

// NewLocalStore creates a LocalStore that reads/writes results in dir.
func NewLocalStore(dir string) *LocalStore {
	return &LocalStore{
		dir:   dir,
		cache: make(map[string]*models.EvaluationOutcome),
		files: make(map[string]struct{}),
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
	ls.files[path] = struct{}{}
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

	cache, files, err := ReadLocalResults(ls.dir, ls.files)
	if err != nil {
		return err
	}
	ls.cache = cache
	ls.files = files
	ls.loaded = true
	return nil
}

// Reload replaces the local cache only after a successful artifact scan.
func (ls *LocalStore) Reload() error {
	return ls.load()
}

// ReadLocalResults builds a replacement snapshot without mutating the caller's
// cache. Previously admitted file paths must remain readable valid artifacts;
// newly introduced unrelated or malformed files retain tolerant admission.
func ReadLocalResults(dir string, previousFiles map[string]struct{}) (map[string]*models.EvaluationOutcome, map[string]struct{}, error) {
	return readLocalResults(dir, previousFiles, filepath.WalkDir)
}

func readLocalResults(dir string, previousFiles map[string]struct{}, walk func(string, fs.WalkDirFunc) error) (map[string]*models.EvaluationOutcome, map[string]struct{}, error) {
	results := make(map[string]*models.EvaluationOutcome)
	files := make(map[string]struct{})
	if dir == "" {
		return results, files, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) && len(previousFiles) == 0 {
			return results, files, nil
		}
		return nil, nil, fmt.Errorf("reading results directory: %w", err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("results path %q is not a directory", dir)
	}

	err = walk(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			knownSubtree := path == dir
			for file := range previousFiles {
				if file == path || strings.HasPrefix(file, path+string(filepath.Separator)) {
					knownSubtree = true
					break
				}
			}
			if knownSubtree {
				return fmt.Errorf("walking results path %q: %w", path, err)
			}
			return nil
		}
		_, known := previousFiles[path]
		if d.IsDir() {
			if known {
				return fmt.Errorf("previously loaded results artifact %q is now a directory", path)
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		outcome, err := readLocalResult(path, known)
		if err != nil {
			return err
		}
		if outcome == nil {
			return nil
		}

		if outcome.RunID == "" {
			relPath, relErr := filepath.Rel(dir, path)
			if relErr != nil {
				relPath = d.Name()
			}
			outcome.RunID = strings.TrimSuffix(filepath.ToSlash(relPath), ".json")
		}

		results[outcome.RunID] = outcome
		files[path] = struct{}{}
		return nil
	})

	if err != nil {
		return nil, nil, err
	}

	return results, files, nil
}

func readLocalResult(path string, known bool) (*models.EvaluationOutcome, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if known {
			return nil, fmt.Errorf("reading previously loaded results artifact %q: %w", path, err)
		}
		return nil, nil
	}
	_, ok, err := models.ProbeEvaluationOutcomeSchemaVersion(data)
	if err != nil {
		if known {
			return nil, fmt.Errorf("probing previously loaded results artifact %q: %w", path, err)
		}
		return nil, nil
	}
	if !ok {
		if known {
			return nil, fmt.Errorf("previously loaded results artifact %q is no longer a results object", path)
		}
		return nil, nil
	}
	outcome, err := models.ParseEvaluationOutcome(data, path)
	if err != nil {
		if known {
			return nil, fmt.Errorf("decoding previously loaded results artifact %q: %w", path, err)
		}
		slog.Warn("skipping unreadable results artifact", "path", path, "error", err)
		return nil, nil
	}
	return outcome, nil
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

// sanitizeFilename replaces path separators and other unsafe characters
// so the run ID can be used as a filename.
func sanitizeFilename(id string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return r.Replace(id)
}

// Ensure LocalStore satisfies ResultStore at compile time.
var _ ResultStore = (*LocalStore)(nil)
