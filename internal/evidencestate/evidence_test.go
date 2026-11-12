package evidencestate

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

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func intPointer(n int64) *int64 { return &n }

func sample() Document {
	d := Document{
		Kind: "waza.execution-evidence", SchemaVersion: Version, Publication: "derived",
		OriginManifest: strings.Repeat("a", 64), Lineage: []Lineage{},
	}
	for _, category := range []Category{Conversation, Tool, Grader, Reliability, Usage, Provenance} {
		d.Categories = append(d.Categories, Record{
			Category: category, State: Present, Source: Mock, Context: Observed,
			Artifacts: []string{}, ObservedCount: intPointer(0),
		})
	}
	return d
}

func wire(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func reject(t *testing.T, data []byte) {
	t.Helper()
	if d, err := Parse(data); d != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid evidence accepted or error classification lost: %s, %+v, %v", data, d, err)
	}
}

func TestFrozenCanonicalAndPrecision(t *testing.T) {
	fixture, err := os.ReadFile("testdata/canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(fixture, []byte("\n")) {
		t.Fatal("text fixture must retain its LF")
	}
	fixture = fixture[:len(fixture)-1]
	d, err := Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if *d.Categories[2].ObservedCount != 9007199254740993 || d.Categories[2].Limit.Total != nil {
		t.Fatal("precision or unknown total lost")
	}
	slices.Reverse(d.Categories)
	slices.Reverse(d.Lineage)
	before := wire(t, d)
	encoded, err := Encode(*d)
	if err != nil || !bytes.Equal(encoded, fixture) {
		t.Fatalf("canonical fixture mismatch: %s, %v", encoded, err)
	}
	if !bytes.Equal(before, wire(t, d)) {
		t.Fatal("Encode mutated caller input")
	}
	digest, err := Digest(*d)
	if err != nil {
		t.Fatal(err)
	}
	// Independent byte hashing, not the Go canonicalizer under test.
	output, err := exec.Command("node", "-e",
		`const fs=require("node:fs"),crypto=require("node:crypto");const b=fs.readFileSync("testdata/canonical.json");process.stdout.write(crypto.createHash("sha256").update(b.subarray(0,b.length-1)).digest("hex"));`).CombinedOutput()
	if err != nil || string(output) != digest {
		t.Fatalf("independent fixture hash differs: %s, %s, %v", output, digest, err)
	}
	if !bytes.Equal(before, wire(t, d)) {
		t.Fatal("Digest mutated caller input")
	}
}

func TestEachCategoryAndState(t *testing.T) {
	for index, category := range sample().Categories {
		for _, state := range []State{Present, Unavailable, Incomplete, Redacted, Conflicting} {
			t.Run(string(category.Category)+"/"+string(state), func(t *testing.T) {
				d := sample()
				r := &d.Categories[index]
				r.State = state
				switch state {
				case Unavailable:
					r.ObservedCount, r.Reason = nil, NotRecorded
				case Incomplete:
					r.ObservedCount, r.Reason = nil, PartialCapture
				case Redacted:
					r.ObservedCount, r.Reason = nil, Privacy
				case Conflicting:
					r.ObservedCount, r.Reason = nil, SourceConflict
				}
				encoded, err := Encode(d)
				if err != nil {
					t.Fatal(err)
				}
				got, err := Parse(encoded)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, result := range got.Categories {
					if result.Category == r.Category {
						found = true
						if result.State != state || result.Reason != r.Reason ||
							!reflect.DeepEqual(result.ObservedCount, r.ObservedCount) || result.Artifacts == nil {
							t.Fatalf("category/state/presence lost: %+v", result)
						}
					}
				}
				if !found || len(got.Categories) != 6 {
					t.Fatal("missing category")
				}
			})
		}
	}
}

