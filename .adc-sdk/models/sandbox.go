// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

import "time"

// VmmType represents the Virtual Machine Monitor type.
type VmmType string

const (
	// VmmTypeCloudHypervisor uses Cloud Hypervisor as the VMM.
	VmmTypeCloudHypervisor VmmType = "cloudhypervisor"
)

// SandboxState represents the state of a sandbox.
type SandboxState string

const (
	// SandboxStateRunning indicates the sandbox is running.
	SandboxStateRunning SandboxState = "Running"
	// SandboxStateStopped indicates the sandbox is stopped.
	SandboxStateStopped SandboxState = "Stopped"
	// SandboxStateIdle indicates the sandbox is idle (auto-suspended by the system).
	SandboxStateIdle SandboxState = "Idle"
)

// EgressPolicyAction represents the action for egress policy rules.
type EgressPolicyAction string

const (
	// EgressPolicyActionAllow allows the traffic.
	EgressPolicyActionAllow EgressPolicyAction = "Allow"
	// EgressPolicyActionDeny denies the traffic.
	EgressPolicyActionDeny EgressPolicyAction = "Deny"
)

// PortActivationMode represents how a port is activated.
type PortActivationMode string

const (
	// PortActivationModeManual requires manual activation.
	PortActivationModeManual PortActivationMode = "Manual"
	// PortActivationModeOnDemand activates the port on first request.
	PortActivationModeOnDemand PortActivationMode = "OnDemand"
)

// PortProtocol represents the HTTP protocol version for port forwarding.
type PortProtocol string

const (
	// PortProtocolHTTP uses HTTP/1.1.
	PortProtocolHTTP PortProtocol = "Http"
	// PortProtocolHTTP2 uses HTTP/2.
	PortProtocolHTTP2 PortProtocol = "Http2"
)

// SandboxSuspendMode represents the suspend mode for auto-suspend.
type SandboxSuspendMode string

const (
	// SandboxSuspendModeMemory suspends to memory (snapshot).
	SandboxSuspendModeMemory SandboxSuspendMode = "Memory"
	// SandboxSuspendModeDisk suspends to disk.
	SandboxSuspendModeDisk SandboxSuspendMode = "Disk"
)

// EgressHostRule defines a rule for egress traffic to a specific host pattern.
type EgressHostRule struct {
	// Pattern is the host pattern to match (e.g., "*.example.com").
	Pattern string `json:"pattern"`
	// Action is the action for matching hosts.
	Action EgressPolicyAction `json:"action,omitempty"`
}

// EgressPolicy defines the egress policy for outbound network traffic.
type EgressPolicy struct {
	// DefaultAction is the default action for hosts not matching any rule.
	DefaultAction EgressPolicyAction `json:"defaultAction"`
	// HostRules are host-specific rules (legacy format).
	HostRules []EgressHostRule `json:"hostRules,omitempty"`
	// Rules are rule-based egress policy rules supporting Allow, Deny, Transform, and Rewrite actions.
	Rules []EgressPolicyRule `json:"rules,omitempty"`
}

// EgressPolicyRuleActionType represents the action type for rule-based egress policies.
type EgressPolicyRuleActionType string

const (
	// EgressPolicyRuleActionTypeAllow allows the request to proceed.
	EgressPolicyRuleActionTypeAllow EgressPolicyRuleActionType = "Allow"
	// EgressPolicyRuleActionTypeDeny denies the request.
	EgressPolicyRuleActionTypeDeny EgressPolicyRuleActionType = "Deny"
	// EgressPolicyRuleActionTypeTransform allows the request and applies header transformations.
	EgressPolicyRuleActionTypeTransform EgressPolicyRuleActionType = "Transform"
	// EgressPolicyRuleActionTypeRewrite rewrites the request destination and optionally applies header transformations.
	EgressPolicyRuleActionTypeRewrite EgressPolicyRuleActionType = "Rewrite"
)

