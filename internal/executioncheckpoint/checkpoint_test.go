package executioncheckpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/execution"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/runnerunit"
)

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func encodeJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFrozenCodec(t *testing.T) {
	data := readFixture(t, "testdata/before-unit.json")
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("frozen oracle must retain final LF")
	}
	data = data[:len(data)-1]
	checkpoint, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := Encode(*checkpoint)
	if err != nil || !bytes.Equal(canonical, data) {
		t.Fatalf("frozen canonical bytes differ: %s, %v", canonical, err)
	}
	alternate := append(append([]byte(" \r\n"), data...), '\r', '\n')
	if _, err := Parse(alternate); err != nil {
		t.Fatalf("alternate JSON whitespace is valid: %v", err)
	}
}

func TestStrictWireAndBoundaryContents(t *testing.T) {
	data := readFixture(t, "testdata/before-unit.json")
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	reject := func(data []byte) {
		t.Helper()
		value, err := Parse(data)
		if value != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid checkpoint accepted or cause lost: %+v, %v", value, err)
		}
	}
	compatibility, ok := root["compatibility"].(map[string]any)
	if !ok {
		t.Fatal("fixture has no compatibility object")
	}
	for _, object := range []map[string]any{root, compatibility} {
		for key, original := range object {
			t.Run(key, func(t *testing.T) {
				delete(object, key)
				reject(encodeJSON(t, root))
				for _, bad := range []any{nil, "", 0, false, []any{}, map[string]any{}} {
					object[key] = bad
					reject(encodeJSON(t, root))
				}
				object[key] = original
			})
		}
		object["unknown"] = true
		reject(encodeJSON(t, root))
		delete(object, "unknown")
	}
	for _, timestamp := range []string{
		"0000-01-01T00:00:00Z", "2026-02-30T00:00:00Z", "2026-11-07T24:00:00Z",
		"2026-11-07T16:00:00+00:00", "2026-11-07T16:00:00.000Z",
	} {
		root["created_at"] = timestamp
		reject(encodeJSON(t, root))
	}
	root["created_at"] = "2026-11-07T16:00:00.123456789Z"
	if _, err := Parse(encodeJSON(t, root)); err != nil {
		t.Fatal(err)
	}
	root["manifest_digest"] = strings.Repeat("e", 64)
	reject(encodeJSON(t, root))
	root["completion_digest"] = strings.Repeat("f", 64)
	reject(encodeJSON(t, root))
	root["boundary"] = FinalizedUnit
	if _, err := Parse(encodeJSON(t, root)); err != nil {
		t.Fatal(err)
	}
	delete(root, "completion_digest")
	reject(encodeJSON(t, root))
	root["completion_digest"] = strings.Repeat("f", 64)
	delete(root, "manifest_digest")
	reject(encodeJSON(t, root))

	base := strings.TrimSpace(string(data))
	for _, mutation := range []struct{ from, to string }{
		{`"kind":`, `"k\u0069nd":"wrong","kind":`},
		{`"schema_version":"1.0"`, `"schema_version":"2.0"`},
		{`"boundary":"before-unit"`, `"boundary":"mid-runtime"`},
		{`"reservation_digest":"`, `"reservation_digest":"\ud800`},
		{`"runner_revision":"`, "\"runner_revision\":\"\xff"},
	} {
		changed := strings.Replace(base, mutation.from, mutation.to, 1)
		if changed == base {
			t.Fatal("mutation did not exercise fixture")
		}
		reject([]byte(changed))
	}
	for _, invalid := range []string{"", "null", "[]", base + "{}", base[:len(base)-1], strings.Repeat(" ", MaxBytes+1)} {
		reject([]byte(invalid))
	}
	value, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	value.Boundary = "mid-runtime"
	if encoded, err := Encode(*value); encoded != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed invalid value accepted: %s, %v", encoded, err)
	}
}

