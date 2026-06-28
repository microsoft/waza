package argmatcher

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMatcherYAMLDecode(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantErr  string
		wantKind Kind
	}{
		{name: "equals string", yaml: `{equals: "hello"}`, wantKind: KindEquals},
		{name: "equals number", yaml: `{equals: 42}`, wantKind: KindEquals},
		{name: "equals object", yaml: `{equals: {a: 1, b: 2}}`, wantKind: KindEquals},
		{name: "regex", yaml: `{regex: "^auth"}`, wantKind: KindRegex},
		{name: "contains", yaml: `{contains: "abc"}`, wantKind: KindContains},
		{name: "range gte+lte", yaml: `{range: {gte: 1, lte: 5}}`, wantKind: KindRange},
		{name: "range gt only", yaml: `{range: {gt: 0}}`, wantKind: KindRange},
		{name: "json_schema", yaml: `{json_schema: {type: object, required: [a]}}`, wantKind: KindJSONSchema},

		{name: "empty matcher", yaml: `{}`, wantErr: "empty matcher"},
		{name: "two keys", yaml: `{regex: "a", contains: "b"}`, wantErr: "exactly one matcher kind"},
		{name: "unknown kind", yaml: `{wat: "x"}`, wantErr: "unknown matcher kind"},
		{name: "regex empty", yaml: `{regex: ""}`, wantErr: "non-empty pattern"},
		{name: "regex invalid", yaml: `{regex: "["}`, wantErr: "invalid regex"},
		{name: "contains empty", yaml: `{contains: ""}`, wantErr: "non-empty substring"},
		{name: "range no bounds", yaml: `{range: {}}`, wantErr: "at least one bound"},
		{name: "json_schema empty", yaml: `{json_schema: {}}`, wantErr: "non-empty schema"},
		{name: "json_schema invalid", yaml: `{json_schema: {type: "nonsense"}}`, wantErr: "invalid json_schema"},
		{name: "not a mapping", yaml: `"foo"`, wantErr: "expected mapping"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Matcher
			err := yaml.Unmarshal([]byte(tt.yaml), &m)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if m.Kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", m.Kind, tt.wantKind)
			}
		})
	}
}

func TestMatcherEquals(t *testing.T) {
	cases := []struct {
		name     string
		expected any
		actual   any
		want     bool
	}{
		{"string match", "hello", "hello", true},
		{"string mismatch", "hello", "world", false},
		{"int vs float", 42, 42.0, true},
		{"object match", map[string]any{"a": 1}, map[string]any{"a": 1.0}, true},
		{"object mismatch", map[string]any{"a": 1}, map[string]any{"a": 2}, false},
		{"nil match", nil, nil, true},
		{"array match", []any{1, 2}, []any{1, 2}, true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m := &Matcher{Kind: KindEquals, Equals: tt.expected}
			ok, msg := m.Match(tt.actual)
			if ok != tt.want {
				t.Fatalf("want %v, got %v (%s)", tt.want, ok, msg)
			}
			if msg == "" {
				t.Fatal("expected non-empty message")
			}
		})
	}
}

func TestMatcherRegex(t *testing.T) {
	m := &Matcher{Kind: KindRegex, Regex: "^auth"}
	if err := m.Compile(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Match("auth-login"); !ok {
		t.Fatal("expected match")
	}
	if ok, _ := m.Match("login"); ok {
		t.Fatal("expected no match")
	}
	// non-string value falls back to JSON encoding
	if ok, _ := m.Match(map[string]any{"x": 1}); ok {
		t.Fatal("expected no match for object")
	}
}

func TestMatcherContains(t *testing.T) {
	m := &Matcher{Kind: KindContains, Contains: "hello"}
	if err := m.Compile(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Match("say hello world"); !ok {
		t.Fatal("expected match")
	}
	if ok, _ := m.Match("goodbye"); ok {
		t.Fatal("expected no match")
	}
}

func TestMatcherRange(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	m := &Matcher{Kind: KindRange, Range: &RangeSpec{GTE: f(1), LTE: f(5)}}
	if err := m.Compile(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Match(3); !ok {
		t.Fatal("expected 3 in range")
	}
	if ok, _ := m.Match(0); ok {
		t.Fatal("expected 0 below range")
	}
	if ok, _ := m.Match(6); ok {
		t.Fatal("expected 6 above range")
	}
	if ok, _ := m.Match("not a number"); ok {
		t.Fatal("expected non-numeric rejection")
	}

	// exclusive bounds
	m2 := &Matcher{Kind: KindRange, Range: &RangeSpec{GT: f(0), LT: f(10)}}
	_ = m2.Compile()
	if ok, _ := m2.Match(0); ok {
		t.Fatal("gt boundary should fail")
	}
	if ok, _ := m2.Match(10); ok {
		t.Fatal("lt boundary should fail")
	}
	if ok, _ := m2.Match(5); !ok {
		t.Fatal("5 within exclusive bounds")
	}
}

func TestMatcherJSONSchema(t *testing.T) {
	m := &Matcher{
		Kind: KindJSONSchema,
		JSONSchema: map[string]any{
			"type":     "object",
			"required": []any{"name"},
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"age":  map[string]any{"type": "integer", "minimum": 0},
			},
		},
	}
	if err := m.Compile(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := m.Match(map[string]any{"name": "alice", "age": 30}); !ok {
		t.Fatal("expected valid")
	}
	if ok, _ := m.Match(map[string]any{"age": 30}); ok {
		t.Fatal("expected required-field failure")
	}
	if ok, _ := m.Match(map[string]any{"name": "a", "age": -1}); ok {
		t.Fatal("expected minimum failure")
	}
}

func TestMatcherJSONRoundTrip(t *testing.T) {
	src := `{"regex":"^auth"}`
	var m Matcher
	if err := json.Unmarshal([]byte(src), &m); err != nil {
		t.Fatal(err)
	}
	if m.Kind != KindRegex || m.Regex != "^auth" {
		t.Fatalf("unexpected matcher: %+v", m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != src {
		t.Fatalf("round trip mismatch: got %s want %s", out, src)
	}
}

func TestMatcherJSONRejectsMultipleKinds(t *testing.T) {
	var m Matcher
	err := json.Unmarshal([]byte(`{"regex":"a","contains":"b"}`), &m)
	if err == nil || !strings.Contains(err.Error(), "exactly one matcher kind") {
		t.Fatalf("expected multi-kind error, got %v", err)
	}
}

func TestMatcherZeroValueErrors(t *testing.T) {
	var m Matcher
	ok, msg := m.Match("x")
	if ok || msg == "" {
		t.Fatalf("zero matcher must fail, got ok=%v msg=%q", ok, msg)
	}
}