// EgressPolicyHeaderOperation represents a header transformation operation.
type EgressPolicyHeaderOperation string

const (
	// EgressPolicyHeaderOperationSet sets or overwrites the header value.
	EgressPolicyHeaderOperationSet EgressPolicyHeaderOperation = "Set"
	// EgressPolicyHeaderOperationInsert inserts the header only if not already present.
	EgressPolicyHeaderOperationInsert EgressPolicyHeaderOperation = "Insert"
	// EgressPolicyHeaderOperationRemove removes the header.
	EgressPolicyHeaderOperationRemove EgressPolicyHeaderOperation = "Remove"
)

// EgressPolicySecretRef references a secret for dynamic header value injection.
type EgressPolicySecretRef struct {
	// SecretID is the secret ID from the Secrets API.
	SecretID string `json:"secretId"`
	// SecretKey is the specific key within the secret (optional).
	SecretKey string `json:"secretKey,omitempty"`
	// Format is a format string using {value} placeholder (e.g., "Bearer {value}").
	Format string `json:"format,omitempty"`
}

// EgressPolicyManagedIdentityRef references a managed identity token for header value injection.
type EgressPolicyManagedIdentityRef struct {
	// Resource is the resource URI to request a token for.
	Resource string `json:"resource"`
	// Format is a format string using {value} placeholder (e.g., "Bearer {value}").
	Format string `json:"format,omitempty"`
}

// EgressPolicyValueRef is a dynamic value reference for header transforms.
type EgressPolicyValueRef struct {
	// SecretRef is a secret reference for value resolution.
	SecretRef *EgressPolicySecretRef `json:"secretRef,omitempty"`
	// ManagedIdentityRef is a managed identity reference for value resolution.
	ManagedIdentityRef *EgressPolicyManagedIdentityRef `json:"managedIdentityRef,omitempty"`
}

// EgressPolicyHeaderTransform describes a header transformation to apply to matching requests.
type EgressPolicyHeaderTransform struct {
	// Operation is the header operation (Set, Insert, or Remove).
	Operation EgressPolicyHeaderOperation `json:"operation"`
	// Name is the header name.
	Name string `json:"name"`
	// Value is the static header value (required for Set/Insert unless ValueRef is provided).
	Value string `json:"value,omitempty"`
	// ValueRef is a dynamic value reference (alternative to static Value).
	ValueRef *EgressPolicyValueRef `json:"valueRef,omitempty"`
}

// EgressPolicyRuleMatch defines request matching criteria for an egress policy rule.
type EgressPolicyRuleMatch struct {
	// Host is the host pattern to match (e.g., "*.github.com").
	Host string `json:"host"`
	// Path is the path pattern to match (e.g., "/api/v1/*"). Optional.
	Path string `json:"path,omitempty"`
	// Methods are the HTTP methods to match (e.g., ["GET", "POST"]). Optional.
	Methods []string `json:"methods,omitempty"`
}

// EgressPolicyRuleAction defines the action to take when an egress policy rule matches.
type EgressPolicyRuleAction struct {
	// Type is the action type (Allow, Deny, Transform, or Rewrite).
	Type EgressPolicyRuleActionType `json:"type"`
	// Headers are the header transformations to apply.
	Headers []EgressPolicyHeaderTransform `json:"headers,omitempty"`
	// Scheme is the target scheme for Rewrite actions ("http" or "https").
	Scheme string `json:"scheme,omitempty"`
	// Host is the target host for Rewrite actions (e.g., "api.openai.com").
	Host string `json:"host,omitempty"`
	// Path is the target path for Rewrite actions (e.g., "/v1/completions").
	Path string `json:"path,omitempty"`
}

// EgressPolicyRule is a single egress policy rule. Rules are evaluated in order; first match wins.
type EgressPolicyRule struct {
	// Name is the rule name (for identification and debugging).
	Name string `json:"name,omitempty"`
	// Match is the request matching criteria.
	Match EgressPolicyRuleMatch `json:"match"`
	// Action is the action to take when matched.
	Action EgressPolicyRuleAction `json:"action"`
}

