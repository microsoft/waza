package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestWorkspaceCaptureSortedSanitizedDetached(t *testing.T) {
	root := workspaceTestRoot(t)
	original := "contact person@example.com\n"
	workspaceTestWrite(t, root, "z.txt", original)
	workspaceTestWrite(t, root, "sub/a.txt", "こんにちは\n")
	p := DefaultPolicy()
	p.RedactString(original)
	before := p.MatchCount()
	paths := []string{"z.txt", "sub/a.txt"}
	files, err := CaptureWorkspace(root, paths, nil, p, WorkspaceLimits{1024, 2048})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "sub/a.txt" || files[1].Path != "z.txt" {
		t.Fatalf("unexpected sorted files: %#v", files)
	}
	if !reflect.DeepEqual(paths, []string{"z.txt", "sub/a.txt"}) || p.MatchCount() != before {
		t.Fatal("caller inputs or policy counters were modified")
	}
	if files[0].Content != "こんにちは\n" || files[0].Redacted || files[1].Content != "contact [REDACTED]\n" || !files[1].Redacted {
		t.Fatalf("unexpected sanitized evidence: %#v", files)
	}
	for _, file := range files {
		digest := sha256.Sum256([]byte(file.Content))
		if file.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatal("digest does not hash exact sanitized content")
		}
	}
	content, err := os.ReadFile(filepath.Join(root, "z.txt"))
	if err != nil || string(content) != original {
		t.Fatal("original content was changed")
	}
	workspaceTestWrite(t, root, "z.txt", "changed")
	if files[1].Content != "contact [REDACTED]\n" {
		t.Fatal("returned content is not detached")
	}
	nilPolicy, err := CaptureWorkspace(root, []string{"sub/a.txt"}, nil, nil, WorkspaceLimits{1024, 2048})
	if err != nil || len(nilPolicy) != 1 {
		t.Fatalf("default policy failed: %v", err)
	}
}

func TestWorkspaceCaptureValidation(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.txt", "safe")
	tests := []struct {
		name, root string
		paths      []string
		excluded   []string
		limits     WorkspaceLimits
		policy     *Policy
	}{
		{name: "empty root", paths: []string{"a.txt"}, limits: WorkspaceLimits{20, 40}},
		{name: "missing root", root: filepath.Join(root, "missing"), paths: []string{"a.txt"}, limits: WorkspaceLimits{20, 40}},
		{name: "no allowlist", root: root, limits: WorkspaceLimits{20, 40}},
		{name: "invalid file bound", root: root, paths: []string{"a.txt"}, limits: WorkspaceLimits{0, 40}},
		{name: "invalid total bound", root: root, paths: []string{"a.txt"}, limits: WorkspaceLimits{20, -1}},
		{name: "invalid exclusion", root: root, paths: []string{"a.txt"}, excluded: []string{"../private"}, limits: WorkspaceLimits{20, 40}},
		{name: "invalid policy", root: root, paths: []string{"a.txt"}, limits: WorkspaceLimits{20, 40}, policy: &Policy{Rules: []RedactionRule{{Name: "bad", Pattern: "["}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			files, err := CaptureWorkspace(test.root, test.paths, test.excluded, test.policy, test.limits)
			if err == nil || len(files) != 0 {
				t.Fatalf("expected rejected capture: %#v, %v", files, err)
			}
		})
	}
}

func TestWorkspaceCaptureRejectedPaths(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.txt", "safe")
	for _, name := range []string{
		"", ".", "..", "../outside", "a/../b", "/absolute", "C:/drive", "a:b",
		"a\\b", "a\x00b", "a//b", "./a.txt", "a/", ".env", ".ENV", ".git/config",
		"a/.hidden/b", "secret.txt", "keys/a", "credentials.txt", "API_TOKEN.txt",
		"person@example.com", "a/\xff",
	} {
		t.Run(strconv.Quote(name), func(t *testing.T) {
			files, err := CaptureWorkspace(root, []string{name}, nil, nil, WorkspaceLimits{100, 200})
			if err == nil || len(files) != 0 {
				t.Fatalf("expected path rejection: %#v, %v", files, err)
			}
			if name != "" && strings.Contains(err.Error(), name) {
				t.Fatal("diagnostic leaked unsafe input")
			}
		})
	}
	files, err := CaptureWorkspace(root, []string{"a.txt", "a.txt"}, nil, nil, WorkspaceLimits{100, 200})
	if err == nil || len(files) != 1 || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("duplicate must report partial capture: %#v, %v", files, err)
	}
	p := &Policy{EnvKeyDenyList: []string{"internal"}}
	if files, err := CaptureWorkspace(root, []string{"internal/a"}, nil, p, WorkspaceLimits{100, 200}); err == nil || len(files) != 0 {
		t.Fatal("custom sensitive key was not rejected")
	}
	p = &Policy{Rules: []RedactionRule{{Name: "placeholder", Pattern: `\[REDACTED\]`}}}
	if files, err := CaptureWorkspace(root, []string{"[REDACTED]"}, nil, p, WorkspaceLimits{100, 200}); err == nil || len(files) != 0 {
		t.Fatal("matching file identity was accepted because replacement was identical")
	}
	if p.Rules[0].compiled != nil || p.MatchCount() != 0 {
		t.Fatal("capture compiled or changed the caller's policy")
	}
}

