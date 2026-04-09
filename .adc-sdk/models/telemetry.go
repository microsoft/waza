// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import (
	"encoding/json"
	"fmt"
)

// TelemetryData specifies which category of telemetry data to route to an endpoint.
type TelemetryData string

const (
	// TelemetryDataContainerStdoutStderr routes customer container stdout/stderr logs.
	TelemetryDataContainerStdoutStderr TelemetryData = "ContainerStdoutStderr"
	// TelemetryDataContainerOtel routes customer container OpenTelemetry signals.
	TelemetryDataContainerOtel TelemetryData = "ContainerOtel"
	// TelemetryDataMetrics routes ADC operational metrics.
	TelemetryDataMetrics TelemetryData = "Metrics"
)

// TelemetryProtocol specifies the protocol used to send telemetry data to an OTLP endpoint.
type TelemetryProtocol string

const (
	// TelemetryProtocolGrpc uses OTLP over gRPC.
	TelemetryProtocolGrpc TelemetryProtocol = "Grpc"
	// TelemetryProtocolHTTP uses OTLP over HTTP.
	TelemetryProtocolHTTP TelemetryProtocol = "Http"
)

// TelemetryAuth is the authentication configuration for a telemetry endpoint.
type TelemetryAuth struct {
	// HeaderName is the HTTP header to inject (e.g., "x-api-key", "Authorization").
	HeaderName string `json:"headerName"`
	// SecretID is the reference to a user-managed secret containing key-value pairs.
	SecretID string `json:"secretId"`
	// SecretKey is the key within the secret whose value should be used as the header value.
	SecretKey string `json:"secretKey"`
}

// TelemetryEndpoint is the interface for all telemetry endpoint types.
type TelemetryEndpoint interface {
	// TelemetryKind returns the endpoint kind discriminator (e.g., "OTLP", "LogAnalyticsLegacy").
	TelemetryKind() string
}

// OtlpTelemetryEndpoint is an OTLP telemetry endpoint configuration.
type OtlpTelemetryEndpoint struct {
	// Kind is the endpoint kind discriminator. Always "OTLP".
	Kind string `json:"kind"`
	// Endpoint is the OTLP endpoint URL.
	Endpoint string `json:"endpoint"`
	// Protocol is the protocol (Grpc or Http).
	Protocol TelemetryProtocol `json:"protocol"`
	// Data specifies which telemetry data to route.
	Data []TelemetryData `json:"data"`
	// Auth is the optional authentication configuration.
	Auth *TelemetryAuth `json:"auth,omitempty"`
}

// TelemetryKind returns the endpoint kind discriminator.
func (e OtlpTelemetryEndpoint) TelemetryKind() string { return e.Kind }

// TelemetrySecretRef is a secret reference for endpoints that handle authentication internally
// (e.g., LogAnalyticsLegacy where Fluent Bit computes HMAC-SHA256 signatures).
// No HTTP header injection — the secret flows into the VM directly.
type TelemetrySecretRef struct {
	// SecretID is the reference to a user-managed secret containing key-value pairs.
	SecretID string `json:"secretId"`
	// SecretKey is the key within the secret whose value should be used.
	SecretKey string `json:"secretKey"`
}

// LogAnalyticsTelemetryEndpoint is an Azure Monitor Log Analytics telemetry endpoint configuration.
// Uses the Logs Ingestion API via Data Collection Endpoint (DCE) and Data Collection Rule (DCR).
// Authentication is handled by ADC's managed identity — no auth field needed.
type LogAnalyticsTelemetryEndpoint struct {
	// Kind is the endpoint kind discriminator. Always "LogAnalytics".
	Kind string `json:"kind"`
	// DceEndpoint is the Data Collection Endpoint URL.
	DceEndpoint string `json:"dceEndpoint"`
	// DcrImmutableID is the Data Collection Rule immutable ID.
	DcrImmutableID string `json:"dcrImmutableId"`
	// TableName is the target stream name in the DCR.
	TableName string `json:"tableName"`
	// Data specifies which telemetry data to route.
	Data []TelemetryData `json:"data"`
}

// TelemetryKind returns the endpoint kind discriminator.
func (e LogAnalyticsTelemetryEndpoint) TelemetryKind() string { return e.Kind }

// LogAnalyticsLegacyTelemetryEndpoint is a Log Analytics Legacy (Data Collector API) telemetry endpoint configuration.
type LogAnalyticsLegacyTelemetryEndpoint struct {
	// Kind is the endpoint kind discriminator. Always "LogAnalyticsLegacy".
	Kind string `json:"kind"`
	// WorkspaceID is the Log Analytics workspace ID.
	WorkspaceID string `json:"workspaceId"`
	// TableName is the custom log table name (e.g., "ContainerAppConsoleLogs").
	TableName string `json:"tableName"`
	// Data specifies which telemetry data to route.
	Data []TelemetryData `json:"data"`
	// Auth is the required secret reference for shared key authentication.
	Auth *TelemetrySecretRef `json:"auth"`
}

// TelemetryKind returns the endpoint kind discriminator.
func (e LogAnalyticsLegacyTelemetryEndpoint) TelemetryKind() string { return e.Kind }

// TelemetryConfig configures telemetry collection for a sandbox.
type TelemetryConfig struct {
	// Endpoints are the telemetry endpoints to route data to.
	Endpoints []TelemetryEndpoint `json:"-"`
}

// MarshalJSON implements custom JSON marshaling for TelemetryConfig.
// Each endpoint is marshaled using its concrete type's standard JSON serialization.
func (c TelemetryConfig) MarshalJSON() ([]byte, error) {
	// Marshal each concrete endpoint using standard encoding/json
	rawEndpoints := make([]json.RawMessage, 0, len(c.Endpoints))
	for _, ep := range c.Endpoints {
		b, err := json.Marshal(ep)
		if err != nil {
			return nil, err
		}
		rawEndpoints = append(rawEndpoints, b)
	}

	return json.Marshal(struct {
		Endpoints []json.RawMessage `json:"endpoints"`
	}{
		Endpoints: rawEndpoints,
	})
}

// UnmarshalJSON implements custom JSON unmarshaling for TelemetryConfig.
// It peeks at the "kind" field to dispatch to the correct concrete type.
func (c *TelemetryConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Endpoints []json.RawMessage `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	c.Endpoints = make([]TelemetryEndpoint, 0, len(raw.Endpoints))
	for _, epData := range raw.Endpoints {
		// Peek at the kind field
		var peek struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(epData, &peek); err != nil {
			return err
		}

		switch peek.Kind {
		case "OTLP":
			var ep OtlpTelemetryEndpoint
			if err := json.Unmarshal(epData, &ep); err != nil {
				return err
			}
			c.Endpoints = append(c.Endpoints, ep)
		case "LogAnalytics":
			var ep LogAnalyticsTelemetryEndpoint
			if err := json.Unmarshal(epData, &ep); err != nil {
				return err
			}
			c.Endpoints = append(c.Endpoints, ep)
		case "LogAnalyticsLegacy":
			var ep LogAnalyticsLegacyTelemetryEndpoint
			if err := json.Unmarshal(epData, &ep); err != nil {
				return err
			}
			c.Endpoints = append(c.Endpoints, ep)
		default:
			return fmt.Errorf("unknown telemetry endpoint kind: %q", peek.Kind)
		}
	}

	return nil
}