// SandboxAutoSuspendPolicy defines auto-suspend behavior.
type SandboxAutoSuspendPolicy struct {
	// Enabled indicates whether auto-suspend is enabled.
	Enabled bool `json:"enabled"`
	// Interval is the suspend interval in seconds (required when enabled).
	Interval *int `json:"interval,omitempty"`
	// Mode is the suspend mode.
	Mode SandboxSuspendMode `json:"mode,omitempty"`
}

// SandboxAutoDeletePolicy defines auto-delete behavior.
type SandboxAutoDeletePolicy struct {
	// Enabled indicates whether auto-delete is enabled.
	Enabled bool `json:"enabled"`
	// DeleteIntervalInSeconds is the delete interval in seconds (required when enabled).
	DeleteIntervalInSeconds *int64 `json:"deleteIntervalInSeconds,omitempty"`
}

// SandboxLifecyclePolicy defines lifecycle policy for a sandbox.
type SandboxLifecyclePolicy struct {
	// AutoSuspendPolicy is the auto-suspend policy.
	AutoSuspendPolicy *SandboxAutoSuspendPolicy `json:"autoSuspendPolicy,omitempty"`
	// AutoDeletePolicy is the auto-delete policy.
	AutoDeletePolicy *SandboxAutoDeletePolicy `json:"autoDeletePolicy,omitempty"`
}

// EgressDecisionEntry represents a single egress decision.
type EgressDecisionEntry struct {
	// Timestamp is when the request was made.
	Timestamp time.Time `json:"timestamp"`
	// Host is the host header of the request.
	Host string `json:"host,omitempty"`
	// Method is the HTTP method.
	Method string `json:"method,omitempty"`
	// Path is the request path.
	Path string `json:"path,omitempty"`
	// Scheme is the protocol scheme (http, https).
	Scheme string `json:"scheme,omitempty"`
}

// NetworkEgressDecisions contains allowed and denied egress requests.
type NetworkEgressDecisions struct {
	// Allowed contains the last 50 allowed egress requests.
	Allowed []EgressDecisionEntry `json:"allowed"`
	// Denied contains the last 50 denied egress requests.
	Denied []EgressDecisionEntry `json:"denied"`
}

// EgressDecisionsResponse is the response from getting egress decisions.
type EgressDecisionsResponse struct {
	// NetworkEgress contains the network egress decisions.
	NetworkEgress NetworkEgressDecisions `json:"networkEgress"`
	// LastUpdated is when the decisions were last updated.
	LastUpdated time.Time `json:"lastUpdated"`
}

// PortAuthConfigGithub contains GitHub-specific auth configuration for a port.
type PortAuthConfigGithub struct {
	// Enabled indicates whether GitHub auth is enabled.
	Enabled bool `json:"enabled"`
	// Emails is a list of exact email addresses allowed.
	Emails []string `json:"emails"`
	// EmailSuffixes is a list of email suffixes allowed (e.g., "@github.com").
	EmailSuffixes []string `json:"emailSuffixes"`
	// Usernames is a list of exact GitHub usernames allowed.
	Usernames []string `json:"usernames"`
	// UsernameSuffixes is a list of username suffixes allowed (e.g., "_microsoft").
	UsernameSuffixes []string `json:"usernameSuffixes"`
}

// PortAuthConfigPublicGithub contains public GitHub-specific auth configuration for a port.
type PortAuthConfigPublicGithub struct {
	// Enabled indicates whether public GitHub auth is enabled.
	Enabled bool `json:"enabled"`
	// Emails is a list of exact email addresses allowed.
	Emails []string `json:"emails"`
	// EmailSuffixes is a list of email suffixes allowed (e.g., "@github.com").
	EmailSuffixes []string `json:"emailSuffixes"`
	// Usernames is a list of exact GitHub usernames allowed.
	Usernames []string `json:"usernames"`
	// UsernameSuffixes is a list of username suffixes allowed (e.g., "_microsoft").
	UsernameSuffixes []string `json:"usernameSuffixes"`
}

