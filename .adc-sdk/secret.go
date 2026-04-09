// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"fmt"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

// SecretAPI provides methods for managing secrets.
type SecretAPI struct {
	client *HTTPClient
}

// NewSecretAPI creates a new SecretAPI.
func NewSecretAPI(client *HTTPClient) *SecretAPI {
	return &SecretAPI{client: client}
}

// Upsert creates or updates a secret's key-value pairs.
func (api *SecretAPI) Upsert(ctx context.Context, secretID string, values map[string]string) (*models.SecretData, error) {
	req := models.UpsertSecretRequest{Values: values}
	var result models.SecretData
	err := api.client.PutJSON(ctx, fmt.Sprintf("/secrets/%s", secretID), req, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// Peek retrieves a secret's key-value pairs.
func (api *SecretAPI) Peek(ctx context.Context, secretID string) (*models.SecretPeekResponse, error) {
	var result models.SecretPeekResponse
	err := api.client.PostJSON(ctx, fmt.Sprintf("/secrets/%s/peek", secretID), struct{}{}, nil, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

// List returns all secrets (metadata only).
func (api *SecretAPI) List(ctx context.Context) ([]models.SecretData, error) {
	var response models.SecretListResponse
	err := api.client.GetJSON(ctx, "/secrets", nil, &response)
	if err != nil {
		return nil, err
	}

	return response.Secrets, nil
}

// Delete removes a secret.
func (api *SecretAPI) Delete(ctx context.Context, secretID string) error {
	_, err := api.client.Delete(ctx, fmt.Sprintf("/secrets/%s", secretID), nil)
	return err
}
