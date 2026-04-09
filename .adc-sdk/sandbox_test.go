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

func TestSandboxAPI_CreateFromDiskImage(t *testing.T) {
	expectedSandbox := models.SandboxData{
		ID:        "sb-123",
		Labels:    map[string]string{"name": "test"},
		State:     models.SandboxStateRunning,
		VmmType:   models.VmmTypeCloudHypervisor,
		Ports:     []models.SandboxPort{},
		Resources: models.SandboxResources{CPU: "1000m", Memory: "1024Mi"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxes" {
			t.Errorf("expected path /sandboxes, got %s", r.URL.Path)
		}

		var req models.CreateSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.SourcesRef.DiskImage == nil {
			t.Error("expected disk image source")
		}
		if req.SourcesRef.DiskImage.ID != "disk-123" {
			t.Errorf("expected disk image ID disk-123, got %s", req.SourcesRef.DiskImage.ID)
		}
		if req.Resources.CPU != "1000m" {
			t.Errorf("expected CPU 1000m, got %s", req.Resources.CPU)
		}
		if req.VmmType != models.VmmTypeCloudHypervisor {
			t.Errorf("expected VmmType cloudhypervisor, got %s", req.VmmType)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSandbox)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Sandboxes.CreateFromDiskImage(context.Background(), models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{ID: "disk-123"},
		Labels:    map[string]string{"name": "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID() != "sb-123" {
		t.Errorf("expected ID sb-123, got %s", result.ID())
	}
	if result.State() != models.SandboxStateRunning {
		t.Errorf("expected state Running, got %s", result.State())
	}
}

func TestSandboxAPI_CreateFromSnapshot(t *testing.T) {
	expectedSandbox := models.SandboxData{
		ID:      "sb-123",
		State:   models.SandboxStateRunning,
		VmmType: models.VmmTypeCloudHypervisor,
		Ports:   []models.SandboxPort{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req models.CreateSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.SourcesRef.Snapshot == nil {
			t.Error("expected snapshot source")
		}
		if req.SourcesRef.Snapshot.ID != "snap-123" {
			t.Errorf("expected snapshot ID snap-123, got %s", req.SourcesRef.Snapshot.ID)
		}
		// Resources should not be set for snapshot
		if req.Resources != nil {
			t.Error("expected resources to be nil for snapshot")
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSandbox)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Sandboxes.CreateFromSnapshot(context.Background(), models.CreateFromSnapshotOptions{
		SnapshotID: "snap-123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID() != "sb-123" {
		t.Errorf("expected ID sb-123, got %s", result.ID())
	}
}

func TestSandboxAPI_Get(t *testing.T) {
	expectedSandbox := models.SandboxData{
		ID:      "sb-123",
		State:   models.SandboxStateRunning,
		VmmType: models.VmmTypeCloudHypervisor,
		Ports:   []models.SandboxPort{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxes/sb-123" {
			t.Errorf("expected path /sandboxes/sb-123, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSandbox)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Sandboxes.Get(context.Background(), "sb-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID() != "sb-123" {
		t.Errorf("expected ID sb-123, got %s", result.ID())
	}
}

func TestSandboxAPI_List(t *testing.T) {
	expectedSandboxes := []models.SandboxData{
		{ID: "sb-1", Ports: []models.SandboxPort{}},
		{ID: "sb-2", Ports: []models.SandboxPort{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxes" {
			t.Errorf("expected path /sandboxes, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedSandboxes)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Sandboxes.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 sandboxes, got %d", len(result))
	}
}

func TestSandboxAPI_Count(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxes/count" {
			t.Errorf("expected path /sandboxes/count, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(42)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	count, err := client.Sandboxes.Count(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if count != 42 {
		t.Errorf("expected count 42, got %d", count)
	}
}

func TestSandboxAPI_CreateBatch(t *testing.T) {
	expectedResponse := models.BatchSandboxResponse{
		TotalRequested: 3,
		Succeeded:      3,
		Failed:         0,
		Sandboxes: []models.SandboxData{
			{ID: "sb-1"},
			{ID: "sb-2"},
			{ID: "sb-3"},
		},
		Errors: []string{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/sandboxes/batch" {
			t.Errorf("expected path /sandboxes/batch, got %s", r.URL.Path)
		}

		var req models.BatchSandboxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Count != 3 {
			t.Errorf("expected count 3, got %d", req.Count)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedResponse)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Sandboxes.CreateBatch(context.Background(), models.BatchSandboxRequest{
		Count: 3,
		Sandbox: models.CreateSandboxRequest{
			SourcesRef: models.SandboxSource{
				DiskImage: &models.SandboxSourceDiskImage{ID: "disk-123"},
			},
			Resources: &models.SandboxResources{CPU: "1000m", Memory: "1024Mi"},
			VmmType:   models.VmmTypeCloudHypervisor,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Succeeded != 3 {
		t.Errorf("expected 3 succeeded, got %d", result.Succeeded)
	}
	if len(result.Sandboxes) != 3 {
		t.Errorf("expected 3 sandboxes, got %d", len(result.Sandboxes))
	}
}

func TestSandbox_AddPort_WithCorsOptions(t *testing.T) {
	allowedOrigin := "vscode-file://vscode-app"
	maxAge := 86400
	expectedSandbox := models.SandboxData{
		ID:      "sb-123",
		State:   models.SandboxStateRunning,
		VmmType: models.VmmTypeCloudHypervisor,
		Ports: []models.SandboxPort{
			{
				Port: 8080,
				URL:  "https://sb-123--8080.proxy.adc.io",
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sb-123/ports/add":
			var req models.AddPortRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Cors == nil {
				t.Fatal("expected cors to be set")
			}
			if len(req.Cors.AllowOrigins) != 1 || req.Cors.AllowOrigins[0] != allowedOrigin {
				t.Fatalf("unexpected allow origins: %+v", req.Cors.AllowOrigins)
			}
			if req.Cors.AllowCredentials != true {
				t.Fatalf("expected allowCredentials=true")
			}
			if req.Cors.MaxAge == nil || *req.Cors.MaxAge != maxAge {
				t.Fatalf("expected maxAge=%d", maxAge)
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(models.PortsListResponse{
				Ports: []models.SandboxPort{{Port: 8080, URL: "https://sb-123--8080.proxy.adc.io"}},
			})
			return
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedSandbox)
			return
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	sandbox := NewSandbox(client.client, client.config, expectedSandbox)
	result, err := sandbox.AddPort(context.Background(), 8080, &models.AddPortOptions{
		Name: "web",
		Cors: &models.PortCorsConfig{
			AllowOrigins:     []string{allowedOrigin},
			AllowMethods:     []string{"GET", "POST"},
			AllowHeaders:     []string{"Content-Type", "Authorization"},
			AllowCredentials: true,
			MaxAge:           &maxAge,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Port != 8080 {
		t.Fatalf("expected port 8080, got %d", result.Port)
	}
}

func TestSandbox_AddPort_WithPublicGithubAuth(t *testing.T) {
	expectedSandbox := models.SandboxData{
		ID:      "sb-123",
		State:   models.SandboxStateRunning,
		VmmType: models.VmmTypeCloudHypervisor,
		Ports: []models.SandboxPort{
			{
				Port: 3000,
				URL:  "https://sb-123--3000.proxy.adc.io",
				Auth: &models.PortAuthConfig{
					PublicGithub: &models.PortAuthConfigPublicGithub{
						Enabled:          true,
						Emails:           []string{"user@microsoft.com"},
						EmailSuffixes:    []string{"@microsoft.com"},
						Usernames:        []string{"msftuser"},
						UsernameSuffixes: []string{"_microsoft"},
					},
				},
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sb-123/ports/add":
			var req models.AddPortRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.Auth == nil || req.Auth.PublicGithub == nil {
				t.Fatal("expected publicGithub auth to be set")
			}
			if !req.Auth.PublicGithub.Enabled {
				t.Fatal("expected publicGithub enabled=true")
			}
			if len(req.Auth.PublicGithub.Emails) != 1 || req.Auth.PublicGithub.Emails[0] != "user@microsoft.com" {
				t.Fatalf("unexpected emails: %+v", req.Auth.PublicGithub.Emails)
			}
			if req.Auth.Github != nil {
				t.Fatal("expected github auth to be nil")
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(models.PortsListResponse{
				Ports: expectedSandbox.Ports,
			})
			return
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedSandbox)
			return
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	sandbox := NewSandbox(client.client, client.config, models.SandboxData{ID: "sb-123"})
	result, err := sandbox.AddPort(context.Background(), 3000, &models.AddPortOptions{
		Name: "public",
		Auth: &models.PortAuthConfig{
			PublicGithub: &models.PortAuthConfigPublicGithub{
				Enabled:          true,
				Emails:           []string{"user@microsoft.com"},
				EmailSuffixes:    []string{"@microsoft.com"},
				Usernames:        []string{"msftuser"},
				UsernameSuffixes: []string{"_microsoft"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Auth == nil || result.Auth.PublicGithub == nil {
		t.Fatal("expected publicGithub auth in result")
	}
	if !result.Auth.PublicGithub.Enabled {
		t.Fatal("expected publicGithub enabled=true in result")
	}
	if result.Auth.Github != nil {
		t.Fatal("expected github auth to be nil in result")
	}
}

func TestSandbox_sandboxBasePath_WithoutGroup_UsesFlatPath(t *testing.T) {
	sandbox := NewSandbox(nil, nil, models.SandboxData{
		ID: "sb-123",
	})
	path := sandbox.sandboxBasePath()
	expected := "/sandboxes/sb-123"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestSandbox_sandboxBasePath_WithGroup_UsesGroupPath(t *testing.T) {
	sandbox := NewSandbox(nil, nil, models.SandboxData{
		ID:             "sb-123",
		SandboxGroupID: "grp-456",
	})
	path := sandbox.sandboxBasePath()
	expected := "/sandboxGroups/grp-456/sandboxes/sb-123"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestSandbox_sandboxBasePath_WithEmptyGroup_UsesFlatPath(t *testing.T) {
	sandbox := NewSandbox(nil, nil, models.SandboxData{
		ID:             "sb-123",
		SandboxGroupID: "",
	})
	path := sandbox.sandboxBasePath()
	expected := "/sandboxes/sb-123"
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

func TestSandbox_SetLifecyclePolicy_WithAutoDelete(t *testing.T) {
	expectedSandbox := models.SandboxData{
		ID:      "sb-123",
		State:   models.SandboxStateRunning,
		VmmType: models.VmmTypeCloudHypervisor,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sandboxes/sb-123/lifecycle":
			var req models.SandboxLifecyclePolicy
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("failed to decode request: %v", err)
			}
			if req.AutoDeletePolicy == nil {
				t.Fatal("expected autoDeletePolicy to be set")
			}
			if !req.AutoDeletePolicy.Enabled {
				t.Fatal("expected autoDeletePolicy enabled=true")
			}
			if req.AutoDeletePolicy.DeleteIntervalInSeconds == nil || *req.AutoDeletePolicy.DeleteIntervalInSeconds != 3600 {
				t.Fatal("expected deleteIntervalInSeconds=3600")
			}
			w.WriteHeader(http.StatusOK)
			return
		case r.Method == http.MethodGet && r.URL.Path == "/sandboxes/sb-123":
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(expectedSandbox)
			return
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})
	sandbox := NewSandbox(client.client, client.config, expectedSandbox)
	interval := int64(3600)
	err := sandbox.SetLifecyclePolicy(context.Background(), models.SandboxLifecyclePolicy{
		AutoDeletePolicy: &models.SandboxAutoDeletePolicy{
			Enabled:                 true,
			DeleteIntervalInSeconds: &interval,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