func TestEmbeddedSchemaEnforcesEveryCategoryAndLimit(t *testing.T) {
	schema, err := evidenceSchema()
	if err != nil {
		t.Fatal(err)
	}
	validate := func(d Document) error {
		t.Helper()
		value, err := jsonutil.Parse(wire(t, d))
		if err != nil {
			t.Fatal(err)
		}
		return schema.Validate(value)
	}
	d := sample()
	if err := validate(d); err != nil {
		t.Fatal(err)
	}
	for i := range d.Categories {
		mutated := sample()
		mutated.Categories[i] = mutated.Categories[(i+1)%6]
		if err := validate(mutated); err == nil {
			t.Fatalf("schema-only consumer admitted missing category %s", d.Categories[i].Category)
		}
	}
	for _, tc := range []struct {
		reason Reason
		bound  int64
	}{
		{ArtifactCountLimit, 128},
		{ObjectSizeLimit, artifactmanifest.MaxArtifactBytes},
		{TotalSizeLimit, artifactmanifest.MaxTotalBytes},
	} {
		d := sample()
		r := &d.Categories[0]
		r.State, r.Reason, r.ObservedCount = Incomplete, tc.reason, nil
		r.Limit = &Limit{Observed: tc.bound + 1}
		if err := validate(d); err != nil {
			t.Fatalf("schema rejected alternative-valid exceeded bound: %v", err)
		}
		r.Limit.Observed = tc.bound
		if err := validate(d); err == nil {
			t.Fatalf("schema admitted a limit not exceeded: %s", tc.reason)
		}
	}
}

func TestSchemaMissingCategoryRedGreenControl(t *testing.T) {
	var schemaDoc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(schemas.ExecutionEvidenceSchemaJSON), &schemaDoc); err != nil {
		t.Fatal(err)
	}
	var properties, categories map[string]json.RawMessage
	if err := json.Unmarshal(schemaDoc["properties"], &properties); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(properties["categories"], &categories); err != nil {
		t.Fatal(err)
	}
	if _, exists := categories["allOf"]; !exists {
		t.Fatal("setup must remove the actual completeness constraints")
	}
	delete(categories, "allOf")
	properties["categories"] = wire(t, categories)
	schemaDoc["properties"] = wire(t, properties)
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-evidence-1.0.schema.json": string(wire(t, schemaDoc)),
		"execution-attempt-1.0.schema.json":  schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":  schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":    schemas.RunnerTerminalSchemaJSON,
	} {
		resource, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, resource); err != nil {
			t.Fatal(err)
		}
	}
	mutated, err := compiler.Compile("https://microsoft.github.io/waza/schemas/execution-evidence-1.0.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	d := sample()
	for i := range d.Categories {
		d.Categories[i].Category = Conversation
	}
	value, err := jsonutil.Parse(wire(t, d))
	if err != nil {
		t.Fatal(err)
	}
	if err := mutated.Validate(value); err != nil {
		t.Fatalf("RED setup must reproduce schema-only missing-category admission: %v", err)
	}
	actual, err := evidenceSchema()
	if err != nil {
		t.Fatal(err)
	}
	if err := actual.Validate(value); err == nil {
		t.Fatal("GREEN schema must reject the same six-conversation document")
	}
}

func TestPresenceAndWrongWireTypes(t *testing.T) {
	root := map[string]json.RawMessage{}
	if err := json.Unmarshal(wire(t, sample()), &root); err != nil {
		t.Fatal(err)
	}
	var categories []map[string]json.RawMessage
	if err := json.Unmarshal(root["categories"], &categories); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct {
		name string
		keys []string
		obj  map[string]json.RawMessage
	}{
		{"document", []string{"kind", "schema_version", "publication", "origin_manifest", "categories", "lineage"}, root},
		{"record", []string{"category", "state", "source", "context", "artifacts", "observed_count"}, categories[0]},
	} {
		for _, key := range scope.keys {
			original := scope.obj[key]
			for _, replacement := range []json.RawMessage{nil, []byte("null"), []byte("false"), []byte(`{}`), []byte(`1.5`)} {
				t.Run(scope.name+"/"+key+"/"+string(replacement), func(t *testing.T) {
					if replacement == nil {
						delete(scope.obj, key)
					} else {
						scope.obj[key] = replacement
					}
					if scope.name == "record" {
						root["categories"] = wire(t, categories)
					}
					reject(t, wire(t, root))
					scope.obj[key] = original
					root["categories"] = wire(t, categories)
				})
			}
		}
	}
	for _, scope := range []map[string]json.RawMessage{root, categories[0]} {
		scope["payload"] = []byte(`"restricted text must not be persisted"`)
		root["categories"] = wire(t, categories)
		reject(t, wire(t, root))
		delete(scope, "payload")
	}
}

