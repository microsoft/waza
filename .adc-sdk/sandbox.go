// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// SandboxAPI provides methods for managing sandboxes.
type SandboxAPI struct {
	client *HTTPClient
	config *ConfigManager
}

// NewSandboxAPI creates a new SandboxAPI.
func NewSandboxAPI(client *HTTPClient, config *ConfigManager) *SandboxAPI {
	return &SandboxAPI{client: client, config: config}
}

// Create creates a new sandbox with the given request.
func (api *SandboxAPI) Create(ctx context.Context, request models.CreateSandboxRequest) (*Sandbox, error) {
	var data models.SandboxData
	err := api.client.PutJSON(ctx, "/sandboxes", request, nil, &data)
	if err != nil {
		return nil, err
	}

	return NewSandbox(api.client, api.config, data), nil
}

// CreateFromDiskImage creates a sandbox from a disk image.
// Note: resources (cpu, memory) and vmmType can only be specified when creating
// from a disk image, not from a snapshot.
func (api *SandboxAPI) CreateFromDiskImage(ctx context.Context, opts models.CreateFromDiskImageOptions) (*Sandbox, error) {
	cpu := opts.CPU
	if cpu == "" {
		cpu = "1000m"
	}

	memory := opts.Memory
	if memory == "" {
		memory = "1024Mi"
	}

	vmmType := opts.VmmType
	if vmmType == "" {
		vmmType = models.VmmTypeCloudHypervisor
	}

	request := models.CreateSandboxRequest{
		SourcesRef: models.SandboxSource{
			DiskImage: &opts.DiskImage,
		},
		Resources: &models.SandboxResources{
			CPU:    cpu,
			Memory: memory,
		},
		Labels:                  opts.Labels,
		Entrypoint:              opts.Entrypoint,
		Cmd:                     opts.Cmd,
		VmmType:                 vmmType,
		Connections:             opts.Connections,
		EgressPolicy:            opts.EgressPolicy,
		Environment:             opts.Environment,
		Ports:                   opts.Ports,
		SkipEgressProxy:         opts.SkipEgressProxy,
		Volumes:                 opts.Volumes,
		TelemetryConfig:         opts.TelemetryConfig,
		ContentPackageDownloads: opts.ContentPackageDownloads,
		SandboxGroupID:          opts.SandboxGroupID,
	}

	return api.Create(ctx, request)
}

// CreateFromSnapshot creates a sandbox from a snapshot.
// Note: When creating from a snapshot, resources and vmmType cannot be specified.
// The sandbox will use the resources and VMM type that were configured when the
// snapshot was taken.
func (api *SandboxAPI) CreateFromSnapshot(ctx context.Context, opts models.CreateFromSnapshotOptions) (*Sandbox, error) {
	request := models.CreateSandboxRequest{
		SourcesRef: models.SandboxSource{
			Snapshot: &models.SandboxSourceSnapshot{
				ID: opts.SnapshotID,
			},
		},
		Labels:          opts.Labels,
		Entrypoint:      opts.Entrypoint,
		Cmd:             opts.Cmd,
		Connections:     opts.Connections,
		EgressPolicy:    opts.EgressPolicy,
		Ports:           opts.Ports,
		SkipEgressProxy: opts.SkipEgressProxy,
		TelemetryConfig: opts.TelemetryConfig,
		SandboxGroupID:  opts.SandboxGroupID,
	}

	return api.Create(ctx, request)
}

// Get retrieves a sandbox by ID.
func (api *SandboxAPI) Get(ctx context.Context, sandboxID string) (*Sandbox, error) {
	var data models.SandboxData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxes/%s", sandboxID), nil, &data)
	if err != nil {
		return nil, err
	}

	return NewSandbox(api.client, api.config, data), nil
}

// List retrieves all sandboxes, optionally filtered by labels.
func (api *SandboxAPI) List(ctx context.Context, opts *ListOptions) ([]*Sandbox, error) {
	options := buildListOptions(opts)

	var dataList []models.SandboxData
	err := api.client.GetJSON(ctx, "/sandboxes", options, &dataList)
	if err != nil {
		return nil, err
	}

	sandboxes := make([]*Sandbox, len(dataList))
	for i, data := range dataList {
		sandboxes[i] = NewSandbox(api.client, api.config, data)
	}

	return sandboxes, nil
}

// Count returns the number of sandboxes.
func (api *SandboxAPI) Count(ctx context.Context) (int, error) {
	var count int
	err := api.client.GetJSON(ctx, "/sandboxes/count", nil, &count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// CreateBatch creates multiple sandboxes in batch.
func (api *SandboxAPI) CreateBatch(ctx context.Context, request models.BatchSandboxRequest) (*models.BatchSandboxResponse, error) {
	var response models.BatchSandboxResponse
	err := api.client.PutJSON(ctx, "/sandboxes/batch", request, nil, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}
