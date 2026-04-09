// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"strings"
	"time"
)

const (
	// DefaultAPIURL is the default ADC API endpoint.
	DefaultAPIURL = "https://management.azuredevcompute.io"
	// DefaultTimeout is the default request timeout.
	DefaultTimeout = 300 * time.Second
	// DefaultMaxRetries is the default number of retry attempts.
	DefaultMaxRetries = 3
)

// Config holds the configuration for the ADC client.
// Either APIKey, Token, or GitHubToken should be provided for authentication.
type Config struct {
	// APIKey is the ADC API key for authentication.
	// Mutually exclusive with Token and GitHubToken.
	APIKey string

	// Token is the bearer token for authentication.
	// Mutually exclusive with APIKey and GitHubToken.
	Token string

	// GitHubToken is a GitHub PAT or OAuth token for authentication.
	// Uses the "Authorization: GitHub {token}" header scheme.
	// Mutually exclusive with APIKey and Token.
	GitHubToken string

	// APIURL is the base URL for the ADC API.
	// Defaults to DefaultAPIURL if not specified.
	APIURL string

	// Timeout is the request timeout duration.
	// Defaults to DefaultTimeout if not specified.
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts for failed requests.
	// Defaults to DefaultMaxRetries if not specified.
	MaxRetries int

	// SpaceID is the optional sandbox space ID to scope all operations.
	// When set, all API requests will include this as the sandboxSpaceId parameter.
	SpaceID string
}

// ConfigManager manages the configuration with applied defaults.
type ConfigManager struct {
	APIKey         string
	Token          string
	GitHubToken    string
	APIURL         string
	Timeout        time.Duration
	MaxRetries     int
	SandboxSpaceID string
}

// NewConfigManager creates a new ConfigManager with defaults applied.
func NewConfigManager(config Config) *ConfigManager {
	cm := &ConfigManager{
		APIKey:         config.APIKey,
		Token:          config.Token,
		GitHubToken:    config.GitHubToken,
		APIURL:         config.APIURL,
		Timeout:        config.Timeout,
		MaxRetries:     config.MaxRetries,
		SandboxSpaceID: config.SpaceID,
	}

	// Apply defaults
	if cm.APIURL == "" {
		cm.APIURL = DefaultAPIURL
	}
	// Remove trailing slash
	cm.APIURL = strings.TrimSuffix(cm.APIURL, "/")

	if cm.Timeout == 0 {
		cm.Timeout = DefaultTimeout
	}

	if cm.MaxRetries == 0 {
		cm.MaxRetries = DefaultMaxRetries
	}

	return cm
}

// SetToken updates the token (used after GitHub OAuth login).
func (cm *ConfigManager) SetToken(token string) {
	cm.GitHubToken = token
}