func TestDeclaredCompatibilityMatrix(t *testing.T) {
	original := readFixture(t, "../../examples/runner/unit.json")
	request, err := runnercontract.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 40)
	base, err := ForRequest(original, revision)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Compatibility = base
	cases := []struct {
		name       string
		mutate     func(*runnercontract.Request)
		compatible bool
	}{
		{"same", func(*runnercontract.Request) {}, true},
		{"next-attempt", func(r *runnercontract.Request) { r.Selection.Attempt++ }, true},
		{"capability-order", func(r *runnercontract.Request) {
			r.RequiredCapabilities[0], r.RequiredCapabilities[1] = r.RequiredCapabilities[1], r.RequiredCapabilities[0]
		}, true},
		{"crlf", func(r *runnercontract.Request) {
			r.Input.Eval = strings.ReplaceAll(r.Input.Eval, "\n", "\r\n")
			r.Input.Task = strings.ReplaceAll(r.Input.Task, "\n", "\r\n")
		}, true},
		{"model", func(r *runnercontract.Request) {
			r.Input.Eval = strings.ReplaceAll(r.Input.Eval, "mock-model", "other-model")
		}, false},
		{"trial", func(r *runnercontract.Request) { r.Selection.Trial++ }, false},
		{"timeout", func(r *runnercontract.Request) { r.TimeoutMS++ }, false},
		{"run-key", func(r *runnercontract.Request) { r.RunKey += "-changed" }, false},
		{"prompt", func(r *runnercontract.Request) {
			r.Input.Task = strings.ReplaceAll(r.Input.Task, "prompt: hello", "prompt: changed")
		}, false},
		{"grader", func(r *runnercontract.Request) {
			r.Input.Task = strings.ReplaceAll(r.Input.Task, "- hello", "- changed")
		}, false},
		{"tool-policy", func(r *runnercontract.Request) { r.Input.Task += "tool_policy:\n  allowed: []\n" }, false},
		{"absent-to-null", func(r *runnercontract.Request) { r.Input.Task += "timeout_seconds: null\n" }, false},
		{"consent", func(r *runnercontract.Request) { r.AllowPaidExecution = true }, false},
		{"optional-capability", func(r *runnercontract.Request) { r.OptionalCapabilities = []string{"negotiation"} }, false},
		{"required-capability", func(r *runnercontract.Request) {
			r.RequiredCapabilities = append(r.RequiredCapabilities, "terminal-result")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy, err := runnercontract.Parse(original)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(copy)
			changed := encodeJSON(t, copy)
			if tc.name != "same" && bytes.Equal(encodeJSON(t, request), changed) {
				t.Fatal("control did not mutate actual request")
			}
			current, err := ForRequest(changed, revision)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateCompatibility(*checkpoint, current)
			if tc.compatible && err != nil {
				t.Fatal(err)
			}
			if !tc.compatible && !errors.Is(err, ErrIncompatible) {
				t.Fatalf("incompatible input accepted or cause lost: %v", err)
			}
		})
	}
	changed, err := ForRequest(original, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCompatibility(*checkpoint, changed); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("different runner revision accepted: %v", err)
	}
	if _, err := ForRequest(original, "development"); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("unstamped revision accepted: %v", err)
	}
	for _, invalid := range [][]byte{nil, []byte("{}"), []byte("null")} {
		if _, err := ForRequest(invalid, revision); !errors.Is(err, ErrIncompatible) {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
	copy, err := runnercontract.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	copy.RequiredCapabilities = append(copy.RequiredCapabilities, "unknown")
	if _, err := ForRequest(encodeJSON(t, copy), revision); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("unsupported negotiation accepted: %v", err)
	}
	copy, err = runnercontract.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	copy.Input.Task = "id: &task selected\nname: *task\ninputs:\n  prompt: hello\n"
	if _, err := ForRequest(encodeJSON(t, copy), revision); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("unsupported identity alias accepted: %v", err)
	}
	if !bytes.Equal(original, readFixture(t, "../../examples/runner/unit.json")) {
		t.Fatal("compatibility derivation changed captured source")
	}
}

func TestCompatibilityReportsEveryChangedDimension(t *testing.T) {
	checkpoint, err := Parse(readFixture(t, "testdata/before-unit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]string
	if err := json.Unmarshal(encodeJSON(t, checkpoint.Compatibility), &source); err != nil {
		t.Fatal(err)
	}
	for field, value := range source {
		t.Run(field, func(t *testing.T) {
			source[field] = value + "-changed"
			var current Compatibility
			if err := json.Unmarshal(encodeJSON(t, source), &current); err != nil {
				t.Fatal(err)
			}
			source[field] = value
			err := ValidateCompatibility(*checkpoint, current)
			if !errors.Is(err, ErrIncompatible) || !strings.Contains(err.Error(), "changed "+field+";") {
				t.Fatalf("missing exact diagnostic for %s: %v", field, err)
			}
		})
	}
	err = ValidateCompatibility(*checkpoint, Compatibility{})
	if !errors.Is(err, ErrIncompatible) {
		t.Fatalf("empty compatibility accepted: %v", err)
	}
	for field := range source {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("independent dimension %s hidden by another mismatch: %v", field, err)
		}
	}
	checkpoint.Boundary = "mid-runtime"
	if err := ValidateCompatibility(*checkpoint, checkpoint.Compatibility); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid source checkpoint accepted: %v", err)
	}
}