func TestMalformedAndAmbiguousJSON(t *testing.T) {
	data := wire(t, sample())
	for _, mutated := range [][]byte{
		append(slices.Clone(data), []byte("{}")...),
		bytes.Replace(data, []byte(`"kind":`), []byte(`"kind":"waza.execution-evidence","kind":`), 1),
		bytes.Replace(data, []byte(`"state":`), []byte(`"\u0073tate":"present","state":`), 1),
		bytes.Replace(data, []byte(`"present"`), []byte(`"Present"`), 1),
		bytes.Replace(data, []byte(`"present"`), []byte(`"\ud800"`), 1),
		bytes.Replace(data, []byte(`"present"`), []byte{'"', 0xff, '"'}, 1),
		bytes.Replace(data, []byte(`"observed_count":0`), []byte(`"observed_count":9223372036854775808`), 1),
		bytes.Replace(data, []byte(`"observed_count":0`), []byte(`"observed_count":-1`), 1),
		bytes.Repeat([]byte(" "), MaxDocumentBytes+1),
	} {
		reject(t, mutated)
	}
	for _, tc := range []struct {
		name string
		edit func(*Document)
	}{
		{"missing category", func(d *Document) { d.Categories = d.Categories[:5] }},
		{"duplicate category", func(d *Document) { d.Categories[0] = d.Categories[1] }},
		{"empty positive", func(d *Document) { d.Categories[0].ObservedCount = intPointer(1) }},
		{"zero with reference", func(d *Document) { d.Categories[0].Artifacts = []string{"terminal"} }},
		{"present reason", func(d *Document) { d.Categories[0].Reason = PartialCapture }},
		{"unavailable refs", func(d *Document) {
			d.Categories[0] = Record{Category: Conversation, State: Unavailable, Source: Unknown, Context: UnknownContext, Reason: NotRecorded, Artifacts: []string{"secret"}}
		}},
		{"redacted refs", func(d *Document) {
			d.Categories[0] = Record{Category: Conversation, State: Redacted, Source: Unknown, Context: UnknownContext, Reason: Privacy, Artifacts: []string{"secret"}}
		}},
		{"self lineage", func(d *Document) { d.Lineage = []Lineage{{d.OriginManifest, Retry}} }},
		{"duplicate lineage", func(d *Document) {
			link := Lineage{strings.Repeat("b", 64), Cache}
			d.Lineage = []Lineage{link, link}
		}},
		{"overcount references", func(d *Document) {
			for i := range 129 {
				d.Categories[0].Artifacts = append(d.Categories[0].Artifacts, fmt.Sprintf("artifact-%d", i))
			}
			d.Categories[0].ObservedCount = intPointer(129)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := sample()
			tc.edit(&d)
			reject(t, wire(t, d))
			if _, err := Encode(d); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Encode accepted invalid typed document: %v", err)
			}
		})
	}
}

func TestObservedLimitBoundsAndUnknownTotals(t *testing.T) {
	for _, tc := range []struct {
		reason Reason
		bound  int64
	}{
		{ArtifactCountLimit, 128},
		{ObjectSizeLimit, artifactmanifest.MaxArtifactBytes},
		{TotalSizeLimit, artifactmanifest.MaxTotalBytes},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			for _, exact := range []bool{false, true} {
				d := sample()
				r := &d.Categories[0]
				r.State, r.Reason, r.ObservedCount = Incomplete, tc.reason, nil
				r.Limit = &Limit{Observed: tc.bound + 1}
				if exact {
					r.Limit.Total = intPointer(tc.bound + 1)
				}
				encoded, err := Encode(d)
				if err != nil {
					t.Fatal(err)
				}
				got, err := Parse(encoded)
				if err != nil || !reflect.DeepEqual(got.Categories[0].Limit, r.Limit) || got.Categories[0].State != Incomplete {
					t.Fatalf("degraded limit/absence changed: %+v, %v", got, err)
				}
				r.Limit.Observed = tc.bound
				reject(t, wire(t, d))
				r.Limit.Observed = tc.bound + 1
				r.Limit.Total = intPointer(tc.bound)
				reject(t, wire(t, d))
				r.Limit.Total = nil
				r.State = Present
				r.ObservedCount = intPointer(0)
				reject(t, wire(t, d))
			}
		})
	}
}

