package executionattempt

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("frozen fixture requires final LF")
	}
	return data[:len(data)-1]
}

func sample(t *testing.T) Attempt {
	t.Helper()
	got, err := ParseAttempt(fixture(t, "attempt"))
	if err != nil {
		t.Fatal(err)
	}
	return *got
}

func wire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFrozenCodecs(t *testing.T) {
	a := sample(t)
	got, err := EncodeAttempt(a)
	if err != nil || !bytes.Equal(got, fixture(t, "attempt")) {
		t.Fatalf("attempt differs from frozen bytes: %s, %v", got, err)
	}
	d, err := ParseDecision(fixture(t, "decision"))
	if err != nil {
		t.Fatal(err)
	}
	got, err = EncodeDecision(*d)
	if err != nil || !bytes.Equal(got, fixture(t, "decision")) {
		t.Fatalf("decision differs from frozen bytes: %s, %v", got, err)
	}
	if _, err := ParseAttempt(fixture(t, "decision")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("attempt accepted decision: %v", err)
	}
	if _, err := ParseDecision(fixture(t, "attempt")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("decision accepted attempt: %v", err)
	}
	for _, name := range []string{"attempt", "decision"} {
		t.Run(name+"/whitespace", func(t *testing.T) {
			data := append([]byte(" \r\n"), fixture(t, name)...)
			data = append(data, '\r', '\n')
			if name == "attempt" {
				if _, err := ParseAttempt(data); err != nil {
					t.Fatal(err)
				}
			} else if _, err := ParseDecision(data); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOriginalWirePresenceAndTypes(t *testing.T) {
	for _, name := range []string{"attempt", "decision"} {
		t.Run(name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal(fixture(t, name), &root); err != nil {
				t.Fatal(err)
			}
			reject := func(t *testing.T, data []byte) {
				t.Helper()
				var err error
				if name == "attempt" {
					var got *Attempt
					got, err = ParseAttempt(data)
					if got != nil {
						t.Fatal("invalid attempt returned data")
					}
				} else {
					var got *Decision
					got, err = ParseDecision(data)
					if got != nil {
						t.Fatal("invalid decision returned data")
					}
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("malformed %s accepted or error cause lost: %v", name, err)
				}
			}
			object := func(value any) map[string]any {
				t.Helper()
				obj, ok := value.(map[string]any)
				if !ok {
					t.Fatalf("fixture object is %T", value)
				}
				return obj
			}
			binding := object(root["binding"])
			scopes := []map[string]any{root, object(root["logical"]), binding, object(binding["identity"]), object(binding["selection"])}
			if name == "decision" {
				scopes = append(scopes, object(root["source"]))
			} else {
				root["parent"] = map[string]any{"execution_id": "exec-" + strings.Repeat("b", 64), "relationship": "retry"}
				scopes = append(scopes, object(root["parent"]))
				if _, err := ParseAttempt(wire(t, root)); err != nil {
					t.Fatalf("valid parent setup: %v", err)
				}
			}
			for index, scope := range scopes {
				t.Run(fmt.Sprintf("scope_%d", index), func(t *testing.T) {
					scope["unknown"] = false
					reject(t, wire(t, root))
					delete(scope, "unknown")
					for key, original := range scope {
						t.Run(key, func(t *testing.T) {
							// Parent itself is optional; explicit null is not omission.
							if key != "parent" {
								delete(scope, key)
								reject(t, wire(t, root))
								scope[key] = original
							}
							for _, wrong := range []any{nil, false, float64(0), "", []any{}, map[string]any{}} {
								if reflect.TypeOf(wrong) == reflect.TypeOf(original) {
									continue
								}
								scope[key] = wrong
								reject(t, wire(t, root))
								scope[key] = original
							}
						})
					}
				})
			}
			base := string(fixture(t, name))
			for _, change := range []struct{ old, replacement string }{
				{`"kind":`, `"k\u0069nd":"wrong","kind":`},
				{`"trial":1`, `"trial":1.000000000000000000000000000000000001`},
				{`"attempt":2`, `"attempt":9223372036854775808`},
				{`"attempt":2`, `"attempt":0`},
				{`"task":"task"`, `"task":"\ud800"`},
				{`"task":"task"`, "\"task\":\"\xff\""},
				{`"grader_ids":[]`, `"grader_ids":null`},
				{`"grader_ids":[]`, `"grader_ids":["a","a"]`},
				{`"grader_ids":[]`, `"grader_ids":[""]`},
			} {
				if !strings.Contains(base, change.old) {
					t.Fatalf("mutation does not match fixture: %s", change.old)
				}
				reject(t, []byte(strings.Replace(base, change.old, change.replacement, 1)))
			}
			for _, data := range []string{"", "null", "[]", base + "{}", base[:len(base)-1], strings.Repeat(" ", MaxSidecarBytes+1)} {
				reject(t, []byte(data))
			}
		})
	}
}

func TestAlternativeValidAndInvalidTypedInputs(t *testing.T) {
	a := sample(t)
	a.Binding.GraderIDs = []string{"z", "a", "é"}
	a.Binding.Selection.Task = "é<&>"
	a.Parent = &Parent{ExecutionID: "exec-" + strings.Repeat("b", 64), Relationship: "branch"}
	before := wire(t, a)
	data, err := EncodeAttempt(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAttempt(data)
	if err != nil || !slices.Equal(parsed.Binding.GraderIDs, []string{"a", "z", "é"}) || parsed.Binding.Selection.Task != "é<&>" {
		t.Fatalf("alternative valid Unicode/graders/branch: %+v, %v", parsed, err)
	}
	if !bytes.Equal(before, wire(t, a)) {
		t.Fatal("encoder mutated input")
	}
	for _, mutate := range []func(*Attempt){
		func(a *Attempt) { a.Logical.Run = "run-" + strings.Repeat("f", 64) },
		func(a *Attempt) { a.Logical.Work = "trial-" + strings.Repeat("f", 64) },
		func(a *Attempt) { a.Parent.ExecutionID = a.ExecutionID },
		func(a *Attempt) { a.Parent.Relationship = "next" },
		func(a *Attempt) { a.Binding.Selection.Task = strings.Repeat("é", 129) },
		func(a *Attempt) { a.Binding.Selection.Task = "\xff" },
		func(a *Attempt) { a.Binding.GraderIDs = []string{strings.Repeat("é", 129)} },
		func(a *Attempt) { a.Binding.GraderIDs = []string{"\xff"} },
		func(a *Attempt) { a.Binding.GraderIDs = nil },
		func(a *Attempt) { a.ExecutionID += "\n" },
		func(a *Attempt) { a.ExecutionID = strings.ToUpper(a.ExecutionID) },
	} {
		value, err := ParseAttempt(data)
		if err != nil {
			t.Fatal(err)
		}
		mutate(value)
		if got, err := EncodeAttempt(*value); !errors.Is(err, ErrInvalid) || got != nil {
			t.Fatalf("invalid typed attempt accepted: %s, %v", got, err)
		}
	}
}

func TestDecisionPolicies(t *testing.T) {
	base, err := ParseDecision(fixture(t, "decision"))
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"reused", "unfinished", "conflict"} {
		value := *base
		value.Outcome = outcome
		before := wire(t, value)
		data, err := EncodeDecision(value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseDecision(data)
		if err != nil || got.Outcome != outcome || *got.Source != *base.Source || got.ExecutionID != "" {
			t.Fatalf("source decision changed: %+v, %v", got, err)
		}
		if !bytes.Equal(before, wire(t, value)) {
			t.Fatal("decision encoder mutated input")
		}
	}
	distinct := *base
	distinct.Policy, distinct.Outcome, distinct.Source = "distinct", "distinct", nil
	distinct.ExecutionID = sample(t).ExecutionID
	if _, err := EncodeDecision(distinct); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Decision){
		func(d *Decision) { d.Source = nil },
		func(d *Decision) { d.Policy = "distinct" },
		func(d *Decision) { d.Outcome = "distinct" },
		func(d *Decision) { d.ExecutionID = sample(t).ExecutionID },
		func(d *Decision) { d.Source.ManifestDigest = "" },
		func(d *Decision) { d.Source.ManifestDigest += "\n" },
		func(d *Decision) { d.Logical.Work = "trial-" + strings.Repeat("f", 64) },
		func(d *Decision) { d.Binding.Selection.Task = "\xff" },
	} {
		value, err := ParseDecision(fixture(t, "decision"))
		if err != nil {
			t.Fatal(err)
		}
		mutate(value)
		if data, err := EncodeDecision(*value); !errors.Is(err, ErrInvalid) || data != nil {
			t.Fatalf("invalid decision accepted: %s, %v", data, err)
		}
	}
	distinct.ExecutionID = ""
	if _, err := EncodeDecision(distinct); !errors.Is(err, ErrInvalid) {
		t.Fatalf("distinct decision lacks execution ID: %v", err)
	}
}

func TestLineage(t *testing.T) {
	root := sample(t)
	child := sample(t)
	child.ExecutionID = "exec-" + strings.Repeat("b", 64)
	child.Parent = &Parent{ExecutionID: root.ExecutionID, Relationship: "retry"}
	branch := sample(t)
	branch.ExecutionID = "exec-" + strings.Repeat("c", 64)
	branch.Parent = &Parent{ExecutionID: root.ExecutionID, Relationship: "branch"}
	for _, attempts := range [][]Attempt{{root}, {branch, child, root}, {child, root, branch}} {
		if err := ValidateLineage(attempts); err != nil {
			t.Fatalf("valid unordered lineage: %v", err)
		}
		independent := branch
		independent.Parent = nil
		if err := ValidateLineage([]Attempt{root, independent}); err != nil {
			t.Fatalf("independent roots with identical declared ordinal: %v", err)
		}
	}
	cycle := root
	cycle.Parent = &Parent{ExecutionID: child.ExecutionID, Relationship: "retry"}
	other := sample(t)
	other.Logical.Work = "trial-" + strings.Repeat("f", 64)
	other.Binding.Identity.Trial = other.Logical.Work
	for _, attempts := range [][]Attempt{nil, {root, root}, {child}, {cycle, child}, {other, child}, make([]Attempt, MaxLineage+1)} {
		if err := ValidateLineage(attempts); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid lineage accepted: %v", err)
		}
	}
}

func TestOpaqueIDGeneration(t *testing.T) {
	seen := make(map[string]bool)
	for range 32 {
		id, err := NewExecutionID()
		if err != nil || seen[id] {
			t.Fatalf("identifier error/collision in sample: %s, %v", id, err)
		}
		seen[id] = true
		a := sample(t)
		a.ExecutionID = id
		if _, err := EncodeAttempt(a); err != nil {
			t.Fatal(err)
		}
	}
	id, err := NewDecisionID()
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDecision(fixture(t, "decision"))
	if err != nil {
		t.Fatal(err)
	}
	d.DecisionID = id
	if _, err := EncodeDecision(*d); err != nil {
		t.Fatal(err)
	}
	got, err := newID("exec-", bytes.NewReader(bytes.Repeat([]byte{0xab}, 32)))
	if err != nil || got != "exec-"+strings.Repeat("ab", 32) {
		t.Fatalf("deterministic entropy: %s, %v", got, err)
	}
	if got, err := newID("exec-", strings.NewReader("short")); !errors.Is(err, io.ErrUnexpectedEOF) || got != "" {
		t.Fatalf("entropy failure lost: %s, %v", got, err)
	}
}

func TestRealIdentityAndOriginalTerminalOrdinal(t *testing.T) {
	data, err := os.ReadFile("../runnercontract/testdata/request.json")
	if err != nil {
		t.Fatal(err)
	}
	request, err := runnercontract.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runneridentity.Derive(request)
	if err != nil {
		t.Fatal(err)
	}
	a := sample(t)
	a.Binding = artifactmanifest.Binding{Identity: *first, Selection: request.Selection, ExecutionMode: "mock", GraderIDs: []string{}}
	a.Logical = LogicalFrom(*first)
	request.Selection.Attempt++
	second, err := runneridentity.Derive(request)
	if err != nil {
		t.Fatal(err)
	}
	if LogicalFrom(*first) != LogicalFrom(*second) || first.Attempt == second.Attempt || first.CanonicalDigest == second.CanonicalDigest {
		t.Fatal("real native identity must preserve logical trial across declared ordinals")
	}
	b := a
	b.ExecutionID = "exec-" + strings.Repeat("b", 64)
	b.Binding.Identity, b.Binding.Selection = *second, request.Selection
	b.Parent = &Parent{ExecutionID: a.ExecutionID, Relationship: "retry"}
	if err := ValidateLineage([]Attempt{b, a}); err != nil {
		t.Fatal(err)
	}
	e := runnerterminal.Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: runnerterminal.Version,
		Outcome: runnerterminal.Success, ExitCode: 0, FailureBoundary: "none", Validation: "valid", EngineCalls: 1,
		Selection: &a.Binding.Selection, Identity: &a.Binding.Identity, Diagnostics: []runnerterminal.Diagnostic{},
		Observation: &runnerterminal.Observation{
			ExecutionMode: "mock", EngineCalls: 1, Selection: a.Binding.Selection,
			RunNumber: a.Binding.Selection.Trial, Attempts: a.Binding.Selection.Attempt,
			Status: "passed", Validations: []runnerterminal.Verdict{},
		},
	}
	terminal, err := runnerterminal.Encode(e, a.Binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runnerterminal.Validate(terminal, b.Binding.TerminalBinding()); !errors.Is(err, runnerterminal.ErrInvalid) {
		t.Fatalf("original terminal retagged for later declared ordinal: %v", err)
	}
	e.Observation.Attempts++
	if _, err := runnerterminal.Encode(e, a.Binding.TerminalBinding()); !errors.Is(err, runnerterminal.ErrInvalid) {
		t.Fatalf("native observation ordinal cross-check lost: %v", err)
	}
}
