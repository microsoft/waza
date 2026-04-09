// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

package adc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/coreai-microsoft/adc-sdk-go/models"
	"github.com/gorilla/websocket"
)

// ExecStreamSession is an interactive exec stream session via WebSocket.
type ExecStreamSession struct {
	ws        *websocket.Conn
	sessionID string
	closed    bool
	mu        sync.Mutex
}

// newExecStreamSession creates a new exec stream session.
// sandboxBasePath should be the result of Sandbox.sandboxBasePath() (e.g., "/sandboxes/{id}" or "/sandboxGroups/{gid}/sandboxes/{id}").
func newExecStreamSession(apiURL, apiKey, token, githubToken, sandboxBasePath string) (*ExecStreamSession, string, http.Header) {
	// Convert HTTP URL to WebSocket URL
	wsURL := strings.Replace(apiURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	wsURL = fmt.Sprintf("%s%s/exec/stream", wsURL, sandboxBasePath)

	// Build auth headers
	headers := http.Header{}
	if apiKey != "" {
		headers.Set(apiKeyHeader, apiKey)
	} else if token != "" {
		headers.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	} else if githubToken != "" {
		headers.Set("Authorization", fmt.Sprintf("GitHub %s", githubToken))
	}

	return &ExecStreamSession{}, wsURL, headers
}

// connect establishes the WebSocket connection and sends the start message.
// Waits for the session_id message before returning.
func (s *ExecStreamSession) connect(wsURL string, headers http.Header, request *models.ExecStreamStartRequest) error {
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		return fmt.Errorf("failed to connect to WebSocket: %w", err)
	}
	s.ws = conn

	// Send start message
	startMsg := map[string]interface{}{
		"type": "start",
		"start": map[string]interface{}{
			"command":          request.Command,
			"args":             request.Args,
			"environment":      request.Environment,
			"workingDirectory": request.WorkingDirectory,
			"tty":              request.Tty,
			"stdin":            request.Stdin,
			"height":           request.Height,
			"width":            request.Width,
		},
	}
	if err := conn.WriteJSON(startMsg); err != nil {
		return fmt.Errorf("failed to send start message: %w", err)
	}

	// Wait for session_id message before returning
	_, messageData, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("failed to read session_id message: %w", err)
	}
	var wireMsg struct {
		Type string `json:"type"`
		Data string `json:"data,omitempty"`
	}
	if err := json.Unmarshal(messageData, &wireMsg); err == nil && wireMsg.Type == "session_id" {
		s.sessionID = wireMsg.Data
	}

	return nil
}

// SessionID returns the session ID assigned by the server.
func (s *ExecStreamSession) SessionID() string {
	return s.sessionID
}

// IsConnected returns whether the WebSocket is connected.
func (s *ExecStreamSession) IsConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ws != nil && !s.closed
}

// ReadOutput returns a channel that yields messages from the exec stream.
// The channel is closed when the command exits or an error occurs.
func (s *ExecStreamSession) ReadOutput() <-chan *models.ExecStreamMessage {
	ch := make(chan *models.ExecStreamMessage)

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

			_, messageData, err := ws.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				return
			}

			var wireMsg struct {
				Type     string `json:"type"`
				Data     string `json:"data,omitempty"`
				ExitCode *int   `json:"exitCode,omitempty"`
				Error    string `json:"error,omitempty"`
			}
			if err := json.Unmarshal(messageData, &wireMsg); err != nil {
				continue
			}

			msg := s.parseMessage(&wireMsg)
			if msg == nil {
				continue
			}

			if msg.Type == models.ExecStreamMessageTypeSessionID {
				s.sessionID = msg.SessionID
			}

			ch <- msg

			if msg.Type == models.ExecStreamMessageTypeExitCode || msg.Type == models.ExecStreamMessageTypeError {
				return
			}
		}
	}()

	return ch
}

// SendStdin sends stdin data to the process.
func (s *ExecStreamSession) SendStdin(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ws == nil || s.closed {
		return fmt.Errorf("WebSocket is not connected")
	}

	message := map[string]interface{}{
		"type": "stdin",
		"data": base64.StdEncoding.EncodeToString(data),
	}
	return s.ws.WriteJSON(message)
}

// SendStdinString sends stdin text to the process.
func (s *ExecStreamSession) SendStdinString(text string) error {
	return s.SendStdin([]byte(text))
}

// Resize resizes the terminal window.
func (s *ExecStreamSession) Resize(height, width int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ws == nil || s.closed {
		return fmt.Errorf("WebSocket is not connected")
	}

	message := map[string]interface{}{
		"type":   "resize",
		"resize": map[string]int{"height": height, "width": width},
	}
	return s.ws.WriteJSON(message)
}

// WaitForExit waits for the process to exit and returns the exit code.
func (s *ExecStreamSession) WaitForExit() (int, error) {
	for msg := range s.ReadOutput() {
		if msg.Type == models.ExecStreamMessageTypeExitCode && msg.ExitCode != nil {
			return *msg.ExitCode, nil
		}
		if msg.Type == models.ExecStreamMessageTypeError {
			return -1, fmt.Errorf("exec stream error: %s", msg.Error)
		}
	}
	return -1, fmt.Errorf("exec stream closed without exit code")
}

// Close closes the WebSocket connection.
func (s *ExecStreamSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.ws != nil && !s.closed {
		s.closed = true
		return s.ws.Close()
	}
	return nil
}

func (s *ExecStreamSession) parseMessage(wireMsg *struct {
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	Error    string `json:"error,omitempty"`
}) *models.ExecStreamMessage {
	switch wireMsg.Type {
	case "session_id":
		return &models.ExecStreamMessage{
			Type:      models.ExecStreamMessageTypeSessionID,
			SessionID: wireMsg.Data,
		}
	case "stdout":
		var data []byte
		if wireMsg.Data != "" {
			data, _ = base64.StdEncoding.DecodeString(wireMsg.Data)
		}
		return &models.ExecStreamMessage{
			Type: models.ExecStreamMessageTypeStdout,
			Data: data,
		}
	case "stderr":
		var data []byte
		if wireMsg.Data != "" {
			data, _ = base64.StdEncoding.DecodeString(wireMsg.Data)
		}
		return &models.ExecStreamMessage{
			Type: models.ExecStreamMessageTypeStderr,
			Data: data,
		}
	case "exit_code":
		return &models.ExecStreamMessage{
			Type:     models.ExecStreamMessageTypeExitCode,
			ExitCode: wireMsg.ExitCode,
		}
	case "error":
		return &models.ExecStreamMessage{
			Type:  models.ExecStreamMessageTypeError,
			Error: wireMsg.Error,
		}
	default:
		return nil
	}
}