func TestOptionalPresenceAndTypedBounds(t *testing.T) {
	for _, field := range []string{"reason", "observed_count", "limit"} {
		for _, value := range []json.RawMessage{[]byte("null"), []byte(`""`), []byte("false"), []byte("-1")} {
			var root map[string]json.RawMessage
			if err := json.Unmarshal(wire(t, sample()), &root); err != nil {
				t.Fatal(err)
			}
			var records []map[string]json.RawMessage
			if err := json.Unmarshal(root["categories"], &records); err != nil {
				t.Fatal(err)
			}
			records[0][field] = value
			root["categories"] = wire(t, records)
			reject(t, wire(t, root))
		}
	}
	for _, state := range []State{Unavailable, Redacted, Conflicting, Incomplete} {
		d := sample()
		r := &d.Categories[0]
		r.State = state
		r.ObservedCount = nil
		reject(t, wire(t, d)) // A degraded state always needs a structured reason.
		r.Reason = NotRecorded
		if state != Unavailable {
			reject(t, wire(t, d))
		}
		if state == Unavailable || state == Redacted {
			if state == Redacted {
				r.Reason = Privacy
			}
			r.ObservedCount = intPointer(0)
			reject(t, wire(t, d))
		}
	}
	for _, edit := range []func(*Document){
		func(d *Document) { d.Categories = nil },
		func(d *Document) { d.Lineage = make([]Lineage, 129) },
		func(d *Document) { d.Categories[0].Artifacts = make([]string, 129) },
		func(d *Document) { d.Categories[0].Artifacts = []string{strings.Repeat("x", 129)} },
		func(d *Document) { d.Categories[0].Artifacts = []string{string([]byte{0xff})} },
	} {
		d := sample()
		edit(&d)
		before := wire(t, d)
		if _, err := Encode(d); !errors.Is(err, ErrInvalid) {
			t.Fatalf("typed bounds accepted: %v", err)
		}
		if !bytes.Equal(before, wire(t, d)) {
			t.Fatal("rejected Encode mutated input")
		}
	}
}

func TestPositiveReferencesAndCanonicalOrdering(t *testing.T) {
	d := sample()
	for i := range d.Categories {
		d.Categories[i].Artifacts = []string{"z", "a"}
		d.Categories[i].ObservedCount = intPointer(1)
	}
	d.Lineage = []Lineage{
		{strings.Repeat("c", 64), Resume},
		{strings.Repeat("b", 64), Retry},
		{strings.Repeat("b", 64), Cache},
	}
	before := wire(t, d)
	encoded, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Categories[0].Artifacts, []string{"a", "z"}) ||
		got.Lineage[0].Relationship != Cache || got.Lineage[1].Relationship != Retry ||
		got.Lineage[2].Manifest != strings.Repeat("c", 64) {
		t.Fatalf("canonical sorting lost: %+v", got)
	}
	if !bytes.Equal(before, wire(t, d)) {
		t.Fatal("nested slices mutated")
	}
	d.Categories[0].Artifacts = []string{"a", "a"}
	reject(t, wire(t, d))
}

