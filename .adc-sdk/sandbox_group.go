// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"errors"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// errEmptyGroupID is returned when a groupID parameter is empty.
var errEmptyGroupID = errors.New("groupID is required and cannot be empty")

// SandboxGroupAPI provides methods for managing sandbox groups.
type SandboxGroupAPI struct {
	client *HTTPClient
	config *ConfigManager
}

// NewSandboxGroupAPI creates a new SandboxGroupAPI.
func NewSandboxGroupAPI(client *HTTPClient, config *ConfigManager) *SandboxGroupAPI {
	return &SandboxGroupAPI{client: client, config: config}
}

// Create creates a new sandbox group with the given request.
func (api *SandboxGroupAPI) Create(ctx context.Context, request models.CreateSandboxGroupRequest) (*models.SandboxGroup, error) {
	var data models.SandboxGroup
	err := api.client.PutJSON(ctx, "/sandboxGroups", request, nil, &data)
	if err != nil {
		return nil, err
	}

	return &data, nil
}

// Update updates an existing sandbox group.
func (api *SandboxGroupAPI) Update(ctx context.Context, groupID string, request models.UpdateSandboxGroupRequest) (*models.SandboxGroup, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var data models.SandboxGroup
	err := api.client.PutJSON(ctx, fmt.Sprintf("/sandboxGroups/%s", groupID), request, nil, &data)
	if err != nil {
		return nil, err
	}

	return &data, nil
}

// Get retrieves a sandbox group by ID.
func (api *SandboxGroupAPI) Get(ctx context.Context, groupID string) (*models.SandboxGroup, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var data models.SandboxGroup
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s", groupID), nil, &data)
	if err != nil {
		return nil, err
	}

	return &data, nil
}

// List retrieves all sandbox groups, optionally filtered by labels.
func (api *SandboxGroupAPI) List(ctx context.Context, opts *ListOptions) ([]*models.SandboxGroup, error) {
	options := buildListOptions(opts)

	var dataList []models.SandboxGroup
	err := api.client.GetJSON(ctx, "/sandboxGroups", options, &dataList)
	if err != nil {
		return nil, err
	}

	groups := make([]*models.SandboxGroup, len(dataList))
	for i := range dataList {
		groups[i] = &dataList[i]
	}

	return groups, nil
}

// GetSandbox retrieves a sandbox within a sandbox group.
func (api *SandboxGroupAPI) GetSandbox(ctx context.Context, groupID, sandboxID string) (*Sandbox, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var data models.SandboxData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/sandboxes/%s", groupID, sandboxID), nil, &data)
	if err != nil {
		return nil, err
	}

	return NewSandbox(api.client, api.config, data), nil
}

// ListSandboxes retrieves all sandboxes in a sandbox group, optionally filtered by labels.
func (api *SandboxGroupAPI) ListSandboxes(ctx context.Context, groupID string, opts *ListOptions) ([]*Sandbox, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := buildListOptions(opts)

	var dataList []models.SandboxData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/sandboxes", groupID), options, &dataList)
	if err != nil {
		return nil, err
	}

	sandboxes := make([]*Sandbox, len(dataList))
	for i, data := range dataList {
		sandboxes[i] = NewSandbox(api.client, api.config, data)
	}

	return sandboxes, nil
}

// Delete deletes a sandbox group by ID.
func (api *SandboxGroupAPI) Delete(ctx context.Context, groupID string) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s", groupID), nil)
	return err
}

// ── Disk Images ───────────────────────────────────────────────────────────────

// ListDiskImages retrieves disk images within a sandbox group.
func (api *SandboxGroupAPI) ListDiskImages(ctx context.Context, groupID string, opts *ListOptions) ([]models.DiskImage, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := buildListOptions(opts)

	var diskImages []models.DiskImage
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/diskimages", groupID), options, &diskImages)
	if err != nil {
		return nil, err
	}

	return diskImages, nil
}

