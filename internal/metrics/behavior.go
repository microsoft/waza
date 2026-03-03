package metrics

import "github.com/microsoft/waza/internal/models"

// BehaviorMetrics captures quality metrics for agent behavior during a run.
type BehaviorMetrics struct {
	ToolCallCount        int      `json:"tool_call_count"`
	IterationCount       int      `json:"iteration_count"`
	MaxToolCallsAllowed  int      `json:"max_tool_calls_allowed,omitempty"`
	MaxToolCallsPassed   bool     `json:"max_tool_calls_passed"`
	MaxIterations        int      `json:"max_iterations,omitempty"`
	MaxIterationsPassed  bool     `json:"max_iterations_passed"`
	RequiredTools        []string `json:"required_tools,omitempty"`
	RequiredToolsUsed    []string `json:"required_tools_used,omitempty"`
	RequiredToolsMissed  []string `json:"required_tools_missed,omitempty"`
	RequiredToolsPassed  bool     `json:"required_tools_passed"`
	ForbiddenTools       []string `json:"forbidden_tools,omitempty"`
	ForbiddenToolsUsed   []string `json:"forbidden_tools_used,omitempty"`
	ForbiddenToolsPassed bool     `json:"forbidden_tools_passed"`
	EfficiencyScore      float64  `json:"efficiency_score"`
}

// ComputeBehaviorMetrics analyzes a RunResult against BehaviorRules and returns
// quality metrics including compliance checks and an efficiency score.
func ComputeBehaviorMetrics(run *models.RunResult, rules *models.BehaviorRules) *BehaviorMetrics {
	iterationCount := 0
	if run.SessionDigest.Usage != nil {
		iterationCount = run.SessionDigest.Usage.Turns
	}
	m := &BehaviorMetrics{
		ToolCallCount:  run.SessionDigest.ToolCallCount,
		IterationCount: iterationCount,
	}

	toolSet := make(map[string]bool, len(run.SessionDigest.ToolsUsed))
	for _, t := range run.SessionDigest.ToolsUsed {
		toolSet[t] = true
	}

	// Max tool calls compliance
	if rules.MaxToolInvocations > 0 {
		m.MaxToolCallsAllowed = rules.MaxToolInvocations
		m.MaxToolCallsPassed = m.ToolCallCount <= rules.MaxToolInvocations
	} else {
		m.MaxToolCallsPassed = true
	}

	// Max iterations compliance
	if rules.MaxRounds > 0 {
		m.MaxIterations = rules.MaxRounds
		m.MaxIterationsPassed = m.IterationCount <= rules.MaxRounds
	} else {
		m.MaxIterationsPassed = true
	}

	// Required tools compliance
	m.RequiredTools = rules.MustUseTool
	m.RequiredToolsPassed = true
	for _, req := range rules.MustUseTool {
		if toolSet[req] {
			m.RequiredToolsUsed = append(m.RequiredToolsUsed, req)
		} else {
			m.RequiredToolsMissed = append(m.RequiredToolsMissed, req)
			m.RequiredToolsPassed = false
		}
	}

	// Forbidden tools compliance
	m.ForbiddenTools = rules.ForbidTool
	m.ForbiddenToolsPassed = true
	for _, fb := range rules.ForbidTool {
		if toolSet[fb] {
			m.ForbiddenToolsUsed = append(m.ForbiddenToolsUsed, fb)
			m.ForbiddenToolsPassed = false
		}
	}

	m.EfficiencyScore = computeEfficiency(m)
	return m
}

// AllConstraintsPassed returns true when every behavioral constraint is met.
func (m *BehaviorMetrics) AllConstraintsPassed() bool {
	return m.MaxToolCallsPassed &&
		m.MaxIterationsPassed &&
		m.RequiredToolsPassed &&
		m.ForbiddenToolsPassed
}

// computeEfficiency scores 0.0–1.0 based on how many constraint checks passed.
// Each of the four constraint categories contributes 0.25.
func computeEfficiency(m *BehaviorMetrics) float64 {
	score := 0.0
	if m.MaxToolCallsPassed {
		score += 0.25
	}
	if m.MaxIterationsPassed {
		score += 0.25
	}
	if m.RequiredToolsPassed {
		score += 0.25
	}
	if m.ForbiddenToolsPassed {
		score += 0.25
	}
	return score
}
