package runnerterminal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
)

func admittedFixture() *Binding {
	digest := strings.Repeat("a", 64)
	identity := &runneridentity.Identity{Version: runneridentity.Version, CanonicalDigest: digest,
		Run: "run-" + digest, Shard: "shard-" + digest, Task: "task-" + digest,
		Trial: "trial-" + digest, Attempt: "attempt-" + digest}
	return &Binding{
		Selection: runnercontract.Selection{Shard: "only", Task: "task", Trial: 2, Attempt: 3},
		Identity:  identity,
		GraderIDs: []string{"text"},
	}
}

func fixture(outcome Outcome) (Envelope, *Binding) {
	exit, _ := outcome.ExitCode()
	e := Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: Version, Outcome: outcome,
		ExitCode: exit, FailureBoundary: "none", Validation: "valid", Diagnostics: []Diagnostic{},
	}
	if outcome != Success {
		e.Diagnostics = []Diagnostic{{Code: string(outcome), Message: "Synthetic fixture, not runtime evidence."}}
	}
	b := admittedFixture()
	switch outcome {
	case Success, EvaluationFailure:
		e.EngineCalls = 1
		e.Selection, e.Identity = &b.Selection, b.Identity
		status := "passed"
		if outcome == EvaluationFailure {
			status = "failed"
		}
		e.Observation = &Observation{
			ExecutionMode: "mock", EngineCalls: 1, Selection: b.Selection,
			RunNumber: 2, Attempts: 3, Status: status,
			Validations: []Verdict{{Identifier: "text", Passed: outcome == Success}},
		}
	case InfrastructureFailure:
		e.FailureBoundary = "identity"
		b = nil
	case Canceled, Timeout:
		e.FailureBoundary, e.Validation = "input", "not_checked"
		b = nil
	case UnsupportedCapability:
		e.FailureBoundary, e.Validation = "negotiation", "not_checked"
		b = nil
	case InvalidInput:
		e.FailureBoundary, e.Validation = "validation", "invalid"
		b = nil
	}
	return e, b
}

