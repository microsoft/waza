package artifactmanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/microsoft/waza/internal/runnerterminal"
)

const manifestOracleDigest = "5b9c9d9c32dcf710f0662de777e1fc356add8b559673b3b839da66b6a1398298"
const manifestEmptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func manifestGolden(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		t.Fatal("frozen text fixture must end in one LF")
	}
	return data[:len(data)-1]
}

func manifestSample(t *testing.T) Manifest {
	t.Helper()
	var m Manifest
	if err := json.Unmarshal(manifestGolden(t), &m); err != nil {
		t.Fatal(err)
	}
	m.Artifacts = []Artifact{{Name: "terminal", SHA256: manifestEmptyDigest, SizeBytes: 0, MediaType: "application/json"}}
	m.Binding.GraderIDs = []string{}
	return m
}

func manifestWire(t *testing.T, m Manifest) []byte {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func manifestReject(t *testing.T, data []byte) {
	t.Helper()
	got, err := Parse(data)
	if !errors.Is(err, ErrInvalid) || got != nil {
		t.Fatalf("Parse accepted malformed declaration or lost ErrInvalid: result=%+v error=%v", got, err)
	}
}

func TestCanonicalFrozenNodeOracle(t *testing.T) {
	output, err := exec.Command("node", "testdata/oracle.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("independent Node byte/hash oracle failed: %v\n%s", err, output)
	}
	if string(output) != manifestOracleDigest+"\n" {
		t.Fatalf("Node oracle digest = %q", output)
	}
	var m Manifest
	golden := manifestGolden(t)
	if err := json.Unmarshal(golden, &m); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(m.Artifacts)
	slices.Reverse(m.Binding.GraderIDs)
	before := manifestWire(t, m)
	got, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, golden) {
		t.Fatalf("canonical bytes differ from frozen oracle\nwant: %s\n got: %s", golden, got)
	}
	if !bytes.Equal(before, manifestWire(t, m)) {
		t.Fatal("Encode mutated caller-owned artifact or grader slices")
	}
	digest, err := Digest(m)
	if err != nil || digest != manifestOracleDigest {
		t.Fatalf("Digest = %q, %v; want independent pinned digest", digest, err)
	}
	if !bytes.Equal(before, manifestWire(t, m)) {
		t.Fatal("Digest mutated caller input")
	}
	parsed, err := Parse(golden)
	if err != nil || parsed == nil {
		t.Fatalf("Parse frozen oracle: %v", err)
	}
	if !reflect.DeepEqual(parsed.Binding.GraderIDs, []string{"a", "z", "é"}) ||
		parsed.Binding.Selection.Task != "é😀\"\\\n\t\x00\u2028\u2029" {
		t.Fatalf("Unicode/escaping or grader ordering changed: %+v", parsed.Binding)
	}
}

func TestHashFrozenVectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"empty", nil, manifestEmptyDigest},
		{"abc", []byte("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"canonical", manifestGolden(t), manifestOracleDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Hash(tc.data); got != tc.want {
				t.Fatalf("Hash = %q; want %q", got, tc.want)
			}
		})
	}
	if Hash(append(slices.Clone(manifestGolden(t)), '\n')) == manifestOracleDigest {
		t.Fatal("Hash silently discarded trailing LF")
	}
}