// PortAuthConfig contains auth configuration for a port.
type PortAuthConfig struct {
	// Anonymous indicates whether anonymous access is allowed.
	Anonymous bool `json:"anonymous"`
	// Github contains GitHub-specific auth configuration.
	Github *PortAuthConfigGithub `json:"github,omitempty"`
	// PublicGithub contains public GitHub-specific auth configuration.
	PublicGithub *PortAuthConfigPublicGithub `json:"publicGithub,omitempty"`
}

// IpAccessControlAction represents the action for IP access control rules.
type IpAccessControlAction string

const (
	// IpAccessControlActionAllow allows the traffic.
	IpAccessControlActionAllow IpAccessControlAction = "Allow"
	// IpAccessControlActionDeny denies the traffic.
	IpAccessControlActionDeny IpAccessControlAction = "Deny"
)

// PortIpAccessControlRule defines a single IP access control rule.
type PortIpAccessControlRule struct {
	// Name is the name of the rule (must be unique within the port).
	Name string `json:"name"`
	// Action is the action to take when the rule matches.
	Action IpAccessControlAction `json:"action"`
	// Priority is the priority of the rule (lower values are evaluated first).
	Priority int `json:"priority"`
	// SourceCidrs are the CIDR ranges to match against.
	SourceCidrs []string `json:"sourceCidrs"`
}

// PortIpAccessControl defines IP access control configuration for a port.
type PortIpAccessControl struct {
	// DefaultAction is the default action when no rules match.
	DefaultAction IpAccessControlAction `json:"defaultAction"`
	// Rules are the IP access control rules.
	Rules []PortIpAccessControlRule `json:"rules,omitempty"`
}

// PortCorsConfig defines CORS configuration for a port.
type PortCorsConfig struct {
	// AllowOrigins is the list of allowed origins.
	AllowOrigins []string `json:"allowOrigins,omitempty"`
	// AllowMethods is the list of allowed HTTP methods.
	AllowMethods []string `json:"allowMethods,omitempty"`
	// AllowHeaders is the list of allowed request headers.
	AllowHeaders []string `json:"allowHeaders,omitempty"`
	// AllowCredentials indicates whether credentials are allowed.
	AllowCredentials bool `json:"allowCredentials,omitempty"`
	// MaxAge is the max age for preflight cache in seconds.
	MaxAge *int `json:"maxAge,omitempty"`
}

// PerfTrace represents a performance trace for waterfall visualization.
type PerfTrace struct {
	// ID is the unique span ID.
	ID string `json:"id"`
	// ParentID is the parent span ID (empty for root spans).
	ParentID string `json:"parentId,omitempty"`
	// StartMs is the Unix timestamp in milliseconds when the span started.
	StartMs int64 `json:"startMs"`
	// DurationMs is the span duration in milliseconds.
	DurationMs int64 `json:"durationMs"`
	// Name is the descriptive name of the span.
	Name string `json:"name"`
}

// DebugInfo contains debug information for API responses.
type DebugInfo struct {
	// TraceID is the OTEL trace ID for correlating logs.
	TraceID string `json:"traceId,omitempty"`
	// Traces contains performance traces for waterfall visualization.
	Traces []PerfTrace `json:"traces,omitempty"`
}

// SandboxSourceDiskImage represents a disk image source for creating a sandbox.
// Either ID or (Name + IsPublic) should be set.
type SandboxSourceDiskImage struct {
	// ID is the disk image ID (GUID) for custom user disk images.
	ID string `json:"id,omitempty"`
	// Name is the disk image name for public disk images.
	Name string `json:"name,omitempty"`
	// IsPublic must be true when using Name.
	IsPublic bool `json:"isPublic,omitempty"`
}

