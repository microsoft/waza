package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/models"
	"github.com/stretchr/testify/require"
)

func TestShaBytesDeterministic(t *testing.T) {
	a := shaBytes([]byte("hello"))
	b := shaBytes([]byte("hello"))
	require.Equal(t, a, b)
	require.NotEqual(t, a, shaBytes([]byte("hellp")))
	require.Len(t, a, 64)
}

func TestWriterRoot(t *testing.T) {
	require.Equal(t, "", (*Writer)(nil).Root())
	w := NewWriter("/tmp/snap")
	require.Equal(t, "/tmp/snap", w.Root())
}

func TestLoadPolicyExtendAndReplace(t *testing.T) {
	dir := t.TempDir()

	extend := filepath.Join(dir, "extend.yaml")
	require.NoError(t, os.WriteFile(extend, []byte(`extend: true
rules:
  - name: custom_token
    pattern: "CUSTOM-[A-Z0-9]{8}"
envKeyDenyList:
  - CUSTOM_SECRET
`), 0o644))

	p, err := LoadPolicy(extend)
	require.NoError(t, err)
	require.Equal(t, "default+custom", p.Label())
	require.Contains(t, p.RedactString("token=CUSTOM-ABCD1234 end"), RedactionPlaceholder)
	require.True(t, p.IsSensitiveKey("CUSTOM_SECRET_X"))

	replace := filepath.Join(dir, "replace.yaml")
	require.NoError(t, os.WriteFile(replace, []byte(`rules:
  - name: only_one
    pattern: "ONLY-[0-9]+"
envKeyDenyList:
  - MINE
`), 0o644))
	p2, err := LoadPolicy(replace)
	require.NoError(t, err)
	require.Equal(t, "custom", p2.Label())
	require.Equal(t, "[REDACTED]", p2.RedactString("ONLY-12345"))
	// Default rules are not present in replace mode.
	require.Equal(t, "AKIAIOSFODNN7EXAMPLE", p2.RedactString("AKIAIOSFODNN7EXAMPLE"))
	require.True(t, p2.IsSensitiveKey("MINE_X"))
	require.False(t, p2.IsSensitiveKey("TOKEN"))
}

func TestLoadPolicyErrors(t *testing.T) {
	_, err := LoadPolicy(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("not: [valid: yaml"), 0o644))
	_, err = LoadPolicy(bad)
	require.Error(t, err)

	badRule := filepath.Join(t.TempDir(), "rule.yaml")
	require.NoError(t, os.WriteFile(badRule, []byte(`rules:
  - name: bad
    pattern: "["
`), 0o644))
	_, err = LoadPolicy(badRule)
	require.Error(t, err)
}

func TestRedactStringMapAndMatchedRules(t *testing.T) {
	p := DefaultPolicy()
	require.Empty(t, p.MatchedRules())

	out := p.RedactStringMap(map[string]string{
		"GITHUB_TOKEN": "ghp_" + strings.Repeat("a", 36),
		"NOTES":        "contact foo@example.com please",
	})
	require.Equal(t, RedactionPlaceholder, out["GITHUB_TOKEN"])
	require.NotContains(t, out["NOTES"], "@example.com")

	rules := p.MatchedRules()
	require.NotEmpty(t, rules)
	require.Contains(t, rules, "email")

	// nil receiver and empty input fast-paths return the input unchanged
	in := map[string]string{"a": "b"}
	require.Equal(t, in, (*Policy)(nil).RedactStringMap(in))
	require.Equal(t, map[string]string(nil), p.RedactStringMap(nil))

	// ResetCounters wipes match state.
	p.ResetCounters()
	require.Empty(t, p.MatchedRules())
	require.Zero(t, p.MatchCount())

	// Pointer/struct types fall through unchanged.
	require.Equal(t, 42, p.RedactAny(42))
}

