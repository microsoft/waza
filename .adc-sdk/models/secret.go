// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package models

// SecretData represents metadata for a secret.
type SecretData struct {
	// ID is the unique identifier for the secret.
	ID string `json:"id"`
	// CreatedAt is the timestamp when the secret was created.
	CreatedAt string `json:"createdAt,omitempty"`
	// UpdatedAt is the timestamp when the secret was last updated.
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// SecretPeekResponse contains the key-value pairs of a secret.
type SecretPeekResponse struct {
	// Values are the secret key-value pairs.
	Values map[string]string `json:"values"`
}

// SecretListResponse is the response from listing secrets.
type SecretListResponse struct {
	// Secrets is the list of secret metadata.
	Secrets []SecretData `json:"secrets"`
}

// UpsertSecretRequest is the request body for creating or updating a secret.
type UpsertSecretRequest struct {
	// Values are the key-value pairs to store.
	Values map[string]string `json:"values"`
}