func TestWorkspaceCaptureEvaluatorOnlyBeforeRead(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "private-neighbor.txt", "safe")
	for _, name := range []string{"private", "private/missing.txt", "hidden/.state.txt"} {
		files, err := CaptureWorkspace(root, []string{name}, []string{"private", "hidden"}, nil, WorkspaceLimits{100, 200})
		if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "evaluator-only") {
			t.Fatalf("exclusion must precede reads: %#v, %v", files, err)
		}
	}
	files, err := CaptureWorkspace(root, []string{"private-neighbor.txt"}, []string{"private"}, nil, WorkspaceLimits{100, 200})
	if err != nil || len(files) != 1 {
		t.Fatal("ancestor exclusion incorrectly matched neighbor")
	}
}

func TestWorkspaceCaptureContentAndBounds(t *testing.T) {
	root := workspaceTestRoot(t)
	tests := []struct {
		name, content string
		limits        WorkspaceLimits
	}{
		{"invalid UTF8", "\xff", WorkspaceLimits{20, 40}},
		{"NUL", "hi\x00there", WorkspaceLimits{20, 40}},
		{"binary control", "hi\x01there", WorkspaceLimits{20, 40}},
		{"binary DEL", "hi\x7fthere", WorkspaceLimits{20, 40}},
		{"file size", "12345", WorkspaceLimits{4, 40}},
		{"total size", "12345", WorkspaceLimits{20, 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspaceTestWrite(t, root, "a.txt", test.content)
			files, err := CaptureWorkspace(root, []string{"a.txt"}, nil, nil, test.limits)
			if err == nil || len(files) != 0 {
				t.Fatalf("expected content rejection: %#v, %v", files, err)
			}
		})
	}
	workspaceTestWrite(t, root, "a.txt", "1234")
	workspaceTestWrite(t, root, "b.txt", "1234")
	files, err := CaptureWorkspace(root, []string{"b.txt", "a.txt"}, nil, nil, WorkspaceLimits{4, 7})
	if err == nil || len(files) != 1 || files[0].Path != "a.txt" {
		t.Fatalf("aggregate input was not bounded: %#v, %v", files, err)
	}
	files, err = CaptureWorkspace(root, []string{"b.txt", "a.txt"}, nil, nil, WorkspaceLimits{4, 8})
	if err != nil || len(files) != 2 {
		t.Fatalf("exact boundary failed: %#v, %v", files, err)
	}
	workspaceTestWrite(t, root, "a.txt", "")
	files, err = CaptureWorkspace(root, []string{"a.txt"}, nil, nil, WorkspaceLimits{1, 1})
	if err != nil || len(files) != 1 || files[0].Content != "" {
		t.Fatalf("empty regular file failed: %#v, %v", files, err)
	}
}

