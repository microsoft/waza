package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