func encodeFixture(t *testing.T, e Envelope, b *Binding) []byte {
	t.Helper()
	data, err := Encode(e, b)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFailureDiagnosticsRemainBoundedAndExplicit(t *testing.T) {
	for _, control := range []struct {
		message string
		marker  string
	}{
		{strings.Repeat("large error ", 1000), "diagnostic truncated"},
		{"invalid error " + string([]byte{0xff}), "invalid UTF-8 replaced"},
	} {
		envelope := NewFailure(InvalidInput, "input", "not_checked", "invalid_input", control.message)
		data, err := Encode(envelope, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(control.marker)) || envelope.Outcome != InvalidInput || envelope.ExitCode != 6 {
			t.Fatalf("diagnostic normalization lost explicit failure: %s", data)
		}
	}
}

func TestOutcomeMappingAndRoundTrip(t *testing.T) {
	for code, outcome := range []Outcome{Success, EvaluationFailure, InfrastructureFailure, Canceled, Timeout, UnsupportedCapability, InvalidInput} {
		t.Run(string(outcome), func(t *testing.T) {
			got, ok := outcome.ExitCode()
			if !ok || got != code {
				t.Fatalf("exit=(%d,%v), want=(%d,true)", got, ok, code)
			}
			e, b := fixture(outcome)
			data := encodeFixture(t, e, b)
			if bytes.Count(data, []byte{'\n'}) != 1 || data[len(data)-1] != '\n' {
				t.Fatalf("not one JSON line: %s", data)
			}
			decoded, err := Validate(data, b)
			if err != nil || !reflect.DeepEqual(*decoded, e) {
				t.Fatalf("round trip: got=%+v err=%v want=%+v", decoded, err, e)
			}
			for wrong := range 7 {
				if wrong == code {
					continue
				}
				e.ExitCode = wrong
				if _, err := Encode(e, b); !errors.Is(err, ErrInvalid) {
					t.Fatalf("accepted outcome %s with exit %d: %v", outcome, wrong, err)
				}
			}
		})
	}
	if code, ok := Outcome("unknown").ExitCode(); ok || code != 2 {
		t.Fatalf("unknown outcome defaults to (%d,%v)", code, ok)
	}
}

func TestContextAndObservationConsistency(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Envelope, **Binding)
	}{
		{"missing binding", func(_ *Envelope, b **Binding) { *b = nil }},
		{"missing identity", func(e *Envelope, _ **Binding) { e.Identity = nil }},
		{"identity partial", func(e *Envelope, _ **Binding) { e.Identity = &runneridentity.Identity{Version: runneridentity.Version} }},
		{"identity wrong algorithm", func(e *Envelope, _ **Binding) {
			e.Identity.Version = "1.0"
		}},
		{"identity wrong prefix", func(e *Envelope, _ **Binding) {
			e.Identity.Run = e.Identity.Task
		}},
		{"identity trailing newline", func(e *Envelope, _ **Binding) {
			e.Identity.Run += "\n"
		}},
		{"different admitted identity", func(_ *Envelope, b **Binding) {
			copy := *(*b).Identity
			copy.CanonicalDigest = strings.Repeat("b", 64)
			(*b).Identity = &copy
		}},
		{"invalid admitted identity", func(_ *Envelope, b **Binding) { (*b).Identity = nil }},
		{"different selection", func(e *Envelope, _ **Binding) {
			s := *e.Selection
			s.Attempt++
			e.Selection = &s
		}},
		{"different observed selection", func(e *Envelope, _ **Binding) { e.Observation.Selection.Task = "adjacent" }},
		{"wrong observed trial", func(e *Envelope, _ **Binding) { e.Observation.RunNumber = 1 }},
		{"wrong selected attempt", func(e *Envelope, _ **Binding) { e.Observation.Attempts = 1 }},
		{"zero engine calls", func(e *Envelope, _ **Binding) { e.EngineCalls = 0 }},
		{"adjacent calls", func(e *Envelope, _ **Binding) { e.Observation.EngineCalls = 2 }},
		{"native execution claim", func(e *Envelope, _ **Binding) { e.Observation.ExecutionMode = "native" }},
		{"missing observation", func(e *Envelope, _ **Binding) { e.Observation = nil }},
		{"skipped observation", func(e *Envelope, _ **Binding) { e.Observation.Status = "skipped" }},
		{"contradictory status", func(e *Envelope, _ **Binding) { e.Observation.Status = "failed" }},
		{"contradictory grader", func(e *Envelope, _ **Binding) { e.Observation.Validations[0].Passed = false }},
		{"missing observed graders", func(e *Envelope, _ **Binding) { e.Observation.Validations = []Verdict{} }},
		{"null observed graders", func(e *Envelope, _ **Binding) { e.Observation.Validations = nil }},
		{"unknown grader", func(e *Envelope, _ **Binding) { e.Observation.Validations[0].Identifier = "invented" }},
		{"duplicate grader", func(e *Envelope, _ **Binding) {
			e.Observation.Validations = append(e.Observation.Validations, e.Observation.Validations[0])
		}},
		{"unavailable declared graders", func(_ *Envelope, b **Binding) { (*b).GraderIDs = nil }},
		{"empty declared grader", func(_ *Envelope, b **Binding) { (*b).GraderIDs = []string{""} }},
		{"duplicate declared graders", func(_ *Envelope, b **Binding) { (*b).GraderIDs = []string{"text", "text"} }},
		{"error on passed observation", func(e *Envelope, _ **Binding) { e.Observation.ErrorMsg = "shutdown failed" }},
		{"success diagnostic", func(e *Envelope, _ **Binding) { e.Diagnostics = []Diagnostic{{Code: "error", Message: "failure"}} }},
		{"unvalidated success", func(e *Envelope, _ **Binding) { e.Validation = "not_checked" }},
		{"success at startup", func(e *Envelope, _ **Binding) { e.FailureBoundary = "startup" }},
		{"admitted input boundary", func(e *Envelope, _ **Binding) {
			e.Outcome, e.ExitCode, e.FailureBoundary = Canceled, 3, "input"
			e.Diagnostics = []Diagnostic{{Code: "canceled", Message: "Cancellation observed."}}
			e.Observation, e.EngineCalls = nil, 0
		}},
		{"execution before startup", func(e *Envelope, _ **Binding) {
			e.Outcome, e.ExitCode, e.FailureBoundary = InfrastructureFailure, 2, "startup"
			e.Diagnostics = []Diagnostic{{Code: "startup", Message: "Startup failed."}}
			e.Observation = nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, b := fixture(Success)
			encodeFixture(t, e, b)
			tt.mutate(&e, &b)
			if _, err := Encode(e, b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("mutation accepted: %v", err)
			}
		})
	}
}

