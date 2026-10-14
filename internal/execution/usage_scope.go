package execution

import (
	"context"
	"maps"
	"sync"

	"github.com/microsoft/waza/internal/models"
)

type usageScopeKey struct{}

// UsageScope records executions rather than final task results, so discarded
// retries and auxiliary sessions remain billable and cache hits do not.
// A scope belongs to one evaluation pass and its engine.
type UsageScope struct {
	mu       sync.Mutex
	sessions []models.EvaluationSessionUsage
	named    map[string]int
	recorded map[string]bool
}

func NewUsageScope(ctx context.Context) (context.Context, *UsageScope) {
	scope := &UsageScope{named: make(map[string]int), recorded: make(map[string]bool)}
	return context.WithValue(ctx, usageScopeKey{}, scope), scope
}

type usageExecutor interface {
	Execute(context.Context, *ExecutionRequest) (*ExecutionResponse, error)
}

// ExecuteRecorded preserves the executor's response/error contract, including
// responses returned with errors. Without a scope it simply executes.
func ExecuteRecorded(ctx context.Context, executor usageExecutor, req *ExecutionRequest) (*ExecutionResponse, error) {
	scope, _ := ctx.Value(usageScopeKey{}).(*UsageScope)
	if scope == nil {
		return executor.Execute(ctx, req)
	}

	scope.mu.Lock()
	if req.SessionID != "" {
		if _, exists := scope.named[req.SessionID]; !exists {
			var initial *models.UsageStats
			if source, ok := executor.(interface {
				SessionUsage(string) *models.UsageStats
			}); ok {
				initial = cloneUsage(source.SessionUsage(req.SessionID))
			}
			scope.named[req.SessionID] = len(scope.sessions)
			scope.sessions = append(scope.sessions, models.EvaluationSessionUsage{
				SessionID: req.SessionID, InitialUsage: initial, UnknownBaseline: initial == nil,
			})
		}
	}
	scope.mu.Unlock()

	resp, err := executor.Execute(ctx, req)
	scope.mu.Lock()
	defer scope.mu.Unlock()
	id := req.SessionID
	if resp != nil {
		id = resp.SessionID
	}
	var usage *models.UsageStats
	cumulative := false
	if resp != nil {
		usage = cloneUsage(resp.Usage)
		cumulative = resp.UsageIsCumulative
	}
	if id == "" {
		scope.sessions = append(scope.sessions, models.EvaluationSessionUsage{Usage: usage, Cumulative: cumulative})
		return resp, err
	}
	idx, exists := scope.named[id]
	if !exists {
		idx = len(scope.sessions)
		scope.named[id] = idx
		scope.sessions = append(scope.sessions, models.EvaluationSessionUsage{SessionID: id})
	}
	entry := &scope.sessions[idx]
	if cumulative && resp.UsageRevision != 0 && resp.UsageRevision < entry.UsageRevision {
		return resp, err
	}
	if cumulative || !scope.recorded[id] {
		entry.Usage = latestUsage(entry.Usage, usage)
	} else {
		entry.Usage = models.AggregateUsageStats([]*models.UsageStats{entry.Usage, usage})
	}
	entry.Cumulative = cumulative
	if resp != nil {
		entry.UsageRevision = resp.UsageRevision
	}
	scope.recorded[id] = true
	return resp, err
}

func (s *UsageScope) Snapshot() *models.EvaluationUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := &models.EvaluationUsage{Sessions: make([]models.EvaluationSessionUsage, len(s.sessions))}
	for i, entry := range s.sessions {
		entry.Usage = cloneUsage(entry.Usage)
		entry.InitialUsage = cloneUsage(entry.InitialUsage)
		result.Sessions[i] = entry
	}
	return result
}

func cloneUsage(usage *models.UsageStats) *models.UsageStats {
	if usage == nil {
		return nil
	}
	copy := *usage
	copy.AICredits = cloneCredit(usage.AICredits)
	copy.ModelMetrics = maps.Clone(usage.ModelMetrics)
	for model, metric := range copy.ModelMetrics {
		metric.AICredits = cloneCredit(metric.AICredits)
		copy.ModelMetrics[model] = metric
	}
	return &copy
}

