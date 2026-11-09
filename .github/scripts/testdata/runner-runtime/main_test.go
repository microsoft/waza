package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvocationLogHostReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpc.ndjson")
	t.Setenv("WAZA_SYNTHETIC_MODE", "startup-failure")
	t.Setenv("WAZA_SYNTHETIC_LOG", path)
	const secret = "synthetic-secret-must-not-be-in-host-readable-log"
	t.Setenv("WAZA_TEST_SECRET_SENTINEL", secret)
	if err := run(); err == nil || err.Error() != "synthetic startup rejection" {
		t.Fatalf("expected deliberate startup rejection after logging: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0644 {
		t.Fatalf("test-only log must be host-readable without granting other UIDs write access: %v", info.Mode())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		Method          string   `json:"method"`
		EnvironmentKeys []string `json:"environment_keys"`
	}
	if err := json.Unmarshal(body, &start); err != nil || start.Method != "fixture.start" {
		t.Fatalf("missing invocation evidence: %s (%v)", body, err)
	}
	if bytes.Contains(body, []byte(secret)) {
		t.Fatal("host-readable log contains an environment value")
	}
	found := false
	for _, key := range start.EnvironmentKeys {
		if strings.Contains(key, "=") {
			t.Fatalf("environment entry includes a value: %q", key)
		}
		found = found || key == "WAZA_TEST_SECRET_SENTINEL"
	}
	if !found {
		t.Fatal("environment key sentinel missing; secrecy assertion did not exercise the injected environment")
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "creating synthetic invocation log") {
		t.Fatalf("existing evidence must not be overwritten: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(body, after) {
		t.Fatalf("existing evidence changed: %s (%v)", after, err)
	}
}

func TestFrameBounds(t *testing.T) {
	for _, wire := range []string{
		"other: 1\r\n\r\nx", "Content-Length: 0\r\n\r\n", "Content-Length: 1048577\r\n\r\n",
		"Content-Length: bad\r\n\r\n", "Content-Length: 1\r\nx\nx", "Content-Length: 2\r\n\r\nx",
		"Content-Length: 1\r\n\r\n", "Content-Length:", strings.Repeat("x", 8192),
	} {
		if _, err := readFrame(bufio.NewReader(strings.NewReader(wire))); err == nil {
			t.Fatalf("accepted invalid frame %q", wire)
		}
	}
	var wire bytes.Buffer
	if err := writeFrame(&wire, map[string]string{"value": "hello"}); err != nil {
		t.Fatal(err)
	}
	body, err := readFrame(bufio.NewReader(&wire))
	if err != nil || string(body) != `{"value":"hello"}` {
		t.Fatalf("round trip: %s, %v", body, err)
	}
}

func TestUnexpectedMethodFails(t *testing.T) {
	var wire bytes.Buffer
	if err := respond(request{ID: json.RawMessage(`1`), Method: "models.list"}, "success", &wire); err != nil {
		t.Fatal(err)
	}

	body, err := readFrame(bufio.NewReader(&wire))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error.Code != -32601 || response.Result != nil {
		t.Fatalf("unexpected method returned a success-shaped response: %s", body)
	}
}

func TestOptionsUpdateRejection(t *testing.T) {
	for _, mode := range []string{"success", "options-failure"} {
		t.Run(mode, func(t *testing.T) {
			var wire bytes.Buffer
			if err := respond(request{
				ID: json.RawMessage(`7`), Method: "session.options.update",
				Params: json.RawMessage(`{"sessionId":"synthetic-session","skipCustomInstructions":true}`),
			}, mode, &wire); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(&wire)
			body, err := readFrame(reader)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatal(err)
			}
			if response.ID != 7 {
				t.Fatalf("response is not bound to the options request: %s", body)
			}
			if mode == "options-failure" {
				if response.Error == nil || response.Error.Code != -32000 || response.Result != nil {
					t.Fatalf("missing explicit options rejection: %s", body)
				}
			} else if response.Error != nil || string(response.Result) != `{"success":true}` {
				t.Fatalf("missing options acknowledgement: %s", body)
			}
			if reader.Buffered() != 0 {
				t.Fatal("options update must not emit assistant output or completion")
			}
		})
	}
}
