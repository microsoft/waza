package executionledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
)

func completionFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/completion.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("frozen fixture must end in LF")
	}
	return data[:len(data)-1]
}

func completionContext(t *testing.T) (Completion, []byte, []byte) {
	t.Helper()
	data, err := os.ReadFile("../executionattempt/testdata/attempt.json")
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := executionattempt.ParseAttempt(data)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := executionattempt.EncodeAttempt(*attempt)
	if err != nil {
		t.Fatal(err)
	}
	manifest := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version,
		Binding: attempt.Binding, TerminalArtifact: "terminal",
		Artifacts: []artifactmanifest.Artifact{{
			Name: "terminal", SHA256: artifactmanifest.Hash(nil), SizeBytes: 0, MediaType: "application/json",
		}},
	}
	manifestBytes, err := artifactmanifest.Encode(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := artifactmanifest.Digest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return Completion{
		Kind: "waza.execution-completion", SchemaVersion: Version, ExecutionID: attempt.ExecutionID,
		ReservationDigest: artifactmanifest.Hash(reservation), ManifestDigest: digest,
	}, reservation, manifestBytes
}

func TestCompletionFrozenCodec(t *testing.T) {
	data := completionFixture(t)
	value, err := ParseCompletion(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EncodeCompletion(*value)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("frozen completion bytes changed: %s, %v", got, err)
	}
	padded := append(append([]byte(" \r\n"), data...), '\r', '\n')
	if _, err := ParseCompletion(padded); err != nil {
		t.Fatalf("valid alternate whitespace: %v", err)
	}
}

func TestCompletionWireStrictness(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(completionFixture(t), &root); err != nil {
		t.Fatal(err)
	}
	reject := func(data []byte) {
		t.Helper()
		got, err := ParseCompletion(data)
		if got != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid completion accepted or error cause lost: %+v, %v", got, err)
		}
	}
	marshal := func() []byte {
		t.Helper()
		data, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for key, original := range root {
		t.Run(key, func(t *testing.T) {
			delete(root, key)
			reject(marshal())
			for _, wrong := range []any{nil, "", false, 0, []any{}, map[string]any{}} {
				root[key] = wrong
				reject(marshal())
			}
			root[key] = original
		})
	}
	root["unknown"] = true
	reject(marshal())
	base := string(completionFixture(t))
	for _, mutate := range []struct{ old, replacement string }{
		{`"kind":`, `"k\u0069nd":"wrong","kind":`},
		{`"schema_version":"1.0"`, `"schema_version":1.0`},
		{`"manifest_digest":"`, `"manifest_digest":"\ud800`},
		{`"execution_id":"exec-`, `"execution_id":"exec-\n`},
		{`"manifest_digest":"`, "\"manifest_digest\":\"\xff"},
	} {
		if !strings.Contains(base, mutate.old) {
			t.Fatal("mutation missed original fixture")
		}
		reject([]byte(strings.Replace(base, mutate.old, mutate.replacement, 1)))
	}
	for _, data := range []string{"", "null", "[]", base + "{}", base[:len(base)-1], strings.Repeat(" ", executionattempt.MaxSidecarBytes+1)} {
		reject([]byte(data))
	}
	value, err := ParseCompletion(completionFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	value.ManifestDigest += "\n"
	if data, err := EncodeCompletion(*value); data != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed invalid completion accepted: %s, %v", data, err)
	}
}

