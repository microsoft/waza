package webapi

import (
	"errors"
	"sort"
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/evidence"
	"github.com/microsoft/waza/internal/judgeevidence"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/pricing"
	"github.com/microsoft/waza/internal/statistics"
	"github.com/microsoft/waza/internal/storage"
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
	files   map[string]struct{}
	loaded  bool
	loadErr error
}

// NewFileStore creates a FileStore that reads results from dir.
func NewFileStore(dir string) *FileStore {
	return &FileStore{
		dir:   dir,
		runs:  make(map[string]*models.EvaluationOutcome),
		files: make(map[string]struct{}),
	}
}

// load reads all result JSON files from the configured directory.
func (fs *FileStore) load() error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	replacement, files, err := storage.ReadLocalResults(fs.dir, fs.files)
	if err != nil {
		fs.loadErr = err
		return err
	}

	fs.runs = replacement
	fs.files = files
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
	premiumRequests := 0.0
	var perRunUsage []*models.UsageStats
	for _, t := range o.TestOutcomes {
		for _, r := range t.Runs {
			if r.SessionDigest.Usage != nil {
				tokens += r.SessionDigest.Usage.InputTokens + r.SessionDigest.Usage.OutputTokens
				premiumRequests += r.SessionDigest.Usage.PremiumRequests
			}
			perRunUsage = append(perRunUsage, r.SessionDigest.Usage)
			for _, digest := range r.GraderSessions {
				if digest.SessionID != "" && digest.SessionID == r.SessionDigest.SessionID {
					continue
				}
				perRunUsage = append(perRunUsage, digest.Usage)
			}
		}
	}

	aggUsage := models.AggregateUsageStats(perRunUsage)

	// Digest.Usage is the run-level aggregate written after engine shutdown. It
	// covers every session the run started, including trigger sessions that the
	// per-test runs don't carry, so it is the authoritative billing aggregate.
	// Only legacy artifacts without an execution scope fall back to per-run
	// diagnostics; a current empty scope must never rebill cached usage.
	billingUsage := o.Digest.Usage
	if billingUsage == nil && o.EvaluationUsage == nil {
		billingUsage = aggUsage
	}
	costUsage := aggUsage
	if o.EvaluationUsage != nil {
		costUsage = billingUsage
		tokens = 0
		premiumRequests = 0
		if billingUsage != nil {
			tokens = billingUsage.InputTokens + billingUsage.OutputTokens
			premiumRequests = billingUsage.PremiumRequests
		}
	}
	cost, costSource := pricing.Compute(costUsage)

	var weightedScore *float64
	if o.Digest.HasWeightedScore() && len(o.TestOutcomes) > 0 && len(o.TestOutcomes) == o.Digest.TotalTests {
		complete := true
		for _, task := range o.TestOutcomes {
			if task.Stats == nil {
				complete = false
				break
			}
		}
		if complete {
			score := o.Digest.WeightedScore
			weightedScore = &score
		}
	}

	return RunSummary{
		skillRecorded:   o.SkillTested == "" && o.HasSkillTested(),
		ID:              o.RunID,
		Spec:            o.BenchName,
		Model:           o.Setup.ModelID,
		JudgeModel:      o.Setup.JudgeModel,
		Outcome:         outcome,
		PassCount:       o.Digest.Succeeded,
		TaskCount:       o.Digest.TotalTests,
		Tokens:          tokens,
		PremiumRequests: premiumRequests,
		AICredits:       aiCreditsOf(billingUsage),
		ModelUsage:      modelUsageResponses(billingUsage),
		Cost:            cost,
		CostSource:      costSource,
		Duration:        float64(o.Digest.DurationMs) / 1000.0,
		Timestamp:       o.Timestamp,
		Source:          "local",
		Skill:           o.SkillTested,
		Repetitions:     o.Setup.RunsPerTest,
		WeightedScore:   weightedScore,
	}
}

// aiCreditAccumulator averages authoritative AI-credit totals across the runs
// that report one. Runs without AI-credit metrics (legacy artifacts, older
// runtimes) are excluded so they don't drag the average toward zero; when no
// run reports credits the average is unavailable rather than 0.
type aiCreditAccumulator struct {
	total float64
	runs  int
}

func (a *aiCreditAccumulator) add(credits *float64) {
	if credits == nil {
		return
	}
	a.total += *credits
	a.runs++
}

func (a *aiCreditAccumulator) average() *float64 {
	if a.runs == 0 {
		return nil
	}
	avg := a.total / float64(a.runs)
	return &avg
}

