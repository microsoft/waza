// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// Connection represents a connection to an external service.
type Connection struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	State             string            `json:"state"`
	Labels            map[string]string `json:"labels"`
	CreatedAt         *time.Time        `json:"createdAt,omitempty"`
	Deletable         bool              `json:"deletable"`
	PolicyRules       []PolicyRule      `json:"policyRules,omitempty"`
	EnabledToolGroups []string          `json:"enabledToolGroups,omitempty"`
}

// PolicyRule is a policy rule that activates a declared policy hook.
type PolicyRule struct {
	HookID   string   `json:"hookId"`
	Patterns []string `json:"patterns"`
}

// CreateConnectionRequest is the request body to create a connection.
type CreateConnectionRequest struct {
	Name                    string            `json:"name"`
	Type                    string            `json:"type"`
	Labels                  map[string]string `json:"labels,omitempty"`
	ParameterValueSetName   string            `json:"parameterValueSetName,omitempty"`
	ParameterValueSetValues map[string]string `json:"parameterValueSetValues,omitempty"`
	PolicyRules             []PolicyRule      `json:"policyRules,omitempty"`
	EnabledToolGroups       []string          `json:"enabledToolGroups,omitempty"`
}

// AuthorizeConnectionRequest is the request body to authorize a connection.
type AuthorizeConnectionRequest struct {
	ParameterValues map[string]string `json:"parameterValues"`
}

// UpdatePolicyRulesRequest is the request body to update policy rules.
type UpdatePolicyRulesRequest struct {
	PolicyRules       []PolicyRule `json:"policyRules"`
	EnabledToolGroups []string     `json:"enabledToolGroups,omitempty"`
}

// GenerateConsentLinkRequest is the internal request body for generating a consent link.
type GenerateConsentLinkRequest struct {
	RedirectURL string `json:"redirectUrl,omitempty"`
}

// ConsentLinkResponse is the response from generating a consent link.
type ConsentLinkResponse struct {
	ConsentLink string `json:"consentLink"`
}
