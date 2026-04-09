// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// Sandbox represents an ADC sandbox instance with methods for management.
type Sandbox struct {
	client *HTTPClient
	config *ConfigManager
	// Data contains the sandbox data from the API.
	Data models.SandboxData
}

// NewSandbox creates a new Sandbox instance.
func NewSandbox(client *HTTPClient, config *ConfigManager, data models.SandboxData) *Sandbox {
	return &Sandbox{
		client: client,
		config: config,
		Data:   data,
	}
}

// ID returns the sandbox ID.
func (s *Sandbox) ID() string {
	return s.Data.ID
}

// sandboxBasePath returns the base API path for this sandbox.
// If the sandbox belongs to a group, returns /sandboxGroups/{groupId}/sandboxes/{id}.
// Otherwise, returns /sandboxes/{id}.
func (s *Sandbox) sandboxBasePath() string {
	if s.Data.SandboxGroupID != "" {
		return fmt.Sprintf("/sandboxGroups/%s/sandboxes/%s", s.Data.SandboxGroupID, s.ID())
	}
	return fmt.Sprintf("/sandboxes/%s", s.ID())
}

// Labels returns the labels attached to the sandbox.
func (s *Sandbox) Labels() map[string]string {
	return s.Data.Labels
}

// State returns the current state of the sandbox.
func (s *Sandbox) State() models.SandboxState {
	return s.Data.State
}

// VmmType returns the VMM type of the sandbox.
func (s *Sandbox) VmmType() models.VmmType {
	return s.Data.VmmType
}

// Ports returns the port mappings for the sandbox.
func (s *Sandbox) Ports() []models.SandboxPort {
	return s.Data.Ports
}

// Connections returns the connection IDs associated with the sandbox.
func (s *Sandbox) Connections() []string {
	return s.Data.Connections
}

// EgressPolicy returns the egress policy for the sandbox.
func (s *Sandbox) EgressPolicy() *models.EgressPolicy {
	return s.Data.EgressPolicy
}

// Debug returns the debug information for the sandbox.
func (s *Sandbox) Debug() *models.DebugInfo {
	return s.Data.Debug
}

// Refresh updates the sandbox data from the API.
func (s *Sandbox) Refresh(ctx context.Context) error {
	var data models.SandboxData
	err := s.client.GetJSON(ctx, s.sandboxBasePath(), nil, &data)
	if err != nil {
		return err
	}
	s.Data = data
	return nil
}