func sourceFixture(t *testing.T) (Document, map[string][]byte, map[string][]byte, artifactmanifest.Manifest) {
	t.Helper()
	digest := strings.Repeat("a", 64)
	binding := artifactmanifest.Binding{
		Identity: runneridentity.Identity{
			Version: runneridentity.Version, CanonicalDigest: digest,
			Run: "run-" + digest, Shard: "shard-" + digest, Task: "task-" + digest,
			Trial: "trial-" + digest, Attempt: "attempt-" + digest,
		},
		Selection:     runnercontract.Selection{Shard: "only", Task: "task", Trial: 2, Attempt: 3},
		ExecutionMode: "mock", GraderIDs: []string{"text"},
	}
	terminal := runnerterminal.Envelope{
		Kind: "waza.runner-terminal", SchemaVersion: runnerterminal.Version,
		Outcome: runnerterminal.Success, ExitCode: 0, FailureBoundary: "none", Validation: "valid",
		EngineCalls: 1, Selection: &binding.Selection, Identity: &binding.Identity,
		Observation: &runnerterminal.Observation{
			ExecutionMode: "mock", EngineCalls: 1, Selection: binding.Selection, RunNumber: 2, Attempts: 3,
			Status: "passed", Validations: []runnerterminal.Verdict{{Identifier: "text", Passed: true}},
		},
		Diagnostics: []runnerterminal.Diagnostic{},
	}
	terminalBytes, err := runnerterminal.Encode(terminal, binding.TerminalBinding())
	if err != nil {
		t.Fatal(err)
	}
	// Payloads are producer-unintegrated authored controls, not live evidence.
	objects := map[string][]byte{"terminal": terminalBytes, "recorded": []byte(`{"ai_credits":0,"value":0.12345678901234567}`)}
	m := artifactmanifest.Manifest{
		Kind: "waza.artifact-manifest", SchemaVersion: artifactmanifest.Version, Binding: binding,
		TerminalArtifact: "terminal", Artifacts: []artifactmanifest.Artifact{},
	}
	for name, data := range objects {
		m.Artifacts = append(m.Artifacts, artifactmanifest.Artifact{
			Name: name, SHA256: artifactmanifest.Hash(data), SizeBytes: int64(len(data)), MediaType: "application/json",
		})
	}
	manifestBytes, err := artifactmanifest.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	d := sample()
	d.OriginManifest = artifactmanifest.Hash(manifestBytes)
	for i := range d.Categories {
		d.Categories[i].ObservedCount = intPointer(1)
		d.Categories[i].Artifacts = []string{"recorded", "terminal"}
	}
	return d, map[string][]byte{d.OriginManifest: manifestBytes}, objects, m
}

func TestSourceVerificationDoesNotAttestCompletenessOrRewriteVerdict(t *testing.T) {
	for index, category := range sample().Categories {
		t.Run(string(category.Category), func(t *testing.T) {
			d, sources, objects, _ := sourceFixture(t)
			before := wire(t, d)
			originalTerminal := slices.Clone(objects["terminal"])
			originalManifest := slices.Clone(sources[d.OriginManifest])
			if err := VerifySources(d, sources, objects); err != nil {
				t.Fatal(err)
			}
			for _, state := range []State{Unavailable, Incomplete, Redacted, Conflicting} {
				r := &d.Categories[index]
				r.State, r.ObservedCount, r.Artifacts = state, nil, []string{}
				switch state {
				case Unavailable:
					r.Reason = NotRecorded
				case Incomplete:
					r.Reason = PartialCapture
				case Redacted:
					r.Reason = Privacy
				case Conflicting:
					r.Reason = SourceConflict
				}
				if err := VerifySources(d, sources, objects); err != nil {
					t.Fatalf("source integrity is independent of declared %s: %v", state, err)
				}
				encoded, err := Encode(d)
				if err != nil || bytes.Contains(encoded, []byte(`"ai_credits"`)) || bytes.Contains(encoded, []byte(`"passed"`)) {
					t.Fatalf("payload/verdict copied into sidecar: %s, %v", encoded, err)
				}
				if !bytes.Equal(objects["terminal"], originalTerminal) || !bytes.Equal(sources[d.OriginManifest], originalManifest) {
					t.Fatal("derived declaration mutated finalized artifacts")
				}
			}
			if bytes.Equal(before, wire(t, d)) {
				t.Fatal("degradation control did not change evidence")
			}
			if !bytes.Contains(originalTerminal, []byte(`"passed":true`)) {
				t.Fatal("fixture must have a recorded passing verdict independent of degraded evidence")
			}
		})
	}
}

