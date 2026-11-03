// This synthetic stdio peer tests the production CLI/SDK subprocess boundary.
// It is not a Copilot runtime and never performs inference or network I/O.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type request struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(64)
	}
}

func run() error {
	mode := os.Getenv("WAZA_SYNTHETIC_MODE")
	switch mode {
	case "success", "failed-check", "session-error", "timeout", "startup-failure", "bad-protocol", "options-failure":
	default:
		return fmt.Errorf("unknown synthetic mode %q", mode)
	}
	log, err := os.OpenFile(os.Getenv("WAZA_SYNTHETIC_LOG"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("creating synthetic invocation log: %w", err)
	}
	// This test-only log crosses container/host UIDs and contains no credentials.
	if err := log.Chmod(0644); err != nil {
		return errors.Join(fmt.Errorf("making synthetic invocation log host-readable: %w", err), log.Close())
	}
	var environmentKeys []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		environmentKeys = append(environmentKeys, key)
	}
	if err := json.NewEncoder(log).Encode(map[string]any{
		"method": "fixture.start", "args": os.Args[1:], "environment_keys": environmentKeys,
	}); err != nil {
		return errors.Join(err, log.Close())
	}
	if err := log.Close(); err != nil {
		return err
	}
	if mode == "startup-failure" {
		return fmt.Errorf("synthetic startup rejection")
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		body, err := readFrame(reader)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil {
			return err
		}
		log, err := os.OpenFile(os.Getenv("WAZA_SYNTHETIC_LOG"), os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(log).Encode(req); err != nil {
			return errors.Join(err, log.Close())
		}
		if err := log.Close(); err != nil {
			return err
		}
		if len(req.ID) == 0 {
			continue
		}
		if err := respond(req, mode, os.Stdout); err != nil {
			return err
		}
	}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	header, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && len(header) != 0 {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	value, ok := strings.CutPrefix(string(header), "Content-Length: ")
	if !ok {
		return nil, fmt.Errorf("expected Content-Length header")
	}
	length, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || length <= 0 || length > 1<<20 {
		return nil, fmt.Errorf("invalid synthetic frame length %q", value)
	}
	if blank, err := reader.ReadSlice('\n'); err != nil || string(blank) != "\r\n" {
		return nil, fmt.Errorf("expected header terminator")
	}
	body := make([]byte, length)
	_, err = io.ReadFull(reader, body)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return body, err
}

func writeFrame(writer io.Writer, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "Content-Length: %d\r\n\r\n%s", len(body), body)
	return err
}

func respond(req request, mode string, writer io.Writer) error {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if len(req.Params) != 0 {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return err
		}
		if req.Method == "session.options.update" && mode == "options-failure" {
			return writeFrame(writer, map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32000, "message": "synthetic options update rejection"},
			})
		}
	}
	var result any
	switch req.Method {
	case "connect":
		version := 3
		if mode == "bad-protocol" {
			version = 999
		}
		result = map[string]any{"ok": true, "protocolVersion": version, "version": "synthetic-runner-fixture"}
	case "auth.getStatus":
		result = map[string]any{"isAuthenticated": true}
	case "session.create":
		result = map[string]any{"sessionId": params.SessionID}
	case "session.send":
		result = map[string]any{"messageId": "synthetic-message"}
	case "session.usage.getMetrics":
		// Synthetic values only: these are not observed provider billing.
		result = map[string]any{
			"totalNanoAiu": 0,
			"modelMetrics": map[string]any{
				"synthetic-no-provider": map[string]any{
					"totalNanoAiu": 0, "usage": map[string]any{"inputTokens": 1},
				},
			},
		}
	case "session.detach", "session.delete", "session.options.update", "runtime.shutdown":
		result = map[string]any{"success": true}
	default:
		return writeFrame(writer, map[string]any{
			"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "unsupported synthetic method: " + req.Method},
		})
	}
	if err := writeFrame(writer, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
		return err
	}
	if req.Method != "session.send" || mode == "timeout" {
		return nil
	}
	emit := func(kind string, data map[string]any) error {
		return writeFrame(writer, map[string]any{
			"jsonrpc": "2.0", "method": "session.event",
			"params": map[string]any{
				"sessionId": params.SessionID,
				"event": map[string]any{
					"id": "synthetic-" + kind, "type": kind, "timestamp": "2026-01-01T00:00:00Z",
					"parentId": nil, "data": data,
				},
			},
		})
	}
	if mode == "session-error" {
		return emit("session.error", map[string]any{"message": "synthetic execution rejection", "errorType": "fixture"})
	}
	content := "hello"
	if mode == "failed-check" {
		content = "synthetic near miss"
	}
	if err := emit("assistant.message", map[string]any{"content": content, "messageId": "synthetic-message"}); err != nil {
		return err
	}
	return emit("session.idle", map[string]any{})
}