// ExecuteCommand executes a command in the sandbox (HTTP).
func (s *Sandbox) ExecuteCommand(ctx context.Context, command string, args []string, environment map[string]string, workingDirectory string) (*models.CommandExecutionResult, error) {
	request := models.ExecuteCommandRequest{
		Command:          command,
		Args:             args,
		Environment:      environment,
		WorkingDirectory: workingDirectory,
	}

	var result models.CommandExecutionResult
	err := s.client.PostJSON(ctx, fmt.Sprintf("%s/executeCommand", s.sandboxBasePath()), request, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// ExecuteShellCommand executes a shell command in the sandbox (HTTP).
func (s *Sandbox) ExecuteShellCommand(ctx context.Context, command, shell string, environment map[string]string, workingDirectory string) (*models.CommandExecutionResult, error) {
	request := models.ExecuteShellCommandRequest{
		Command:          command,
		Shell:            shell,
		Environment:      environment,
		WorkingDirectory: workingDirectory,
	}

	var result models.CommandExecutionResult
	err := s.client.PostJSON(ctx, fmt.Sprintf("%s/executeShellCommand", s.sandboxBasePath()), request, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// StartExecStream starts an interactive exec stream session via WebSocket.
// This provides real-time streaming output and supports interactive
// use cases like TTY mode, stdin input, and terminal resizing.
func (s *Sandbox) StartExecStream(ctx context.Context, request *models.ExecStreamStartRequest) (*ExecStreamSession, error) {
	if s.config == nil {
		return nil, fmt.Errorf("streaming not available: sandbox must be created via Adc client")
	}

	session, wsURL, headers := newExecStreamSession(s.config.APIURL, s.config.APIKey, s.config.Token, s.config.GitHubToken, s.sandboxBasePath())
	if err := session.connect(wsURL, headers, request); err != nil {
		return nil, err
	}
	return session, nil
}

// StartPortForward starts a port-forward session to tunnel TCP traffic to a port inside the sandbox.
//
// Establishes a bidirectional TCP tunnel via WebSocket. After the handshake,
// all data is transferred as raw binary frames for maximum throughput.
//
// Example:
//
//	session, err := sandbox.StartPortForward(ctx, 8080)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer session.Close()
//
//	// Send an HTTP request
//	session.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
//
//	// Read the response
//	for chunk := range session.Read(ctx) {
//	    fmt.Print(string(chunk))
//	}
func (s *Sandbox) StartPortForward(ctx context.Context, port int) (*PortForwardSession, error) {
	if s.config == nil {
		return nil, fmt.Errorf("streaming not available: sandbox must be created via Adc client")
	}

	session, wsURL, headers := newPortForwardSession(s.config.APIURL, s.config.APIKey, s.config.Token, s.config.GitHubToken, s.sandboxBasePath())
	if err := session.connect(wsURL, headers, port); err != nil {
		return nil, err
	}
	return session, nil
}

// LogStreamOptions configures log streaming behavior.
type LogStreamOptions struct {
	// Tail is the number of historical lines to include (0-300, default: 100).
	// Use a pointer to distinguish between "not set" (nil, uses default 100) and "explicitly 0" (no history).
	Tail *int
	// LogFormat is the format: "text" (plain text) or "json" (NDJSON).
	LogFormat string
}

// IntPtr returns a pointer to the given int value. Helper for LogStreamOptions.Tail.
func IntPtr(i int) *int {
	return &i
}

// StreamLogs streams container logs in real-time via HTTP chunked transfer.
// It returns a channel that yields log lines as they arrive.
// The channel is closed when the stream ends or an error occurs.
//
// Example:
//
//	lines, err := sandbox.StreamLogs(ctx, &adc.LogStreamOptions{Tail: adc.IntPtr(10)})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	for line := range lines {
//	    fmt.Println(line)
//	}
func (s *Sandbox) StreamLogs(ctx context.Context, options *LogStreamOptions) (<-chan string, error) {
	tail := 100
	logFormat := "text"
	if options != nil {
		if options.Tail != nil {
			tail = *options.Tail
		}
		if options.LogFormat != "" {
			logFormat = options.LogFormat
		}
	}

	url := fmt.Sprintf("%s/logstream?tailLines=%d&logFormat=%s", s.sandboxBasePath(), tail, logFormat)

	resp, err := s.client.GetStream(ctx, url)
	if err != nil {
		return nil, err
	}

	ch := make(chan string)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			case ch <- scanner.Text():
			}
		}
	}()

	return ch, nil
}

// Resume starts a stopped sandbox.
func (s *Sandbox) Resume(ctx context.Context) error {
	_, err := s.client.Post(ctx, fmt.Sprintf("%s/resume", s.sandboxBasePath()), nil, nil)
	if err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// Stop stops a running sandbox.
func (s *Sandbox) Stop(ctx context.Context) error {
	_, err := s.client.Post(ctx, fmt.Sprintf("%s/stop", s.sandboxBasePath()), nil, nil)
	if err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// Snapshot creates a snapshot of the sandbox.
func (s *Sandbox) Snapshot(ctx context.Context, labels map[string]string) (*models.Snapshot, error) {
	request := models.CreateSnapshotRequest{
		Labels: labels,
	}

	var snapshot models.Snapshot
	err := s.client.PostJSON(ctx, fmt.Sprintf("%s/snapshot", s.sandboxBasePath()), request, nil, &snapshot)
	if err != nil {
		return nil, err
	}

	return &snapshot, nil
}

// Delete deletes the sandbox.
func (s *Sandbox) Delete(ctx context.Context) error {
	_, err := s.client.Delete(ctx, s.sandboxBasePath(), nil)
	return err
}

// AddPort adds a port mapping to the sandbox.
func (s *Sandbox) AddPort(ctx context.Context, port int, opts *models.AddPortOptions) (*models.SandboxPort, error) {
	request := models.AddPortRequest{
		Port: port,
	}

	if opts != nil {
		request.Name = opts.Name
		request.Auth = opts.Auth
		request.ActivationMode = opts.ActivationMode
		request.Protocol = opts.Protocol
		request.IpAccessControl = opts.IpAccessControl
		request.Cors = opts.Cors
	}

	var response models.PortsListResponse
	err := s.client.PostJSON(ctx, fmt.Sprintf("%s/ports/add", s.sandboxBasePath()), request, nil, &response)
	if err != nil {
		return nil, err
	}

	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}

	// Find the port we just added
	for _, p := range response.Ports {
		if p.Port == port {
			return &p, nil
		}
	}

	return nil, fmt.Errorf("port %d was added but not found in response", port)
}

// RemovePort removes a port mapping from the sandbox.
// Either Port or Name must be specified in the request.
func (s *Sandbox) RemovePort(ctx context.Context, request models.RemovePortRequest) error {
	_, err := s.client.Post(ctx, fmt.Sprintf("%s/ports/remove", s.sandboxBasePath()), request, nil)
	if err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// ListPorts lists all port mappings for the sandbox.
func (s *Sandbox) ListPorts(ctx context.Context) ([]models.SandboxPort, error) {
	var response models.PortsListResponse
	err := s.client.GetJSON(ctx, fmt.Sprintf("%s/ports", s.sandboxBasePath()), nil, &response)
	if err != nil {
		return nil, err
	}
	return response.Ports, nil
}

// UpdatePorts updates all port mappings for the sandbox.
// This replaces all existing port mappings with the provided list.
func (s *Sandbox) UpdatePorts(ctx context.Context, ports []models.SandboxPort) ([]models.SandboxPort, error) {
	request := models.UpdatePortsRequest{
		Ports: ports,
	}

	var response models.PortsListResponse
	err := s.client.PutJSON(ctx, fmt.Sprintf("%s/ports", s.sandboxBasePath()), request, nil, &response)
	if err != nil {
		return nil, err
	}

	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}

	return response.Ports, nil
}

// PatchPort patches a single port mapping identified by port number or name.
// Only the specified fields in the request are updated; other fields remain unchanged.
func (s *Sandbox) PatchPort(ctx context.Context, request models.PatchPortRequest) ([]models.SandboxPort, error) {
	var response models.PortsListResponse
	err := s.client.PatchJSON(ctx, fmt.Sprintf("%s/ports", s.sandboxBasePath()), request, nil, &response)
	if err != nil {
		return nil, err
	}

	if err := s.Refresh(ctx); err != nil {
		return nil, err
	}

	return response.Ports, nil
}

// SetEgressPolicy sets the egress policy for the sandbox.
func (s *Sandbox) SetEgressPolicy(ctx context.Context, policy models.EgressPolicy) error {
	_, err := s.client.Put(ctx, fmt.Sprintf("%s/egress-policy", s.sandboxBasePath()), policy, nil)
	if err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// SetLifecyclePolicy sets the lifecycle policy for the sandbox.
func (s *Sandbox) SetLifecyclePolicy(ctx context.Context, policy models.SandboxLifecyclePolicy) error {
	_, err := s.client.Post(ctx, fmt.Sprintf("%s/lifecycle", s.sandboxBasePath()), policy, nil)
	if err != nil {
		return err
	}
	return s.Refresh(ctx)
}

// GetEgressDecisions returns the egress decisions for the sandbox.
func (s *Sandbox) GetEgressDecisions(ctx context.Context) (*models.EgressDecisionsResponse, error) {
	var response models.EgressDecisionsResponse
	err := s.client.GetJSON(ctx, fmt.Sprintf("%s/egress-decisions", s.sandboxBasePath()), nil, &response)
	if err != nil {
		return nil, err
	}
	return &response, nil
}

// ==================== File Operations ====================

// ReadFile reads a file from the sandbox.
func (s *Sandbox) ReadFile(ctx context.Context, path string) ([]byte, error) {
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	return s.client.Get(ctx, fmt.Sprintf("%s/files", s.sandboxBasePath()), options)
}

// ReadFileText reads a text file from the sandbox.
func (s *Sandbox) ReadFileText(ctx context.Context, path string) (string, error) {
	data, err := s.ReadFile(ctx, path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFile writes a file to the sandbox.
func (s *Sandbox) WriteFile(ctx context.Context, path string, content []byte, opts *models.WriteFileOptions) (*models.WriteFileResult, error) {
	params := map[string]string{
		"path":       path,
		"createDirs": "false",
	}

	if opts != nil {
		if opts.CreateDirs {
			params["createDirs"] = "true"
		}
		if opts.Mode != 0 {
			params["mode"] = strconv.Itoa(opts.Mode)
		}
	}

	options := &RequestOptions{
		Params: params,
		Headers: map[string]string{
			"Content-Type": "application/octet-stream",
		},
	}

	var result models.WriteFileResult
	err := s.client.PutJSON(ctx, fmt.Sprintf("%s/files", s.sandboxBasePath()), content, options, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// WriteFileText writes a text file to the sandbox.
func (s *Sandbox) WriteFileText(ctx context.Context, path, content string, opts *models.WriteFileOptions) (*models.WriteFileResult, error) {
	return s.WriteFile(ctx, path, []byte(content), opts)
}

// ListFiles lists directory contents in the sandbox.
func (s *Sandbox) ListFiles(ctx context.Context, path string) (*models.DirListing, error) {
	if path == "" {
		path = "/"
	}

	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var listing models.DirListing
	err := s.client.GetJSON(ctx, fmt.Sprintf("%s/files/list", s.sandboxBasePath()), options, &listing)
	if err != nil {
		return nil, err
	}

	return &listing, nil
}

// StatFile gets file or directory metadata.
func (s *Sandbox) StatFile(ctx context.Context, path string) (*models.FileInfo, error) {
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var info models.FileInfo
	err := s.client.GetJSON(ctx, fmt.Sprintf("%s/files/stat", s.sandboxBasePath()), options, &info)
	if err != nil {
		return nil, err
	}

	return &info, nil
}

// DeleteFile deletes a file or directory in the sandbox.
func (s *Sandbox) DeleteFile(ctx context.Context, path string, recursive bool) (*models.FileOpResult, error) {
	options := &RequestOptions{
		Params: map[string]string{
			"path":      path,
			"recursive": strconv.FormatBool(recursive),
		},
	}

	data, err := s.client.Delete(ctx, fmt.Sprintf("%s/files", s.sandboxBasePath()), options)
	if err != nil {
		return nil, err
	}

	var result models.FileOpResult
	if len(data) > 0 {
		if err := jsonUnmarshal(data, &result); err != nil {
			return nil, err
		}
	}

	return &result, nil
}

// Mkdir creates a directory in the sandbox.
func (s *Sandbox) Mkdir(ctx context.Context, path string, opts *models.MkdirOptions) (*models.FileOpResult, error) {
	request := models.MkDirRequest{
		Path: path,
	}

	if opts != nil {
		request.CreateParents = opts.CreateParents
		request.Mode = opts.Mode
	}

	var result models.FileOpResult
	err := s.client.PostJSON(ctx, fmt.Sprintf("%s/files/mkdir", s.sandboxBasePath()), request, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// String returns a string representation of the sandbox.
func (s *Sandbox) String() string {
	return fmt.Sprintf("Sandbox(id=%s, state=%s, vmmType=%s)", s.ID(), s.State(), s.VmmType())
}

// jsonUnmarshal is a helper for unmarshaling JSON.
func jsonUnmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
