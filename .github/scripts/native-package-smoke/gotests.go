package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

type testEvent struct {
	Action     string
	Package    string
	ImportPath string
	Test       string
}

// requireGoTests reads a `go test -json` stream and requires every named
// top-level test to have been selected and passed, never skipped, with no
// failure anywhere in the stream.
func requireGoTests(r io.Reader, required []string, w io.Writer) error {
	results := map[string]map[string]bool{}
	failures := []string{}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	events := 0
	for scanner.Scan() {
		var event testEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return fmt.Errorf("malformed go test -json event %q: %w", scanner.Text(), err)
		}
		events++
		if event.Action == "fail" || event.Action == "build-fail" {
			failures = append(failures, strings.TrimSpace(event.Package+event.ImportPath+" "+event.Test))
		}
		if event.Test == "" || strings.Contains(event.Test, "/") {
			continue
		}
		if results[event.Test] == nil {
			results[event.Test] = map[string]bool{}
		}
		results[event.Test][event.Action] = true
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if events == 0 {
		return fmt.Errorf("go test produced no JSON events")
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("go test failures: %s", strings.Join(failures, ", "))
	}
	for _, name := range required {
		actions := results[name]
		switch {
		case actions == nil:
			return fmt.Errorf("required test %s was not selected", name)
		case actions["skip"]:
			return fmt.Errorf("required test %s was skipped", name)
		case !actions["pass"]:
			return fmt.Errorf("required test %s did not pass", name)
		}
		fmt.Fprintf(w, "PASS %s\n", name)
	}
	return nil
}
