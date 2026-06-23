// Copyright 2024 Microsoft Corp. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package models

// ProviderConfig holds BYOK (Bring Your Own Key) custom LLM provider settings
// for eval.yaml. Field names use snake_case YAML/JSON tags, consistent with
// the surrounding Config fields (e.g., judge_model, timeout_seconds).
type ProviderConfig struct {
	BaseURL     string `yaml:"base_url,omitempty"     json:"base_url,omitempty"`
	Type        string `yaml:"type,omitempty"          json:"type,omitempty"`
	WireAPI     string `yaml:"wire_api,omitempty"      json:"wire_api,omitempty"`
	APIKey      string `yaml:"api_key,omitempty"       json:"api_key,omitempty"`
	BearerToken string `yaml:"bearer_token,omitempty"  json:"bearer_token,omitempty"`
}
