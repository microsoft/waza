// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func TestClientAppsCreateAndLoginRefresh(t *testing.T) {
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/apps" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request models.AppRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Name != "sample" {
			t.Errorf("request = %+v, err = %v", request, err)
		}
		tokens = append(tokens, r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"id":"app-1","name":"sample"}`)
	}))
	defer server.Close()
	if New(Config{}).Apps == nil {
		t.Fatal("default client has no Apps API")
	}
	client := New(Config{APIURL: server.URL, GitHubToken: "before", MaxRetries: 1})
	create := func() {
		t.Helper()
		if client.Apps == nil {
			t.Fatal("client has no Apps API")
		}
		app, err := client.Apps.Create(context.Background(), models.AppRequest{Name: "sample"})
		if err != nil || app.ID() != "app-1" {
			t.Fatalf("app = %+v, err = %v", app, err)
		}
		if client.Apps.client != client.client || client.Apps.config != client.config {
			t.Fatal("Apps does not share refreshed HTTP client/config")
		}
	}
	create()
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.String() {
		case githubDeviceCodeURL:
			body = `{"device_code":"test","user_code":"test","verification_uri":"http://offline.invalid","expires_in":60,"interval":0}`
		case githubTokenURL:
			body = `{"access_token":"after"}`
		default:
			return originalTransport.RoundTrip(r)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	if err := client.Login(context.Background(), &LoginOptions{Callback: func(string, string) {}}); err != nil {
		t.Fatal(err)
	}
	create()
	if len(tokens) != 2 || tokens[0] != "GitHub before" || tokens[1] != "GitHub after" {
		t.Fatalf("authorization headers = %q", tokens)
	}
	if client.client.maxRetries != 1 {
		t.Fatal("token refresh lost retry configuration")
	}
}