func TestSourceCorruptionAndLineageControls(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Document, map[string][]byte, map[string][]byte, artifactmanifest.Manifest)
		want error
	}{
		{"missing origin", func(d *Document, m, _ map[string][]byte, _ artifactmanifest.Manifest) { delete(m, d.OriginManifest) }, artifactmanifest.ErrMissing},
		{"corrupt origin", func(d *Document, m, _ map[string][]byte, _ artifactmanifest.Manifest) {
			m[d.OriginManifest] = []byte("{}")
		}, artifactmanifest.ErrCorrupt},
		{"noncanonical origin", func(d *Document, m, _ map[string][]byte, original artifactmanifest.Manifest) {
			data := append(wire(t, original), '\n')
			d.OriginManifest = artifactmanifest.Hash(data)
			m[d.OriginManifest] = data
		}, artifactmanifest.ErrCorrupt},
		{"missing bytes", func(_ *Document, _, a map[string][]byte, _ artifactmanifest.Manifest) { delete(a, "recorded") }, artifactmanifest.ErrMissing},
		{"same size corrupt bytes", func(_ *Document, _, a map[string][]byte, _ artifactmanifest.Manifest) {
			a["recorded"] = bytes.Replace(a["recorded"], []byte(`"ai_credits":0`), []byte(`"ai_credits":1`), 1)
		}, artifactmanifest.ErrCorrupt},
		{"length mismatch", func(_ *Document, _, a map[string][]byte, _ artifactmanifest.Manifest) {
			a["recorded"] = append(a["recorded"], '\n')
		}, artifactmanifest.ErrCorrupt},
		{"undeclared reference", func(d *Document, _, _ map[string][]byte, _ artifactmanifest.Manifest) {
			d.Categories[0].Artifacts = []string{"absent"}
		}, artifactmanifest.ErrConflict},
		{"missing lineage", func(d *Document, _, _ map[string][]byte, _ artifactmanifest.Manifest) {
			d.Lineage = []Lineage{{strings.Repeat("b", 64), Retry}}
		}, artifactmanifest.ErrMissing},
		{"bound terminal disagreement", func(d *Document, m, _ map[string][]byte, original artifactmanifest.Manifest) {
			original.Binding.Selection.Task = "another"
			data, err := artifactmanifest.Encode(original)
			if err != nil {
				t.Fatal(err)
			}
			d.OriginManifest = artifactmanifest.Hash(data)
			m[d.OriginManifest] = data
		}, runnerterminal.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, manifests, artifacts, origin := sourceFixture(t)
			tc.edit(&d, manifests, artifacts, origin)
			if err := VerifySources(d, manifests, artifacts); !errors.Is(err, tc.want) {
				t.Fatalf("VerifySources = %v, want %v", err, tc.want)
			}
		})
	}
	for _, relationship := range []Relationship{Retry, Resume, Cache, Aggregation} {
		t.Run(string(relationship), func(t *testing.T) {
			d, manifests, artifacts, origin := sourceFixture(t)
			origin.Binding.Selection.Task = "earlier"
			data, err := artifactmanifest.Encode(origin)
			if err != nil {
				t.Fatal(err)
			}
			digest := artifactmanifest.Hash(data)
			d.Lineage = []Lineage{{digest, relationship}}
			manifests[digest] = data
			if err := VerifySources(d, manifests, artifacts); err != nil {
				t.Fatal(err)
			}
			manifests[digest] = append(data, '\n')
			if err := VerifySources(d, manifests, artifacts); !errors.Is(err, artifactmanifest.ErrCorrupt) {
				t.Fatalf("corrupt ancestor accepted: %v", err)
			}
		})
	}
}

