// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// VolumeAPI provides methods for managing volumes.
type VolumeAPI struct {
	client *HTTPClient
}

// NewVolumeAPI creates a new VolumeAPI.
func NewVolumeAPI(client *HTTPClient) *VolumeAPI {
	return &VolumeAPI{client: client}
}

// Create creates a new volume.
func (api *VolumeAPI) Create(ctx context.Context, volumeName string, request models.CreateVolumeRequest) (*models.VolumeData, error) {
	var volume models.VolumeData
	err := api.client.PutJSON(ctx, fmt.Sprintf("/volumes/%s", volumeName), request, nil, &volume)
	if err != nil {
		return nil, err
	}

	return &volume, nil
}

// Get retrieves a volume by name.
func (api *VolumeAPI) Get(ctx context.Context, volumeName string) (*models.VolumeData, error) {
	var volume models.VolumeData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/volumes/%s", volumeName), nil, &volume)
	if err != nil {
		return nil, err
	}

	return &volume, nil
}

// List retrieves all volumes, optionally filtered by labels.
func (api *VolumeAPI) List(ctx context.Context, opts *ListOptions) ([]models.VolumeData, error) {
	options := buildListOptions(opts)

	var volumes []models.VolumeData
	err := api.client.GetJSON(ctx, "/volumes", options, &volumes)
	if err != nil {
		return nil, err
	}

	return volumes, nil
}

// Delete deletes a volume by name.
func (api *VolumeAPI) Delete(ctx context.Context, volumeName string) error {
	_, err := api.client.Delete(ctx, fmt.Sprintf("/volumes/%s", volumeName), nil)
	return err
}

// ListFiles lists the contents of a directory in a volume.
func (api *VolumeAPI) ListFiles(ctx context.Context, volumeName string, path string) (*models.VolumeDirectoryListing, error) {
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var listing models.VolumeDirectoryListing
	err := api.client.GetJSON(ctx, fmt.Sprintf("/volumes/%s/files", volumeName), options, &listing)
	if err != nil {
		return nil, err
	}

	return &listing, nil
}

// DownloadFile downloads a file from a volume.
func (api *VolumeAPI) DownloadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	return api.client.Get(ctx, fmt.Sprintf("/volumes/%s/files/download", volumeName), options)
}

// UploadFile uploads a file to a volume.
func (api *VolumeAPI) UploadFile(ctx context.Context, volumeName string, path string, data []byte, overwrite bool) (*models.VolumePathItem, error) {
	options := &RequestOptions{
		Params: map[string]string{
			"path":      path,
			"overwrite": fmt.Sprintf("%t", overwrite),
		},
	}

	var item models.VolumePathItem
	respData, err := api.client.Put(ctx, fmt.Sprintf("/volumes/%s/files/upload", volumeName), data, options)
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

// DeleteFile deletes a file or directory from a volume.
func (api *VolumeAPI) DeleteFile(ctx context.Context, volumeName string, path string, recursive bool) error {
	options := &RequestOptions{
		Params: map[string]string{
			"path":      path,
			"recursive": fmt.Sprintf("%t", recursive),
		},
	}

	_, err := api.client.Delete(ctx, fmt.Sprintf("/volumes/%s/files", volumeName), options)
	return err
}

// CreateDirectory creates a directory in a volume.
func (api *VolumeAPI) CreateDirectory(ctx context.Context, volumeName string, path string) (*models.VolumePathItem, error) {
	options := &RequestOptions{
		Params: map[string]string{"path": path},
	}

	var item models.VolumePathItem
	err := api.client.PostJSON(ctx, fmt.Sprintf("/volumes/%s/files/mkdir", volumeName), nil, options, &item)
	if err != nil {
		return nil, err
	}

	return &item, nil
}
