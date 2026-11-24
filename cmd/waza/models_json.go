package main

import (
	copilot "github.com/microsoft/copilot-sdk/go/v2"
	"github.com/microsoft/copilot-sdk/go/v2/rpc"
)

// modelJSON is the `waza models --json` element. It keeps the field set
// emitted before the SDK v2 migration: capabilities.supports and
// capabilities.limits are always objects, the two support flags are always
// present, policy.terms is a string, and billing carries only multiplier
// and tokenPrices. tokenPrices no longer includes the deprecated cachePrice
// and contextMax fields: SDK v2 and its bundled runtime do not define them,
// and waza does not synthesize them from cacheReadPrice/maxPromptTokens.
type modelJSON struct {
	ID                        string                `json:"id"`
	Name                      string                `json:"name"`
	Provider                  *rpc.ModelProviderRef `json:"provider,omitempty"`
	Capabilities              modelCapabilitiesJSON `json:"capabilities"`
	Policy                    *modelPolicyJSON      `json:"policy,omitempty"`
	Billing                   *modelBillingJSON     `json:"billing,omitempty"`
	Metadata                  map[string]any        `json:"metadata,omitempty"`
	SupportedReasoningEfforts []string              `json:"supportedReasoningEfforts,omitempty"`
	DefaultReasoningEffort    string                `json:"defaultReasoningEffort,omitempty"`
}

type modelCapabilitiesJSON struct {
	Supports modelSupportsJSON `json:"supports"`
	Limits   modelLimitsJSON   `json:"limits"`
}

type modelSupportsJSON struct {
	Vision          bool `json:"vision"`
	ReasoningEffort bool `json:"reasoningEffort"`
}

type modelLimitsJSON struct {
	MaxPromptTokens        *int64                             `json:"max_prompt_tokens,omitempty"`
	MaxOutputTokens        *int64                             `json:"max_output_tokens,omitempty"`
	MaxContextWindowTokens *int64                             `json:"max_context_window_tokens,omitempty"`
	Vision                 *rpc.ModelCapabilitiesLimitsVision `json:"vision,omitempty"`
}

type modelBillingJSON struct {
	Multiplier  *float64                     `json:"multiplier,omitempty"`
	TokenPrices *rpc.ModelBillingTokenPrices `json:"tokenPrices,omitempty"`
}

type modelPolicyJSON struct {
	State string `json:"state"`
	Terms string `json:"terms"`
}

func modelsJSON(models []copilot.Model) []modelJSON {
	out := make([]modelJSON, len(models))
	for i, m := range models {
		item := modelJSON{
			ID:                        m.ID,
			Name:                      m.Name,
			Provider:                  m.Provider,
			Metadata:                  m.Metadata,
			SupportedReasoningEfforts: m.SupportedReasoningEfforts,
		}
		if b := m.Billing; b != nil {
			item.Billing = &modelBillingJSON{Multiplier: b.Multiplier, TokenPrices: b.TokenPrices}
		}
		if m.DefaultReasoningEffort != nil {
			item.DefaultReasoningEffort = *m.DefaultReasoningEffort
		}
		if s := m.Capabilities.Supports; s != nil {
			item.Capabilities.Supports.Vision = s.Vision != nil && *s.Vision
			item.Capabilities.Supports.ReasoningEffort = s.ReasoningEffort != nil && *s.ReasoningEffort
		}
		if l := m.Capabilities.Limits; l != nil {
			item.Capabilities.Limits = modelLimitsJSON{
				MaxPromptTokens:        l.MaxPromptTokens,
				MaxOutputTokens:        l.MaxOutputTokens,
				MaxContextWindowTokens: l.MaxContextWindowTokens,
				Vision:                 l.Vision,
			}
		}
		if m.Policy != nil {
			item.Policy = &modelPolicyJSON{State: string(m.Policy.State)}
			if m.Policy.Terms != nil {
				item.Policy.Terms = *m.Policy.Terms
			}
		}
		out[i] = item
	}
	return out
}