func TestCompletionOriginalContextAndHashes(t *testing.T) {
	base, reservation, manifest := completionContext(t)
	if err := ValidateContext(base, reservation, manifest); err != nil {
		t.Fatal(err)
	}

	t.Run("reuse_across_declared_ordinals_keeps_original_source", func(t *testing.T) {
		completion, reservation, manifest := completionContext(t)
		completionBytes, err := EncodeCompletion(completion)
		if err != nil {
			t.Fatal(err)
		}
		source, err := executionattempt.ParseAttempt(reservation)
		if err != nil {
			t.Fatal(err)
		}
		decision := executionattempt.Decision{
			Kind: "waza.duplicate-decision", SchemaVersion: executionattempt.Version,
			DecisionID: "decision-" + strings.Repeat("d", 64), Logical: source.Logical,
			Binding: source.Binding, Policy: "reuse", Outcome: "reused",
			Source: &executionattempt.Source{ExecutionID: source.ExecutionID, ManifestDigest: completion.ManifestDigest},
		}
		decision.Binding.Selection.Attempt++
		decision.Binding.Identity.Attempt = "attempt-" + strings.Repeat("e", 64)
		decision.Binding.Identity.CanonicalDigest = strings.Repeat("e", 64)
		beforeDecision, err := executionattempt.EncodeDecision(decision)
		if err != nil {
			t.Fatal(err)
		}
		beforeReservation, beforeCompletion, beforeManifest := bytes.Clone(reservation), bytes.Clone(completionBytes), bytes.Clone(manifest)
		if err := ValidateReuseContext(decision, reservation, completionBytes, manifest); err != nil {
			t.Fatalf("different caller ordinal must not require retagged source: %v", err)
		}
		afterDecision, err := executionattempt.EncodeDecision(decision)
		if err != nil || !bytes.Equal(beforeDecision, afterDecision) ||
			!bytes.Equal(beforeReservation, reservation) || !bytes.Equal(beforeCompletion, completionBytes) ||
			!bytes.Equal(beforeManifest, manifest) {
			t.Fatal("reuse admission mutated caller or original source bytes")
		}
		for _, mutate := range []func(*executionattempt.Decision){
			func(d *executionattempt.Decision) { d.Source.ExecutionID = "exec-" + strings.Repeat("f", 64) },
			func(d *executionattempt.Decision) { d.Source.ManifestDigest = strings.Repeat("f", 64) },
			func(d *executionattempt.Decision) { d.Binding.Selection.Task = "different-task" },
			func(d *executionattempt.Decision) { d.Binding.ExecutionMode = "copilot" },
			func(d *executionattempt.Decision) { d.Binding.Identity.Task = "task-" + strings.Repeat("f", 64) },
			func(d *executionattempt.Decision) { d.Binding.GraderIDs = []string{"unexpected"} },
			func(d *executionattempt.Decision) { d.Outcome = "unfinished" },
			func(d *executionattempt.Decision) { d.Source = nil },
		} {
			value, err := executionattempt.ParseDecision(beforeDecision)
			if err != nil {
				t.Fatal(err)
			}
			mutate(value)
			if err := ValidateReuseContext(*value, reservation, completionBytes, manifest); !errors.Is(err, ErrContext) {
				t.Fatalf("invalid source/context/decision accepted: %v", err)
			}
		}

		for _, bad := range [][]byte{nil, append(bytes.Clone(completionBytes), '\n')} {
			if err := ValidateReuseContext(decision, reservation, bad, manifest); !errors.Is(err, ErrContext) {
				t.Fatalf("missing/noncanonical completion accepted: %v", err)
			}
		}
	})
	// The empty terminal declaration is intentionally not valid terminal content.
	// Context/hash admission must not be presented as artifact/final verification.
	for _, tc := range []struct {
		name    string
		value   Completion
		attempt []byte
		source  []byte
	}{
		{"truncated_reservation", base, reservation[:len(reservation)-1], manifest},
		{"truncated_manifest", base, reservation, manifest[:len(manifest)-1]},
		{"noncanonical_reservation", base, append(bytes.Clone(reservation), '\n'), manifest},
		{"noncanonical_manifest", base, reservation, append(bytes.Clone(manifest), '\n')},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateContext(tc.value, tc.attempt, tc.source); !errors.Is(err, ErrContext) {
				t.Fatalf("invalid context accepted: %v", err)
			}
		})
	}
	for _, mutate := range []func(*Completion){
		func(c *Completion) { c.ExecutionID = "exec-" + strings.Repeat("f", 64) },
		func(c *Completion) { c.ReservationDigest = strings.Repeat("f", 64) },
		func(c *Completion) { c.ManifestDigest = strings.Repeat("f", 64) },
	} {
		value := base
		mutate(&value)
		if err := ValidateContext(value, reservation, manifest); !errors.Is(err, ErrContext) {
			t.Fatalf("mismatched reference accepted: %v", err)
		}
	}
	invalid := base
	invalid.Kind = ""
	if err := ValidateContext(invalid, reservation, manifest); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid completion lost cause: %v", err)
	}
	for _, change := range []func(*artifactmanifest.Manifest){
		func(m *artifactmanifest.Manifest) { m.Binding.Selection.Attempt++ },
		func(m *artifactmanifest.Manifest) { m.Binding.Identity.Attempt = "attempt-" + strings.Repeat("f", 64) },
		func(m *artifactmanifest.Manifest) { m.Binding.ExecutionMode = "copilot" },
		func(m *artifactmanifest.Manifest) { m.Binding.GraderIDs = []string{"new-grader"} },
	} {
		m, err := artifactmanifest.Parse(manifest)
		if err != nil {
			t.Fatal(err)
		}
		change(m)
		changed, err := artifactmanifest.Encode(*m)
		if err != nil {
			t.Fatal(err)
		}
		value := base
		value.ManifestDigest = artifactmanifest.Hash(changed)
		if err := ValidateContext(value, reservation, changed); !errors.Is(err, ErrContext) {
			t.Fatalf("rehashed different original binding accepted: %v", err)
		}
	}
}