// GetDiskImage retrieves a disk image within a sandbox group by ID.
func (api *SandboxGroupAPI) GetDiskImage(ctx context.Context, groupID, diskImageID string) (*models.DiskImage, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var diskImage models.DiskImage
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/diskimages/%s", groupID, diskImageID), nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}

// CreateDiskImage creates a disk image within a sandbox group.
func (api *SandboxGroupAPI) CreateDiskImage(ctx context.Context, groupID string, opts models.CreateDiskImageOptions) (*models.DiskImage, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	request := models.CreateDiskImageRequest{
		Labels: opts.Labels,
		Image: models.DiskImageImage{
			Base:       opts.BaseImage,
			Entrypoint: opts.Entrypoint,
		},
		RegistryCredentials: opts.RegistryCredentials,
	}

	var diskImage models.DiskImage
	err := api.client.PutJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/diskimages", groupID), request, nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}

// DeleteDiskImage deletes a disk image within a sandbox group.
func (api *SandboxGroupAPI) DeleteDiskImage(ctx context.Context, groupID, diskImageID string) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/diskimages/%s", groupID, diskImageID), nil)
	return err
}

// BuildDiskImageFromDockerfile builds a disk image from a Dockerfile within a sandbox group.
func (api *SandboxGroupAPI) BuildDiskImageFromDockerfile(ctx context.Context, groupID string, opts models.BuildDiskImageFromDockerfileOptions) (*models.DiskImage, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	request := models.BuildDiskImageFromDockerfileRequest{
		Name:                    opts.Name,
		Dockerfile:              opts.Dockerfile,
		Labels:                  opts.Labels,
		BuildArgs:               opts.BuildArgs,
		Secrets:                 opts.Secrets,
		ContextContentPackageID: opts.ContextContentPackageID,
	}

	var diskImage models.DiskImage
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/diskimages/dockerfile", groupID), request, nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}

// ── Snapshots ─────────────────────────────────────────────────────────────────

// ListSnapshots retrieves snapshots within a sandbox group.
func (api *SandboxGroupAPI) ListSnapshots(ctx context.Context, groupID string, opts *ListOptions) ([]models.Snapshot, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := buildListOptions(opts)

	var snapshots []models.Snapshot
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/snapshots", groupID), options, &snapshots)
	if err != nil {
		return nil, err
	}

	return snapshots, nil
}

// GetSnapshot retrieves a snapshot within a sandbox group by ID.
func (api *SandboxGroupAPI) GetSnapshot(ctx context.Context, groupID, snapshotID string) (*models.Snapshot, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var snapshot models.Snapshot
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/snapshots/%s", groupID, snapshotID), nil, &snapshot)
	if err != nil {
		return nil, err
	}

	return &snapshot, nil
}

// CountSnapshots returns the number of snapshots within a sandbox group.
func (api *SandboxGroupAPI) CountSnapshots(ctx context.Context, groupID string) (int, error) {
	if groupID == "" {
		return 0, errEmptyGroupID
	}
	var count int
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/snapshots/count", groupID), nil, &count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// DeleteSnapshot deletes a snapshot within a sandbox group.
func (api *SandboxGroupAPI) DeleteSnapshot(ctx context.Context, groupID, snapshotID string) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/snapshots/%s", groupID, snapshotID), nil)
	return err
}

// ── Sandbox Count ─────────────────────────────────────────────────────────────

// ── Secrets ───────────────────────────────────────────────────────────────────

// ListSecrets retrieves secrets within a sandbox group.
func (api *SandboxGroupAPI) ListSecrets(ctx context.Context, groupID string) ([]models.SecretData, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var response models.SecretListResponse
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/secrets", groupID), nil, &response)
	if err != nil {
		return nil, err
	}

	return response.Secrets, nil
}

// UpsertSecret creates or updates a secret within a sandbox group.
func (api *SandboxGroupAPI) UpsertSecret(ctx context.Context, groupID, secretID string, values map[string]string) (*models.SecretData, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	req := models.UpsertSecretRequest{Values: values}
	var result models.SecretData
	err := api.client.PutJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/secrets/%s", groupID, secretID), req, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// PeekSecret retrieves a secret's values within a sandbox group.
func (api *SandboxGroupAPI) PeekSecret(ctx context.Context, groupID, secretID string) (*models.SecretPeekResponse, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var result models.SecretPeekResponse
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/secrets/%s/peek", groupID, secretID), struct{}{}, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// DeleteSecret removes a secret within a sandbox group.
func (api *SandboxGroupAPI) DeleteSecret(ctx context.Context, groupID, secretID string) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/secrets/%s", groupID, secretID), nil)
	return err
}

// ── Sandbox Count ─────────────────────────────────────────────────────────────

// CountSandboxes returns the number of sandboxes within a sandbox group.
func (api *SandboxGroupAPI) CountSandboxes(ctx context.Context, groupID string) (int, error) {
	if groupID == "" {
		return 0, errEmptyGroupID
	}
	var count int
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/sandboxes/count", groupID), nil, &count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// ── Volumes ───────────────────────────────────────────────────────────────────

// ListVolumes retrieves volumes within a sandbox group.
func (api *SandboxGroupAPI) ListVolumes(ctx context.Context, groupID string, opts *ListOptions) ([]models.VolumeData, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := buildListOptions(opts)

	var volumes []models.VolumeData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes", groupID), options, &volumes)
	if err != nil {
		return nil, err
	}

	return volumes, nil
}

// GetVolume retrieves a volume within a sandbox group by name.
func (api *SandboxGroupAPI) GetVolume(ctx context.Context, groupID string, volumeName string) (*models.VolumeData, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var volume models.VolumeData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s", groupID, volumeName), nil, &volume)
	if err != nil {
		return nil, err
	}

	return &volume, nil
}

// CreateVolume creates a volume within a sandbox group.
func (api *SandboxGroupAPI) CreateVolume(ctx context.Context, groupID string, volumeName string, request models.CreateVolumeRequest) (*models.VolumeData, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	var volume models.VolumeData
	err := api.client.PutJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s", groupID, volumeName), request, nil, &volume)
	if err != nil {
		return nil, err
	}

	return &volume, nil
}

// DeleteVolume deletes a volume within a sandbox group.
func (api *SandboxGroupAPI) DeleteVolume(ctx context.Context, groupID string, volumeName string) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s", groupID, volumeName), nil)
	return err
}

// ListVolumeFiles lists the contents of a directory in a volume within a sandbox group.
func (api *SandboxGroupAPI) ListVolumeFiles(ctx context.Context, groupID string, volumeName string, path string) (*models.VolumeDirectoryListing, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var listing models.VolumeDirectoryListing
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s/files", groupID, volumeName), options, &listing)
	if err != nil {
		return nil, err
	}

	return &listing, nil
}

// DownloadVolumeFile downloads a file from a volume within a sandbox group.
func (api *SandboxGroupAPI) DownloadVolumeFile(ctx context.Context, groupID string, volumeName string, path string) ([]byte, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	return api.client.Get(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s/files/download", groupID, volumeName), options)
}

// UploadVolumeFile uploads a file to a volume within a sandbox group.
func (api *SandboxGroupAPI) UploadVolumeFile(ctx context.Context, groupID string, volumeName string, path string, data []byte, overwrite bool) (*models.VolumePathItem, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := &RequestOptions{
		Params: map[string]string{
			"path":      path,
			"overwrite": fmt.Sprintf("%t", overwrite),
		},
	}

	var item models.VolumePathItem
	respData, err := api.client.Put(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s/files/upload", groupID, volumeName), data, options)
	if err != nil {
		return nil, err
	}

	if len(respData) > 0 {
		if err := jsonUnmarshal(respData, &item); err != nil {
			return nil, err
		}
	}

	return &item, nil
}

// DeleteVolumeFile deletes a file or directory from a volume within a sandbox group.
func (api *SandboxGroupAPI) DeleteVolumeFile(ctx context.Context, groupID string, volumeName string, path string, recursive bool) error {
	if groupID == "" {
		return errEmptyGroupID
	}
	options := &RequestOptions{
		Params: map[string]string{
			"path":      path,
			"recursive": fmt.Sprintf("%t", recursive),
		},
	}

	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s/files", groupID, volumeName), options)
	return err
}

// CreateVolumeDirectory creates a directory in a volume within a sandbox group.
func (api *SandboxGroupAPI) CreateVolumeDirectory(ctx context.Context, groupID string, volumeName string, path string) (*models.VolumePathItem, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var item models.VolumePathItem
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/volumes/%s/files/mkdir", groupID, volumeName), nil, options, &item)
	if err != nil {
		return nil, err
	}

	return &item, nil
}

// ── Connections ───────────────────────────────────────────────────────

// ListConnections retrieves connections within a sandbox group.
func (api *SandboxGroupAPI) ListConnections(ctx context.Context, groupID string) ([]models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connections []models.Connection
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections", groupID), nil, &connections)
	if err != nil {
		return nil, err
	}

	return connections, nil
}

// GetConnection retrieves a connection within a sandbox group by ID.
func (api *SandboxGroupAPI) GetConnection(ctx context.Context, groupID, connectionID string) (*models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connection models.Connection
	err := api.client.GetJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s", groupID, connectionID), nil, &connection)
	if err != nil {
		return nil, err
	}

	return &connection, nil
}

// CreateConnection creates a connection within a sandbox group.
func (api *SandboxGroupAPI) CreateConnection(ctx context.Context, groupID string, request models.CreateConnectionRequest) (*models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connection models.Connection
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections", groupID), request, nil, &connection)
	if err != nil {
		return nil, err
	}

	return &connection, nil
}

// DeleteConnection deletes a connection within a sandbox group.
func (api *SandboxGroupAPI) DeleteConnection(ctx context.Context, groupID, connectionID string) error {
	if groupID == "" {
		return errEmptyGroupID
	}

	_, err := api.client.Delete(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s", groupID, connectionID), nil)
	return err
}

// AuthorizeConnection authorizes a connection within a sandbox group.
func (api *SandboxGroupAPI) AuthorizeConnection(ctx context.Context, groupID, connectionID string, request models.AuthorizeConnectionRequest) (*models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connection models.Connection
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s/authorize", groupID, connectionID), request, nil, &connection)
	if err != nil {
		return nil, err
	}

	return &connection, nil
}

// RefreshConnection refreshes a connection's credentials within a sandbox group.
func (api *SandboxGroupAPI) RefreshConnection(ctx context.Context, groupID, connectionID string) (*models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connection models.Connection
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s/refresh", groupID, connectionID), nil, nil, &connection)
	if err != nil {
		return nil, err
	}

	return &connection, nil
}

// GenerateConnectionConsentLink generates a consent link for an OAuth-based connection.
func (api *SandboxGroupAPI) GenerateConnectionConsentLink(ctx context.Context, groupID, connectionID string, redirectURL string) (*models.ConsentLinkResponse, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	request := models.GenerateConsentLinkRequest{RedirectURL: redirectURL}
	var response models.ConsentLinkResponse
	err := api.client.PostJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s/generateConsentLink", groupID, connectionID), request, nil, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}

// UpdateConnectionPolicyRules updates the policy rules on a connection.
func (api *SandboxGroupAPI) UpdateConnectionPolicyRules(ctx context.Context, groupID, connectionID string, request models.UpdatePolicyRulesRequest) (*models.Connection, error) {
	if groupID == "" {
		return nil, errEmptyGroupID
	}

	var connection models.Connection
	err := api.client.PutJSON(ctx, fmt.Sprintf("/sandboxGroups/%s/connections/%s/policyRules", groupID, connectionID), request, nil, &connection)
	if err != nil {
		return nil, err
	}

	return &connection, nil
}