func TestWorkspaceCaptureRedactionExpansion(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.txt", "!")
	workspaceTestWrite(t, root, "b.txt", "!")
	p := &Policy{Rules: []RedactionRule{{Name: "marker", Pattern: "!"}}}
	for _, limits := range []WorkspaceLimits{{1, 100}, {100, 1}} {
		files, err := CaptureWorkspace(root, []string{"a.txt"}, nil, p, limits)
		if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "sanitized") {
			t.Fatalf("expansion exceeded budget: %#v, %v", files, err)
		}
	}
	p = &Policy{Rules: []RedactionRule{
		{Name: "marker", Pattern: "!"},
		{Name: "expanded", Pattern: "REDACTED"},
	}}
	files, err := CaptureWorkspace(root, []string{"a.txt"}, nil, p, WorkspaceLimits{10, 100})
	if err == nil || len(files) != 0 {
		t.Fatalf("intermediate rule expansion exceeded budget: %#v, %v", files, err)
	}
	p = &Policy{Rules: []RedactionRule{{Name: "marker", Pattern: "!"}}}
	files, err = CaptureWorkspace(root, []string{"a.txt", "b.txt"}, nil, p, WorkspaceLimits{10, 19})
	if err == nil || len(files) != 1 || files[0].Content != RedactionPlaceholder {
		t.Fatalf("aggregate output was not bounded: %#v, %v", files, err)
	}
}

func TestWorkspaceCaptureJSONSanitization(t *testing.T) {
	root := workspaceTestRoot(t)
	tests := []struct {
		name, input, expected string
		redacted              bool
	}{
		{
			"nested short credentials",
			`{"items":[{"password":"short"},{"inner":{"api_key":"x","safe":"ok","credentials":{"value":"tiny"}}}],"token":123}`,
			`{"items":[{"password":"[REDACTED]"},{"inner":{"api_key":"[REDACTED]","credentials":"[REDACTED]","safe":"ok"}}],"token":"[REDACTED]"}`,
			true,
		},
		{
			"exact large numbers",
			`{"n":900719925474099312345678901234567890,"nested":[1.234567890123456789,1e1000]}`,
			`{"n":900719925474099312345678901234567890,"nested":[1.234567890123456789,1e1000]}`,
			false,
		},
		{"whitespace normalization", " { \"value\" : \"ok\" }\n", `{"value":"ok"}`, true},
		{"already normalized", `{"value":"ok"}`, `{"value":"ok"}`, false},
		{"pattern value", `{"value":"person@example.com"}`, `{"value":"[REDACTED]"}`, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspaceTestWrite(t, root, "data.JSON", test.input)
			files, err := CaptureWorkspace(root, []string{"data.JSON"}, nil, nil, WorkspaceLimits{4096, 8192})
			if err != nil || len(files) != 1 {
				t.Fatalf("JSON capture failed: %#v, %v", files, err)
			}
			if files[0].Content != test.expected || files[0].Redacted != test.redacted {
				t.Fatalf("unexpected sanitized JSON: %#v", files[0])
			}
			digest := sha256.Sum256([]byte(test.expected))
			if files[0].SHA256 != hex.EncodeToString(digest[:]) {
				t.Fatal("JSON digest does not hash exact sanitized bytes")
			}
			original, err := os.ReadFile(filepath.Join(root, "data.JSON"))
			if err != nil || string(original) != test.input {
				t.Fatal("JSON source was modified")
			}
		})
	}
}

func TestWorkspaceCaptureJSONRejectsInvalidContentAndKeys(t *testing.T) {
	root := workspaceTestRoot(t)
	for _, test := range []struct {
		name, input, diagnostic string
	}{
		{"malformed", `{"password":"short"`, "invalid JSON"},
		{"empty", "", "invalid JSON"},
		{"trailing document", `{} {"password":"short"}`, "invalid JSON"},
		{"unsafe key", `{"nested":{"person@example.com":"short"}}`, "object key requires redaction"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceTestWrite(t, root, "a.json", test.input)
			workspaceTestWrite(t, root, "b.txt", "safe")
			files, err := CaptureWorkspace(root, []string{"a.json", "b.txt"}, nil, nil, WorkspaceLimits{4096, 8192})
			if err == nil || len(files) != 1 || files[0].Path != "b.txt" || !strings.Contains(err.Error(), test.diagnostic) || !strings.Contains(err.Error(), "partial") {
				t.Fatalf("expected explicit partial JSON rejection: %#v, %v", files, err)
			}
			if strings.Contains(err.Error(), "short") || strings.Contains(err.Error(), "person@example.com") {
				t.Fatal("JSON diagnostic exposed source content")
			}
		})
	}
}