func cloneCredit(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// UsageAccumulator combines responses without double-counting cumulative
// snapshots. Anonymous responses are independent executions.
type UsageAccumulator struct {
	named     map[string]*models.UsageStats
	revisions map[string]uint64
	anonymous []*models.UsageStats
}

func (a *UsageAccumulator) Add(resp *ExecutionResponse) {
	if a.named == nil {
		a.named = make(map[string]*models.UsageStats)
		a.revisions = make(map[string]uint64)
	}
	if resp.SessionID == "" {
		a.anonymous = append(a.anonymous, cloneUsage(resp.Usage))
		return
	}
	previous, exists := a.named[resp.SessionID]
	if resp.UsageIsCumulative && resp.UsageRevision != 0 && resp.UsageRevision < a.revisions[resp.SessionID] {
		return
	}
	if resp.UsageIsCumulative || !exists {
		a.named[resp.SessionID] = latestUsage(previous, resp.Usage)
	} else {
		a.named[resp.SessionID] = models.AggregateUsageStats([]*models.UsageStats{previous, resp.Usage})
	}
	a.revisions[resp.SessionID] = resp.UsageRevision
}

func (a *UsageAccumulator) Usage() *models.UsageStats {
	stats := append([]*models.UsageStats(nil), a.anonymous...)
	for _, usage := range a.named {
		stats = append(stats, usage)
	}
	return models.AggregateUsageStats(stats)
}

func MergeResponseUsage(resp, follow *ExecutionResponse) {
	if resp.usageAccumulator == nil {
		resp.usageAccumulator = &UsageAccumulator{}
		resp.usageAccumulator.Add(resp)
	}
	resp.usageAccumulator.Add(follow)
	resp.Usage = resp.usageAccumulator.Usage()
}

// EvaluationSessionStats removes a prior evaluation's cumulative counters.
// Missing baselines and resets keep billing unavailable, not a partial total.
func EvaluationSessionStats(entry models.EvaluationSessionUsage) *models.UsageStats {
	usage := cloneUsage(entry.Usage)
	if usage == nil {
		return nil
	}
	if entry.UnknownBaseline && entry.Cumulative {
		clearCredits(usage)
		return usage
	}
	initial := entry.InitialUsage
	if initial == nil || !entry.Cumulative {
		return usage
	}
	valid := true
	subtract := func(current, prior int) int {
		if current < prior {
			valid = false
			return current
		}
		return current - prior
	}
	usage.Turns = subtract(usage.Turns, initial.Turns)
	usage.InputTokens = subtract(usage.InputTokens, initial.InputTokens)
	usage.OutputTokens = subtract(usage.OutputTokens, initial.OutputTokens)
	usage.CacheReadTokens = subtract(usage.CacheReadTokens, initial.CacheReadTokens)
	usage.CacheWriteTokens = subtract(usage.CacheWriteTokens, initial.CacheWriteTokens)
	if usage.PremiumRequests >= initial.PremiumRequests {
		usage.PremiumRequests -= initial.PremiumRequests
	} else {
		valid = false
	}
	usage.AICredits = creditDifference(usage.AICredits, initial.AICredits)
	if initial.AICredits != nil && entry.Usage.AICredits != nil && *entry.Usage.AICredits < *initial.AICredits {
		valid = false
	}
	for model, metric := range usage.ModelMetrics {
		prior, exists := initial.ModelMetrics[model]
		metric.InputTokens = subtract(metric.InputTokens, prior.InputTokens)
		metric.OutputTokens = subtract(metric.OutputTokens, prior.OutputTokens)
		metric.CacheReadTokens = subtract(metric.CacheReadTokens, prior.CacheReadTokens)
		metric.CacheWriteTokens = subtract(metric.CacheWriteTokens, prior.CacheWriteTokens)
		if metric.RequestCount >= prior.RequestCount && metric.RequestCost >= prior.RequestCost {
			metric.RequestCount -= prior.RequestCount
			metric.RequestCost -= prior.RequestCost
		} else {
			valid = false
		}
		if exists {
			metric.AICredits = creditDifference(metric.AICredits, prior.AICredits)
		} else if len(initial.ModelMetrics) == 0 {
			metric.AICredits = nil
		}
		usage.ModelMetrics[model] = metric
	}
	for model := range initial.ModelMetrics {
		if _, exists := usage.ModelMetrics[model]; !exists {
			valid = false
		}
	}
	if !valid {
		clearCredits(usage)
	}
	return usage
}

func creditDifference(current, initial *float64) *float64 {
	if current == nil || initial == nil || *current < *initial {
		return nil
	}
	difference := *current - *initial
	return &difference
}

func clearCredits(usage *models.UsageStats) {
	usage.AICredits = nil
	for model, metric := range usage.ModelMetrics {
		metric.AICredits = nil
		usage.ModelMetrics[model] = metric
	}
}

func latestUsage(previous, latest *models.UsageStats) *models.UsageStats {
	if latest != nil {
		return cloneUsage(latest)
	}
	usage := cloneUsage(previous)
	if usage != nil {
		clearCredits(usage)
	}
	return usage
}
