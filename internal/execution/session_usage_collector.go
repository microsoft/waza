package execution

import (
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/github/copilot-sdk/go/rpc"
	"github.com/microsoft/waza/internal/copilotevents"
	"github.com/microsoft/waza/internal/models"
)

// SessionUsageCollector tracks token, AI-credit, and request usage from Copilot
// SDK session events. Its On method implements [copilot.SessionEventHandler] and
// should be registered via session.On(collector.On).
//
// Usage data arrives through three channels:
//   - The accumulated session.usage.getMetrics RPC — authoritative.
//   - Per-turn events (AssistantUsage) — accumulated as a fallback.
//   - SessionShutdown events — totals that override per-turn data when
//     the RPC is unavailable. Shutdown metrics carry
//     the final nano-AI-unit totals GitHub billed for the session, both overall
//     and per model; those are recorded as AI credits and are never
//     reconstructed from a local token-rate table.
type SessionUsageCollector struct {
	// Per-turn accumulated usage (fallback when session-level data is absent)
	turnUsage *models.UsageStats

	turns int

	// Session-level usage from termination events (authoritative)
	sessionUsage *models.UsageStats
	rpcUsage     *models.UsageStats

	mut *sync.RWMutex
}

func NewSessionUsageCollector() *SessionUsageCollector {
	return &SessionUsageCollector{
		mut: &sync.RWMutex{},
	}
}

// On handles a single session event, extracting any usage data it carries.
// Pass this method to session.On as a [copilot.SessionEventHandler].
func (s *SessionUsageCollector) On(event copilot.SessionEvent) {
	s.mut.Lock()
	defer s.mut.Unlock()

	switch event.Type() {
	case copilot.SessionEventTypeAssistantTurnStart:
		s.turns++
	case copilot.SessionEventTypeAssistantUsage:
		s.extractTurnUsage(event)
	case copilot.SessionEventTypeSessionShutdown:
		s.extractSessionUsage(event)
	}
}

// UsageStats returns the collected usage statistics. Returns nil if no usage
// data was collected. The accumulated RPC snapshot is authoritative, with
// SessionShutdown data then per-turn accumulated data (from
// AssistantUsage) as fallback.
func (s *SessionUsageCollector) UsageStats() *models.UsageStats {
	s.mut.RLock()
	defer s.mut.RUnlock()

	if s.rpcUsage != nil {
		result := *s.rpcUsage
		result.Turns = s.turns
		return &result
	}
	if s.sessionUsage != nil {
		result := *s.sessionUsage
		result.Turns = s.turns
		if result.InputTokens == 0 && result.OutputTokens == 0 && s.turnUsage != nil {
			result.InputTokens = s.turnUsage.InputTokens
			result.OutputTokens = s.turnUsage.OutputTokens
			result.CacheReadTokens = s.turnUsage.CacheReadTokens
			result.CacheWriteTokens = s.turnUsage.CacheWriteTokens
		}

		return &result
	}
	if s.turnUsage != nil {
		result := *s.turnUsage
		result.Turns = s.turns
		return &result
	}
	return nil
}

func (s *SessionUsageCollector) beginTurn() {
	s.mut.Lock()
	defer s.mut.Unlock()
	s.rpcUsage = nil
	s.sessionUsage = nil
}

func (s *SessionUsageCollector) hasMetrics() bool {
	s.mut.RLock()
	defer s.mut.RUnlock()
	return s.rpcUsage != nil
}

// SetMetrics records the accumulated RPC snapshot, authoritative over events.
func (s *SessionUsageCollector) SetMetrics(metrics *rpc.UsageGetMetricsResult) {
	s.mut.Lock()
	defer s.mut.Unlock()
	usage := &models.UsageStats{
		PremiumRequests: metrics.TotalPremiumRequestCost,
		ModelMetrics:    make(map[string]models.ModelUsage, len(metrics.ModelMetrics)),
	}
	if metrics.TotalNanoAiu != nil {
		credits := models.AICreditsFromNanoAIU(*metrics.TotalNanoAiu)
		usage.AICredits = &credits
	}
	for name, mm := range metrics.ModelMetrics {
		mu := models.ModelUsage{
			InputTokens:      int(mm.Usage.InputTokens),
			OutputTokens:     int(mm.Usage.OutputTokens),
			CacheReadTokens:  int(mm.Usage.CacheReadTokens),
			CacheWriteTokens: int(mm.Usage.CacheWriteTokens),
			RequestCount:     float64(mm.Requests.Count),
			RequestCost:      mm.Requests.Cost,
		}
		if mm.TotalNanoAiu != nil {
			credits := models.AICreditsFromNanoAIU(*mm.TotalNanoAiu)
			mu.AICredits = &credits
		}
		usage.ModelMetrics[name] = mu
		usage.InputTokens += mu.InputTokens
		usage.OutputTokens += mu.OutputTokens
		usage.CacheReadTokens += mu.CacheReadTokens
		usage.CacheWriteTokens += mu.CacheWriteTokens
	}
	s.rpcUsage = usage
}

