// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// AppState represents the state of an app.
type AppState string

const (
	// AppStateRunning indicates the app is running.
	AppStateRunning AppState = "Running"
	// AppStateStopped indicates the app is stopped.
	AppStateStopped AppState = "Stopped"
)

// AppGpu represents GPU configuration for an app.
type AppGpu struct {
	// Sku is the GPU SKU.
	Sku string `json:"sku"`
	// Quantity is the number of GPUs.
	Quantity string `json:"quantity"`
}

// AppResources represents resources allocated to an app.
type AppResources struct {
	// CPU is the CPU allocation (e.g., "1000m").
	CPU string `json:"cpu"`
	// Memory is the memory allocation (e.g., "1024Mi").
	Memory string `json:"memory"`
	// Disk is the disk allocation (optional).
	Disk string `json:"disk,omitempty"`
	// GPU is the GPU configuration (optional).
	GPU *AppGpu `json:"gpu,omitempty"`
}

// AppPort represents a port mapping for an app.
type AppPort struct {
	// Port is the port number exposed.
	Port int `json:"port"`
	// Protocol is the protocol (e.g., "http", "http2").
	Protocol string `json:"protocol,omitempty"`
}

// AppEgressPolicy defines the egress policy for an app.
type AppEgressPolicy struct {
	// AllowedHosts is the list of allowed hosts.
	AllowedHosts []string `json:"allowedHosts"`
	// BlockByDefault indicates whether to block all egress traffic by default.
	BlockByDefault bool `json:"blockByDefault"`
}

// AppEnvironmentVariable represents an environment variable for an app,
// supporting both plain values and secret references.
type AppEnvironmentVariable struct {
	// Name is the environment variable name.
	Name string `json:"name"`
	// Value is the plain-text value (mutually exclusive with SecretRef).
	// Pointer distinguishes "not set" from an intentionally empty string.
	Value *string `json:"value,omitempty"`
	// SecretRef is a secret reference (mutually exclusive with Value).
	// Pointer distinguishes "not set" from an intentionally empty string.
	SecretRef *string `json:"secretRef,omitempty"`
}

// AppScale defines the scale configuration for an app.
type AppScale struct {
	// Min is the minimum number of replicas (0-1000).
	Min int `json:"min"`
	// Max is the maximum number of replicas (1-1000).
	Max int `json:"max"`
}

// AppReplica represents a running replica of an app.
type AppReplica struct {
	// InstanceID is the instance ID of the replica.
	InstanceID string `json:"instanceId"`
}

// AppData represents app data returned from the API.
type AppData struct {
	// ID is the app ID.
	ID string `json:"id"`
	// Name is the optional name of the app.
	Name string `json:"name,omitempty"`
	// Labels are the labels attached to the app.
	Labels map[string]string `json:"labels,omitempty"`
	// ContainerImage is the container image reference.
	ContainerImage string `json:"containerImage"`
	// Resources is the resource configuration.
	Resources *AppResources `json:"resources,omitempty"`
	// CreatedAt is the creation timestamp.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// State is the current state.
	State AppState `json:"state,omitempty"`
	// Ports are the port mappings.
	Ports []AppPort `json:"ports,omitempty"`
	// Connections are the connection IDs associated with this app.
	Connections []string `json:"connections,omitempty"`
	// EgressPolicy is the egress policy.
	EgressPolicy *AppEgressPolicy `json:"egressPolicy,omitempty"`
	// Scale is the scale configuration.
	Scale *AppScale `json:"scale,omitempty"`
	// Replicas are the running replicas.
	Replicas []AppReplica `json:"replicas,omitempty"`
	// AppURL is the URL for accessing the app.
	AppURL string `json:"appUrl,omitempty"`
	// Environment is the list of environment variables configured on the app.
	Environment []AppEnvironmentVariable `json:"environment,omitempty"`
	// Debug contains debug information.
	Debug *DebugInfo `json:"debug,omitempty"`
	// TelemetryConfig is the telemetry configuration for the app.
	TelemetryConfig *TelemetryConfig `json:"telemetryConfig,omitempty"`
}

// AppRequest is the request to create an app.
type AppRequest struct {
	// ContainerImage is the container image reference (required).
	ContainerImage string `json:"containerImage"`
	// Resources is the resource configuration (required).
	Resources *AppResources `json:"resources"`
	// Name is the optional name for the app.
	Name string `json:"name,omitempty"`
	// Labels are the labels to attach to the app.
	Labels map[string]string `json:"labels,omitempty"`
	// Entrypoint is the entrypoint override.
	Entrypoint []string `json:"entrypoint,omitempty"`
	// Cmd is the command override.
	Cmd []string `json:"cmd,omitempty"`
	// Environment is environment variables to set.
	Environment []AppEnvironmentVariable `json:"environment,omitempty"`
	// Connections are the connection IDs.
	Connections []string `json:"connections,omitempty"`
	// Ports are the ports to expose.
	Ports []AddAppPortRequest `json:"ports,omitempty"`
	// EgressPolicy is the egress policy.
	EgressPolicy *AppEgressPolicy `json:"egressPolicy,omitempty"`
	// Scale is the scale configuration.
	Scale *AppScale `json:"scale,omitempty"`
	// RegistryCredentials are optional credentials for authenticating to private container registries.
	// When provided, these credentials will be used to pull the container image from a private registry.
	RegistryCredentials *RegistryCredentials `json:"registryCredentials,omitempty"`
	// TelemetryConfig configures telemetry collection for the app.
	TelemetryConfig *TelemetryConfig `json:"telemetryConfig,omitempty"`
}

// AddAppPortRequest is the request to add a port to an app.
type AddAppPortRequest struct {
	// Port is the port number.
	Port int `json:"port"`
	// Protocol is the protocol (e.g., "http", "http2").
	Protocol string `json:"protocol,omitempty"`
}

// RemoveAppPortRequest is the request to remove a port from an app.
type RemoveAppPortRequest struct {
	// Port is the port number to remove.
	Port int `json:"port"`
}

// UpdateAppPortsRequest is the request to update all ports on an app.
type UpdateAppPortsRequest struct {
	// Ports are the new port mappings.
	Ports []AppPort `json:"ports"`
}

// AppPortsListResponse is the response containing app ports.
type AppPortsListResponse struct {
	// Ports are the port mappings.
	Ports []AppPort `json:"ports"`
}