func TestManifestRequiredPresenceAndTypes(t *testing.T) {
	var root map[string]any
	if err := json.Unmarshal(manifestWire(t, manifestSample(t)), &root); err != nil {
		t.Fatal(err)
	}
	// Enumerate every required member independently of the schema under test.
	object := func(value any) map[string]any {
		t.Helper()
		result, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("fixture object is %T", value)
		}
		return result
	}
	binding := object(root["binding"])
	artifacts, ok := root["artifacts"].([]any)
	if !ok || len(artifacts) != 1 {
		t.Fatal("fixture must contain one artifact")
	}
	scopes := []struct {
		name string
		obj  map[string]any
		keys []string
	}{
		{"manifest", root, []string{"kind", "schema_version", "binding", "terminal_artifact", "artifacts"}},
		{"binding", binding, []string{"identity", "selection", "execution_mode", "grader_ids"}},
		{"identity", object(binding["identity"]),
			[]string{"version", "canonical_digest", "run", "shard", "task", "trial", "attempt"}},
		{"selection", object(binding["selection"]), []string{"shard", "task", "trial", "attempt"}},
		{"artifact", object(artifacts[0]), []string{"name", "sha256", "size_bytes", "media_type"}},
	}
	marshal := func(t *testing.T) []byte {
		t.Helper()
		data, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	if _, err := Parse(marshal(t)); err != nil {
		t.Fatalf("valid zero-size and empty-graders control: %v", err)
	}
	for _, scope := range scopes {
		t.Run(scope.name+"/unknown", func(t *testing.T) {
			scope.obj["unknown"] = true
			defer delete(scope.obj, "unknown")
			manifestReject(t, marshal(t))
		})
		for _, key := range scope.keys {
			original := scope.obj[key]
			t.Run(scope.name+"/"+key+"/omitted", func(t *testing.T) {
				delete(scope.obj, key)
				defer func() { scope.obj[key] = original }()
				manifestReject(t, marshal(t))
			})
			for _, wrong := range []any{nil, false, float64(0), "", []any{}, map[string]any{}} {
				if reflect.TypeOf(wrong) == reflect.TypeOf(original) {
					continue
				}
				t.Run(fmt.Sprintf("%s/%s/type_%T", scope.name, key, wrong), func(t *testing.T) {
					scope.obj[key] = wrong
					defer func() { scope.obj[key] = original }()
					manifestReject(t, marshal(t))
				})
			}
		}
	}
}

func TestManifestOriginalWireStrictness(t *testing.T) {
	base := string(manifestWire(t, manifestSample(t)))
	for _, tc := range []struct{ name, old, replacement string }{
		{"top_duplicate", `"kind":`, `"kind":"waza.artifact-manifest","kind":`},
		{"escaped_key_alias", `"kind":`, `"k\u0069nd":"waza.artifact-manifest","kind":`},
		{"binding_duplicate", `"execution_mode":`, `"execution_mode":"copilot","execution_mode":`},
		{"identity_duplicate", `"version":`, `"version":"sha256-waza-c14n-v1","version":`},
		{"selection_duplicate", `"trial":1`, `"trial":2,"trial":1`},
		{"artifact_duplicate", `"size_bytes":0`, `"size_bytes":1,"size_bytes":0`},
		{"fractional_size", `"size_bytes":0`, `"size_bytes":0.1`},
		{"precision_size", `"size_bytes":0`, `"size_bytes":0.0000000000000000000000000000000000000001`},
		{"fractional_trial", `"trial":1`, `"trial":1.0000000000000000000000000000000000000001`},
		{"overflow_size", `"size_bytes":0`, `"size_bytes":9223372036854775808`},
		{"unpaired_high", `"media_type":"application/json"`, `"media_type":"\ud800"`},
		{"unpaired_low", `"media_type":"application/json"`, `"media_type":"\udfff"`},
		{"broken_pair", `"media_type":"application/json"`, `"media_type":"\ud800\u0041"`},
		{"raw_invalid_utf8", `"media_type":"application/json"`, "\"media_type\":\"text/\xff\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(base, tc.old) {
				t.Fatal("malformed control did not match original wire")
			}
			manifestReject(t, []byte(strings.Replace(base, tc.old, tc.replacement, 1)))
		})
	}
	for _, data := range []string{"", "null", "[]", base + "{}", base + "false", base[:len(base)-1], "\xef\xbb\xbf" + base} {
		t.Run(fmt.Sprintf("document_%d", len(data)), func(t *testing.T) {
			manifestReject(t, []byte(data))
		})
	}
	// Valid surrogate pairs and literal backslash-u text are not malformed.
	for _, media := range []string{`"text/\ud83d\ude00"`, `"text/😀"`, `"text/\\ud800"`, `"text/�"`} {
		extra := `{"name":"extra","sha256":"` + manifestEmptyDigest + `","size_bytes":0,"media_type":` + media + `},`
		data := strings.Replace(base, `"artifacts":[`, `"artifacts":[`+extra, 1)
		if _, err := Parse([]byte(data)); err != nil {
			t.Fatalf("alternative valid Unicode %s rejected: %v", media, err)
		}
	}
}

