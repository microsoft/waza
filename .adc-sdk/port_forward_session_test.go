// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// wsUpgrader is a WebSocket upgrader for test servers.
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type blockedWriteConn struct {
	net.Conn
	block     atomic.Bool
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func (c *blockedWriteConn) Write(data []byte) (int, error) {
	if c.block.Load() {
		c.startOnce.Do(func() { close(c.started) })
		<-c.closed
		return 0, net.ErrClosed
	}
	return c.Conn.Write(data)
}

func (c *blockedWriteConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

func TestPortForwardCancellationUnblocksWriter(t *testing.T) {
	peerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(peerDone)
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		conn.ReadMessage()
	}))
	defer server.Close()
	var wire *blockedWriteConn
	dialer := websocket.Dialer{NetDialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		wire = &blockedWriteConn{Conn: conn, started: make(chan struct{}), closed: make(chan struct{})}
		return wire, nil
	}}
	conn, _, err := dialer.Dial(strings.Replace(server.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatal(err)
	}
	session := &PortForwardSession{ws: conn}
	defer session.Close()
	wire.block.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := session.Read(ctx)
	writeDone := make(chan error, 1)
	go func() { writeDone <- session.Write([]byte("blocked")) }()
	select {
	case <-wire.started:
	case <-time.After(time.Second):
		t.Fatal("write did not reach blocked transport")
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected data")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation was blocked by the writer")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked write unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("writer did not unblock")
	}
	select {
	case <-peerDone:
	case <-time.After(time.Second):
		t.Fatal("peer did not observe closure")
	}
}

func TestPortForwardSessionReadCancellation(t *testing.T) {
	for _, mode := range []string{"idle", "already canceled", "concurrent close", "blocked send", "natural end", "unconnected"} {
		t.Run(mode, func(t *testing.T) {
			peerDone := make(chan struct{})
			frameSent := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(peerDone)
				conn, err := wsUpgrader.Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				if _, _, err := conn.ReadMessage(); err != nil {
					t.Error(err)
					return
				}
				if err := conn.WriteJSON(map[string]string{"type": "connected"}); err != nil {
					t.Error(err)
					return
				}
				if mode == "natural end" {
					return
				}
				if mode == "blocked send" {
					if err := conn.WriteMessage(websocket.BinaryMessage, []byte("pending")); err != nil {
						t.Error(err)
						return
					}
					close(frameSent)
				}
				conn.ReadMessage()
			}))
			defer server.Close()
			session := &PortForwardSession{}
			if mode != "unconnected" {
				if err := session.connect(strings.Replace(server.URL, "http://", "ws://", 1), nil, 8080); err != nil {
					t.Fatal(err)
				}
			}
			defer session.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "already canceled" {
				cancel()
			}
			ch := session.Read(ctx)
			if mode == "blocked send" {
				select {
				case <-frameSent:
				case <-time.After(time.Second):
					t.Fatal("peer did not send frame")
				}
			}
			var concurrentClose sync.WaitGroup
			if mode == "concurrent close" {
				concurrentClose.Add(1)
				go func() {
					defer concurrentClose.Done()
					session.Close()
				}()
			}
			if mode != "natural end" && mode != "unconnected" {
				cancel()
			}
			// A frame already in flight may arrive, but the channel must then close.
			deadline := time.After(time.Second)
		drain:
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						break drain
					}
				case <-deadline:
					t.Fatal("Read channel did not close")
				}
			}
			concurrentClose.Wait()
			if mode != "natural end" && mode != "unconnected" && session.IsConnected() {
				t.Fatal("canceled tunnel still reports connected")
			}
			if mode != "unconnected" {
				select {
				case <-peerDone:
				case <-time.After(time.Second):
					t.Fatal("peer did not observe tunnel closure")
				}
			}
			// Cancellation after a natural end must not leave a watcher behind.
			cancel()
		})
	}
}

