// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import (
	"encoding/json"
	"testing"
)

func TestTelemetryConfig_MarshalJSON_OtlpEndpoint(t *testing.T) {
	cfg := TelemetryConfig{
		Endpoints: []TelemetryEndpoint{
			OtlpTelemetryEndpoint{
				Kind:     "OTLP",
				Endpoint: "https://otel.example.com",
				Protocol: TelemetryProtocolGrpc,
				Data:     []TelemetryData{TelemetryDataContainerStdoutStderr},
			},
		},
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string][]map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw failed: %v", err)
	}

	eps := raw["endpoints"]
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0]["kind"] != "OTLP" {
		t.Errorf("expected kind OTLP, got %v", eps[0]["kind"])
	}
	if eps[0]["endpoint"] != "https://otel.example.com" {
		t.Errorf("expected endpoint URL, got %v", eps[0]["endpoint"])
	}
}

func TestTelemetryConfig_MarshalJSON_LogAnalyticsLegacy(t *testing.T) {
	cfg := TelemetryConfig{
		Endpoints: []TelemetryEndpoint{
			LogAnalyticsLegacyTelemetryEndpoint{
				Kind:        "LogAnalyticsLegacy",
				WorkspaceID: "abc-123",
				TableName:   "ContainerAppConsoleLogs",
				Data:        []TelemetryData{TelemetryDataContainerStdoutStderr},
				Auth: &TelemetrySecretRef{
					SecretID:  "my-secret",
					SecretKey: "primaryKey",
				},
			},
		},
	}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var raw map[string][]map[string]interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw failed: %v", err)
	}

	eps := raw["endpoints"]
	if len(eps) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(eps))
	}
	if eps[0]["kind"] != "LogAnalyticsLegacy" {
		t.Errorf("expected kind LogAnalyticsLegacy, got %v", eps[0]["kind"])
	}
	if eps[0]["workspaceId"] != "abc-123" {
		t.Errorf("expected workspaceId, got %v", eps[0]["workspaceId"])
	}
	if eps[0]["tableName"] != "ContainerAppConsoleLogs" {
		t.Errorf("expected tableName, got %v", eps[0]["tableName"])
	}
}

func TestTelemetryConfig_UnmarshalJSON_MixedEndpoints(t *testing.T) {
	input := `{
		"endpoints": [
			{
				"kind": "OTLP",
				"endpoint": "https://otel.example.com",
				"protocol": "Grpc",
				"data": ["ContainerStdoutStderr"]
			},
			{
				"kind": "LogAnalyticsLegacy",
				"workspaceId": "abc-123",
				"tableName": "ContainerAppConsoleLogs",
				"data": ["ContainerStdoutStderr"],
				"auth": {
					"secretId": "my-secret",
					"secretKey": "primaryKey"
				}
			}
		]
	}`

	var cfg TelemetryConfig
	if err := json.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(cfg.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(cfg.Endpoints))
	}

	otlp, ok := cfg.Endpoints[0].(OtlpTelemetryEndpoint)
	if !ok {
		t.Fatalf("expected OtlpTelemetryEndpoint, got %T", cfg.Endpoints[0])
	}
	if otlp.Endpoint != "https://otel.example.com" {
		t.Errorf("expected OTLP endpoint URL, got %s", otlp.Endpoint)
	}
	if otlp.TelemetryKind() != "OTLP" {
		t.Errorf("expected TelemetryKind OTLP, got %s", otlp.TelemetryKind())
	}

	law, ok := cfg.Endpoints[1].(LogAnalyticsLegacyTelemetryEndpoint)
	if !ok {
		t.Fatalf("expected LogAnalyticsLegacyTelemetryEndpoint, got %T", cfg.Endpoints[1])
	}
	if law.WorkspaceID != "abc-123" {
		t.Errorf("expected workspaceId abc-123, got %s", law.WorkspaceID)
	}
	if law.TableName != "ContainerAppConsoleLogs" {
		t.Errorf("expected tableName, got %s", law.TableName)
	}
	if law.Auth == nil {
		t.Fatal("expected auth to be non-nil")
	}
	if law.Auth.SecretID != "my-secret" {
		t.Errorf("expected secretId my-secret, got %s", law.Auth.SecretID)
	}
}

func TestTelemetryConfig_RoundTrip(t *testing.T) {
	cfg := TelemetryConfig{
		Endpoints: []TelemetryEndpoint{
			OtlpTelemetryEndpoint{
				Kind:     "OTLP",
				Endpoint: "https://otel.example.com",
				Protocol: TelemetryProtocolGrpc,
				Data:     []TelemetryData{TelemetryDataContainerStdoutStderr},
			},
			LogAnalyticsLegacyTelemetryEndpoint{
				Kind:        "LogAnalyticsLegacy",
				WorkspaceID: "abc-123",
				TableName:   "ContainerAppConsoleLogs",
				Data:        []TelemetryData{TelemetryDataContainerStdoutStderr},
			},
		},
	}

	b1, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var cfg2 TelemetryConfig
	if err := json.Unmarshal(b1, &cfg2); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	b2, err := json.Marshal(cfg2)
	if err != nil {
		t.Fatalf("Re-marshal failed: %v", err)
	}

	if string(b1) != string(b2) {
		t.Errorf("round-trip mismatch:\n  first:  %s\n  second: %s", string(b1), string(b2))
	}
}

func TestTelemetryConfig_UnmarshalJSON_UnknownKind_ReturnsError(t *testing.T) {
	input := `{
		"endpoints": [
			{
				"kind": "Datadog",
				"endpoint": "https://intake.datadoghq.com"
			}
		]
	}`

	var cfg TelemetryConfig
	err := json.Unmarshal([]byte(input), &cfg)
	if err == nil {
		t.Fatal("expected error for unknown kind, got nil")
	}
}

func TestTelemetryConfig_MarshalJSON_EmptyEndpoints(t *testing.T) {
	cfg := TelemetryConfig{Endpoints: []TelemetryEndpoint{}}

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	expected := `{"endpoints":[]}`
	if string(b) != expected {
		t.Errorf("expected %s, got %s", expected, string(b))
	}
}