// SandboxSourceSnapshot represents a snapshot source for creating a sandbox.
type SandboxSourceSnapshot struct {
	// ID is the snapshot ID.
	ID string `json:"id"`
}

// SandboxSource represents the source for creating a sandbox.
// Either DiskImage or Snapshot should be set.
type SandboxSource struct {
	// DiskImage is the disk image source.
	DiskImage *SandboxSourceDiskImage `json:"diskImage,omitempty"`
	// Snapshot is the snapshot source.
	Snapshot *SandboxSourceSnapshot `json:"snapshot,omitempty"`
}

// SandboxGpu represents GPU configuration for a sandbox.
type SandboxGpu struct {
	// Sku is the GPU SKU.
	Sku string `json:"sku"`
	// Quantity is the number of GPUs.
	Quantity string `json:"quantity"`
}

// SandboxResources represents resources allocated to a sandbox.
type SandboxResources struct {
	// CPU is the CPU allocation (e.g., "1000m").
	CPU string `json:"cpu"`
	// Memory is the memory allocation (e.g., "1024Mi").
	Memory string `json:"memory"`
	// Disk is the disk allocation (optional).
	Disk string `json:"disk,omitempty"`
	// GPU is the GPU configuration (optional).
	GPU *SandboxGpu `json:"gpu,omitempty"`
}

// SandboxPort represents a port mapping for a sandbox.
type SandboxPort struct {
	// Port is the port number exposed.
	Port int `json:"port"`
	// URL is the URL to access this port.
	URL string `json:"url"`
	// Name is the optional name for the port mapping.
	Name string `json:"name,omitempty"`
	// Auth is the optional auth configuration for the port.
	Auth *PortAuthConfig `json:"auth,omitempty"`
	// ActivationMode is the activation mode for the port.
	ActivationMode PortActivationMode `json:"activationMode,omitempty"`
	// Protocol is the HTTP protocol version for forwarding requests.
	Protocol PortProtocol `json:"protocol,omitempty"`
	// IpAccessControl is the optional IP access control configuration for the port.
	IpAccessControl *PortIpAccessControl `json:"ipAccessControl,omitempty"`
	// Cors is the optional CORS configuration for the port.
	Cors *PortCorsConfig `json:"cors,omitempty"`
}

// SandboxGroup represents a sandbox group returned from the API.
type SandboxGroup struct {
	// ID is the sandbox group ID.
	ID string `json:"id"`
	// Labels are key-value pairs attached to the sandbox group.
	Labels map[string]string `json:"labels"`
	// AllowedLocations are the locations where sandboxes can be created.
	AllowedLocations []string `json:"allowedLocations"`
	// Connections are the connection IDs associated with this sandbox group.
	Connections []string `json:"connections"`
	// CreatedAt is when the sandbox group was created.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
}

// CreateSandboxGroupRequest is the request for creating a sandbox group.
type CreateSandboxGroupRequest struct {
	// Labels are key-value pairs to attach to the sandbox group.
	Labels map[string]string `json:"labels,omitempty"`
	// AllowedLocations are the locations where sandboxes can be created.
	AllowedLocations []string `json:"allowedLocations,omitempty"`
	// Connections are the connection IDs to associate with the sandbox group.
	Connections []string `json:"connections,omitempty"`
}

// UpdateSandboxGroupRequest is the request for updating a sandbox group.
// Same shape as CreateSandboxGroupRequest.
type UpdateSandboxGroupRequest = CreateSandboxGroupRequest

