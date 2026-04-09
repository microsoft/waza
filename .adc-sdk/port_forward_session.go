// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// PortForwardSession is a bidirectional TCP tunnel to a port inside a sandbox via WebSocket.
//
// After the JSON handshake, all data is transferred as raw binary WebSocket frames
// (no base64 encoding), making it efficient for high-throughput TCP tunneling.
type PortForwardSession struct {
	ws     *websocket.Conn
	closed bool
	mu     sync.Mutex
}

// newPortForwardSession creates a new port-forward session.
// sandboxBasePath should be the result of Sandbox.sandboxBasePath() (e.g., "/sandboxes/{id}" or "/sandboxGroups/{gid}/sandboxes/{id}").
func newPortForwardSession(apiURL, apiKey, token, githubToken, sandboxBasePath string) (*PortForwardSession, string, http.Header) {
	// Convert HTTP URL to WebSocket URL
	wsURL := strings.Replace(apiURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s%s/portforward/stream", wsURL, sandboxBasePath)

	// Build auth headers
	headers := http.Header{}
	if apiKey != "" {
		headers.Set(apiKeyHeader, apiKey)
	} else if token != "" {
		headers.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	} else if githubToken != "" {
		headers.Set("Authorization", fmt.Sprintf("GitHub %s", githubToken))
	}

	return &PortForwardSession{}, wsURL, headers
}

// connect establishes the WebSocket connection and performs the port-forward handshake.
// Waits for the "connected" message before returning.
func (s *PortForwardSession) connect(wsURL string, headers http.Header, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}

	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	s.ws = conn

	// Send start message (JSON text frame)
	startMsg := map[string]interface{}{
		"type": "start",
		"port": port,
	}
	if err := conn.WriteJSON(startMsg); err != nil {
		conn.Close()
		s.ws = nil
		return fmt.Errorf("failed to send start message: %w", err)
	}

	// Wait for connected/error response
	_, messageData, err := conn.ReadMessage()
	if err != nil {
		conn.Close()
		s.ws = nil
		return fmt.Errorf("failed to read handshake response: %w", err)
	}
	var wireMsg struct {
		Type string `json:"type"`
		Data string `json:"data,omitempty"`
	}
	if err := json.Unmarshal(messageData, &wireMsg); err != nil {
		conn.Close()
		s.ws = nil
		return fmt.Errorf("invalid handshake response: %w", err)
	}

	switch wireMsg.Type {
	case "connected":
		return nil
	case "error":
		conn.Close()
		s.ws = nil
		return fmt.Errorf("port-forward error: %s", wireMsg.Data)
	default:
		conn.Close()
		s.ws = nil
		return fmt.Errorf("unexpected handshake message type: %s", wireMsg.Type)
	}
}

// IsConnected returns whether the WebSocket is connected.
func (s *PortForwardSession) IsConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws != nil && !s.closed
}

// Read returns a channel that yields raw TCP data chunks from the remote port.
// The channel is closed when the tunnel ends, an error occurs, or the context is cancelled.
func (s *PortForwardSession) Read(ctx context.Context) <-chan []byte {
	ch := make(chan []byte)

	go func() {
		defer close(ch)

		for {
			s.mu.Lock()
			if s.closed || s.ws == nil {
				s.mu.Unlock()
				return
			}
			ws := s.ws
			s.mu.Unlock()

			msgType, data, err := ws.ReadMessage()
			if err != nil {
				return
			}

			// Only relay binary frames (raw TCP data)
			if msgType == websocket.BinaryMessage {
				select {
				case ch <- data:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return ch
}

// Write sends data through the tunnel to the remote port as a binary WebSocket frame.
func (s *PortForwardSession) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ws == nil || s.closed {
		return fmt.Errorf("WebSocket is not connected")
	}

	return s.ws.WriteMessage(websocket.BinaryMessage, data)
}

// Close closes the WebSocket connection.
func (s *PortForwardSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ws != nil && !s.closed {
		s.closed = true
		return s.ws.Close()
	}
	return nil
}
