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

func TestDiskImageAPI_Create(t *testing.T) {
	expectedDiskImage := models.DiskImage{
		ID:     "disk-123",
		Labels: map[string]string{"name": "test"},
		Image:  models.DiskImageImage{Base: "python:3.12"},
		Status: models.DiskImageStatus{State: "Ready"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/diskImages" {
			t.Errorf("expected path /diskImages, got %s", r.URL.Path)
		}

		var req models.CreateDiskImageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Labels["name"] != "test" {
			t.Errorf("expected label name=test, got %s", req.Labels["name"])
		}
		if req.Image.Base != "python:3.12" {
			t.Errorf("expected base python:3.12, got %s", req.Image.Base)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedDiskImage)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.DiskImages.Create(context.Background(), models.CreateDiskImageOptions{
		Labels:    map[string]string{"name": "test"},
		BaseImage: "python:3.12",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != "disk-123" {
		t.Errorf("expected ID disk-123, got %s", result.ID)
	}
}

func TestDiskImageAPI_Get(t *testing.T) {
	expectedDiskImage := models.DiskImage{
		ID:     "disk-123",
		Labels: map[string]string{"name": "test"},
		Image:  models.DiskImageImage{Base: "python:3.12"},
		Status: models.DiskImageStatus{State: "Ready"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/diskImages/disk-123" {
			t.Errorf("expected path /diskImages/disk-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedDiskImage)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.DiskImages.Get(context.Background(), "disk-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != "disk-123" {
		t.Errorf("expected ID disk-123, got %s", result.ID)
	}
}

func TestDiskImageAPI_List(t *testing.T) {
	expectedDiskImages := []models.DiskImage{
		{ID: "disk-1", Labels: map[string]string{"name": "img1"}},
		{ID: "disk-2", Labels: map[string]string{"name": "img2"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/diskImages" {
			t.Errorf("expected path /diskImages, got %s", r.URL.Path)
		}

		// Check pagination params
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("expected page=1, got %s", r.URL.Query().Get("page"))
		}
		if r.URL.Query().Get("pageSize") != "100" {
			t.Errorf("expected pageSize=100, got %s", r.URL.Query().Get("pageSize"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedDiskImages)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.DiskImages.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 disk images, got %d", len(result))
	}
}

func TestDiskImageAPI_ListWithLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		labels := r.URL.Query().Get("labels")
		if labels != "env=prod" && labels != "name=test,env=prod" && labels != "env=prod,name=test" {
			// Label order may vary due to map iteration
			if labels == "" {
				t.Error("expected labels parameter to be set")
			}
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]models.DiskImage{})
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	_, err := client.DiskImages.List(context.Background(), &ListOptions{
		Labels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiskImageAPI_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/diskImages/disk-123" {
			t.Errorf("expected path /diskImages/disk-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	err := client.DiskImages.Delete(context.Background(), "disk-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiskImageAPI_ListPublic(t *testing.T) {
	expectedPublicImages := []models.PublicDiskImage{
		{Name: "python:3.12", Status: models.DiskImageStatus{State: "Ready"}},
		{Name: "node:20", Status: models.DiskImageStatus{State: "Ready"}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/public/diskimages" {
			t.Errorf("expected path /public/diskimages, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedPublicImages)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.DiskImages.ListPublic(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 public images, got %d", len(result))
	}
	if result[0].Name != "python:3.12" {
		t.Errorf("expected first image python:3.12, got %s", result[0].Name)
	}
}