// SandboxData represents sandbox data returned from the API.
type SandboxData struct {
	// ID is the sandbox ID.
	ID string `json:"id"`
	// SandboxGroupID is the sandbox group ID, if the sandbox belongs to a group.
	SandboxGroupID string `json:"sandboxGroupId,omitempty"`
	// Labels are key-value pairs attached to the sandbox.
	Labels map[string]string `json:"labels"`
	// CreatedAt is when the sandbox was created.
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	// State is the current state of the sandbox.
	State SandboxState `json:"state,omitempty"`
	// Ports are the port mappings.
	Ports []SandboxPort `json:"ports"`
	// SourcesRef is the source reference (disk image or snapshot).
	SourcesRef SandboxSource `json:"sourcesRef"`
	// Resources is the resource configuration.
	Resources SandboxResources `json:"resources"`
	// VmmType is the virtual machine monitor type.
	VmmType VmmType `json:"vmmType"`
	// SnapshotID is the snapshot ID if the sandbox was created from a snapshot.
	SnapshotID string `json:"snapshotId,omitempty"`
	// Connections are the connection IDs associated with this sandbox.
	Connections []string `json:"connections,omitempty"`
	// EgressPolicy is the egress policy for outbound network traffic.
	EgressPolicy *EgressPolicy `json:"egressPolicy,omitempty"`
	// Volumes are the volumes mounted to this sandbox.
	Volumes []SandboxVolume `json:"volumes,omitempty"`
	// Debug contains debug information including performance timing data.
	Debug *DebugInfo `json:"debug,omitempty"`
	// Lifecycle is the lifecycle policy for auto-suspend.
	Lifecycle *SandboxLifecyclePolicy `json:"lifecycle,omitempty"`
}

// CreateSandboxRequest is the request for creating a sandbox.
type CreateSandboxRequest struct {
	// SourcesRef is the source reference (disk image or snapshot).
	SourcesRef SandboxSource `json:"sourcesRef"`
	// Resources is the resource configuration (required for disk image, not for snapshot).
	Resources *SandboxResources `json:"resources,omitempty"`
	// Labels are key-value pairs to attach to the sandbox.
	Labels map[string]string `json:"labels,omitempty"`
	// Entrypoint overrides the container entrypoint.
	Entrypoint []string `json:"entrypoint,omitempty"`
	// Cmd overrides the container command.
	Cmd []string `json:"cmd,omitempty"`
	// VmmType is the VMM type (required for disk image, not for snapshot).
	VmmType VmmType `json:"vmmType,omitempty"`
	// Connections are the connection IDs to associate with the sandbox.
	Connections []string `json:"connections,omitempty"`
	// EgressPolicy is the egress policy for outbound network traffic.
	EgressPolicy *EgressPolicy `json:"egressPolicy,omitempty"`
	// Environment contains environment variables to set in the sandbox.
	Environment map[string]string `json:"environment,omitempty"`
	// Ports are the ports to expose on the sandbox.
	Ports []AddPortRequest `json:"ports,omitempty"`
	// SkipEgressProxy bypasses the egress proxy for this sandbox.
	SkipEgressProxy bool `json:"skipEgressProxy,omitempty"`
	// Volumes are volumes to mount into the sandbox.
	Volumes []SandboxVolume `json:"volumes,omitempty"`
	// TelemetryConfig configures telemetry collection for the sandbox.
	TelemetryConfig *TelemetryConfig `json:"telemetryConfig,omitempty"`
	// ContentPackageDownloads are content packages to download into the sandbox during creation.
	ContentPackageDownloads []ContentPackageDownload `json:"contentPackageDownloads,omitempty"`
	// SandboxGroupID is the sandbox group ID to create the sandbox within.
	SandboxGroupID string `json:"sandboxGroupId,omitempty"`
}

// BatchSandboxRequest is the request for creating multiple sandboxes.
type BatchSandboxRequest struct {
	// Count is the number of sandboxes to create (1-1000).
	Count int `json:"count"`
	// Sandbox is the configuration to use for all sandboxes.
	Sandbox CreateSandboxRequest `json:"sandbox"`
}