func TestPortForwardSession_ConnectSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/portforward/stream") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// Read start message
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read start message failed: %v", err)
			return
		}

		var startMsg struct {
			Type string `json:"type"`
			Port int    `json:"port"`
		}
		if err := json.Unmarshal(msg, &startMsg); err != nil {
			t.Fatalf("unmarshal start message failed: %v", err)
			return
		}
		if startMsg.Type != "start" {
			t.Errorf("expected type 'start', got %s", startMsg.Type)
		}
		if startMsg.Port != 8080 {
			t.Errorf("expected port 8080, got %d", startMsg.Port)
		}

		// Send connected response
		conn.WriteJSON(map[string]string{"type": "connected"})

		// Echo binary data back (simulate TCP relay)
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if msgType == websocket.BinaryMessage {
				conn.WriteMessage(websocket.BinaryMessage, data)
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/sandboxes/sb-1/portforward/stream"
	session := &PortForwardSession{}
	err := session.connect(wsURL, http.Header{}, 8080)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer session.Close()

	if !session.IsConnected() {
		t.Error("expected session to be connected")
	}
}

func TestPortForwardSession_ConnectError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Read start message
		conn.ReadMessage()

		// Send error response
		conn.WriteJSON(map[string]string{"type": "error", "data": "connection refused"})
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/sandboxes/sb-1/portforward/stream"
	session := &PortForwardSession{}
	err := session.connect(wsURL, http.Header{}, 9999)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("expected 'connection refused' in error, got: %s", err.Error())
	}
}

func TestPortForwardSession_ReadWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// Handshake
		conn.ReadMessage()
		conn.WriteJSON(map[string]string{"type": "connected"})

		// Echo binary data back
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if msgType == websocket.BinaryMessage {
				conn.WriteMessage(websocket.BinaryMessage, data)
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/sandboxes/sb-1/portforward/stream"
	session := &PortForwardSession{}
	if err := session.connect(wsURL, http.Header{}, 8080); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer session.Close()

	// Write data
	testData := []byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")
	if err := session.Write(testData); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	// Read echoed data
	ch := session.Read(context.Background())
	received := <-ch
	if string(received) != string(testData) {
		t.Errorf("expected %q, got %q", string(testData), string(received))
	}
}

func TestPortForwardSession_Close(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.ReadMessage()
		conn.WriteJSON(map[string]string{"type": "connected"})

		// Keep reading until close
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}))
	defer server.Close()

	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/sandboxes/sb-1/portforward/stream"
	session := &PortForwardSession{}
	if err := session.connect(wsURL, http.Header{}, 8080); err != nil {
		t.Fatalf("connect failed: %v", err)
	}

	if err := session.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	if session.IsConnected() {
		t.Error("expected session to be disconnected after close")
	}

	// Write after close should fail
	if err := session.Write([]byte("data")); err == nil {
		t.Error("expected error writing to closed session")
	}
}

func TestPortForwardSession_AuthHeaders(t *testing.T) {
	var receivedHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header
		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		conn.ReadMessage()
		conn.WriteJSON(map[string]string{"type": "connected"})
	}))
	defer server.Close()

	// Test with API key
	_, wsURL, headers := newPortForwardSession(
		strings.Replace(server.URL, "http://", "https://", 1),
		"test-api-key", "", "",
		"/sandboxes/sb-1",
	)
	// Fix URL scheme for test (httptest uses http)
	wsURL = strings.Replace(wsURL, "wss://", "ws://", 1)

	session := &PortForwardSession{}
	if err := session.connect(wsURL, headers, 8080); err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	session.Close()

	if receivedHeaders.Get(apiKeyHeader) != "test-api-key" {
		t.Errorf("expected API key header, got %q", receivedHeaders.Get(apiKeyHeader))
	}
}

func TestNewPortForwardSession_URLConstruction(t *testing.T) {
	tests := []struct {
		name        string
		apiURL      string
		basePath    string
		expectedURL string
	}{
		{
			name:        "simple sandbox path",
			apiURL:      "https://api.example.com",
			basePath:    "/sandboxes/sb-1",
			expectedURL: "wss://api.example.com/sandboxes/sb-1/portforward/stream",
		},
		{
			name:        "sandbox group path",
			apiURL:      "https://api.example.com",
			basePath:    "/sandboxGroups/grp-1/sandboxes/sb-1",
			expectedURL: "wss://api.example.com/sandboxGroups/grp-1/sandboxes/sb-1/portforward/stream",
		},
		{
			name:        "http to ws conversion",
			apiURL:      "http://localhost:8080",
			basePath:    "/sandboxes/sb-1",
			expectedURL: "ws://localhost:8080/sandboxes/sb-1/portforward/stream",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, wsURL, _ := newPortForwardSession(tt.apiURL, "", "", "", tt.basePath)
			if wsURL != tt.expectedURL {
				t.Errorf("expected URL %q, got %q", tt.expectedURL, wsURL)
			}
		})
	}
}