func TestManifestDeclarationBounds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Manifest)
		valid  bool
	}{
		{"zero_empty", func(*Manifest) {}, true},
		{"copilot", func(m *Manifest) { m.Binding.ExecutionMode = "copilot" }, true},
		{"bad_kind", func(m *Manifest) { m.Kind = "other" }, false},
		{"bad_version", func(m *Manifest) { m.SchemaVersion = "2.0" }, false},
		{"bad_mode", func(m *Manifest) { m.Binding.ExecutionMode = "live" }, false},
		{"nil_graders", func(m *Manifest) { m.Binding.GraderIDs = nil }, false},
		{"duplicate_graders", func(m *Manifest) { m.Binding.GraderIDs = []string{"g", "g"} }, false},
		{"empty_grader", func(m *Manifest) { m.Binding.GraderIDs = []string{""} }, false},
		{"grader_length_max", func(m *Manifest) { m.Binding.GraderIDs = []string{strings.Repeat("a", 256)} }, true},
		{"grader_length_over", func(m *Manifest) { m.Binding.GraderIDs = []string{strings.Repeat("a", 257)} }, false},
		{"grader_utf8_max", func(m *Manifest) { m.Binding.GraderIDs = []string{strings.Repeat("😀", 64)} }, true},
		{"grader_utf8_over", func(m *Manifest) { m.Binding.GraderIDs = []string{strings.Repeat("😀", 65)} }, false},
		{"grader_utf8_schema_max", func(m *Manifest) { m.Binding.GraderIDs = []string{strings.Repeat("😀", 256)} }, false},
		{"no_artifacts", func(m *Manifest) { m.Artifacts = []Artifact{} }, false},
		{"nil_artifacts", func(m *Manifest) { m.Artifacts = nil }, false},
		{"missing_terminal", func(m *Manifest) { m.TerminalArtifact = "missing" }, false},
		{"duplicate_names", func(m *Manifest) { m.Artifacts = append(m.Artifacts, m.Artifacts[0]) }, false},
		{"terminal_media", func(m *Manifest) { m.Artifacts[0].MediaType = "text/plain" }, false},
		{"terminal_size_max", func(m *Manifest) { m.Artifacts[0].SizeBytes = runnerterminal.MaxEnvelopeBytes }, true},
		{"terminal_size_over", func(m *Manifest) { m.Artifacts[0].SizeBytes = runnerterminal.MaxEnvelopeBytes + 1 }, false},
		{"negative_size", func(m *Manifest) { m.Artifacts[0].SizeBytes = -1 }, false},
		{"trial_zero", func(m *Manifest) { m.Binding.Selection.Trial = 0 }, false},
		{"attempt_zero", func(m *Manifest) { m.Binding.Selection.Attempt = 0 }, false},
		{"trial_max", func(m *Manifest) { m.Binding.Selection.Trial = 2147483647 }, true},
		{"trial_over", func(m *Manifest) { m.Binding.Selection.Trial = 2147483648 }, false},
		{"attempt_max", func(m *Manifest) { m.Binding.Selection.Attempt = 2147483647 }, true},
		{"attempt_over", func(m *Manifest) { m.Binding.Selection.Attempt = 2147483648 }, false},
		{"selection_empty", func(m *Manifest) { m.Binding.Selection.Shard = "" }, false},
		{"selection_max", func(m *Manifest) { m.Binding.Selection.Task = strings.Repeat("a", 256) }, true},
		{"selection_over", func(m *Manifest) { m.Binding.Selection.Task = strings.Repeat("a", 257) }, false},
		{"selection_task_utf8_max", func(m *Manifest) { m.Binding.Selection.Task = strings.Repeat("😀", 64) }, true},
		{"selection_task_utf8_over", func(m *Manifest) { m.Binding.Selection.Task = strings.Repeat("😀", 65) }, false},
		{"selection_shard_utf8_max", func(m *Manifest) { m.Binding.Selection.Shard = strings.Repeat("😀", 64) }, true},
		{"selection_shard_utf8_over", func(m *Manifest) { m.Binding.Selection.Shard = strings.Repeat("😀", 65) }, false},
		{"identity_short", func(m *Manifest) { m.Binding.Identity.Run = "run-1" }, false},
		{"identity_version", func(m *Manifest) { m.Binding.Identity.Version = "1.0" }, false},
		{"identity_upper", func(m *Manifest) { m.Binding.Identity.CanonicalDigest = strings.Repeat("A", 64) }, false},
		{"digest_upper", func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("A", 64) }, false},
		{"digest_short", func(m *Manifest) { m.Artifacts[0].SHA256 = "a" }, false},
		{"digest_nonhex", func(m *Manifest) { m.Artifacts[0].SHA256 = strings.Repeat("g", 64) }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := manifestSample(t)
			tc.change(&m)
			data := manifestWire(t, m)
			parsed, parseErr := Parse(data)
			encoded, encodeErr := Encode(m)
			digest, digestErr := Digest(m)
			if tc.valid {
				if parseErr != nil || encodeErr != nil || digestErr != nil || parsed == nil || len(encoded) == 0 || len(digest) != 64 {
					t.Fatalf("valid control: Parse=%v Encode=%v Digest=%v", parseErr, encodeErr, digestErr)
				}
			} else if !errors.Is(parseErr, ErrInvalid) || !errors.Is(encodeErr, ErrInvalid) ||
				!errors.Is(digestErr, ErrInvalid) || parsed != nil || encoded != nil || digest != "" {
				t.Fatalf("invalid control: Parse=%v Encode=%v Digest=%q,%v", parseErr, encodeErr, digest, digestErr)
			}
		})
	}
}

