package execution

import (
	"sync"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/models"
)

// SessionUsageCollector tracks token and premium request usage from Copilot SDK
// session events. Its On method implements [copilot.SessionEventHandler] and should
// be registered via session.On(collector.On).
//
// Usage data arrives through two channels:
//   - Per-turn events (AssistantUsage) — accumulated as a fallback.
//   - Session termination events (SessionShutdown) — authoritative totals that
//     override per-turn data when available.
type SessionUsageCollector struct {
	// Per-turn accumulated usage (fallback when session-level data is absent)
	turnUsage *models.UsageStats

	turns int

	// Session-level usage from termination events (authoritative)
	sessionUsage *models.UsageStats

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
		s.extractTurnUsage(event.Data)
	case copilot.SessionEventTypeSessionShutdown,
		copilot.SessionEventTypeSessionIdle,
		copilot.SessionEventTypeSessionError,
		copilot.SessionEventTypeSessionUsageInfo:
		s.extractSessionUsage(event.Data)
	}
}

// UsageStats returns the collected usage statistics. Returns nil if no usage
// data was collected. Session-level data (from SessionShutdown) is preferred as
// the authoritative source; per-turn accumulated data (from AssistantUsage) is
// used as fallback.
func (s *SessionUsageCollector) UsageStats() *models.UsageStats {
	s.mut.RLock()
	defer s.mut.RUnlock()

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

// extractSessionUsage captures cumulative usage from session termination events.
// If it's called multiple times (e.g. due to multiple SDK events for the same
// session), later data will overwrite earlier data. This is by design and should
// be okay because the data is cumulative; later events will have the same or higher
// totals than earlier events.
func (s *SessionUsageCollector) extractSessionUsage(data copilot.SessionEventData) {
	d, ok := data.(*copilot.SessionShutdownData)
	if !ok {
		return
	}

	if s.sessionUsage == nil {
		s.sessionUsage = &models.UsageStats{}
	}
	s.sessionUsage.PremiumRequests = d.TotalPremiumRequests

	if len(d.ModelMetrics) == 0 {
		return
	}

	s.sessionUsage.ModelMetrics = make(map[string]models.ModelUsage, len(d.ModelMetrics))

	totalIn, totalOut, totalCacheRead, totalCacheWrite := 0, 0, 0, 0
	for name, mm := range d.ModelMetrics {
		mu := models.ModelUsage{
			InputTokens:      int(mm.Usage.InputTokens),
			OutputTokens:     int(mm.Usage.OutputTokens),
			CacheReadTokens:  int(mm.Usage.CacheReadTokens),
			CacheWriteTokens: int(mm.Usage.CacheWriteTokens),
			RequestCount:     mm.Requests.Count,
			RequestCost:      mm.Requests.Cost,
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

// extractTurnUsage captures per-turn usage from AssistantUsage events.
// This data is only used when session-level data (ModelMetrics/TotalPremiumRequests)
// is not available.
func (s *SessionUsageCollector) extractTurnUsage(data copilot.SessionEventData) {
	d, ok := data.(*copilot.AssistantUsageData)
	if !ok {
		return
	}
	if d.InputTokens == nil && d.OutputTokens == nil && d.CacheReadTokens == nil && d.CacheWriteTokens == nil && d.Cost == nil {
		return
	}
	if s.turnUsage == nil {
		s.turnUsage = &models.UsageStats{}
	}
	if d.InputTokens != nil {
		s.turnUsage.InputTokens += int(*d.InputTokens)
	}
	if d.OutputTokens != nil {
		s.turnUsage.OutputTokens += int(*d.OutputTokens)
	}
	if d.CacheReadTokens != nil {
		s.turnUsage.CacheReadTokens += int(*d.CacheReadTokens)
	}
	if d.CacheWriteTokens != nil {
		s.turnUsage.CacheWriteTokens += int(*d.CacheWriteTokens)
	}
	if d.Cost != nil {
		s.turnUsage.PremiumRequests += *d.Cost
	}
}