// BatchSandboxResponse is the response for batch sandbox creation.
type BatchSandboxResponse struct {
	// TotalRequested is the number of sandboxes requested.
	TotalRequested int `json:"totalRequested"`
	// Succeeded is the number of sandboxes successfully created.
	Succeeded int `json:"succeeded"`
	// Failed is the number of sandboxes that failed to create.
	Failed int `json:"failed"`
	// Sandboxes are the successfully created sandboxes.
	Sandboxes []SandboxData `json:"sandboxes"`
	// Errors are the error messages for failed creations.
	Errors []string `json:"errors"`
}

// ExecuteCommandRequest is the request for executing a command in a sandbox.
type ExecuteCommandRequest struct {
	// Command is the command to execute.
	Command string `json:"command"`
	// Args are the command arguments.
	Args []string `json:"args,omitempty"`
	// Environment contains environment variables.
	Environment map[string]string `json:"environment,omitempty"`
	// WorkingDirectory is the working directory for the command.
	WorkingDirectory string `json:"workingDirectory,omitempty"`
}

// ExecuteShellCommandRequest is the request for executing a shell command.
type ExecuteShellCommandRequest struct {
	// Command is the shell command to execute.
	Command string `json:"command"`
	// Shell is the shell to use (default: /bin/sh).
	Shell string `json:"shell,omitempty"`
	// Environment contains environment variables.
	Environment map[string]string `json:"environment,omitempty"`
	// WorkingDirectory is the working directory for the command.
	WorkingDirectory string `json:"workingDirectory,omitempty"`
}

// CommandExecutionResult is the result of executing a command.
type CommandExecutionResult struct {
	// ExitCode is the exit code of the command.
	ExitCode int `json:"exitCode"`
	// Stdout is the standard output.
	Stdout string `json:"stdout"`
	// Stderr is the standard error.
	Stderr string `json:"stderr"`
	// ExecutionTimeMs is the execution time in milliseconds.
	ExecutionTimeMs int64 `json:"executionTimeMs"`
}

// AddPortRequest is the request for adding a port mapping.
type AddPortRequest struct {
	// Port is the port number to expose.
	Port int `json:"port"`
	// Name is the optional unique name for the port mapping.
	Name string `json:"name,omitempty"`
	// Auth is the optional auth configuration for the port.
	Auth *PortAuthConfig `json:"auth,omitempty"`
	// ActivationMode is the activation mode for the port.
	ActivationMode PortActivationMode `json:"activationMode,omitempty"`
	// Protocol is the HTTP protocol version for forwarding requests.
	Protocol PortProtocol `json:"protocol,omitempty"`
	// IpAccessControl is the optional IP access control configuration for the port.
	IpAccessControl *PortIpAccessControl `json:"ipAccessControl,omitempty"`
	// Cors is the optional CORS configuration for the port.
	Cors *PortCorsConfig `json:"cors,omitempty"`
}

// RemovePortRequest is the request for removing a port mapping.
// Either Port or Name should be set.
type RemovePortRequest struct {
	// Port is the port number to remove.
	Port int `json:"port,omitempty"`
	// Name is the name of the port mapping to remove.
	Name string `json:"name,omitempty"`
}

// PortsListResponse is the response containing a list of ports.
type PortsListResponse struct {
	// Ports is the list of ports.
	Ports []SandboxPort `json:"ports"`
}

// UpdatePortsRequest is the request to update all ports on a sandbox.
type UpdatePortsRequest struct {
	// Ports is the list of ports to set (replaces all existing ports).
	Ports []SandboxPort `json:"ports"`
}

// PatchPortRequest is the request to patch a single port on a sandbox.
// Identify the port by Port or Name, then set only the fields to update.
type PatchPortRequest struct {
	// Port is the port number to identify the port to patch.
	Port int `json:"port,omitempty"`
	// Name is the name to identify the port to patch.
	Name string `json:"name,omitempty"`
	// Auth is the auth configuration to update.
	Auth *PortAuthConfig `json:"auth,omitempty"`
	// ActivationMode is the activation mode to update.
	ActivationMode PortActivationMode `json:"activationMode,omitempty"`
	// Protocol is the protocol to update.
	Protocol PortProtocol `json:"protocol,omitempty"`
	// IpAccessControl is the IP access control configuration to update.
	IpAccessControl *PortIpAccessControl `json:"ipAccessControl,omitempty"`
	// Cors is the CORS configuration to update.
	Cors *PortCorsConfig `json:"cors,omitempty"`
}