func TestManifestArtifactAndCollectionLimits(t *testing.T) {
	for _, count := range []int{128, 129} {
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			m := manifestSample(t)
			for i := 1; i < count; i++ {
				a := m.Artifacts[0]
				a.Name = fmt.Sprintf("artifact-%03d", i)
				m.Artifacts = append(m.Artifacts, a)
			}
			if count == 129 {
				manifestReject(t, manifestWire(t, m))
			} else if _, err := Parse(manifestWire(t, m)); err != nil {
				t.Fatal(err)
			}
			m = manifestSample(t)
			for i := range count {
				m.Binding.GraderIDs = append(m.Binding.GraderIDs, fmt.Sprintf("grader-%03d", i))
			}
			if count == 129 {
				manifestReject(t, manifestWire(t, m))
			} else if _, err := Parse(manifestWire(t, m)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, tc := range []struct {
		name string
		size int64
		want bool
	}{
		{"artifact_max", MaxArtifactBytes, true},
		{"artifact_over", MaxArtifactBytes + 1, false},
		{"artifact_negative", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := manifestSample(t)
			m.Artifacts = append(m.Artifacts, Artifact{Name: "extra", SHA256: manifestEmptyDigest, SizeBytes: tc.size, MediaType: "application/octet-stream"})
			if tc.want {
				if _, err := Parse(manifestWire(t, m)); err != nil {
					t.Fatal(err)
				}
			} else {
				manifestReject(t, manifestWire(t, m))
			}
		})
	}
	for _, extra := range []int64{0, 1} {
		m := manifestSample(t)
		for i := range 4 {
			m.Artifacts = append(m.Artifacts, Artifact{Name: fmt.Sprintf("part-%d", i), SHA256: manifestEmptyDigest, SizeBytes: MaxArtifactBytes, MediaType: "application/octet-stream"})
		}
		m.Artifacts[0].SizeBytes = extra
		if extra == 0 {
			if _, err := Parse(manifestWire(t, m)); err != nil {
				t.Fatalf("exact total byte limit: %v", err)
			}
		} else {
			manifestReject(t, manifestWire(t, m))
		}
	}
	base := manifestWire(t, manifestSample(t))
	atLimit := append(slices.Clone(base), bytes.Repeat([]byte(" "), MaxManifestBytes-len(base))...)
	if _, err := Parse(atLimit); err != nil {
		t.Fatalf("exact original wire byte limit: %v", err)
	}
	manifestReject(t, append(atLimit, ' '))
}

func TestManifestArtifactStringBounds(t *testing.T) {
	for _, tc := range []struct {
		name, media string
		valid       bool
	}{
		{"A._-09", "text/plain", true},
		{"a", strings.Repeat("x", 128), true},
		{strings.Repeat("a", 128), "text/plain", true},
		{strings.Repeat("a", 129), "text/plain", false},
		{"", "text/plain", false},
		{".hidden", "text/plain", false},
		{"../escape", "text/plain", false},
		{"a/b", "text/plain", false},
		{`a\b`, "text/plain", false},
		{"é", "text/plain", false},
		{"a", "", false},
		{"a", strings.Repeat("x", 129), false},
	} {
		t.Run(fmt.Sprintf("%q_%d", tc.name, len(tc.media)), func(t *testing.T) {
			m := manifestSample(t)
			m.Artifacts = append(m.Artifacts, Artifact{Name: tc.name, SHA256: manifestEmptyDigest, MediaType: tc.media})
			if tc.valid {
				if _, err := Parse(manifestWire(t, m)); err != nil {
					t.Fatal(err)
				}
			} else {
				manifestReject(t, manifestWire(t, m))
			}
		})
	}
}

func TestCanonicalRejectsInvalidTypedUTF8(t *testing.T) {
	for _, field := range []string{"grader", "selection_shard", "selection_task", "media_type", "name", "terminal_artifact", "identity"} {
		t.Run(field, func(t *testing.T) {
			m := manifestSample(t)
			bad := string([]byte{0xff})
			switch field {
			case "grader":
				m.Binding.GraderIDs = []string{bad}
			case "selection_shard":
				m.Binding.Selection.Shard = bad
			case "selection_task":
				m.Binding.Selection.Task = bad
			case "media_type":
				m.Artifacts = append(m.Artifacts, Artifact{Name: "extra", SHA256: manifestEmptyDigest, MediaType: "text/" + bad})
			case "name":
				m.Artifacts[0].Name = bad
			case "terminal_artifact":
				m.TerminalArtifact = bad
			case "identity":
				m.Binding.Identity.CanonicalDigest = bad
			}
			before := manifestWire(t, m)
			data, err := Encode(m)
			if !errors.Is(err, ErrInvalid) || data != nil {
				t.Fatalf("Encode silently repaired invalid UTF-8 in %s: bytes=%s error=%v", field, data, err)
			}
			digest, err := Digest(m)
			if !errors.Is(err, ErrInvalid) || digest != "" {
				t.Fatalf("Digest addressed repaired invalid UTF-8 in %s: digest=%q error=%v", field, digest, err)
			}
			if !bytes.Equal(before, manifestWire(t, m)) {
				t.Fatal("invalid Encode/Digest mutated caller")
			}
		})
	}
}

func TestManifestParsePreservesCallerWireAndOrder(t *testing.T) {
	var m Manifest
	if err := json.Unmarshal(manifestGolden(t), &m); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(m.Artifacts)
	slices.Reverse(m.Binding.GraderIDs)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	before := slices.Clone(data)
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*parsed, m) || !bytes.Equal(data, before) {
		t.Fatal("Parse canonicalized order, lost fields, or mutated original bytes")
	}
	parsed.Binding.GraderIDs[0] = "changed"
	parsed.Artifacts[0].Name = "changed"
	if !bytes.Equal(data, before) {
		t.Fatal("parsed data aliases caller wire")
	}
}
