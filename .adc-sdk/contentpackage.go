// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"bytes"
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// ContentPackageAPI provides methods for managing content packages.
type ContentPackageAPI struct {
	client *HTTPClient
}

// NewContentPackageAPI creates a new ContentPackageAPI.
func NewContentPackageAPI(client *HTTPClient) *ContentPackageAPI {
	return &ContentPackageAPI{client: client}
}

// Get retrieves a content package by ID.
func (api *ContentPackageAPI) Get(ctx context.Context, contentPackageID string) (*models.ContentPackage, error) {
	var contentPackage models.ContentPackage
	err := api.client.GetJSON(ctx, fmt.Sprintf("/contentpackages/%s", contentPackageID), nil, &contentPackage)
	if err != nil {
		return nil, err
	}

	return &contentPackage, nil
}

// List retrieves all content packages, optionally filtered by labels.
func (api *ContentPackageAPI) List(ctx context.Context, opts *ListOptions) ([]models.ContentPackage, error) {
	options := buildListOptions(opts)

	var response models.ContentPackageListResponse
	err := api.client.GetJSON(ctx, "/contentpackages", options, &response)
	if err != nil {
		return nil, err
	}

	return response.Value, nil
}

// CreateOptions contains options for creating a content package.
type CreateContentPackageOptions struct {
	// Data is the binary content to upload.
	Data []byte
	// Labels are key-value pairs to attach to the content package.
	Labels map[string]string
	// ContentType is the content type (default: application/octet-stream).
	ContentType string
}

// Create creates a new content package by uploading binary content.
func (api *ContentPackageAPI) Create(ctx context.Context, opts CreateContentPackageOptions) (*models.ContentPackage, error) {
	contentType := opts.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	params := map[string]string{}
	if len(opts.Labels) > 0 {
		params["labels"] = labelsToQueryString(opts.Labels)
	}

	var contentPackage models.ContentPackage
	err := api.client.PostBinary(ctx, "/contentpackages/upload", bytes.NewReader(opts.Data), contentType, &RequestOptions{Params: params}, &contentPackage)
	if err != nil {
		return nil, err
	}

	return &contentPackage, nil
}

// Delete deletes a content package by ID.
func (api *ContentPackageAPI) Delete(ctx context.Context, contentPackageID string) error {
	_, err := api.client.Delete(ctx, fmt.Sprintf("/contentpackages/%s", contentPackageID), nil)
	return err
}
