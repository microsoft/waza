// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// SnapshotAPI provides methods for managing snapshots.
type SnapshotAPI struct {
	client *HTTPClient
}

// NewSnapshotAPI creates a new SnapshotAPI.
func NewSnapshotAPI(client *HTTPClient) *SnapshotAPI {
	return &SnapshotAPI{client: client}
}

// List retrieves all snapshots, optionally filtered by labels.
func (api *SnapshotAPI) List(ctx context.Context, opts *ListOptions) ([]models.Snapshot, error) {
	options := buildListOptions(opts)

	var snapshots []models.Snapshot
	err := api.client.GetJSON(ctx, "/snapshots", options, &snapshots)
	if err != nil {
		return nil, err
	}

	return snapshots, nil
}

// Get retrieves a snapshot by ID.
func (api *SnapshotAPI) Get(ctx context.Context, snapshotID string) (*models.Snapshot, error) {
	var snapshot models.Snapshot
	err := api.client.GetJSON(ctx, fmt.Sprintf("/snapshots/%s", snapshotID), nil, &snapshot)
	if err != nil {
		return nil, err
	}

	return &snapshot, nil
}

// Count returns the number of snapshots.
func (api *SnapshotAPI) Count(ctx context.Context) (int, error) {
	var count int
	err := api.client.GetJSON(ctx, "/snapshots/count", nil, &count)
	if err != nil {
		return 0, err
	}

	return count, nil
}

// Delete deletes a snapshot by ID.
func (api *SnapshotAPI) Delete(ctx context.Context, snapshotID string) error {
	_, err := api.client.Delete(ctx, fmt.Sprintf("/snapshots/%s", snapshotID), nil)
	return err
}
