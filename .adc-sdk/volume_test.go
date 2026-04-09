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

func TestVolumeAPI_Create(t *testing.T) {
	expectedVolume := models.VolumeData{
		VolumeName: "test-vol",
		Type:       models.VolumeTypeAzureBlob,
		Labels:     map[string]string{"env": "dev"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/test-vol" {
			t.Errorf("expected path /volumes/test-vol, got %s", r.URL.Path)
		}

		var req models.CreateVolumeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}

		if req.Type != models.VolumeTypeAzureBlob {
			t.Errorf("expected type AzureBlob, got %s", req.Type)
		}
		if req.Labels["env"] != "dev" {
			t.Errorf("expected label env=dev, got %s", req.Labels["env"])
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedVolume)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.Create(context.Background(), "test-vol", models.CreateVolumeRequest{
		Type:   models.VolumeTypeAzureBlob,
		Labels: map[string]string{"env": "dev"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.VolumeName != "test-vol" {
		t.Errorf("expected VolumeName test-vol, got %s", result.VolumeName)
	}
	if result.Type != models.VolumeTypeAzureBlob {
		t.Errorf("expected Type AzureBlob, got %s", result.Type)
	}
}

func TestVolumeType_Serialization(t *testing.T) {
	tests := []struct {
		name       string
		volumeType models.VolumeType
		jsonValue  string
	}{
		{"AzureBlob", models.VolumeTypeAzureBlob, "AzureBlob"},
		{"DataDisk", models.VolumeTypeDataDisk, "DataDisk"},
	}

	for _, tt := range tests {
		t.Run(tt.name+"_serialize", func(t *testing.T) {
			req := models.CreateVolumeRequest{Type: tt.volumeType}
			data, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}
			var parsed map[string]interface{}
			json.Unmarshal(data, &parsed)
			if parsed["type"] != tt.jsonValue {
				t.Errorf("expected type %s, got %v", tt.jsonValue, parsed["type"])
			}
		})

		t.Run(tt.name+"_deserialize", func(t *testing.T) {
			jsonStr := `{"volumeName":"v","type":"` + tt.jsonValue + `","labels":{}}`
			var vol models.VolumeData
			if err := json.Unmarshal([]byte(jsonStr), &vol); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}
			if vol.Type != tt.volumeType {
				t.Errorf("expected type %s, got %s", tt.volumeType, vol.Type)
			}
		})
	}
}

func TestVolumeData_DataDisk_Deserialization(t *testing.T) {
	jsonStr := `{
		"volumeName": "my-disk",
		"type": "DataDisk",
		"labels": {"team": "data"},
		"size": "256Mi",
		"isAttached": true,
		"usage": {
			"compressedBlobSizeBytes": 50000,
			"usedSizeBytes": 200000,
			"lastUploadedAtUtc": "2024-06-01T12:00:00Z"
		}
	}`

	var vol models.VolumeData
	if err := json.Unmarshal([]byte(jsonStr), &vol); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if vol.Type != models.VolumeTypeDataDisk {
		t.Errorf("expected type DataDisk, got %s", vol.Type)
	}
	if vol.Size != "256Mi" {
		t.Errorf("expected size 256Mi, got %s", vol.Size)
	}
	if vol.IsAttached == nil || !*vol.IsAttached {
		t.Error("expected isAttached to be true")
	}
	if vol.Usage == nil {
		t.Fatal("expected usage to be set")
	}
	if vol.Usage.CompressedBlobSizeBytes == nil || *vol.Usage.CompressedBlobSizeBytes != 50000 {
		t.Errorf("expected compressedBlobSizeBytes 50000, got %v", vol.Usage.CompressedBlobSizeBytes)
	}
	if vol.Usage.UsedSizeBytes == nil || *vol.Usage.UsedSizeBytes != 200000 {
		t.Errorf("expected usedSizeBytes 200000, got %v", vol.Usage.UsedSizeBytes)
	}
}

func TestCreateVolumeRequest_DataDisk_Serialization(t *testing.T) {
	req := models.CreateVolumeRequest{
		Type: models.VolumeTypeDataDisk,
		Size: "512Mi",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	json.Unmarshal(data, &parsed)

	if parsed["type"] != "DataDisk" {
		t.Errorf("expected type DataDisk, got %v", parsed["type"])
	}
	if parsed["size"] != "512Mi" {
		t.Errorf("expected size 512Mi, got %v", parsed["size"])
	}
}

func TestVolumeAPI_Get(t *testing.T) {
	expectedVolume := models.VolumeData{
		VolumeName: "my-vol",
		Type:       models.VolumeTypeAzureBlob,
		Labels:     map[string]string{},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol" {
			t.Errorf("expected path /volumes/my-vol, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedVolume)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.Get(context.Background(), "my-vol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.VolumeName != "my-vol" {
		t.Errorf("expected VolumeName my-vol, got %s", result.VolumeName)
	}
}

func TestVolumeAPI_List(t *testing.T) {
	expectedVolumes := []models.VolumeData{
		{VolumeName: "vol-1", Type: models.VolumeTypeAzureBlob, Labels: map[string]string{}},
		{VolumeName: "vol-2", Type: models.VolumeTypeAzureBlob, Labels: map[string]string{}},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/volumes" {
			t.Errorf("expected path /volumes, got %s", r.URL.Path)
		}

		// Check default pagination
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("expected page=1, got %s", r.URL.Query().Get("page"))
		}
		if r.URL.Query().Get("pageSize") != "100" {
			t.Errorf("expected pageSize=100, got %s", r.URL.Query().Get("pageSize"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedVolumes)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.List(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("expected 2 volumes, got %d", len(result))
	}
	if result[0].VolumeName != "vol-1" {
		t.Errorf("expected VolumeName vol-1, got %s", result[0].VolumeName)
	}
}

func TestVolumeAPI_ListWithLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		labels := r.URL.Query().Get("labels")
		if labels == "" {
			t.Error("expected labels parameter to be set")
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode([]models.VolumeData{})
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	_, err := client.Volumes.List(context.Background(), &ListOptions{
		Labels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVolumeAPI_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol" {
			t.Errorf("expected path /volumes/my-vol, got %s", r.URL.Path)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	err := client.Volumes.Delete(context.Background(), "my-vol")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVolumeAPI_ListFiles(t *testing.T) {
	expectedListing := models.VolumeDirectoryListing{
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
		if r.URL.Path != "/volumes/my-vol/files" {
			t.Errorf("expected path /volumes/my-vol/files, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data" {
			t.Errorf("expected path=/data, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedListing)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.ListFiles(context.Background(), "my-vol", "/data")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Path != "/data" {
		t.Errorf("expected path /data, got %s", result.Path)
	}
	if len(result.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(result.Items))
	}
	if result.Items[0].ItemName != "file.txt" {
		t.Errorf("expected item name file.txt, got %s", result.Items[0].ItemName)
	}
}

func TestVolumeAPI_DownloadFile(t *testing.T) {
	expectedData := []byte("file content here")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol/files/download" {
			t.Errorf("expected path /volumes/my-vol/files/download, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/file.txt" {
			t.Errorf("expected path=/data/file.txt, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		w.Write(expectedData)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.DownloadFile(context.Background(), "my-vol", "/data/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if string(result) != "file content here" {
		t.Errorf("expected file content, got %s", string(result))
	}
}

func TestVolumeAPI_UploadFile(t *testing.T) {
	expectedItem := models.VolumePathItem{
		ItemName:    "upload.txt",
		Path:        "/data/upload.txt",
		IsDirectory: false,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol/files/upload" {
			t.Errorf("expected path /volumes/my-vol/files/upload, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/upload.txt" {
			t.Errorf("expected path=/data/upload.txt, got %s", r.URL.Query().Get("path"))
		}
		if r.URL.Query().Get("overwrite") != "true" {
			t.Errorf("expected overwrite=true, got %s", r.URL.Query().Get("overwrite"))
		}

		body, _ := io.ReadAll(r.Body)
		if string(body) != "uploaded data" {
			t.Errorf("expected body 'uploaded data', got %s", string(body))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedItem)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.UploadFile(context.Background(), "my-vol", "/data/upload.txt", []byte("uploaded data"), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ItemName != "upload.txt" {
		t.Errorf("expected ItemName upload.txt, got %s", result.ItemName)
	}
	if result.Path != "/data/upload.txt" {
		t.Errorf("expected Path /data/upload.txt, got %s", result.Path)
	}
}

func TestVolumeAPI_DeleteFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol/files" {
			t.Errorf("expected path /volumes/my-vol/files, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/old.txt" {
			t.Errorf("expected path=/data/old.txt, got %s", r.URL.Query().Get("path"))
		}
		if r.URL.Query().Get("recursive") != "true" {
			t.Errorf("expected recursive=true, got %s", r.URL.Query().Get("recursive"))
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	err := client.Volumes.DeleteFile(context.Background(), "my-vol", "/data/old.txt", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVolumeAPI_CreateDirectory(t *testing.T) {
	expectedItem := models.VolumePathItem{
		ItemName:    "newdir",
		Path:        "/data/newdir",
		IsDirectory: true,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/volumes/my-vol/files/mkdir" {
			t.Errorf("expected path /volumes/my-vol/files/mkdir, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "/data/newdir" {
			t.Errorf("expected path=/data/newdir, got %s", r.URL.Query().Get("path"))
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(expectedItem)
	}))
	defer server.Close()

	client := New(Config{APIURL: server.URL, APIKey: "test-key"})

	result, err := client.Volumes.CreateDirectory(context.Background(), "my-vol", "/data/newdir")
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
