// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientConfiguredRetries(t *testing.T) {
	for _, tt := range []struct {
		name         string
		retries      int
		status       int
		successAt    int
		wantAttempts int
	}{
		{"one retry exhausted", 1, 500, 0, 2},
		{"two retries exhausted", 2, 503, 0, 3},
		{"success after retry", 2, 502, 2, 2},
		{"rate limit", 1, 429, 2, 2},
		{"request timeout", 1, 408, 2, 2},
		{"gateway timeout", 1, 504, 2, 2},
		{"bad request", 2, 400, 0, 1},
		{"unauthorized", 2, 401, 0, 1},
		{"not found", 2, 404, 0, 1},
		{"not implemented", 2, 501, 0, 1},
		{"retries disabled", -1, 500, 0, 1},
		{"default retries", 0, 500, 0, DefaultMaxRetries + 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := int(attempts.Add(1))
				if r.URL.Path != "/sandboxes/count" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if tt.successAt == attempt {
					fmt.Fprint(w, "42")
					return
				}
				w.WriteHeader(tt.status)
			}))
			defer server.Close()
			client := New(Config{APIURL: server.URL, MaxRetries: tt.retries})
			result, err := client.Sandboxes.Count(context.Background())
			if int(attempts.Load()) != tt.wantAttempts {
				t.Fatalf("got %d attempts, want %d", attempts.Load(), tt.wantAttempts)
			}
			if tt.successAt > 0 {
				if err != nil || result != 42 {
					t.Fatalf("count = %d, err = %v", result, err)
				}
			} else {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || httpErr.Status != tt.status {
					t.Fatalf("expected HTTP %d, got %v", tt.status, err)
				}
			}
		})
	}
}

func TestHTTPRetriesReplayBodies(t *testing.T) {
	for _, body := range []interface{}{map[string]string{"name": "sample"}, []byte("binary body"), "text body"} {
		t.Run(fmt.Sprintf("%T", body), func(t *testing.T) {
			var bodies []string
			var headers []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				bodies = append(bodies, string(data))
				headers = append(headers, r.Header.Get("X-Request"))
				if len(bodies) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				fmt.Fprint(w, "ok")
			}))
			defer server.Close()
			client := NewHTTPClient(HTTPClientConfig{BaseURL: server.URL, MaxRetries: 1})
			result, err := client.Post(context.Background(), "/test", body,
				&RequestOptions{Headers: map[string]string{"X-Request": "retained"}})
			if err != nil || string(result) != "ok" {
				t.Fatalf("response = %q, err = %v", result, err)
			}
			if len(bodies) != 2 || bodies[0] == "" || bodies[0] != bodies[1] ||
				headers[0] != "retained" || headers[1] != "retained" {
				t.Fatalf("bodies = %q, headers = %q", bodies, headers)
			}
		})
	}
}

func TestHTTPRetriesTransportErrors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"EOF", io.EOF, 2},
		{"unexpected EOF", io.ErrUnexpectedEOF, 2},
		{"timeout", &net.DNSError{IsTimeout: true}, 2},
		{"permanent", errors.New("permanent failure"), 1},
		{"permanent DNS", &net.DNSError{IsNotFound: true}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			client := NewHTTPClient(HTTPClientConfig{BaseURL: "http://offline.invalid", MaxRetries: 1})
			client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				return nil, tt.err
			})
			_, err := client.Get(context.Background(), "/test", nil)
			if err == nil || attempts != tt.attempts {
				t.Fatalf("attempts = %d, err = %v", attempts, err)
			}
		})
	}
}

func TestHTTPRetriesCancellation(t *testing.T) {
	for _, phase := range []string{"before request", "during request", "during backoff"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var attempts atomic.Int32
			closed := make(chan struct{})
			client := NewHTTPClient(HTTPClientConfig{BaseURL: "http://offline.invalid", MaxRetries: 100})
			client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts.Add(1)
				if phase == "during request" {
					cancel()
					<-r.Context().Done()
					return nil, r.Context().Err()
				}
				return &http.Response{StatusCode: 503, Body: &closeSignalBody{closed: closed}, Header: make(http.Header)}, nil
			})
			if phase == "before request" {
				cancel()
			} else if phase == "during backoff" {
				go func() {
					<-closed
					cancel()
				}()
			}
			done := make(chan error, 1)
			go func() {
				_, err := client.Get(ctx, "/test", nil)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("expected cancellation, got %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt request/backoff")
			}
			want := int32(1)
			if phase == "before request" {
				want = 0
			}
			if attempts.Load() != want {
				t.Fatalf("attempts = %d, want %d", attempts.Load(), want)
			}
		})
	}
}

type closeSignalBody struct{ closed chan struct{} }

func (*closeSignalBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b *closeSignalBody) Close() error           { close(b.closed); return nil }

func TestHTTPRetryDelayBounded(t *testing.T) {
	for attempt, want := range map[int]time.Duration{
		0: 100 * time.Millisecond, 1: 200 * time.Millisecond, 4: 1600 * time.Millisecond,
		5: 2 * time.Second, 1000000: 2 * time.Second,
	} {
		if got := retryDelay(attempt); got != want {
			t.Errorf("delay(%d) = %s, want %s", attempt, got, want)
		}
	}
}

func TestHTTPStreamRetriesBeforeReturningBody(t *testing.T) {
	attempts := 0
	client := NewHTTPClient(HTTPClientConfig{BaseURL: "http://offline.invalid", MaxRetries: 1})
	closed := make(chan struct{})
	client.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: 503, Body: &closeSignalBody{closed: closed}, Header: make(http.Header)}, nil
		}
		select {
		case <-closed:
		default:
			t.Error("retry response body was not closed")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("stream")), Header: make(http.Header)}, nil
	})
	resp, err := client.GetStream(context.Background(), "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "stream" || attempts != 2 {
		t.Fatalf("body = %q, attempts = %d, err = %v", data, attempts, err)
	}
}
