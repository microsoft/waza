// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// DiskImageAPI provides methods for managing disk images.
type DiskImageAPI struct {
	client *HTTPClient
}

// NewDiskImageAPI creates a new DiskImageAPI.
func NewDiskImageAPI(client *HTTPClient) *DiskImageAPI {
	return &DiskImageAPI{client: client}
}

// Create creates a new disk image.
func (api *DiskImageAPI) Create(ctx context.Context, opts models.CreateDiskImageOptions) (*models.DiskImage, error) {
	request := models.CreateDiskImageRequest{
		Labels: opts.Labels,
		Image: models.DiskImageImage{
			Base:       opts.BaseImage,
			Entrypoint: opts.Entrypoint,
		},
		RegistryCredentials: opts.RegistryCredentials,
	}

	var diskImage models.DiskImage
	err := api.client.PutJSON(ctx, "/diskImages", request, nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}

// Get retrieves a disk image by ID.
func (api *DiskImageAPI) Get(ctx context.Context, diskImageID string) (*models.DiskImage, error) {
	var diskImage models.DiskImage
	err := api.client.GetJSON(ctx, fmt.Sprintf("/diskImages/%s", diskImageID), nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}

// List retrieves all disk images, optionally filtered by labels.
func (api *DiskImageAPI) List(ctx context.Context, opts *ListOptions) ([]models.DiskImage, error) {
	options := buildListOptions(opts)

	var diskImages []models.DiskImage
	err := api.client.GetJSON(ctx, "/diskImages", options, &diskImages)
	if err != nil {
		return nil, err
	}

	return diskImages, nil
}

// Delete deletes a disk image by ID.
func (api *DiskImageAPI) Delete(ctx context.Context, diskImageID string) error {
	_, err := api.client.Delete(ctx, fmt.Sprintf("/diskImages/%s", diskImageID), nil)
	return err
}

// ListPublic retrieves all publicly available disk images.
func (api *DiskImageAPI) ListPublic(ctx context.Context) ([]models.PublicDiskImage, error) {
	var publicImages []models.PublicDiskImage
	err := api.client.GetJSON(ctx, "/public/diskimages", nil, &publicImages)
	if err != nil {
		return nil, err
	}

	return publicImages, nil
}

// BuildFromDockerfile builds a disk image from a Dockerfile.
func (api *DiskImageAPI) BuildFromDockerfile(ctx context.Context, opts models.BuildDiskImageFromDockerfileOptions) (*models.DiskImage, error) {
	request := models.BuildDiskImageFromDockerfileRequest{
		Name:                    opts.Name,
		Dockerfile:              opts.Dockerfile,
		Labels:                  opts.Labels,
		BuildArgs:               opts.BuildArgs,
		Secrets:                 opts.Secrets,
		ContextContentPackageID: opts.ContextContentPackageID,
	}

	var diskImage models.DiskImage
	err := api.client.PostJSON(ctx, "/diskImages/dockerfile", request, nil, &diskImage)
	if err != nil {
		return nil, err
	}

	return &diskImage, nil
}
