// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func TestSecretAPI_Upsert(t *testing.T) {
	expected := models.SecretData{
		ID:        "secret-1",
		CreatedAt: "2024-01-01T00:00:00Z",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/secrets/secret-1" {
			t.Errorf("expected path /secrets/secret-1, got %s", r.URL.Path)
		}

		var req models.UpsertSecretRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Values["key1"] != "val1" {
			t.Errorf("expected key1=val1, got %s", req.Values["key1"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.Secrets.Upsert(context.Background(), "secret-1", map[string]string{"key1": "val1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "secret-1" {
		t.Errorf("expected ID secret-1, got %s", result.ID)
	}
}

func TestSecretAPI_Peek(t *testing.T) {
	expected := models.SecretPeekResponse{
		Values: map[string]string{"key1": "val1", "key2": "val2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/secrets/secret-1/peek" {
			t.Errorf("expected path /secrets/secret-1/peek, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.Secrets.Peek(context.Background(), "secret-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Values["key1"] != "val1" {
		t.Errorf("expected key1=val1, got %s", result.Values["key1"])
	}
	if result.Values["key2"] != "val2" {
		t.Errorf("expected key2=val2, got %s", result.Values["key2"])
	}
}

func TestSecretAPI_List(t *testing.T) {
	expected := models.SecretListResponse{
		Secrets: []models.SecretData{
			{ID: "secret-1"},
			{ID: "secret-2"},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/secrets" {
			t.Errorf("expected path /secrets, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.Secrets.List(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 secrets, got %d", len(result))
	}
	if result[0].ID != "secret-1" {
		t.Errorf("expected ID secret-1, got %s", result[0].ID)
	}
}

func TestSecretAPI_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/secrets/secret-1" {
			t.Errorf("expected path /secrets/secret-1, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.Secrets.Delete(context.Background(), "secret-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
