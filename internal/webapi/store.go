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

	// Check if directory exists
	if _, err := os.Stat(fs.dir); err != nil {
		if os.IsNotExist(err) {
			fs.loaded = true
			return nil
		}
		fs.loadErr = err
		return err
	}

	// Recursively walk directory tree to find all JSON files
	err := filepath.WalkDir(fs.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip inaccessible paths
			return nil
		}
		if d.IsDir() {
			// Continue walking into subdirectories
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

		// Validate that this is a real EvaluationOutcome, not summary.json or other JSON
		if outcome.BenchName == "" && outcome.Digest.TotalTests == 0 {
			return nil
		}

		if outcome.RunID == "" {
			// Use relative path (without extension) as fallback ID to avoid collisions
			relPath, relErr := filepath.Rel(fs.dir, path)
			if relErr != nil {
				relPath = d.Name()
			}
			outcome.RunID = strings.TrimSuffix(filepath.ToSlash(relPath), ".json")
		}
		fs.runs[outcome.RunID] = &outcome
		return nil
	})

	if err != nil {
		fs.loadErr = err
		return err
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
		ID:         o.RunID,
		Spec:       o.BenchName,
		Model:      o.Setup.ModelID,
		JudgeModel: o.Setup.JudgeModel,
		Outcome:    outcome,
		PassCount:  o.Digest.Succeeded,
		TaskCount:  o.Digest.TotalTests,
		Tokens:     tokens,
		Cost:       estimateCost(tokens),
		Duration:   float64(o.Digest.DurationMs) / 1000.0,
		Timestamp:  o.Timestamp,
		Source:     "local",
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
			if to.Stats.BootstrapCI != nil {
				tr.BootstrapCI = &ConfidenceIntervalResponse{
					Lower:           to.Stats.BootstrapCI.Lower,
					Upper:           to.Stats.BootstrapCI.Upper,
					Mean:            to.Stats.BootstrapCI.Mean,
					ConfidenceLevel: to.Stats.BootstrapCI.ConfidenceLevel,
				}
			}
			tr.IsSignificant = to.Stats.IsSignificant
		}

		// Collect grader results, transcript, and session digest from the first run.
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
					Weight:  v.Weight,
					Message: v.Feedback,
				})
			}
			tr.Transcript = mapTranscriptEvents(run.Transcript)
			tr.SessionDigest = mapSessionDigest(&run.SessionDigest)
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

func mapTranscriptEvents(events []models.TranscriptEvent) []TranscriptEventResponse {
	if len(events) == 0 {
		return nil
	}
	resp := make([]TranscriptEventResponse, 0, len(events))
	for _, e := range events {
		r := TranscriptEventResponse{
			Type:      string(e.Type),
			Arguments: e.Data.Arguments,
			Success:   e.Data.Success,
		}
		if e.Data.Content != nil {
			r.Content = *e.Data.Content
		}
		if e.Data.Message != nil {
			r.Message = *e.Data.Message
		}
		if e.Data.ToolCallID != nil {
			r.ToolCallID = *e.Data.ToolCallID
		}
		if e.Data.ToolName != nil {
			r.ToolName = *e.Data.ToolName
		}
		if e.Data.Result != nil {
			r.ToolResult = e.Data.Result
		}
		resp = append(resp, r)
	}
	return resp
}

func mapSessionDigest(d *models.SessionDigest) *SessionDigestResponse {
	if d == nil {
		return nil
	}
	toolsUsed := d.ToolsUsed
	if toolsUsed == nil {
		toolsUsed = []string{}
	}
	errs := d.Errors
	if errs == nil {
		errs = []string{}
	}
	return &SessionDigestResponse{
		TotalTurns:    d.TotalTurns,
		ToolCallCount: d.ToolCallCount,
		TokensIn:      d.TokensIn,
		TokensOut:     d.TokensOut,
		TokensTotal:   d.TokensTotal,
		ToolsUsed:     toolsUsed,
		Errors:        errs,
	}
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