func TestAlternativeValidEvidence(t *testing.T) {
	for _, boundary := range []string{"input", "negotiation", "validation", "output", "terminalization"} {
		t.Run("infrastructure failure at "+boundary, func(t *testing.T) {
			e, _ := fixture(InfrastructureFailure)
			e.FailureBoundary, e.Validation = boundary, "not_checked"
			encodeFixture(t, e, nil)
		})
	}
	t.Run("explicit zero configured graders", func(t *testing.T) {
		e, b := fixture(Success)
		b.GraderIDs, e.Observation.Validations = []string{}, []Verdict{}
		encodeFixture(t, e, b)
	})
	t.Run("independent result version", func(t *testing.T) {
		e, b := fixture(Success)
		data := encodeFixture(t, e, b)
		if !bytes.Contains(data, []byte(`"schema_version":"1.0"`)) ||
			!bytes.Contains(data, []byte(`"version":"sha256-waza-c14n-v1"`)) {
			t.Fatalf("version domains lost: %s", data)
		}
	})
	t.Run("grader order does not change set", func(t *testing.T) {
		e, b := fixture(Success)
		b.GraderIDs = []string{"second", "text"}
		e.Observation.Validations = append(e.Observation.Validations, Verdict{Identifier: "second", Passed: true})
		encodeFixture(t, e, b)
	})
	for _, outcome := range []Outcome{InfrastructureFailure, Canceled, Timeout} {
		t.Run(string(outcome)+" retains completed evidence at shutdown", func(t *testing.T) {
			e, b := fixture(Success)
			e.Outcome = outcome
			e.ExitCode, _ = outcome.ExitCode()
			e.FailureBoundary = "shutdown"
			e.Diagnostics = []Diagnostic{{Code: "shutdown", Message: "Shutdown did not complete."}}
			data := encodeFixture(t, e, b)
			if !bytes.Contains(data, []byte(`"status":"passed"`)) {
				t.Fatalf("recorded observation was rewritten: %s", data)
			}
		})
		t.Run(string(outcome)+" retains admitted IDs without observation", func(t *testing.T) {
			e, b := fixture(Success)
			e.Outcome, e.FailureBoundary, e.Observation, e.EngineCalls = outcome, "startup", nil, 0
			e.ExitCode, _ = outcome.ExitCode()
			e.Diagnostics = []Diagnostic{{Code: "startup", Message: "Runtime did not start."}}
			encodeFixture(t, e, b)
		})
		t.Run(string(outcome)+" retains partial error observation", func(t *testing.T) {
			e, b := fixture(Success)
			e.Outcome, e.FailureBoundary = outcome, "execution"
			e.ExitCode, _ = outcome.ExitCode()
			e.Diagnostics = []Diagnostic{{Code: "runtime", Message: "Runtime failed."}}
			e.Observation.Status, e.Observation.ErrorMsg = "error", "Runtime failed."
			e.Observation.Validations = []Verdict{}
			encodeFixture(t, e, b)
			e.Observation.ErrorMsg = ""
			if _, err := Encode(e, b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error observation without error accepted: %v", err)
			}
		})
	}
}

func TestUnadmittedPathsCannotInventEvidence(t *testing.T) {
	for _, outcome := range []Outcome{InfrastructureFailure, Canceled, Timeout, UnsupportedCapability, InvalidInput} {
		t.Run(string(outcome), func(t *testing.T) {
			e, _ := fixture(outcome)
			encodeFixture(t, e, nil)
			b := admittedFixture()
			e.Selection, e.Identity = &b.Selection, b.Identity
			if _, err := Encode(e, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unadmitted identity accepted: %v", err)
			}
			e.Selection, e.Identity, e.EngineCalls = nil, nil, 1
			if _, err := Encode(e, nil); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unadmitted engine call accepted: %v", err)
			}
		})
	}
}

