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

func TestSnapshotAPI_List(t *testing.T) {
	expectedSnapshots := []models.Snapshot{
		{ID: "snap-1", Labels: map[string]string{"name": "s1"}},
		{ID: "snap-2", Labels: map[string]string{"name": "s2"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/snapshots" {
			t.Errorf("expected path /snapshots, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSnapshots)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Snapshots.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 snapshots, got %d", len(result))
	}
}

func TestSnapshotAPI_Get(t *testing.T) {
	expectedSnapshot := models.Snapshot{
		ID:        "snap-123",
		SandboxID: "sb-123",
		VmmType:   "cloudhypervisor",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/snapshots/snap-123" {
			t.Errorf("expected path /snapshots/snap-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSnapshot)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Snapshots.Get(context.Background(), "snap-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != "snap-123" {
		t.Errorf("expected ID snap-123, got %s", result.ID)
	}
	if result.SandboxID != "sb-123" {
		t.Errorf("expected SandboxID sb-123, got %s", result.SandboxID)
	}
}

func TestSnapshotAPI_Count(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshots/count" {
			t.Errorf("expected path /snapshots/count, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(15)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	count, err := client.Snapshots.Count(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 15 {
		t.Errorf("expected count 15, got %d", count)
	}
}

func TestSnapshotAPI_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/snapshots/snap-123" {
			t.Errorf("expected path /snapshots/snap-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	err := client.Snapshots.Delete(context.Background(), "snap-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