// extractSessionUsage captures cumulative usage from session termination events.
// If it's called multiple times (e.g. due to multiple SDK events for the same
// session), later data will overwrite earlier data. This is by design and should
// be okay because the data is cumulative; later events will have the same or higher
// totals than earlier events.
func (s *SessionUsageCollector) extractSessionUsage(event copilot.SessionEvent) {
	shutdown, ok := copilotevents.Shutdown(event)
	if !ok {
		return
	}

	if s.sessionUsage == nil {
		s.sessionUsage = &models.UsageStats{}
	}

	if shutdown.TotalPremiumRequests != nil {
		s.sessionUsage.PremiumRequests = *shutdown.TotalPremiumRequests
	}

	// The session-level nano-AI-unit total is the authoritative final AI-credit
	// amount GitHub billed for this session. It is preferred over any
	// locally-computed estimate.
	if shutdown.TotalNanoAiu != nil {
		credits := models.AICreditsFromNanoAIU(*shutdown.TotalNanoAiu)
		s.sessionUsage.AICredits = &credits
	}

	if len(shutdown.ModelMetrics) > 0 {
		s.sessionUsage.ModelMetrics = make(map[string]models.ModelUsage, len(shutdown.ModelMetrics))

		totalIn, totalOut, totalCacheRead, totalCacheWrite := 0, 0, 0, 0
		for name, mm := range shutdown.ModelMetrics {
			mu := models.ModelUsage{
				InputTokens:      int(mm.Usage.InputTokens),
				OutputTokens:     int(mm.Usage.OutputTokens),
				CacheReadTokens:  int(mm.Usage.CacheReadTokens),
				CacheWriteTokens: int(mm.Usage.CacheWriteTokens),
			}
			if mm.Requests.Count != nil {
				mu.RequestCount = float64(*mm.Requests.Count)
			}
			if mm.Requests.Cost != nil {
				mu.RequestCost = *mm.Requests.Cost
			}
			if mm.TotalNanoAiu != nil {
				credits := models.AICreditsFromNanoAIU(*mm.TotalNanoAiu)
				mu.AICredits = &credits
			}
			s.sessionUsage.ModelMetrics[name] = mu
			totalIn += mu.InputTokens
			totalOut += mu.OutputTokens
			totalCacheRead += mu.CacheReadTokens
			totalCacheWrite += mu.CacheWriteTokens
		}

		s.sessionUsage.InputTokens = totalIn
		s.sessionUsage.OutputTokens = totalOut
		s.sessionUsage.CacheReadTokens = totalCacheRead
		s.sessionUsage.CacheWriteTokens = totalCacheWrite
	}
}

// extractTurnUsage captures per-turn usage from AssistantUsage events.
// This data is only used when session-level data (ModelMetrics/TotalPremiumRequests)
// is not available.
func (s *SessionUsageCollector) extractTurnUsage(event copilot.SessionEvent) {
	usage, ok := copilotevents.AssistantUsage(event)
	if !ok {
		return
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil &&
		usage.CacheReadTokens == nil && usage.CacheWriteTokens == nil &&
		usage.Cost == nil {
		return
	}
	if s.turnUsage == nil {
		s.turnUsage = &models.UsageStats{}
	}
	if usage.InputTokens != nil {
		s.turnUsage.InputTokens += int(*usage.InputTokens)
	}
	if usage.OutputTokens != nil {
		s.turnUsage.OutputTokens += int(*usage.OutputTokens)
	}
	if usage.CacheReadTokens != nil {
		s.turnUsage.CacheReadTokens += int(*usage.CacheReadTokens)
	}
	if usage.CacheWriteTokens != nil {
		s.turnUsage.CacheWriteTokens += int(*usage.CacheWriteTokens)
	}
	if usage.Cost != nil {
		s.turnUsage.PremiumRequests += *usage.Cost
	}
}