func TestStrictOriginalWire(t *testing.T) {
	e, b := fixture(Success)
	valid := encodeFixture(t, e, b)
	tests := map[string][]byte{
		"unknown field":       bytes.Replace(valid, []byte(`"kind":`), []byte(`"extra":1,"kind":`), 1),
		"duplicate key":       bytes.Replace(valid, []byte(`"exit_code":0`), []byte(`"exit_code":6,"exit_code":0`), 1),
		"escaped duplicate":   bytes.Replace(valid, []byte(`"exit_code":0`), []byte(`"exit_code":6,"exit_\u0063ode":0`), 1),
		"nested duplicate":    bytes.Replace(valid, []byte(`"passed":true`), []byte(`"passed":false,"passed":true`), 1),
		"missing zero":        bytes.Replace(valid, []byte(`"exit_code":0,`), nil, 1),
		"null zero":           bytes.Replace(valid, []byte(`"exit_code":0`), []byte(`"exit_code":null`), 1),
		"string number":       bytes.Replace(valid, []byte(`"exit_code":0`), []byte(`"exit_code":"0"`), 1),
		"float number":        bytes.Replace(valid, []byte(`"exit_code":0`), []byte(`"exit_code":0.0`), 1),
		"numeric precision":   bytes.Replace(valid, []byte(`"trial":2`), []byte(`"trial":9007199254740993`), 1),
		"trailing value":      append(bytes.Clone(valid), []byte(`{}`)...),
		"lone surrogate":      bytes.Replace(valid, []byte(`"task":"task"`), []byte(`"task":"\ud800"`), 1),
		"invalid UTF-8":       append(bytes.Clone(valid), 0xff),
		"null diagnostics":    bytes.Replace(valid, []byte(`"diagnostics":[]`), []byte(`"diagnostics":null`), 1),
		"missing validations": bytes.Replace(valid, []byte(`"validations":[{"identifier":"text","passed":true}]`), []byte(`"validations":[]`), 1),
		"missing verdict":     bytes.Replace(valid, []byte(`,"passed":true`), nil, 1),
		"null verdict":        bytes.Replace(valid, []byte(`"passed":true`), []byte(`"passed":null`), 1),
		"case alias":          bytes.Replace(valid, []byte(`"passed":true`), []byte(`"passed":false,"Passed":true`), 1),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(data, valid) {
				t.Fatal("mutation did not change original wire")
			}
			if _, err := Validate(data, b); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid original wire accepted: %v", err)
			}
		})
	}
	for _, ending := range []string{"\n", "\r\n", " \t"} {
		if _, err := Validate(append(bytes.TrimSpace(valid), []byte(ending)...), b); err != nil {
			t.Fatalf("valid whitespace %q rejected: %v", ending, err)
		}
	}
}