func TestRedactAnyWalksAllJSONShapes(t *testing.T) {
	p := DefaultPolicy()
	in := map[string]any{
		"TOKEN":   "ghp_" + strings.Repeat("b", 36),
		"emails":  []any{"a@b.com", "c@d.com"},
		"strings": []string{"hi", "x@y.com"},
		"meta":    map[string]string{"AUTH_TOKEN": "abc", "ok": "fine"},
		"nested":  map[string]any{"NESTED_KEY": "ghp_" + strings.Repeat("c", 36)},
		"raw":     "no secrets here",
	}
	outAny := p.RedactAny(in)
	out, ok := outAny.(map[string]any)
	require.True(t, ok)
	require.Equal(t, RedactionPlaceholder, out["TOKEN"])
	meta, ok := out["meta"].(map[string]string)
	require.True(t, ok)
	require.Equal(t, RedactionPlaceholder, meta["AUTH_TOKEN"])
	nested, ok := out["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, RedactionPlaceholder, nested["NESTED_KEY"])
	emails, ok := out["emails"].([]any)
	require.True(t, ok)
	email0, ok := emails[0].(string)
	require.True(t, ok)
	require.NotContains(t, email0, "@")
	strs, ok := out["strings"].([]string)
	require.True(t, ok)
	require.NotContains(t, strs[1], "@")

	// nil receiver passes through.
	require.Equal(t, in, (*Policy)(nil).RedactAny(in))
}

func TestSummariseDivergencesAllShapes(t *testing.T) {
	require.Equal(t, "snapshots match", SummariseDivergences(CompareResult{Match: true}))
	require.Equal(t, "snapshots do not match (no divergence details)", SummariseDivergences(CompareResult{}))

	out := SummariseDivergences(CompareResult{Divergences: []Divergence{{
		Kind:                DivToolName,
		FirstDivergentIndex: 2,
		BaselineValue:       "old",
		ReplayedValue:       "new",
		Description:         "tool name diverges at index 2",
	}}})
	require.Contains(t, out, "first divergence: tool_name")
	require.Contains(t, out, "event #3")
	require.Contains(t, out, "old")
	require.Contains(t, out, "new")

	short := SummariseDivergences(CompareResult{Divergences: []Divergence{{
		Kind:                DivLengthMismatch,
		FirstDivergentIndex: -1,
	}}})
	require.Contains(t, short, "first divergence: length_mismatch")
}

func TestCompareStrictPathsAndBisect(t *testing.T) {
	a := &Snapshot{ToolEvents: []models.ToolEvent{
		{Turn: 1, ToolName: "list", Args: map[string]any{"k": 1.0}, Success: true, Result: "ok"},
		{Turn: 2, ToolName: "get", Args: map[string]any{"id": 1}, Success: true, Result: "ok"},
	}}

	// Identical → match
	res := Compare(a, a, true)
	require.True(t, res.Match)
	turn, _ := Bisect(a, a)
	require.Zero(t, turn)

	// tool name divergence at index 1 (args must match at 0)
	b := &Snapshot{ToolEvents: []models.ToolEvent{
		{Turn: 1, ToolName: "list", Args: map[string]any{"k": 1.0}},
		{Turn: 2, ToolName: "WRONG", Args: map[string]any{"id": 1}},
	}}
	res = Compare(a, b, false)
	require.False(t, res.Match)
	require.Equal(t, DivToolName, res.Divergences[0].Kind)
	turn, _ = Bisect(a, b)
	require.Equal(t, 2, turn)

	// args divergence (canonicalEqual ignores numeric kinding)
	c := &Snapshot{ToolEvents: []models.ToolEvent{
		{Turn: 1, ToolName: "list", Args: map[string]any{"k": 2.0}},
		{Turn: 2, ToolName: "get", Args: map[string]any{"id": 1}},
	}}
	res = Compare(a, c, false)
	require.False(t, res.Match)
	require.Equal(t, DivToolArgs, res.Divergences[0].Kind)

	// Strict mode: success divergence
	d := &Snapshot{ToolEvents: []models.ToolEvent{
		{Turn: 1, ToolName: "list", Args: map[string]any{"k": 1.0}, Success: false},
	}}
	short := &Snapshot{ToolEvents: a.ToolEvents[:1]}
	res = Compare(short, d, true)
	require.False(t, res.Match)
	require.Equal(t, DivToolSuccess, res.Divergences[0].Kind)

	// Strict mode: result divergence
	e := &Snapshot{ToolEvents: []models.ToolEvent{
		{Turn: 1, ToolName: "list", Args: map[string]any{"k": 1.0}, Success: true, Result: "different"},
	}}
	res = Compare(short, e, true)
	require.False(t, res.Match)
	require.Equal(t, DivToolResult, res.Divergences[0].Kind)

	// length mismatch when prefix matches
	f := &Snapshot{ToolEvents: a.ToolEvents[:1]}
	res = Compare(a, f, false)
	require.False(t, res.Match)
	require.Equal(t, DivLengthMismatch, res.Divergences[0].Kind)

	// nil snapshot
	res = Compare(nil, a, false)
	require.False(t, res.Match)
	require.Equal(t, DivLengthMismatch, res.Divergences[0].Kind)

	// Final status divergence
	g := &Snapshot{
		ToolEvents: a.ToolEvents,
		Result:     SnapshotResult{Status: models.StatusPassed, Validations: map[string]models.GraderResults{"x": {Passed: true}}},
	}
	h := &Snapshot{
		ToolEvents: a.ToolEvents,
		Result: SnapshotResult{
			Status:      models.StatusFailed,
			Validations: map[string]models.GraderResults{"x": {Passed: false}},
		},
	}
	res = Compare(g, h, true)
	require.False(t, res.Match)
	hasStatus, hasVal := false, false
	for _, d := range res.Divergences {
		if d.Kind == DivFinalStatus {
			hasStatus = true
		}
		if d.Kind == DivValidation {
			hasVal = true
		}
	}
	require.True(t, hasStatus)
	require.True(t, hasVal)

	// Validation missing in replay
	miss := &Snapshot{
		ToolEvents: a.ToolEvents,
		Result:     SnapshotResult{Status: models.StatusPassed, Validations: map[string]models.GraderResults{}},
	}
	res = Compare(g, miss, true)
	require.False(t, res.Match)
}
