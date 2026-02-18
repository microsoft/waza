package webapi

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spboyer/waza/internal/models"
)

// ErrRunNotFound is returned when a run ID does not match any stored run.
var ErrRunNotFound = errors.New("run not found")

// RunStore provides access to evaluation run data.
type RunStore interface {
	// ListRuns returns all runs, sorted by the given field and order.
	ListRuns(sortField, order string) ([]RunSummary, error)
	// GetRun returns a single run with full task details.
	GetRun(id string) (*RunDetail, error)
	// Summary returns aggregate metrics across all runs.
	Summary() (*SummaryResponse, error)
}

// FileStore reads EvaluationOutcome JSON files from a directory.
type FileStore struct {
	dir string

	mu      sync.RWMutex
	runs    map[string]*models.EvaluationOutcome
	loaded  bool
	loadErr error
}

// NewFileStore creates a FileStore that reads results from dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{
		dir:  dir,
		runs: make(map[string]*models.EvaluationOutcome),
	}
}

// load reads all result JSON files from the configured directory.
func (fs *FileStore) load() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fs.runs = make(map[string]*models.EvaluationOutcome)

	if fs.dir == "" {
		fs.loaded = true
		return nil
	}

	entries, err := os.ReadDir(fs.dir)
	if err != nil {
		if os.IsNotExist(err) {
			fs.loaded = true
			return nil
		}
		fs.loadErr = err
		return err
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(fs.dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var outcome models.EvaluationOutcome
		if err := json.Unmarshal(data, &outcome); err != nil {
			continue
		}
		if outcome.RunID == "" {
			// Use filename (without extension) as fallback ID.
			outcome.RunID = strings.TrimSuffix(e.Name(), ".json")
		}
		fs.runs[outcome.RunID] = &outcome
	}

	fs.loaded = true
	fs.loadErr = nil
	return nil
}

// ensureLoaded loads data if not already loaded.
func (fs *FileStore) ensureLoaded() error {
	fs.mu.RLock()
	if fs.loaded {
		fs.mu.RUnlock()
		return nil
	}
	fs.mu.RUnlock()
	return fs.load()
}

// Reload forces a fresh reload of all result files from disk.
func (fs *FileStore) Reload() error {
	return fs.load()
}

func outcomeToSummary(o *models.EvaluationOutcome) RunSummary {
	outcome := "passed"
	if o.Digest.Failed > 0 || o.Digest.Errors > 0 {
		outcome = "failed"
	}

	tokens := 0
	for _, t := range o.TestOutcomes {
		for _, r := range t.Runs {
			tokens += r.SessionDigest.TokensTotal
		}
	}

	return RunSummary{
		ID:        o.RunID,
		Spec:      o.BenchName,
		Model:     o.Setup.ModelID,
		Outcome:   outcome,
		PassCount: o.Digest.Succeeded,
		TaskCount: o.Digest.TotalTests,
		Tokens:    tokens,
		Cost:      estimateCost(tokens),
		Duration:  float64(o.Digest.DurationMs) / 1000.0,
		Timestamp: o.Timestamp,
	}
}

// estimateCost provides a rough cost estimate based on token count.
func estimateCost(tokens int) float64 {
	// ~$0.00025 per token as a rough estimate
	return float64(tokens) * 0.00025
}

func outcomeToDetail(o *models.EvaluationOutcome) *RunDetail {
	s := outcomeToSummary(o)
	detail := &RunDetail{RunSummary: s}

	for _, to := range o.TestOutcomes {
		tr := TaskResult{
			Name:    to.DisplayName,
			Outcome: string(to.Status),
		}
		if to.Stats != nil {
			tr.Score = to.Stats.AvgScore
			tr.Duration = float64(to.Stats.AvgDurationMs) / 1000.0
		}

		// Collect grader results from the first run.
		if len(to.Runs) > 0 {
			run := to.Runs[0]
			if tr.Duration == 0 {
				tr.Duration = float64(run.DurationMs) / 1000.0
			}
			for _, v := range run.Validations {
				tr.GraderResults = append(tr.GraderResults, GraderResult{
					Name:    v.Name,
					Type:    string(v.Type),
					Passed:  v.Passed,
					Score:   v.Score,
					Message: v.Feedback,
				})
			}
		}
		if tr.GraderResults == nil {
			tr.GraderResults = []GraderResult{}
		}
		detail.Tasks = append(detail.Tasks, tr)
	}
	if detail.Tasks == nil {
		detail.Tasks = []TaskResult{}
	}

	return detail
}

// ListRuns returns all runs sorted by the given field and order.
func (fs *FileStore) ListRuns(sortField, order string) ([]RunSummary, error) {
	if err := fs.ensureLoaded(); err != nil {
		return nil, err
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	runs := make([]RunSummary, 0, len(fs.runs))
	for _, o := range fs.runs {
		runs = append(runs, outcomeToSummary(o))
	}

	sortRuns(runs, sortField, order)
	return runs, nil
}

// GetRun returns a single run with full task details.
func (fs *FileStore) GetRun(id string) (*RunDetail, error) {
	if err := fs.ensureLoaded(); err != nil {
		return nil, err
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	o, ok := fs.runs[id]
	if !ok {
		return nil, ErrRunNotFound
	}

	return outcomeToDetail(o), nil
}

// Summary returns aggregate metrics across all runs.
func (fs *FileStore) Summary() (*SummaryResponse, error) {
	if err := fs.ensureLoaded(); err != nil {
		return nil, err
	}

	fs.mu.RLock()
	defer fs.mu.RUnlock()

	resp := &SummaryResponse{}
	if len(fs.runs) == 0 {
		return resp, nil
	}

	totalTokens := 0
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0

	for _, o := range fs.runs {
		resp.TotalRuns++
		totalTasks += o.Digest.TotalTests
		totalPassed += o.Digest.Succeeded

		s := outcomeToSummary(o)
		totalTokens += s.Tokens
		totalCost += s.Cost
		totalDuration += s.Duration
	}

	resp.TotalTasks = totalTasks
	if totalTasks > 0 {
		resp.PassRate = float64(totalPassed) / float64(totalTasks) * 100.0
	}
	if resp.TotalRuns > 0 {
		resp.AvgTokens = float64(totalTokens) / float64(resp.TotalRuns)
		resp.AvgCost = totalCost / float64(resp.TotalRuns)
		resp.AvgDuration = totalDuration / float64(resp.TotalRuns)
	}

	return resp, nil
}

func sortRuns(runs []RunSummary, field, order string) {
	less := func(i, j int) bool {
		switch field {
		case "tokens":
			return runs[i].Tokens < runs[j].Tokens
		case "cost":
			return runs[i].Cost < runs[j].Cost
		case "duration":
			return runs[i].Duration < runs[j].Duration
		default: // "timestamp" or empty
			return runs[i].Timestamp.Before(runs[j].Timestamp)
		}
	}

	if order == "asc" {
		sort.Slice(runs, less)
	} else {
		sort.Slice(runs, func(i, j int) bool { return less(j, i) })
	}
}

// Ensure FileStore satisfies RunStore.
var _ RunStore = (*FileStore)(nil)