func TestBoundsBeforeSerialization(t *testing.T) {
	e, _ := fixture(InvalidInput)
	e.Diagnostics[0].Message = strings.Repeat("a", MaxMessageBytes)
	encodeFixture(t, e, nil)
	e.Diagnostics[0].Message += "a"
	if _, err := Encode(e, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("message limit: %v", err)
	}
	e.Diagnostics[0].Message = strings.Repeat("é", MaxMessageBytes/2)
	encodeFixture(t, e, nil)
	e.Diagnostics[0].Message += "é"
	if _, err := Encode(e, nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("UTF-8 byte limit: %v", err)
	}
	for _, mutate := range []func(*Envelope, *Binding){
		func(e *Envelope, _ *Binding) { e.Diagnostics = make([]Diagnostic, MaxDiagnostics+1) },
		func(e *Envelope, _ *Binding) { e.Identity.Run = strings.Repeat("a", MaxIdentityBytes+1) },
		func(e *Envelope, _ *Binding) { e.Kind = strings.Repeat("a", 65) },
		func(e *Envelope, _ *Binding) {
			e.Diagnostics = []Diagnostic{{Code: strings.Repeat("a", 65), Message: "error"}}
		},
		func(e *Envelope, _ *Binding) { e.Selection.Task = strings.Repeat("a", MaxIdentifierBytes+1) },
		func(e *Envelope, _ *Binding) { e.Observation.ErrorMsg = strings.Repeat("a", MaxMessageBytes+1) },
		func(e *Envelope, _ *Binding) { e.Observation.ExecutionMode = strings.Repeat("a", 65) },
		func(e *Envelope, _ *Binding) { e.Observation.Validations = make([]Verdict, MaxGraders+1) },
		func(e *Envelope, _ *Binding) {
			e.Observation.Validations[0].Identifier = strings.Repeat("a", MaxIdentifierBytes+1)
		},
		func(_ *Envelope, b *Binding) {
			b.Identity = &runneridentity.Identity{Run: strings.Repeat("a", MaxIdentityBytes+1)}
		},
		func(_ *Envelope, b *Binding) { b.Selection.Shard = strings.Repeat("a", MaxIdentifierBytes+1) },
		func(_ *Envelope, b *Binding) { b.GraderIDs = make([]string, MaxGraders+1) },
		func(_ *Envelope, b *Binding) { b.GraderIDs[0] = strings.Repeat("a", MaxIdentifierBytes+1) },
	} {
		e, b := fixture(Success)
		mutate(&e, b)
		if _, err := Encode(e, b); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("oversize typed input accepted: %v", err)
		}
	}
	e, b := fixture(Success)
	e.Observation.Status = string([]byte{0xff})
	if _, err := Encode(e, b); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 typed input repaired: %v", err)
	}
	if _, err := Validate(bytes.Repeat([]byte(" "), MaxEnvelopeBytes+1), nil); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("wire bound: %v", err)
	}
}

func TestExactCollectionBounds(t *testing.T) {
	e, _ := fixture(InvalidInput)
	for len(e.Diagnostics) < MaxDiagnostics {
		e.Diagnostics = append(e.Diagnostics, Diagnostic{Code: "invalid_input", Message: "failure"})
	}
	encodeFixture(t, e, nil)
	e, b := fixture(Success)
	b.GraderIDs, e.Observation.Validations = []string{}, []Verdict{}
	for i := range MaxGraders {
		id := fmt.Sprintf("g%d", i)
		b.GraderIDs = append(b.GraderIDs, id)
		e.Observation.Validations = append(e.Observation.Validations, Verdict{Identifier: id, Passed: true})
	}
	encodeFixture(t, e, b)
}

func TestOfflineSchemaCompilation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	for _, reference := range []string{server.URL + "/schema", "file:///terminal-sentinel-do-not-open.json"} {
		_, err := compileSchema(fmt.Sprintf(`{"$ref":%q}`, reference))
		invalid, external := schemaloader.ClassifyError(err)
		if err == nil || invalid || !external {
			t.Fatalf("reference %q did not fail offline: %v", reference, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("static schema validation accessed network")
	}
	for _, text := range []string{"{", `{"type":"invalid"}`} {
		if _, err := compileSchema(text); err == nil {
			t.Fatalf("invalid schema accepted: %s", text)
		}
	}
	if _, err := compileSchema(schemas.RunnerTerminalSchemaJSON); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedPreparationExamples(t *testing.T) {
	data, err := os.ReadFile("testdata/envelopes.json")
	if err != nil {
		t.Fatal(err)
	}
	var examples []json.RawMessage
	if err := json.Unmarshal(data, &examples); err != nil {
		t.Fatal(err)
	}
	if len(examples) != 7 {
		t.Fatalf("want all seven outcome examples, got %d", len(examples))
	}
	seen := map[Outcome]bool{}
	for _, example := range examples {
		var raw Envelope
		if err := json.Unmarshal(example, &raw); err != nil {
			t.Fatal(err)
		}
		_, expected := fixture(raw.Outcome)
		e, err := Validate(example, expected)
		if err != nil {
			t.Fatalf("example %s: %v", raw.Outcome, err)
		}
		if seen[e.Outcome] {
			t.Fatalf("duplicate example for %s", e.Outcome)
		}
		seen[e.Outcome] = true
		reencoded := encodeFixture(t, *e, expected)
		if _, err := Validate(reencoded, expected); err != nil {
			t.Fatalf("example %s changed validity after serialization: %v", e.Outcome, err)
		}
	}
}
