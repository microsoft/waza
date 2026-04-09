// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClient_Get(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/test" {
			t.Errorf("expected path /test, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "hello"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	data, err := client.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result["message"] != "hello" {
		t.Errorf("expected message 'hello', got %q", result["message"])
	}
}

func TestHTTPClient_Post(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "123"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	body := map[string]string{"name": "test"}
	data, err := client.Post(context.Background(), "/create", body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if result["id"] != "123" {
		t.Errorf("expected id '123', got %q", result["id"])
	}
}

func TestHTTPClient_Put(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"updated": true}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	body := map[string]string{"name": "updated"}
	data, err := client.Put(context.Background(), "/update", body, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result map[string]bool
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if !result["updated"] {
		t.Error("expected updated to be true")
	}
}

func TestHTTPClient_Delete(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	_, err := client.Delete(context.Background(), "/delete/123", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"message": "not found"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	_, err := client.Get(context.Background(), "/notfound", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("expected HTTPError, got %T", err)
	}

	if httpErr.Status != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", httpErr.Status)
	}

	if httpErr.Message != "not found" {
		t.Errorf("expected message 'not found', got %q", httpErr.Message)
	}
}

func TestHTTPClient_QueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("foo") != "bar" {
			t.Errorf("expected query param foo=bar, got %s", r.URL.Query().Get("foo"))
		}
		if r.URL.Query().Get("page") != "1" {
			t.Errorf("expected query param page=1, got %s", r.URL.Query().Get("page"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
	})

	opts := &RequestOptions{
		Params: map[string]string{"foo": "bar", "page": "1"},
	}

	_, err := client.Get(context.Background(), "/test", opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_DefaultParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sandboxSpaceId") != "test-space" {
			t.Errorf("expected sandboxSpaceId=test-space, got %s", r.URL.Query().Get("sandboxSpaceId"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL:       server.URL,
		DefaultParams: map[string]string{"sandboxSpaceId": "test-space"},
	})

	_, err := client.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHTTPClient_DefaultHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-ms-api-key") != "test-key" {
			t.Errorf("expected x-ms-api-key header 'test-key', got %s", r.Header.Get("x-ms-api-key"))
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		BaseURL: server.URL,
		Headers: map[string]string{"x-ms-api-key": "test-key"},
	})

	_, err := client.Get(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
