// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func TestSandboxGroupAPI_Create(t *testing.T) {
	expected := models.SandboxGroup{
		ID:     "grp-123",
		Labels: map[string]string{"env": "test"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups" {
			t.Errorf("expected path /sandboxGroups, got %s", r.URL.Path)
		}

		var req models.CreateSandboxGroupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Labels["env"] != "test" {
			t.Errorf("expected label env=test, got %s", req.Labels["env"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.Create(context.Background(), models.CreateSandboxGroupRequest{
		Labels: map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "grp-123" {
		t.Errorf("expected ID grp-123, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_Update(t *testing.T) {
	expected := models.SandboxGroup{
		ID:     "grp-123",
		Labels: map[string]string{"env": "prod"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123" {
			t.Errorf("expected path /sandboxGroups/grp-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.Update(context.Background(), "grp-123", models.UpdateSandboxGroupRequest{
		Labels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Labels["env"] != "prod" {
		t.Errorf("expected label env=prod, got %s", result.Labels["env"])
	}
}

func TestSandboxGroupAPI_Get(t *testing.T) {
	expected := models.SandboxGroup{
		ID:     "grp-123",
		Labels: map[string]string{"env": "test"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123" {
			t.Errorf("expected path /sandboxGroups/grp-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.Get(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "grp-123" {
		t.Errorf("expected ID grp-123, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_List(t *testing.T) {
	expected := []models.SandboxGroup{
		{ID: "grp-1", Labels: map[string]string{}},
		{ID: "grp-2", Labels: map[string]string{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups" {
			t.Errorf("expected path /sandboxGroups, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 groups, got %d", len(result))
	}
}

func TestSandboxGroupAPI_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123" {
			t.Errorf("expected path /sandboxGroups/grp-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.Delete(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxGroupAPI_GetSandbox(t *testing.T) {
	expected := models.SandboxData{
		ID:    "sb-456",
		State: models.SandboxStateRunning,
		Ports: []models.SandboxPort{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/sandboxes/sb-456" {
			t.Errorf("expected path /sandboxGroups/grp-123/sandboxes/sb-456, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GetSandbox(context.Background(), "grp-123", "sb-456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID() != "sb-456" {
		t.Errorf("expected ID sb-456, got %s", result.ID())
	}
}

func TestSandboxGroupAPI_ListSandboxes(t *testing.T) {
	expected := []models.SandboxData{
		{ID: "sb-1", Ports: []models.SandboxPort{}},
		{ID: "sb-2", Ports: []models.SandboxPort{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/sandboxes" {
			t.Errorf("expected path /sandboxGroups/grp-123/sandboxes, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListSandboxes(context.Background(), "grp-123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 sandboxes, got %d", len(result))
	}
}

func TestSandboxGroupAPI_CountSandboxes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxGroups/grp-123/sandboxes/count" {
			t.Errorf("expected path /sandboxGroups/grp-123/sandboxes/count, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(5)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	count, err := client.SandboxGroups.CountSandboxes(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 5 {
		t.Errorf("expected count 5, got %d", count)
	}
}

// ── Disk Images ───────────────────────────────────────────────────────────────

func TestSandboxGroupAPI_ListDiskImages(t *testing.T) {
	expected := []models.DiskImage{
		{ID: "di-1"},
		{ID: "di-2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/diskimages" {
			t.Errorf("expected path /sandboxGroups/grp-123/diskimages, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListDiskImages(context.Background(), "grp-123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 disk images, got %d", len(result))
	}
}

func TestSandboxGroupAPI_GetDiskImage(t *testing.T) {
	expected := models.DiskImage{ID: "di-123"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/diskimages/di-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/diskimages/di-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GetDiskImage(context.Background(), "grp-123", "di-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "di-123" {
		t.Errorf("expected ID di-123, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_CreateDiskImage(t *testing.T) {
	expected := models.DiskImage{ID: "di-new"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/diskimages" {
			t.Errorf("expected path /sandboxGroups/grp-123/diskimages, got %s", r.URL.Path)
		}

		var req models.CreateDiskImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Image.Base != "ubuntu:latest" {
			t.Errorf("expected base image ubuntu:latest, got %s", req.Image.Base)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.CreateDiskImage(context.Background(), "grp-123", models.CreateDiskImageOptions{
		Labels:    map[string]string{"name": "test"},
		BaseImage: "ubuntu:latest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "di-new" {
		t.Errorf("expected ID di-new, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_DeleteDiskImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/diskimages/di-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/diskimages/di-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteDiskImage(context.Background(), "grp-123", "di-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxGroupAPI_BuildDiskImageFromDockerfile(t *testing.T) {
	expected := models.DiskImage{ID: "di-docker"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/diskimages/dockerfile" {
			t.Errorf("expected path /sandboxGroups/grp-123/diskimages/dockerfile, got %s", r.URL.Path)
		}

		var req models.BuildDiskImageFromDockerfileRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Name != "my-image" {
			t.Errorf("expected name my-image, got %s", req.Name)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.BuildDiskImageFromDockerfile(context.Background(), "grp-123", models.BuildDiskImageFromDockerfileOptions{
		Name:       "my-image",
		Dockerfile: "FROM ubuntu:latest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "di-docker" {
		t.Errorf("expected ID di-docker, got %s", result.ID)
	}
}

// ── Snapshots ─────────────────────────────────────────────────────────────────

func TestSandboxGroupAPI_ListSnapshots(t *testing.T) {
	expected := []models.Snapshot{
		{ID: "snap-1"},
		{ID: "snap-2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/snapshots" {
			t.Errorf("expected path /sandboxGroups/grp-123/snapshots, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListSnapshots(context.Background(), "grp-123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(result))
	}
}

func TestSandboxGroupAPI_GetSnapshot(t *testing.T) {
	expected := models.Snapshot{ID: "snap-123"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/snapshots/snap-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/snapshots/snap-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GetSnapshot(context.Background(), "grp-123", "snap-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "snap-123" {
		t.Errorf("expected ID snap-123, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_CountSnapshots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxGroups/grp-123/snapshots/count" {
			t.Errorf("expected path /sandboxGroups/grp-123/snapshots/count, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(10)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	count, err := client.SandboxGroups.CountSnapshots(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 10 {
		t.Errorf("expected count 10, got %d", count)
	}
}

func TestSandboxGroupAPI_DeleteSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/snapshots/snap-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/snapshots/snap-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteSnapshot(context.Background(), "grp-123", "snap-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Secrets ───────────────────────────────────────────────────────────────────

func TestSandboxGroupAPI_ListSecrets(t *testing.T) {
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
		if r.URL.Path != "/sandboxGroups/grp-123/secrets" {
			t.Errorf("expected path /sandboxGroups/grp-123/secrets, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListSecrets(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 secrets, got %d", len(result))
	}
}

func TestSandboxGroupAPI_UpsertSecret(t *testing.T) {
	expected := models.SecretData{ID: "secret-1", CreatedAt: "2024-01-01T00:00:00Z"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/secrets/secret-1" {
			t.Errorf("expected path /sandboxGroups/grp-123/secrets/secret-1, got %s", r.URL.Path)
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
	result, err := client.SandboxGroups.UpsertSecret(context.Background(), "grp-123", "secret-1", map[string]string{"key1": "val1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "secret-1" {
		t.Errorf("expected ID secret-1, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_PeekSecret(t *testing.T) {
	expected := models.SecretPeekResponse{
		Values: map[string]string{"key1": "val1"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/secrets/secret-1/peek" {
			t.Errorf("expected path /sandboxGroups/grp-123/secrets/secret-1/peek, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.PeekSecret(context.Background(), "grp-123", "secret-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Values["key1"] != "val1" {
		t.Errorf("expected key1=val1, got %s", result.Values["key1"])
	}
}

func TestSandboxGroupAPI_DeleteSecret(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/secrets/secret-1" {
			t.Errorf("expected path /sandboxGroups/grp-123/secrets/secret-1, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteSecret(context.Background(), "grp-123", "secret-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── Connections ───────────────────────────────────────────────────────────────

func TestSandboxGroupAPI_ListConnections(t *testing.T) {
	expected := []models.Connection{
		{ID: "conn-1", Name: "my-conn-1", Type: "github"},
		{ID: "conn-2", Name: "my-conn-2", Type: "azure"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListConnections(context.Background(), "grp-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("expected 2 connections, got %d", len(result))
	}
}

func TestSandboxGroupAPI_GetConnection(t *testing.T) {
	expected := models.Connection{ID: "conn-123", Name: "my-conn", Type: "github"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GetConnection(context.Background(), "grp-123", "conn-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "conn-123" {
		t.Errorf("expected ID conn-123, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_CreateConnection(t *testing.T) {
	expected := models.Connection{ID: "conn-new", Name: "new-conn", Type: "github"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections, got %s", r.URL.Path)
		}

		var req models.CreateConnectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Name != "new-conn" {
			t.Errorf("expected Name new-conn, got %s", req.Name)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.CreateConnection(context.Background(), "grp-123", models.CreateConnectionRequest{
		Name: "new-conn",
		Type: "github",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ID != "conn-new" {
		t.Errorf("expected ID conn-new, got %s", result.ID)
	}
}

func TestSandboxGroupAPI_DeleteConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteConnection(context.Background(), "grp-123", "conn-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxGroupAPI_AuthorizeConnection(t *testing.T) {
	expected := models.Connection{ID: "conn-123", Name: "my-conn", State: "authorized"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123/authorize" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123/authorize, got %s", r.URL.Path)
		}

		var req models.AuthorizeConnectionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.ParameterValues["token"] != "abc" {
			t.Errorf("expected parameter token=abc, got %s", req.ParameterValues["token"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.AuthorizeConnection(context.Background(), "grp-123", "conn-123", models.AuthorizeConnectionRequest{
		ParameterValues: map[string]string{"token": "abc"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "authorized" {
		t.Errorf("expected State authorized, got %s", result.State)
	}
}

func TestSandboxGroupAPI_RefreshConnection(t *testing.T) {
	expected := models.Connection{ID: "conn-123", Name: "my-conn", State: "connected"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123/refresh" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123/refresh, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.RefreshConnection(context.Background(), "grp-123", "conn-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.State != "connected" {
		t.Errorf("expected State connected, got %s", result.State)
	}
}

func TestSandboxGroupAPI_GenerateConnectionConsentLink(t *testing.T) {
	expected := models.ConsentLinkResponse{ConsentLink: "https://example.com/consent"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123/generateConsentLink" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123/generateConsentLink, got %s", r.URL.Path)
		}

		var req models.GenerateConsentLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.RedirectURL != "https://myapp.com/callback" {
			t.Errorf("expected redirectUrl https://myapp.com/callback, got %s", req.RedirectURL)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GenerateConnectionConsentLink(context.Background(), "grp-123", "conn-123", "https://myapp.com/callback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ConsentLink != "https://example.com/consent" {
		t.Errorf("expected ConsentLink https://example.com/consent, got %s", result.ConsentLink)
	}
}

func TestSandboxGroupAPI_UpdateConnectionPolicyRules(t *testing.T) {
	expected := models.Connection{
		ID:   "conn-123",
		Name: "my-conn",
		PolicyRules: []models.PolicyRule{
			{HookID: "hook-1", Patterns: []string{"*.go"}},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/connections/conn-123/policyRules" {
			t.Errorf("expected path /sandboxGroups/grp-123/connections/conn-123/policyRules, got %s", r.URL.Path)
		}

		var req models.UpdatePolicyRulesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if len(req.PolicyRules) != 1 {
			t.Errorf("expected 1 policy rule, got %d", len(req.PolicyRules))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.UpdateConnectionPolicyRules(context.Background(), "grp-123", "conn-123", models.UpdatePolicyRulesRequest{
		PolicyRules: []models.PolicyRule{
			{HookID: "hook-1", Patterns: []string{"*.go"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.PolicyRules) != 1 {
		t.Errorf("expected 1 policy rule, got %d", len(result.PolicyRules))
	}
}

// ── Volumes ───────────────────────────────────────────────────────────────────

func TestSandboxGroupAPI_ListVolumes(t *testing.T) {
	expected := []models.VolumeData{
		{VolumeName: "vol-1", Type: models.VolumeTypeAzureBlob},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListVolumes(context.Background(), "grp-123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 volume, got %d", len(result))
	}
}

func TestSandboxGroupAPI_GetVolume(t *testing.T) {
	expected := models.VolumeData{VolumeName: "my-vol", Type: models.VolumeTypeAzureBlob}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.GetVolume(context.Background(), "grp-123", "my-vol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.VolumeName != "my-vol" {
		t.Errorf("expected VolumeName my-vol, got %s", result.VolumeName)
	}
}

func TestSandboxGroupAPI_CreateVolume(t *testing.T) {
	expected := models.VolumeData{VolumeName: "new-vol", Type: models.VolumeTypeAzureBlob}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/new-vol" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/new-vol, got %s", r.URL.Path)
		}

		var req models.CreateVolumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Type != models.VolumeTypeAzureBlob {
			t.Errorf("expected type AzureBlob, got %s", req.Type)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.CreateVolume(context.Background(), "grp-123", "new-vol", models.CreateVolumeRequest{
		Type: models.VolumeTypeAzureBlob,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.VolumeName != "new-vol" {
		t.Errorf("expected VolumeName new-vol, got %s", result.VolumeName)
	}
}

func TestSandboxGroupAPI_DeleteVolume(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteVolume(context.Background(), "grp-123", "my-vol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxGroupAPI_ListVolumeFiles(t *testing.T) {
	expected := models.VolumeDirectoryListing{
		Path: "/data",
		Items: []models.VolumePathItem{
			{ItemName: "file.txt", Path: "/data/file.txt", IsDirectory: false},
			{ItemName: "subdir", Path: "/data/subdir", IsDirectory: true},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol/files" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol/files, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data" {
			t.Errorf("expected path=/data, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.ListVolumeFiles(context.Background(), "grp-123", "my-vol", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Path != "/data" {
		t.Errorf("expected path /data, got %s", result.Path)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Items))
	}
}

func TestSandboxGroupAPI_DownloadVolumeFile(t *testing.T) {
	expectedData := []byte("file content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol/files/download" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol/files/download, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/file.txt" {
			t.Errorf("expected path=/data/file.txt, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		w.Write(expectedData)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.DownloadVolumeFile(context.Background(), "grp-123", "my-vol", "/data/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != "file content" {
		t.Errorf("expected 'file content', got %s", string(result))
	}
}

func TestSandboxGroupAPI_UploadVolumeFile(t *testing.T) {
	expected := models.VolumePathItem{
		ItemName:    "test.txt",
		Path:        "/data/test.txt",
		IsDirectory: false,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol/files/upload" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol/files/upload, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/test.txt" {
			t.Errorf("expected path=/data/test.txt, got %s", r.URL.Query().Get("path"))
		}

		body, _ := io.ReadAll(r.Body)
		if string(body) != "uploaded data" {
			t.Errorf("expected body 'uploaded data', got %s", string(body))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.UploadVolumeFile(context.Background(), "grp-123", "my-vol", "/data/test.txt", []byte("uploaded data"), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ItemName != "test.txt" {
		t.Errorf("expected ItemName test.txt, got %s", result.ItemName)
	}
}

func TestSandboxGroupAPI_DeleteVolumeFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol/files" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol/files, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/old.txt" {
			t.Errorf("expected path=/data/old.txt, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	err := client.SandboxGroups.DeleteVolumeFile(context.Background(), "grp-123", "my-vol", "/data/old.txt", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSandboxGroupAPI_CreateVolumeDirectory(t *testing.T) {
	expected := models.VolumePathItem{
		ItemName:    "newdir",
		Path:        "/data/newdir",
		IsDirectory: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxGroups/grp-123/volumes/my-vol/files/mkdir" {
			t.Errorf("expected path /sandboxGroups/grp-123/volumes/my-vol/files/mkdir, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/newdir" {
			t.Errorf("expected path=/data/newdir, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expected)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	result, err := client.SandboxGroups.CreateVolumeDirectory(context.Background(), "grp-123", "my-vol", "/data/newdir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ItemName != "newdir" {
		t.Errorf("expected ItemName newdir, got %s", result.ItemName)
	}
	if !result.IsDirectory {
		t.Error("expected IsDirectory to be true")
	}
}

// ── Empty GroupID Validation ──────────────────────────────────────────────────

func TestSandboxGroupAPI_EmptyGroupID(t *testing.T) {
	client := New(Config{APIURL: "http://localhost", APIKey: "test-key"})
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func() error
	}{
		{"Get", func() error { _, err := client.SandboxGroups.Get(ctx, ""); return err }},
		{"Update", func() error {
			_, err := client.SandboxGroups.Update(ctx, "", models.UpdateSandboxGroupRequest{})
			return err
		}},
		{"Delete", func() error { return client.SandboxGroups.Delete(ctx, "") }},
		{"GetSandbox", func() error { _, err := client.SandboxGroups.GetSandbox(ctx, "", "sb-1"); return err }},
		{"ListSandboxes", func() error { _, err := client.SandboxGroups.ListSandboxes(ctx, "", nil); return err }},
		{"CountSandboxes", func() error { _, err := client.SandboxGroups.CountSandboxes(ctx, ""); return err }},
		{"ListDiskImages", func() error { _, err := client.SandboxGroups.ListDiskImages(ctx, "", nil); return err }},
		{"GetDiskImage", func() error { _, err := client.SandboxGroups.GetDiskImage(ctx, "", "di-1"); return err }},
		{"CreateDiskImage", func() error {
			_, err := client.SandboxGroups.CreateDiskImage(ctx, "", models.CreateDiskImageOptions{})
			return err
		}},
		{"DeleteDiskImage", func() error { return client.SandboxGroups.DeleteDiskImage(ctx, "", "di-1") }},
		{"BuildDiskImageFromDockerfile", func() error {
			_, err := client.SandboxGroups.BuildDiskImageFromDockerfile(ctx, "", models.BuildDiskImageFromDockerfileOptions{})
			return err
		}},
		{"ListSnapshots", func() error { _, err := client.SandboxGroups.ListSnapshots(ctx, "", nil); return err }},
		{"GetSnapshot", func() error { _, err := client.SandboxGroups.GetSnapshot(ctx, "", "snap-1"); return err }},
		{"CountSnapshots", func() error { _, err := client.SandboxGroups.CountSnapshots(ctx, ""); return err }},
		{"DeleteSnapshot", func() error { return client.SandboxGroups.DeleteSnapshot(ctx, "", "snap-1") }},
		{"ListSecrets", func() error { _, err := client.SandboxGroups.ListSecrets(ctx, ""); return err }},
		{"UpsertSecret", func() error {
			_, err := client.SandboxGroups.UpsertSecret(ctx, "", "s-1", map[string]string{"k": "v"})
			return err
		}},
		{"PeekSecret", func() error { _, err := client.SandboxGroups.PeekSecret(ctx, "", "s-1"); return err }},
		{"DeleteSecret", func() error { return client.SandboxGroups.DeleteSecret(ctx, "", "s-1") }},
		{"ListConnections", func() error { _, err := client.SandboxGroups.ListConnections(ctx, ""); return err }},
		{"GetConnection", func() error { _, err := client.SandboxGroups.GetConnection(ctx, "", "conn-1"); return err }},
		{"CreateConnection", func() error {
			_, err := client.SandboxGroups.CreateConnection(ctx, "", models.CreateConnectionRequest{})
			return err
		}},
		{"DeleteConnection", func() error { return client.SandboxGroups.DeleteConnection(ctx, "", "conn-1") }},
		{"AuthorizeConnection", func() error {
			_, err := client.SandboxGroups.AuthorizeConnection(ctx, "", "conn-1", models.AuthorizeConnectionRequest{})
			return err
		}},
		{"RefreshConnection", func() error {
			_, err := client.SandboxGroups.RefreshConnection(ctx, "", "conn-1")
			return err
		}},
		{"GenerateConnectionConsentLink", func() error {
			_, err := client.SandboxGroups.GenerateConnectionConsentLink(ctx, "", "conn-1", "https://example.com")
			return err
		}},
		{"UpdateConnectionPolicyRules", func() error {
			_, err := client.SandboxGroups.UpdateConnectionPolicyRules(ctx, "", "conn-1", models.UpdatePolicyRulesRequest{})
			return err
		}},
		{"ListVolumes", func() error { _, err := client.SandboxGroups.ListVolumes(ctx, "", nil); return err }},
		{"GetVolume", func() error { _, err := client.SandboxGroups.GetVolume(ctx, "", "vol-1"); return err }},
		{"CreateVolume", func() error {
			_, err := client.SandboxGroups.CreateVolume(ctx, "", "vol-1", models.CreateVolumeRequest{})
			return err
		}},
		{"DeleteVolume", func() error { return client.SandboxGroups.DeleteVolume(ctx, "", "vol-1") }},
		{"ListVolumeFiles", func() error {
			_, err := client.SandboxGroups.ListVolumeFiles(ctx, "", "vol-1", "/")
			return err
		}},
		{"DownloadVolumeFile", func() error {
			_, err := client.SandboxGroups.DownloadVolumeFile(ctx, "", "vol-1", "/file.txt")
			return err
		}},
		{"UploadVolumeFile", func() error {
			_, err := client.SandboxGroups.UploadVolumeFile(ctx, "", "vol-1", "/file.txt", []byte("data"), false)
			return err
		}},
		{"DeleteVolumeFile", func() error {
			return client.SandboxGroups.DeleteVolumeFile(ctx, "", "vol-1", "/file.txt", false)
		}},
		{"CreateVolumeDirectory", func() error {
			_, err := client.SandboxGroups.CreateVolumeDirectory(ctx, "", "vol-1", "/dir")
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if err == nil {
				t.Error("expected error for empty groupID, got nil")
			}
			if err != errEmptyGroupID {
				t.Errorf("expected errEmptyGroupID, got: %v", err)
			}
		})
	}
}