// aiCreditsOf returns a copy of the authoritative AI-credit total carried by
// usage, or nil when usage has none.
func aiCreditsOf(usage *models.UsageStats) *float64 {
	if usage == nil || usage.AICredits == nil {
		return nil
	}
	credits := *usage.AICredits
	return &credits
}

// modelUsageResponses converts per-model usage into API responses sorted by
// model ID. Returns nil when no per-model metrics are available.
func modelUsageResponses(usage *models.UsageStats) []ModelUsageResponse {
	if usage == nil || len(usage.ModelMetrics) == 0 {
		return nil
	}
	out := make([]ModelUsageResponse, 0, len(usage.ModelMetrics))
	for model, mu := range usage.ModelMetrics {
		resp := ModelUsageResponse{
			Model:            model,
			InputTokens:      mu.InputTokens,
			CacheReadTokens:  mu.CacheReadTokens,
			CacheWriteTokens: mu.CacheWriteTokens,
			OutputTokens:     mu.OutputTokens,
		}
		if mu.AICredits != nil {
			credits := *mu.AICredits
			resp.AICredits = &credits
		}
		out = append(out, resp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

func outcomeToDetail(o *models.EvaluationOutcome) *RunDetail {
	s := outcomeToSummary(o)
	detail := &RunDetail{RunSummary: s}
	detail.RegradeUsage = judgeevidence.ProjectRegradeUsage(o.RegradeUsage)
	if stats := o.Digest.Statistics; stats != nil && (stats.SuccessRateCI != nil || len(stats.PassHatK) > 0) {
		detail.Statistics = &ReliabilitySummaryResponse{
			SuccessRateCI: confidenceIntervalResponse(stats.SuccessRateCI),
			PassHatK:      append([]float64(nil), stats.PassHatK...),
		}
	}

	for _, to := range o.TestOutcomes {
		tr := TaskResult{
			ID:      to.TestID,
			Name:    to.DisplayName,
			Outcome: string(to.Status),
		}
		hasEvidenceMetadata := false
		hasPromptJudging := false
		for _, run := range to.Runs {
			hasPromptJudging = hasPromptJudging || run.JudgeEvidence != nil || len(run.PromptJudging) > 0
			tr.PromptJudgingRuns = append(tr.PromptJudgingRuns, judgeevidence.ProjectRun(o.RunID, to.TestID, to.Cached, run))
			hasEvidenceMetadata = hasEvidenceMetadata || run.Evidence != nil || len(run.RequirementExplanations) > 0
			row := EvidenceRunResponse{
				RunNumber: run.RunNumber, Attempts: run.Attempts, Cached: to.Cached, Assessment: "unassessed",
				Explanations: run.RequirementExplanations,
				Message:      "Evidence capture was not assessed or not requested; historical absence is not success.",
			}
			if run.Evidence != nil {
				if err := evidence.Bind(run.Evidence, o.RunID, to.TestID, run.RunNumber, run.Attempts, to.Cached); err != nil {
					row.Assessment = "invalid"
					row.Message = "Evidence identity or metadata is invalid; do not use it for state reconstruction."
					row.Explanations = nil
				} else {
					row.Assessment, row.Message = "recorded", "Producer metadata only; content availability must be verified for the requested operation."
					row.Manifest = run.Evidence
				}
			}
			if row.Assessment != "invalid" {
				explanations, pointersWithheld, err := evidence.MetadataExplanations(row.Manifest, to.TestID, row.Explanations)
				if err != nil {
					row.Assessment = "invalid"
					row.Message = "Requirement observation references are invalid; the observations are withheld."
					row.Manifest = nil
					row.Explanations = nil
				} else {
					row.Explanations = explanations
					if pointersWithheld {
						row.Message += " Artifact-relative value pointers are unverified and withheld; references identify artifact metadata only."
					}
				}
			}
			tr.EvidenceRuns = append(tr.EvidenceRuns, row)
		}
		if !hasEvidenceMetadata {
			tr.EvidenceRuns = nil
		}
		if !hasPromptJudging {
			tr.PromptJudgingRuns = nil
		}
		if to.Stats != nil {
			tr.Score = to.Stats.AvgScore
			if to.Stats.HasAvgWeightedScore() {
				score := to.Stats.AvgWeightedScore
				tr.WeightedScore = &score
			}
			tr.Duration = float64(to.Stats.AvgDurationMs) / 1000.0
			tr.BootstrapCI = confidenceIntervalResponse(to.Stats.BootstrapCI)
			tr.PassRateCI = confidenceIntervalResponse(to.Stats.PassRateCI)
			if to.Stats.TotalRuns > 0 {
				tr.Trials = &TrialCountsResponse{
					Passed: to.Stats.PassedRuns,
					Total:  to.Stats.TotalRuns,
				}
			}
		}

		// Collect grader results, transcript, and session digest from the first run.
		if len(to.Runs) > 0 {
			run := to.Runs[0]
			tr.Prompt = promptFromRun(run)
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
			if run.Responder != nil {
				tr.Responder = &ResponderInfoResponse{
					FollowupsSent: run.Responder.FollowupsSent,
					Outcome:       run.Responder.Outcome,
					Reason:        run.Responder.Reason,
				}
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

func confidenceIntervalResponse(ci *statistics.ConfidenceInterval) *ConfidenceIntervalResponse {
	if ci == nil {
		return nil
	}
	return &ConfidenceIntervalResponse{
		Lower:           ci.Lower,
		Upper:           ci.Upper,
		Mean:            ci.Mean,
		ConfidenceLevel: ci.ConfidenceLevel,
	}
}

func promptFromRun(run models.RunResult) string {
	if run.Prompt != "" {
		return run.Prompt
	}
	for _, e := range run.Transcript {
		if data, ok := e.Data.(*copilot.UserMessageData); ok && data.Content != "" {
			return data.Content
		}
	}
	return ""
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
	totalPremium := 0.0
	credits := aiCreditAccumulator{}
	totalCost := 0.0
	totalDuration := 0.0
	totalPassed := 0
	totalTasks := 0
	costSources := make([]string, 0, len(fs.runs))

	for _, o := range fs.runs {
		resp.TotalRuns++
		totalTasks += o.Digest.TotalTests
		totalPassed += o.Digest.Succeeded

		s := outcomeToSummary(o)
		totalTokens += s.Tokens
		totalPremium += s.PremiumRequests
		credits.add(s.AICredits)
		totalCost += s.Cost
		totalDuration += s.Duration
		costSources = append(costSources, s.CostSource)
	}

	resp.TotalTasks = totalTasks
	if totalTasks > 0 {
		resp.PassRate = float64(totalPassed) / float64(totalTasks) * 100.0
	}
	if resp.TotalRuns > 0 {
		resp.AvgTokens = float64(totalTokens) / float64(resp.TotalRuns)
		resp.AvgPremiumRequests = totalPremium / float64(resp.TotalRuns)
		resp.AvgAICredits = credits.average()
		resp.AvgCost = totalCost / float64(resp.TotalRuns)
		resp.AvgDuration = totalDuration / float64(resp.TotalRuns)
	}
	resp.CostSource = pricing.CombineSources(costSources)

	return resp, nil
}

func mapTranscriptEvents(events []models.TranscriptEvent) []TranscriptEventResponse {
	if len(events) == 0 {
		return nil
	}
	resp := make([]TranscriptEventResponse, 0, len(events))
	for _, e := range events {
		r := TranscriptEventResponse{
			Type: string(e.Type()),
		}
		if content, ok := copilotevents.Content(e.SessionEvent); ok {
			r.Content = content
		}
		if message, ok := copilotevents.Message(e.SessionEvent); ok {
			r.Message = message
		}
		if start, ok := copilotevents.ToolStart(e.SessionEvent); ok {
			r.ToolCallID = start.ToolCallID
			r.ToolName = start.ToolName
			r.Arguments = start.Arguments
		}
		if complete, ok := copilotevents.ToolComplete(e.SessionEvent); ok {
			r.ToolCallID = complete.ToolCallID
			r.Success = &complete.Success
			r.ToolResult = complete.Result
		}
		if output, toolCallID, ok := copilotevents.ToolOutput(e.SessionEvent); ok {
			r.ToolCallID = toolCallID
			r.Message = output
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
	res := &SessionDigestResponse{
		ToolPolicyMode:    d.ToolPolicyMode,
		ToolPolicyDenials: d.ToolPolicyDenials,
		ToolCallCount:     d.ToolCallCount,
		ToolsUsed:         toolsUsed,
		Errors:            errs,
	}
	if d.Usage != nil {
		res.TokensIn = d.Usage.InputTokens
		res.TokensOut = d.Usage.OutputTokens
		res.TokensTotal = d.Usage.InputTokens + d.Usage.OutputTokens
		res.TotalTurns = d.Usage.Turns
	}
	return res
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