func TestManifestCapsCannotBeAdmittedByDegradation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason Reason
		edit   func(*artifactmanifest.Manifest)
	}{
		{"artifact count", ArtifactCountLimit, func(m *artifactmanifest.Manifest) {
			for i := range 127 {
				m.Artifacts = append(m.Artifacts, artifactmanifest.Artifact{
					Name: fmt.Sprintf("extra-%d", i), SHA256: artifactmanifest.Hash(nil), MediaType: "application/json",
				})
			}
		}},
		{"object bytes", ObjectSizeLimit, func(m *artifactmanifest.Manifest) {
			m.Artifacts = append(m.Artifacts, artifactmanifest.Artifact{
				Name: "oversize", SHA256: artifactmanifest.Hash(nil), SizeBytes: artifactmanifest.MaxArtifactBytes + 1, MediaType: "application/json",
			})
		}},
		{"total bytes", TotalSizeLimit, func(m *artifactmanifest.Manifest) {
			for i := range 5 {
				m.Artifacts = append(m.Artifacts, artifactmanifest.Artifact{
					Name: fmt.Sprintf("extra-%d", i), SHA256: artifactmanifest.Hash(nil), SizeBytes: artifactmanifest.MaxArtifactBytes, MediaType: "application/json",
				})
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, manifests, artifacts, origin := sourceFixture(t)
			tc.edit(&origin)
			data := wire(t, origin)
			d.OriginManifest = artifactmanifest.Hash(data)
			manifests[d.OriginManifest] = data
			r := &d.Categories[0]
			r.State, r.Reason = Incomplete, tc.reason
			r.Limit = &Limit{Observed: artifactmanifest.MaxTotalBytes + 1}
			if _, err := Encode(d); err != nil {
				t.Fatalf("degraded document must be valid independently of bad origin: %v", err)
			}
			if err := VerifySources(d, manifests, artifacts); !errors.Is(err, artifactmanifest.ErrCorrupt) {
				t.Fatalf("degradation admitted over-bound manifest: %v", err)
			}
		})
	}
}

func attemptSourceFixture(t *testing.T) (Document, map[string][]byte, map[string][]byte, artifactmanifest.Manifest) {
	t.Helper()
	d, manifests, artifacts, origin := sourceFixture(t)
	executionID, err := executionattempt.NewExecutionID()
	if err != nil {
		t.Fatal(err)
	}
	attempt := executionattempt.Attempt{
		Kind: "waza.execution-attempt", SchemaVersion: executionattempt.Version,
		Logical:     executionattempt.LogicalFrom(origin.Binding.Identity),
		ExecutionID: executionID, Binding: origin.Binding,
	}
	data, err := executionattempt.EncodeAttempt(attempt)
	if err != nil {
		t.Fatal(err)
	}
	d.AttemptArtifact = "attempt"
	d.ExecutionSource = &executionattempt.Source{ExecutionID: executionID}
	artifacts["attempt"] = data
	origin.Artifacts = append(origin.Artifacts, artifactmanifest.Artifact{
		Name: "attempt", SHA256: artifactmanifest.Hash(data), SizeBytes: int64(len(data)), MediaType: "application/json",
	})
	refreshAttemptSource(t, &d, manifests, artifacts, &origin)
	return d, manifests, artifacts, origin
}

func refreshAttemptSource(t *testing.T, d *Document, manifests, artifacts map[string][]byte, origin *artifactmanifest.Manifest) {
	t.Helper()
	for i := range origin.Artifacts {
		if origin.Artifacts[i].Name == "attempt" {
			origin.Artifacts[i].SHA256 = artifactmanifest.Hash(artifacts["attempt"])
			origin.Artifacts[i].SizeBytes = int64(len(artifacts["attempt"]))
		}
	}
	data, err := artifactmanifest.Encode(*origin)
	if err != nil {
		t.Fatal(err)
	}
	d.OriginManifest = artifactmanifest.Hash(data)
	d.ExecutionSource.ManifestDigest = d.OriginManifest
	manifests[d.OriginManifest] = data
}

func TestActualAttemptSourcePresenceAndVerification(t *testing.T) {
	d, manifests, artifacts, _ := attemptSourceFixture(t)
	before := wire(t, d)
	encoded, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(encoded)
	if err != nil || !reflect.DeepEqual(parsed.ExecutionSource, d.ExecutionSource) || parsed.AttemptArtifact != "attempt" {
		t.Fatalf("actual source contract was not preserved: %+v, %v", parsed, err)
	}
	if err := VerifySources(*parsed, manifests, artifacts); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, wire(t, d)) {
		t.Fatal("Encode/verification mutated source reference")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"execution_source", "attempt_artifact"} {
		original := root[field]
		for _, replacement := range []json.RawMessage{nil, []byte("null"), []byte("false"), []byte(`""`)} {
			if replacement == nil {
				delete(root, field)
			} else {
				root[field] = replacement
			}
			reject(t, wire(t, root))
			root[field] = original
		}
	}
	d.ExecutionSource.ManifestDigest = strings.Repeat("f", 64)
	reject(t, wire(t, d))
}