// CreateFromDiskImageOptions contains options for creating a sandbox from a disk image.
type CreateFromDiskImageOptions struct {
	// DiskImage is the disk image source.
	DiskImage SandboxSourceDiskImage
	// CPU is the CPU allocation (default: "1000m").
	CPU string
	// Memory is the memory allocation (default: "1024Mi").
	Memory string
	// Labels are key-value pairs to attach to the sandbox.
	Labels map[string]string
	// Entrypoint overrides the container entrypoint.
	Entrypoint []string
	// Cmd overrides the container command.
	Cmd []string
	// VmmType is the VMM type (default: "cloudhypervisor").
	VmmType VmmType
	// Connections are the connection IDs to associate with the sandbox.
	Connections []string
	// EgressPolicy is the egress policy for outbound network traffic.
	EgressPolicy *EgressPolicy
	// Environment contains environment variables to set in the sandbox.
	Environment map[string]string
	// Ports are the ports to expose on the sandbox.
	Ports []AddPortRequest
	// SkipEgressProxy bypasses the egress proxy for this sandbox.
	SkipEgressProxy bool
	// Volumes are volumes to mount into the sandbox.
	Volumes []SandboxVolume
	// TelemetryConfig configures telemetry collection for the sandbox.
	TelemetryConfig *TelemetryConfig
	// ContentPackageDownloads are content packages to download into the sandbox during creation.
	ContentPackageDownloads []ContentPackageDownload
	// SandboxGroupID is the sandbox group ID to create the sandbox within.
	SandboxGroupID string
}

// CreateFromSnapshotOptions contains options for creating a sandbox from a snapshot.
type CreateFromSnapshotOptions struct {
	// SnapshotID is the ID of the snapshot to create from.
	SnapshotID string
	// Labels are key-value pairs to attach to the sandbox.
	Labels map[string]string
	// Entrypoint overrides the container entrypoint.
	Entrypoint []string
	// Cmd overrides the container command.
	Cmd []string
	// Connections are the connection IDs to associate with the sandbox.
	Connections []string
	// EgressPolicy is the egress policy for outbound network traffic.
	EgressPolicy *EgressPolicy
	// Ports are the ports to expose on the sandbox.
	Ports []AddPortRequest
	// SkipEgressProxy bypasses the egress proxy for this sandbox.
	SkipEgressProxy bool
	// TelemetryConfig configures telemetry collection for the sandbox.
	TelemetryConfig *TelemetryConfig
	// SandboxGroupID is the sandbox group ID to create the sandbox within.
	SandboxGroupID string
}

// AddPortOptions contains options for adding a port.
type AddPortOptions struct {
	// Name is the optional unique name for the port mapping.
	Name string
	// Auth is the optional auth configuration for the port.
	Auth *PortAuthConfig
	// ActivationMode is the activation mode for the port.
	ActivationMode PortActivationMode
	// Protocol is the HTTP protocol version for forwarding requests.
	Protocol PortProtocol
	// IpAccessControl is the optional IP access control configuration for the port.
	IpAccessControl *PortIpAccessControl
	// Cors is the optional CORS configuration for the port.
	Cors *PortCorsConfig
}

// WriteFileOptions contains options for writing a file.
type WriteFileOptions struct {
	// CreateDirs creates parent directories if they don't exist.
	CreateDirs bool
	// Mode is the file mode/permissions (Unix-style).
	Mode int
}

// MkdirOptions contains options for creating a directory.
type MkdirOptions struct {
	// CreateParents creates parent directories if they don't exist.
	CreateParents bool
	// Mode is the directory mode/permissions (Unix-style).
	Mode int
}