func TestWorkspaceCaptureJSONOutputBounds(t *testing.T) {
	root := workspaceTestRoot(t)
	input := `{"password":"x"}`
	expected := `{"password":"[REDACTED]"}`
	workspaceTestWrite(t, root, "a.json", input)
	workspaceTestWrite(t, root, "b.json", input)
	for _, limits := range []WorkspaceLimits{{int64(len(input)), 100}, {100, int64(len(input))}} {
		files, err := CaptureWorkspace(root, []string{"a.json"}, nil, nil, limits)
		if err == nil || len(files) != 0 || !strings.Contains(err.Error(), "sanitized JSON content exceeds") {
			t.Fatalf("JSON expansion must respect output limits: %#v, %v", files, err)
		}
	}
	files, err := CaptureWorkspace(root, []string{"a.json", "b.json"}, nil, nil, WorkspaceLimits{100, int64(2*len(expected) - 1)})
	if err == nil || len(files) != 1 || files[0].Content != expected {
		t.Fatalf("aggregate JSON output was not bounded: %#v, %v", files, err)
	}
	files, err = CaptureWorkspace(root, []string{"a.json"}, nil, nil, WorkspaceLimits{int64(len(expected)), int64(len(expected))})
	if err != nil || len(files) != 1 {
		t.Fatalf("exact JSON output boundary failed: %#v, %v", files, err)
	}
	spaced := " { \"value\" : \"ok\" } \n"
	other := `{"v":1}`
	workspaceTestWrite(t, root, "a.json", spaced)
	workspaceTestWrite(t, root, "b.json", other)
	files, err = CaptureWorkspace(root, []string{"a.json", "b.json"}, nil, nil, WorkspaceLimits{100, int64(len(spaced) + len(other) - 1)})
	if err == nil || len(files) != 1 || !strings.Contains(err.Error(), "file exceeds remaining byte limits") {
		t.Fatalf("normalization must not release input budget: %#v, %v", files, err)
	}
}

func TestWorkspaceAccounting(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.json", `{"password":"short","nested":{"value":"person@example.com"}}`)
	workspaceTestWrite(t, root, "b.txt", "person@example.com")
	workspaceTestWrite(t, root, "c.json", " { \"value\" : \"ok\" } \n")
	p := DefaultPolicy()
	p.RedactString("prior@example.com")
	count := p.MatchCount()
	files, summary, err := CaptureWorkspaceWithAccounting(root, []string{"a.json", "b.txt", "c.json", "missing"}, nil, p, WorkspaceLimits{1024, 4096})
	if err == nil || len(files) != 3 || summary.Policy != "default" || summary.RedactionCount != 3 || !reflect.DeepEqual(summary.AppliedRules, []string{"email", "sensitive_key"}) {
		t.Fatalf("unexpected partial accounting: %#v, %#v, %v", files, summary, err)
	}
	if !files[2].Redacted || p.MatchCount() != count || !reflect.DeepEqual(p.MatchedRules(), []string{"email"}) {
		t.Fatal("normalization or caller policy accounting changed")
	}
	summary.AppliedRules[0] = "changed"
	files, summary, err = CaptureWorkspaceWithAccounting(root, []string{"c.json"}, nil, p, WorkspaceLimits{1024, 4096})
	if err != nil || len(files) != 1 || !files[0].Redacted || summary.RedactionCount != 0 || len(summary.AppliedRules) != 0 {
		t.Fatalf("normalization must not count as secret redaction: %#v, %#v, %v", files, summary, err)
	}
	for _, test := range []struct {
		file, rule string
		count      int
	}{{"a.json", "sensitive_key", 2}, {"b.txt", "email", 1}} {
		_, summary, err := CaptureWorkspaceWithAccounting(root, []string{test.file}, nil, p, WorkspaceLimits{1024, 4096})
		if err != nil || summary.RedactionCount != test.count || !strings.Contains(strings.Join(summary.AppliedRules, ","), test.rule) {
			t.Fatalf("workspace-only accounting lost matches: %#v, %v", summary, err)
		}
	}
	short := `{"password":"x","token":"y"}`
	workspaceTestWrite(t, root, "a.json", short)
	_, summary, err = CaptureWorkspaceWithAccounting(root, []string{"a.json"}, nil, p, WorkspaceLimits{int64(len(short)), 4096})
	if err == nil || summary.RedactionCount != 2 {
		t.Fatalf("failed JSON output must retain attempted accounting: %#v, %v", summary, err)
	}
}