func checkpointContext(t *testing.T, failed bool) (Checkpoint, artifactmanifest.Binding, []byte, []byte, []byte) {
	t.Helper()
	request := readFixture(t, "../../examples/runner/unit.json")
	if failed {
		modified := bytes.Replace(request, []byte("- hello"), []byte("- impossible-checkpoint-control"), 1)
		if bytes.Equal(request, modified) {
			t.Fatal("failed grader control did not change captured task")
		}
		request = modified
	}
	// This is real production admission/execution with the concrete mock profile.
	// It is not a recovery/ownership or live-runtime test.
	service := runnerunit.NewService(func(config models.Config) (execution.AgentEngine, error) {
		return execution.NewMockEngine(config.ModelID), nil
	})
	envelope, binding := service.RunTerminal(context.Background(), request)
	expected := runnerterminal.Success
	if failed {
		expected = runnerterminal.EvaluationFailure
	}
	if binding == nil || envelope.Outcome != expected || envelope.EngineCalls != 1 || envelope.Observation == nil ||
		len(envelope.Observation.Validations) == 0 {
		t.Fatalf("production setup did not produce expected observation: %+v", envelope)
	}
	terminal, err := runnerterminal.Encode(envelope, binding)
	if err != nil {
		t.Fatal(err)
	}
	id := "exec-" + strings.Repeat("a", 64)
	attempt := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical: executionattempt.LogicalFrom(*binding.Identity), ExecutionID: id,
		Binding: artifactmanifest.Binding{
			Identity: *binding.Identity, Selection: binding.Selection, ExecutionMode: binding.ExecutionMode, GraderIDs: binding.GraderIDs,
		},
	}
	reservation, err := executionattempt.EncodeAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := artifactmanifest.Encode(artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version, Binding: attempt.Binding,
		TerminalArtifact: "terminal", Artifacts: []artifactmanifest.Artifact{{
			Name: "terminal", SHA256: artifactmanifest.Hash(terminal), SizeBytes: int64(len(terminal)), MediaType: "application/json",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completion, err := executionledger.EncodeCompletion(executionledger.Completion{
		Kind: "waza.execution-completion", SchemaVersion: executionledger.Version, ExecutionID: id,
		ReservationDigest: artifactmanifest.Hash(reservation), ManifestDigest: artifactmanifest.Hash(manifest),
	})
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := ForRequest(request, strings.Repeat("c", 40))
	if err != nil {
		t.Fatal(err)
	}
	return Checkpoint{
		Kind: "waza.execution-checkpoint", SchemaVersion: Version, Boundary: FinalizedUnit,
		CreatedAt: "2026-11-07T16:00:00Z", ExecutionID: id, ReservationDigest: artifactmanifest.Hash(reservation),
		Compatibility: compatibility, CompletionDigest: artifactmanifest.Hash(completion), ManifestDigest: artifactmanifest.Hash(manifest),
	}, attempt.Binding, reservation, completion, manifest
}

func TestOriginalCompletionContext(t *testing.T) {
	for _, failed := range []bool{false, true} {
		checkpoint, binding, reservation, completion, manifest := checkpointContext(t, failed)
		original := encodeJSON(t, checkpoint)
		if err := ValidateContext(checkpoint, binding, reservation, completion, manifest); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(original, encodeJSON(t, checkpoint)) {
			t.Fatal("context admission mutated source")
		}
		for _, tc := range []struct {
			name string
			edit func(*Checkpoint, *[]byte, *[]byte, *[]byte)
		}{
			{"reservation-whitespace", func(_ *Checkpoint, r, _, _ *[]byte) { *r = append(bytes.Clone(*r), '\n') }},
			{"completion-whitespace", func(_ *Checkpoint, _, c, _ *[]byte) { *c = append(bytes.Clone(*c), '\n') }},
			{"manifest-whitespace", func(_ *Checkpoint, _, _, m *[]byte) { *m = append(bytes.Clone(*m), '\n') }},
			{"missing-reservation", func(_ *Checkpoint, r, _, _ *[]byte) { *r = nil }},
			{"missing-completion", func(_ *Checkpoint, _, c, _ *[]byte) { *c = nil }},
			{"missing-manifest", func(_ *Checkpoint, _, _, m *[]byte) { *m = nil }},
			{"source-execution", func(c *Checkpoint, _, _, _ *[]byte) { c.ExecutionID = "exec-" + strings.Repeat("b", 64) }},
			{"reservation-digest", func(c *Checkpoint, _, _, _ *[]byte) { c.ReservationDigest = strings.Repeat("b", 64) }},
			{"completion-digest", func(c *Checkpoint, _, _, _ *[]byte) { c.CompletionDigest = strings.Repeat("b", 64) }},
			{"manifest-digest", func(c *Checkpoint, _, _, _ *[]byte) { c.ManifestDigest = strings.Repeat("b", 64) }},
			{"logical-work", func(c *Checkpoint, _, _, _ *[]byte) { c.Compatibility.Work = "trial-" + strings.Repeat("b", 64) }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				value := checkpoint
				r, c, m := bytes.Clone(reservation), bytes.Clone(completion), bytes.Clone(manifest)
				tc.edit(&value, &r, &c, &m)
				if err := ValidateContext(value, binding, r, c, m); !errors.Is(err, ErrContext) {
					t.Fatalf("wrong context accepted or cause lost: %v", err)
				}
			})
		}
		before := checkpoint
		before.Boundary, before.CompletionDigest, before.ManifestDigest = BeforeUnit, "", ""
		if err := ValidateContext(before, binding, reservation, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := ValidateContext(before, binding, reservation, completion, manifest); !errors.Is(err, ErrContext) {
			t.Fatalf("before-unit accepted completed evidence: %v", err)
		}

		// A self-consistent new completion digest must not hide a source binding
		// mismatch in the manifest emitted from the original production result.
		wrong, err := artifactmanifest.Parse(manifest)
		if err != nil {
			t.Fatal(err)
		}
		wrong.Binding.Selection.Task = "unrelated-task"
		wrongManifest, err := artifactmanifest.Encode(*wrong)
		if err != nil {
			t.Fatal(err)
		}
		wrongCompletion, err := executionledger.ParseCompletion(completion)
		if err != nil {
			t.Fatal(err)
		}
		wrongCompletion.ManifestDigest = artifactmanifest.Hash(wrongManifest)
		wrongCompletionBytes, err := executionledger.EncodeCompletion(*wrongCompletion)
		if err != nil {
			t.Fatal(err)
		}
		wrongCheckpoint := checkpoint
		wrongCheckpoint.ManifestDigest = wrongCompletion.ManifestDigest
		wrongCheckpoint.CompletionDigest = artifactmanifest.Hash(wrongCompletionBytes)
		if err := ValidateContext(wrongCheckpoint, binding, reservation, wrongCompletionBytes, wrongManifest); !errors.Is(err, ErrContext) {
			t.Fatalf("coherently rehashed foreign source accepted: %v", err)
		}
		// Coherently retagging both sidecars is still not the independently
		// admitted source, even when every new declaration hash agrees.
		retagged, err := executionattempt.ParseAttempt(reservation)
		if err != nil {
			t.Fatal(err)
		}
		retagged.Binding.Selection.Attempt++
		retagged.Binding.Identity.Attempt = "attempt-" + strings.Repeat("d", 64)
		retagged.Binding.Identity.CanonicalDigest = strings.Repeat("d", 64)
		retaggedReservation, err := executionattempt.EncodeAttempt(*retagged)
		if err != nil {
			t.Fatal(err)
		}
		wrong.Binding = retagged.Binding
		retaggedManifest, err := artifactmanifest.Encode(*wrong)
		if err != nil {
			t.Fatal(err)
		}
		wrongCompletion.ReservationDigest = artifactmanifest.Hash(retaggedReservation)
		wrongCompletion.ManifestDigest = artifactmanifest.Hash(retaggedManifest)
		retaggedCompletion, err := executionledger.EncodeCompletion(*wrongCompletion)
		if err != nil {
			t.Fatal(err)
		}
		// The old completion helper accepts the internally consistent rewrite;
		// checkpoint admission must reject it against independent source state.
		if err := executionledger.ValidateContext(*wrongCompletion, retaggedReservation, retaggedManifest); err != nil {
			t.Fatalf("retagging control was not internally consistent: %v", err)
		}
		wrongCheckpoint.ReservationDigest = wrongCompletion.ReservationDigest
		wrongCheckpoint.ManifestDigest = wrongCompletion.ManifestDigest
		wrongCheckpoint.CompletionDigest = artifactmanifest.Hash(retaggedCompletion)
		if err := ValidateContext(wrongCheckpoint, binding, retaggedReservation, retaggedCompletion, retaggedManifest); !errors.Is(err, ErrContext) {
			t.Fatalf("coherently retagged source accepted: %v", err)
		}
		retaggedBefore := before
		retaggedBefore.ReservationDigest = wrongCompletion.ReservationDigest
		if err := ValidateContext(retaggedBefore, binding, retaggedReservation, nil, nil); !errors.Is(err, ErrContext) {
			t.Fatalf("before-unit accepted retagged original source: %v", err)
		}
		checkpoint.Boundary = "mid-runtime"
		if err := ValidateContext(checkpoint, binding, reservation, completion, manifest); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid checkpoint accepted by context admission: %v", err)
		}
	}
}
