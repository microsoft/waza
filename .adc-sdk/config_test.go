// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"testing"
	"time"
)

func TestNewConfigManager_Defaults(t *testing.T) {
	cm := NewConfigManager(Config{})

	if cm.APIURL != DefaultAPIURL {
		t.Errorf("expected APIURL %q, got %q", DefaultAPIURL, cm.APIURL)
	}

	if cm.Timeout != DefaultTimeout {
		t.Errorf("expected Timeout %v, got %v", DefaultTimeout, cm.Timeout)
	}

	if cm.MaxRetries != DefaultMaxRetries {
		t.Errorf("expected MaxRetries %d, got %d", DefaultMaxRetries, cm.MaxRetries)
	}
}

func TestNewConfigManager_CustomValues(t *testing.T) {
	config := Config{
		APIKey:     "test-api-key",
		APIURL:     "https://custom.api.com/",
		Timeout:    60 * time.Second,
		MaxRetries: 5,
		SpaceID:    "test-space-id",
	}

	cm := NewConfigManager(config)

	if cm.APIKey != "test-api-key" {
		t.Errorf("expected APIKey %q, got %q", "test-api-key", cm.APIKey)
	}

	// Trailing slash should be removed
	if cm.APIURL != "https://custom.api.com" {
		t.Errorf("expected APIURL %q, got %q", "https://custom.api.com", cm.APIURL)
	}

	if cm.Timeout != 60*time.Second {
		t.Errorf("expected Timeout %v, got %v", 60*time.Second, cm.Timeout)
	}

	if cm.MaxRetries != 5 {
		t.Errorf("expected MaxRetries %d, got %d", 5, cm.MaxRetries)
	}

	if cm.SandboxSpaceID != "test-space-id" {
		t.Errorf("expected SandboxSpaceID %q, got %q", "test-space-id", cm.SandboxSpaceID)
	}
}

func TestNewConfigManager_TokenAuth(t *testing.T) {
	config := Config{
		Token: "bearer-token",
	}

	cm := NewConfigManager(config)

	if cm.Token != "bearer-token" {
		t.Errorf("expected Token %q, got %q", "bearer-token", cm.Token)
	}

	if cm.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cm.APIKey)
	}
}

func TestConfigManager_SetToken(t *testing.T) {
	cm := NewConfigManager(Config{})

	cm.SetToken("new-token")

	if cm.GitHubToken != "new-token" {
		t.Errorf("expected GitHubToken %q, got %q", "new-token", cm.GitHubToken)
	}
}

func TestNewConfigManager_GitHubTokenAuth(t *testing.T) {
	config := Config{
		GitHubToken: "ghp_testtoken123",
	}

	cm := NewConfigManager(config)

	if cm.GitHubToken != "ghp_testtoken123" {
		t.Errorf("expected GitHubToken %q, got %q", "ghp_testtoken123", cm.GitHubToken)
	}

	if cm.APIKey != "" {
		t.Errorf("expected empty APIKey, got %q", cm.APIKey)
	}

	if cm.Token != "" {
		t.Errorf("expected empty Token, got %q", cm.Token)
	}
}