func TestWorkspaceAccountingTextPartialAttempt(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.txt", "!")
	p := &Policy{Rules: []RedactionRule{
		{Name: "marker", Pattern: "!"},
		{Name: "expanded", Pattern: "REDACTED"},
	}}
	files, summary, err := CaptureWorkspaceWithAccounting(root, []string{"a.txt"}, nil, p, WorkspaceLimits{10, 100})
	if err == nil || len(files) != 0 || summary.RedactionCount != 1 || !reflect.DeepEqual(summary.AppliedRules, []string{"marker"}) {
		t.Fatalf("partial text attempt lost accounting: %#v, %#v, %v", files, summary, err)
	}
	if len(p.Rules) != 2 || p.MatchCount() != 0 {
		t.Fatal("text redaction changed caller policy")
	}
}

func TestWorkspaceAccountingConcurrent(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "a.json", `{"password":"short"}`)
	workspaceTestWrite(t, root, "b.txt", "person@example.com")
	p := DefaultPolicy()
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			files, summary, err := CaptureWorkspaceWithAccounting(root, []string{"a.json", "b.txt"}, nil, p, WorkspaceLimits{1024, 4096})
			if err != nil || len(files) != 2 || summary.RedactionCount != 2 || !reflect.DeepEqual(summary.AppliedRules, []string{"email", "sensitive_key"}) {
				t.Errorf("unexpected concurrent accounting: %#v, %#v, %v", files, summary, err)
			}
		})
	}
	group.Wait()
	if p.MatchCount() != 0 || len(p.MatchedRules()) != 0 {
		t.Fatal("concurrent captures modified shared caller policy")
	}
}

func TestWorkspaceCaptureMissingAndDirectory(t *testing.T) {
	root := workspaceTestRoot(t)
	workspaceTestWrite(t, root, "sub/a.txt", "safe")
	for _, name := range []string{"missing", "sub", "sub/a.txt/child"} {
		if files, err := CaptureWorkspace(root, []string{name}, nil, nil, WorkspaceLimits{20, 40}); err == nil || len(files) != 0 {
			t.Fatalf("unsupported entry accepted: %#v, %v", files, err)
		}
	}
	if files, err := CaptureWorkspace(filepath.Join(root, "sub/a.txt"), []string{"anything"}, nil, nil, WorkspaceLimits{20, 40}); err == nil || len(files) != 0 {
		t.Fatal("regular-file root accepted")
	}
}

type workspaceFailReader struct{}

func (workspaceFailReader) Read([]byte) (int, error) {
	return 0, errors.New("sensitive content in underlying error")
}

type workspaceProbeFailReader struct{ first bool }

func (r *workspaceProbeFailReader) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		p[0] = 'a'
		return 1, nil
	}
	return 0, errors.New("sensitive content in underlying error")
}

func TestWorkspaceReadBoundsAndErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		reader      io.Reader
		size, limit int64
	}{
		{"exhausted budget", strings.NewReader(""), 0, 0},
		{"negative size", strings.NewReader(""), -1, 1},
		{"short read", strings.NewReader(""), 1, 2},
		{"read failure", workspaceFailReader{}, 1, 1},
		{"later read failure", &workspaceProbeFailReader{}, 2, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := workspaceRead(test.reader, test.size, test.limit)
			if err == nil || int64(len(data)) > test.limit || strings.Contains(err.Error(), "sensitive content") {
				t.Fatalf("expected bounded sanitized failure: %q, %v", data, err)
			}
		})
	}
	reader := strings.NewReader("123")
	data, err := workspaceRead(reader, 2, 2)
	if err != nil || string(data) != "12" || reader.Len() != 1 {
		t.Fatalf("read must not probe past the byte budget: %q, %v", data, err)
	}
}
