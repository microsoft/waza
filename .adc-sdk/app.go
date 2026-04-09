// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// AppAPI provides methods for managing apps.
type AppAPI struct {
	client *HTTPClient
	config *ConfigManager
}

// NewAppAPI creates a new AppAPI.
func NewAppAPI(client *HTTPClient, config *ConfigManager) *AppAPI {
	return &AppAPI{client: client, config: config}
}

// Create creates a new app.
func (api *AppAPI) Create(ctx context.Context, request models.AppRequest) (*App, error) {
	var data models.AppData
	err := api.client.PutJSON(ctx, "/apps", request, nil, &data)
	if err != nil {
		return nil, err
	}

	return NewApp(api.client, api.config, data), nil
}

// Update updates an existing app.
func (api *AppAPI) Update(ctx context.Context, appID string, request models.AppRequest) (*App, error) {
	var data models.AppData
	err := api.client.PutJSON(ctx, fmt.Sprintf("/apps/%s", appID), request, nil, &data)
	if err != nil {
		return nil, err
	}

	return NewApp(api.client, api.config, data), nil
}

// Get retrieves an app by ID.
func (api *AppAPI) Get(ctx context.Context, appID string) (*App, error) {
	var data models.AppData
	err := api.client.GetJSON(ctx, fmt.Sprintf("/apps/%s", appID), nil, &data)
	if err != nil {
		return nil, err
	}

	return NewApp(api.client, api.config, data), nil
}

// List retrieves all apps, optionally filtered by labels.
func (api *AppAPI) List(ctx context.Context, opts *ListOptions) ([]*App, error) {
	options := buildListOptions(opts)

	var dataList []models.AppData
	err := api.client.GetJSON(ctx, "/apps", options, &dataList)
	if err != nil {
		return nil, err
	}

	apps := make([]*App, len(dataList))
	for i, data := range dataList {
		apps[i] = NewApp(api.client, api.config, data)
	}

	return apps, nil
}

// Count returns the number of apps.
func (api *AppAPI) Count(ctx context.Context) (int, error) {
	var count int
	err := api.client.GetJSON(ctx, "/apps/count", nil, &count)
	if err != nil {
		return 0, err
	}

	return count, nil
}