func TestActualAttemptSourceRejectsUnverifiedAndRetaggedDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Document, map[string][]byte, map[string][]byte, *artifactmanifest.Manifest)
		want error
	}{
		{"missing descriptor", func(_ *Document, _, a map[string][]byte, _ *artifactmanifest.Manifest) {
			delete(a, "attempt")
		}, artifactmanifest.ErrMissing},
		{"undeclared descriptor", func(d *Document, _, _ map[string][]byte, _ *artifactmanifest.Manifest) {
			d.AttemptArtifact = "absent"
		}, artifactmanifest.ErrConflict},
		{"wrong execution ID", func(d *Document, _, _ map[string][]byte, _ *artifactmanifest.Manifest) {
			d.ExecutionSource.ExecutionID = "exec-" + strings.Repeat("f", 64)
		}, artifactmanifest.ErrConflict},
		{"noncanonical descriptor", func(d *Document, m, a map[string][]byte, origin *artifactmanifest.Manifest) {
			a["attempt"] = append(a["attempt"], '\n')
			refreshAttemptSource(t, d, m, a, origin)
		}, artifactmanifest.ErrCorrupt},
		{"invalid descriptor", func(d *Document, m, a map[string][]byte, origin *artifactmanifest.Manifest) {
			a["attempt"] = []byte(`{}`)
			refreshAttemptSource(t, d, m, a, origin)
		}, artifactmanifest.ErrCorrupt},
		{"oversize descriptor", func(d *Document, m, a map[string][]byte, origin *artifactmanifest.Manifest) {
			a["attempt"] = bytes.Repeat([]byte(" "), executionattempt.MaxSidecarBytes+1)
			refreshAttemptSource(t, d, m, a, origin)
		}, artifactmanifest.ErrConflict},
		{"wrong media type", func(d *Document, m, a map[string][]byte, origin *artifactmanifest.Manifest) {
			for i := range origin.Artifacts {
				if origin.Artifacts[i].Name == "attempt" {
					origin.Artifacts[i].MediaType = "text/plain"
				}
			}
			refreshAttemptSource(t, d, m, a, origin)
		}, artifactmanifest.ErrConflict},
		{"retagged binding", func(d *Document, m, a map[string][]byte, origin *artifactmanifest.Manifest) {
			attempt, err := executionattempt.ParseAttempt(a["attempt"])
			if err != nil {
				t.Fatal(err)
			}
			attempt.Binding.Selection.Attempt++
			a["attempt"], err = executionattempt.EncodeAttempt(*attempt)
			if err != nil {
				t.Fatal(err)
			}
			refreshAttemptSource(t, d, m, a, origin)
		}, artifactmanifest.ErrConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, manifests, artifacts, origin := attemptSourceFixture(t)
			tc.edit(&d, manifests, artifacts, &origin)
			if err := VerifySources(d, manifests, artifacts); !errors.Is(err, tc.want) {
				t.Fatalf("unverified attempt reference admitted: %v, want %v", err, tc.want)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{}`))
	fixture, err := os.ReadFile("testdata/canonical.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := Parse(data)
		if err != nil {
			if d != nil {
				t.Fatal("Parse returned invalid partial document")
			}
			return
		}
		encoded, err := Encode(*d)
		if err != nil {
			t.Fatalf("accepted document cannot encode: %v", err)
		}
		again, err := Parse(encoded)
		if err != nil {
			t.Fatal(err)
		}
		reencoded, err := Encode(*again)
		if err != nil || !bytes.Equal(encoded, reencoded) {
			t.Fatalf("canonical profile is unstable: %v", err)
		}
	})
}
