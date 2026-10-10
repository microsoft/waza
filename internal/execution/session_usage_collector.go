package execution

import (
	"sort"
	"strings"
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

	turns         int
	usageRevision uint64

	// Session-level usage from termination events (authoritative)
	sessionUsage        *models.UsageStats
	rpcUsage            *models.UsageStats
	rpcObservation      SessionUsageObservation
	shutdownObservation SessionUsageObservation
	turnModels          map[string]bool

	eventModels           map[string]bool
	modelEventsObserved   uint64
	eventModelsIncomplete bool

	mut *sync.RWMutex
}

func NewSessionUsageCollector() *SessionUsageCollector {
	return &SessionUsageCollector{
		mut:           &sync.RWMutex{},
		usageRevision: 1,
	}
}

// On handles a single session event, extracting any usage data it carries.
// Pass this method to session.On as a [copilot.SessionEventHandler].
func (s *SessionUsageCollector) On(event copilot.SessionEvent) {
	s.mut.Lock()
	defer s.mut.Unlock()

	s.observeEventModels(event)
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
	s.rpcObservation = SessionUsageObservation{}
	s.shutdownObservation = SessionUsageObservation{}
	s.usageRevision++
}

func (s *SessionUsageCollector) revision() uint64 {
	s.mut.RLock()
	defer s.mut.RUnlock()
	return s.usageRevision
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
	if metrics == nil {
		return
	}
	observation := SessionUsageObservation{Source: "rpc", Complete: metrics.TotalNanoAiu != nil && len(metrics.ModelMetrics) > 0, ModelAttributionComplete: len(metrics.ModelMetrics) > 0}
	usage := &models.UsageStats{
		PremiumRequests: metrics.TotalPremiumRequestCost,
		ModelMetrics:    make(map[string]models.ModelUsage, len(metrics.ModelMetrics)),
	}
	if metrics.TotalNanoAiu != nil {
		credits := models.AICreditsFromNanoAIU(*metrics.TotalNanoAiu)
		usage.AICredits = &credits
	}
	for name, mm := range metrics.ModelMetrics {
		if !observedModel(name) {
			observation.Complete = false
			observation.ModelAttributionComplete = false
		} else {
			observation.Models = append(observation.Models, name)
		}
		if mm.TotalNanoAiu == nil {
			observation.Complete = false
		}
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
	sort.Strings(observation.Models)
	s.rpcObservation = observation
}

// extractSessionUsage captures cumulative usage from session termination events.
// If it's called multiple times (e.g. due to multiple SDK events for the same
// session), later data will overwrite earlier data. This is by design and should
// be okay because the data is cumulative; later events will have the same or higher
// totals than earlier events.
func (s *SessionUsageCollector) extractSessionUsage(event copilot.SessionEvent) {
	shutdown, ok := copilotevents.Shutdown(event)
	if !ok || shutdown == nil {
		return
	}
	observation := SessionUsageObservation{Source: "shutdown", Complete: shutdown.TotalNanoAiu != nil && shutdown.TotalPremiumRequests != nil && len(shutdown.ModelMetrics) > 0, ModelAttributionComplete: len(shutdown.ModelMetrics) > 0}
	for name, mm := range shutdown.ModelMetrics {
		if !observedModel(name) {
			observation.Complete = false
			observation.ModelAttributionComplete = false
		} else {
			observation.Models = append(observation.Models, name)
		}
		if mm.TotalNanoAiu == nil || mm.Requests.Count == nil || mm.Requests.Cost == nil {
			observation.Complete = false
		}
	}
	sort.Strings(observation.Models)
	s.shutdownObservation = observation

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
	if !ok || usage == nil {
		return
	}
	if usage.InputTokens == nil && usage.OutputTokens == nil &&
		usage.CacheReadTokens == nil && usage.CacheWriteTokens == nil &&
		usage.Cost == nil {
		return
	}
	if s.turnModels == nil {
		s.turnModels = make(map[string]bool)
	}
	s.turnModels[usage.Model] = true

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

// Observation reports completeness of the same authoritative source selected
// by UsageStats. Per-turn data remains partial; credits are never inferred.
func (s *SessionUsageCollector) Observation() SessionUsageObservation {
	s.mut.RLock()
	defer s.mut.RUnlock()
	var result SessionUsageObservation
	switch {
	case s.rpcUsage != nil:
		result = s.rpcObservation
	case s.sessionUsage != nil:
		result = s.shutdownObservation
	case s.turnUsage != nil:
		result.Source = "events"
		result.ModelAttributionComplete = len(s.turnModels) > 0
		for name := range s.turnModels {
			if observedModel(name) {
				result.Models = append(result.Models, name)
			} else {
				result.ModelAttributionComplete = false
			}
		}
		sort.Strings(result.Models)
	}
	result.Models = append([]string(nil), result.Models...)
	return result
}

func observedModel(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "unknown", "auto":
		return false
	default:
		return true
	}
}

// EventModelObservation is cumulative for this collector's session lifetime;
// RPC snapshots, shutdown totals and beginTurn never replace received evidence.
func (s *SessionUsageCollector) EventModelObservation() SessionEventModelObservation {
	s.mut.RLock()
	defer s.mut.RUnlock()
	result := SessionEventModelObservation{
		EventsObserved: s.modelEventsObserved,
		Complete:       s.modelEventsObserved > 0 && !s.eventModelsIncomplete,
	}
	for name := range s.eventModels {
		result.Models = append(result.Models, name)
	}
	sort.Strings(result.Models)
	return result
}

func (s *SessionUsageCollector) observeEventModels(event copilot.SessionEvent) {
	var ids []*string
	switch data := event.Data.(type) {
	case *copilot.AssistantUsageData:
		if data != nil {
			ids = append(ids, &data.Model)
			if usage := data.CopilotUsage; usage != nil {
				if usage.Model != nil {
					ids = append(ids, usage.Model)
				}
				for _, detail := range usage.TokenDetails {
					if detail.Model != nil {
						ids = append(ids, detail.Model)
					}
				}
			}
		}
	case *copilot.AssistantMessageData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.AssistantTurnStartData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.AssistantTurnEndData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.AssistantTurnRetryData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.ModelCallStartData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.ModelCallFailureData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SessionStartData:
		if data != nil {
			ids = append(ids, data.SelectedModel)
		}
	case *copilot.SessionResumeData:
		if data != nil {
			ids = append(ids, data.SelectedModel)
		}
	case *copilot.SessionModelChangeData:
		if data != nil {
			ids = append(ids, &data.NewModel)
			if data.PreviousModel != nil {
				ids = append(ids, data.PreviousModel)
			}
		}
	case *copilot.SessionModelDeselectedData:
		if data != nil {
			ids = append(ids, &data.PreviousModel)
		}
	case *copilot.SessionShutdownData:
		if data != nil {
			if data.CurrentModel != nil {
				ids = append(ids, data.CurrentModel)
			}
			for model := range data.ModelMetrics {
				ids = append(ids, &model)
			}
			for _, agent := range data.AgentMetrics {
				for model := range agent.ModelMetrics {
					ids = append(ids, &model)
				}
			}
		}
	case *copilot.SessionCompactionStartData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SessionCompactionCompleteData:
		if data != nil && data.CompactionTokensUsed != nil {
			tokens := data.CompactionTokensUsed
			ids = append(ids, tokens.Model)
			if usage := tokens.CopilotUsage; usage != nil {
				if usage.Model != nil {
					ids = append(ids, usage.Model)
				}
				for _, detail := range usage.TokenDetails {
					if detail.Model != nil {
						ids = append(ids, detail.Model)
					}
				}
			}
		}
	case *copilot.AssistantFusionPhaseStartedData:
		if data != nil {
			ids = append(ids, &data.Model)
		}
	case *copilot.AssistantFusionPhaseCompletedData:
		if data != nil {
			ids = append(ids, &data.Model)
		}
	case *copilot.AssistantFusionPhaseFailedData:
		if data != nil {
			ids = append(ids, &data.Model)
		}
	case *copilot.SubagentConfiguredData:
		if data != nil {
			ids = append(ids, &data.Model)
		}
	case *copilot.SubagentStartedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SubagentCompletedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SubagentFailedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.AgentInterruptedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SessionToolsUpdatedData:
		if data != nil {
			ids = append(ids, &data.Model)
		}
	case *copilot.ToolExecutionStartData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.ToolExecutionCompleteData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.SkillInvokedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	case *copilot.ExitPlanModeRequestedData:
		if data != nil {
			ids = append(ids, data.Model)
		}
	default:
		return
	}
	s.modelEventsObserved++
	if len(ids) == 0 {
		s.eventModelsIncomplete = true
	}
	for _, id := range ids {
		if id == nil || !observedModel(*id) {
			s.eventModelsIncomplete = true
			continue
		}
		if s.eventModels == nil {
			s.eventModels = make(map[string]bool)
		}
		s.eventModels[*id] = true
	}
}
