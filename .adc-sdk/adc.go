// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Package adc provides a Go SDK for ADC (Azure Dev Compute) sandboxes.
//
// The SDK allows you to create and manage disk images, sandboxes, and snapshots
// for running isolated workloads in microVMs.
//
// Example:
//
//	client := adc.New(adc.Config{
//		APIKey: "your-api-key",
//	})
//
//	// Create a disk image
//	diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
//		Labels:    map[string]string{"name": "my-image"},
//		BaseImage: "docker.io/library/ubuntu:latest",
//	})
//
//	// Create a sandbox
//	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
//		DiskImage: models.SandboxSourceDiskImage{ID: diskImage.ID},
//		CPU:       "1000m",
//		Memory:    "1024Mi",
//	})
//
//	// Execute a command
//	result, err := sandbox.ExecuteShellCommand(ctx, "echo hello", "", nil, "")
package adc

import (
	"context"
	"fmt"
	"strings"
)

// ListOptions contains options for listing resources with pagination and label filtering.
type ListOptions struct {
	// Labels is a label filter (e.g., {"name": "foo", "env": "dev"}).
	Labels map[string]string
	// Page is the page number (default: 1).
	Page int
	// PageSize is the number of items per page (default: 100).
	PageSize int
}

// labelsToQueryString converts a labels map to a query string format.
func labelsToQueryString(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	parts := make([]string, 0, len(labels))
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// buildListOptions converts ListOptions to RequestOptions for HTTP requests.
func buildListOptions(opts *ListOptions) *RequestOptions {
	params := map[string]string{}

	page := 1
	pageSize := 100

	if opts != nil {
		if opts.Page > 0 {
			page = opts.Page
		}
		if opts.PageSize > 0 {
			pageSize = opts.PageSize
		}
		if len(opts.Labels) > 0 {
			params["labels"] = labelsToQueryString(opts.Labels)
		}
	}

	params["page"] = fmt.Sprintf("%d", page)
	params["pageSize"] = fmt.Sprintf("%d", pageSize)

	return &RequestOptions{Params: params}
}

// Client is the main ADC client for managing disk images, sandboxes, snapshots, and apps.
type Client struct {
	config *ConfigManager
	client *HTTPClient

	// DiskImages provides methods for managing disk images.
	DiskImages *DiskImageAPI
	// Sandboxes provides methods for managing sandboxes.
	Sandboxes *SandboxAPI
	// SandboxGroups provides methods for managing sandbox groups.
	SandboxGroups *SandboxGroupAPI
	// Snapshots provides methods for managing snapshots.
	Snapshots *SnapshotAPI
	// Apps provides methods for managing apps.
	Apps *AppAPI
	// Volumes provides methods for managing volumes.
	Volumes *VolumeAPI
	// Secrets provides methods for managing secrets.
	Secrets *SecretAPI
	// ContentPackages provides methods for managing content packages.
	ContentPackages *ContentPackageAPI
}

// New creates a new ADC client with the given configuration.
func New(config Config) *Client {
	cm := NewConfigManager(config)
	httpClient := createHTTPClient(cm)

	c := &Client{
		config: cm,
		client: httpClient,
	}

	c.DiskImages = NewDiskImageAPI(httpClient)
	c.Sandboxes = NewSandboxAPI(httpClient, cm)
	c.SandboxGroups = NewSandboxGroupAPI(httpClient, cm)
	c.Snapshots = NewSnapshotAPI(httpClient)
	c.Volumes = NewVolumeAPI(httpClient)
	c.Secrets = NewSecretAPI(httpClient)
	c.ContentPackages = NewContentPackageAPI(httpClient)

	return c
}

// createHTTPClient creates an HTTP client from the config manager.
func createHTTPClient(cm *ConfigManager) *HTTPClient {
	headers := map[string]string{
		"Content-Type": "application/json",
	}

	if cm.APIKey != "" {
		headers[apiKeyHeader] = cm.APIKey
	} else if cm.Token != "" {
		headers["Authorization"] = "Bearer " + cm.Token
	} else if cm.GitHubToken != "" {
		headers["Authorization"] = "GitHub " + cm.GitHubToken
	}

	defaultParams := map[string]string{}
	if cm.SandboxSpaceID != "" {
		defaultParams["sandboxSpaceId"] = cm.SandboxSpaceID
	}

	return NewHTTPClient(HTTPClientConfig{
		BaseURL:       cm.APIURL,
		Headers:       headers,
		Timeout:       cm.Timeout,
		DefaultParams: defaultParams,
	})
}

// Login authenticates with GitHub using Device Flow.
// The callback is called with the user code and verification URI for the user to complete authentication.
func (c *Client) Login(ctx context.Context, opts *LoginOptions) error {
	clientID := DefaultGitHubClientID
	if opts != nil && opts.ClientID != "" {
		clientID = opts.ClientID
	}

	auth := NewGitHubDeviceFlowAuth(clientID)

	var callback AuthCallback
	if opts != nil {
		callback = opts.Callback
	}

	token, err := auth.Authenticate(ctx, callback)
	if err != nil {
		return err
	}

	// Update config with token
	c.config.SetToken(token)

	// Reinitialize HTTP client with new token
	c.client = createHTTPClient(c.config)

	// Reinitialize API namespaces with new client
	c.DiskImages = NewDiskImageAPI(c.client)
	c.Sandboxes = NewSandboxAPI(c.client, c.config)
	c.SandboxGroups = NewSandboxGroupAPI(c.client, c.config)
	c.Snapshots = NewSnapshotAPI(c.client)
	c.Apps = NewAppAPI(c.client, c.config)
	c.Volumes = NewVolumeAPI(c.client)
	c.Secrets = NewSecretAPI(c.client)
	c.ContentPackages = NewContentPackageAPI(c.client)

	return nil
}

// LoginOptions contains options for the Login method.
type LoginOptions struct {
	// ClientID is the GitHub OAuth App client ID. Uses default if not provided.
	ClientID string
	// Callback is called when the user needs to authenticate.
	Callback AuthCallback
}

// Close closes the client and releases resources.
// Note: With net/http, explicit cleanup is not strictly necessary,
// but this method is provided for API consistency with other SDKs.
func (c *Client) Close() {
	// No explicit cleanup needed for net/http client
}
