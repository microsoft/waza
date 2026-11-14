package inventory_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/inventory"
	"github.com/microsoft/waza/internal/provenance"
)

const (
	imageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	otherDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	imageRepo   = "ghcr.io/microsoft/waza-runner"
)

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// imageInventory builds a runner image inventory from this test binary (a
// real Go binary with build information) and the Debian fixtures.
func imageInventory(t *testing.T, digest string) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	goComponents, err := inventory.GoBinaryComponents(self)
	if err != nil {
		t.Fatal(err)
	}
	osRelease := read(t, "testdata/os-release")
	osComponents, err := inventory.OSComponents(osRelease)
	if err != nil {
		t.Fatal(err)
	}
	id, version := inventory.OSRelease(osRelease)
	debs, err := inventory.DpkgComponents(read(t, "testdata/dpkg-status"), id, version)
	if err != nil {
		t.Fatal(err)
	}
	data, err := inventory.Generate(inventory.GenerateInput{
		SubjectKind: inventory.SubjectImage, SubjectName: imageRepo + ":0.42.0", SubjectDigest: digest,
		Components: append(append(goComponents, osComponents...), debs...),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func verify(t *testing.T, data []byte, expect inventory.Expectation) *inventory.Report {
	t.Helper()
	report := inventory.Verify(data, expect)
	if _, err := inventory.EncodeReport(report); err != nil {
		t.Fatalf("report violates schema: %v\n%+v", err, report)
	}
	return report
}

func code(r *inventory.Report) string {
	if failure, ok := r.FirstFailure(); ok {
		return failure.Code
	}
	return ""
}

func edit(t *testing.T, data []byte, change func(map[string]any)) []byte {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	change(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func metadataComponent(doc map[string]any) map[string]any {
	return obj(obj(doc["metadata"])["component"])
}

func TestImageInventoryRoundTrip(t *testing.T) {
	data := imageInventory(t, imageDigest)
	if again := imageInventory(t, imageDigest); !bytes.Equal(data, again) {
		t.Fatal("generation is not deterministic")
	}
	report := verify(t, data, inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest, Name: imageRepo})
	if report.Status != "verified" || report.ExitCode != 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.Subject.Name != imageRepo || report.Subject.Digest != imageDigest ||
		!strings.HasPrefix(report.Subject.PURL, "pkg:oci/waza-runner@sha256%3A1111") {
		t.Fatalf("subject = %+v", report.Subject)
	}
	s := report.Summary
	if s.ByType["operating-system"] != 1 || s.ByType["file"] != 1 || s.ByEcosystem["deb"] != 5 || s.ByEcosystem["golang"] < 2 {
		t.Fatalf("summary = %+v", s)
	}
	for _, want := range []string{
		`"purl": "pkg:deb/debian/perl-base@1%3A5.36.0-7+deb12u2?arch=amd64\u0026distro=debian-12"`,
		`"purl": "pkg:golang/stdlib@`,
		`"name": "debian"`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("inventory lacks %s", want)
		}
	}
	if bytes.Contains(data, []byte("removed-tool")) {
		t.Error("inventory lists a package that is not installed")
	}
}

func TestPluginInventoryBindsArtifactDigest(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "plugin.tgz")
	if err := os.WriteFile(plugin, []byte("plugin payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	member, err := inventory.FileComponent(plugin)
	if err != nil {
		t.Fatal(err)
	}
	digest := inventory.DigestOf([]byte("plugin payload"))
	data, err := inventory.Generate(inventory.GenerateInput{
		SubjectKind: inventory.SubjectPlugin, SubjectName: "example-plugin", SubjectDigest: digest,
		Components: []inventory.Component{member, {Type: "library", Name: "left-pad", Version: "1.3.0", PURL: "pkg:npm/left-pad@1.3.0"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r := verify(t, data, inventory.Expectation{Kind: inventory.SubjectPlugin, Digest: digest, Name: "example-plugin"}); r.Status != "verified" {
		t.Fatalf("plugin report = %+v", r)
	}
	altered := inventory.DigestOf([]byte("plugin payload, altered"))
	if r := verify(t, data, inventory.Expectation{Kind: inventory.SubjectPlugin, Digest: altered}); code(r) != "subject_digest_mismatch" {
		t.Fatalf("altered plugin code = %q", code(r))
	}
	if r := verify(t, data, inventory.Expectation{Kind: inventory.SubjectImage, Digest: digest}); code(r) != "subject_kind_mismatch" {
		t.Fatalf("plugin inventory accepted as image: %q", code(r))
	}
}

func TestVerifyFailsClosed(t *testing.T) {
	valid := imageInventory(t, imageDigest)
	expect := inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest}
	cases := []struct {
		name   string
		data   []byte
		expect inventory.Expectation
		want   string
	}{
		{"missing", nil, expect, "inventory_missing"},
		{"malformed", []byte("{"), expect, "inventory_malformed"},
		{"duplicate key", bytes.Replace(valid, []byte(`"version": 1,`), []byte(`"version": 1, "version": 1,`), 1), expect, "inventory_malformed"},
		{"extra field", edit(t, valid, func(d map[string]any) { d["vulnerabilities"] = []any{} }), expect, "inventory_malformed"},
		{"too large", make([]byte, inventory.MaxInventoryBytes+1), expect, "inventory_too_large"},
		{"other profile", edit(t, valid, func(d map[string]any) {
			obj(list(obj(d["metadata"])["properties"])[0])["value"] = "other/1.0"
		}), expect, "profile_unsupported"},
		{"unbound", edit(t, valid, func(d map[string]any) { delete(metadataComponent(d), "hashes") }), expect, "subject_unbound"},
		{"subject digest altered", edit(t, valid, func(d map[string]any) {
			obj(list(metadataComponent(d)["hashes"])[0])["content"] = strings.TrimPrefix(otherDigest, "sha256:")
		}), expect, "subject_purl_mismatch"},
		{"subject kind altered", edit(t, valid, func(d map[string]any) { metadataComponent(d)["type"] = "application" }), expect, "subject_kind_invalid"},
		{"mismatched image", valid, inventory.Expectation{Kind: inventory.SubjectImage, Digest: otherDigest}, "subject_digest_mismatch"},
		{"mismatched repository", valid, inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest, Name: "ghcr.io/other/runner"}, "subject_name_mismatch"},
		{"unverifiable image", valid, inventory.Expectation{Kind: inventory.SubjectImage}, "expected_digest_missing"},
		{"tag instead of digest", valid, inventory.Expectation{Kind: inventory.SubjectImage, Digest: "latest"}, "expected_digest_invalid"},
		{"component altered", edit(t, valid, func(d map[string]any) {
			for _, c := range list(d["components"]) {
				if obj(c)["name"] == "bash" {
					obj(c)["purl"] = "pkg:deb/debian/bash@9.9?arch=amd64"
				}
			}
		}), expect, "component_ref_mismatch"},
		{"component version only altered", edit(t, valid, func(d map[string]any) {
			for _, c := range list(d["components"]) {
				if obj(c)["name"] == "bash" {
					obj(c)["version"] = "9.9"
				}
			}
		}), expect, "component_purl_mismatch"},
		{"component name only altered", edit(t, valid, func(d map[string]any) {
			for _, c := range list(d["components"]) {
				if obj(c)["name"] == "bash" {
					obj(c)["name"] = "zsh"
				}
			}
		}), expect, "component_purl_mismatch"},
		{"components removed", edit(t, valid, func(d map[string]any) { d["components"] = []any{} }), expect, "components_empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := verify(t, tc.data, tc.expect)
			if r.Status != "failed" || r.ExitCode != 1 || code(r) != tc.want {
				t.Fatalf("code = %q status = %s; want %s\n%+v", code(r), r.Status, tc.want, r.Checks)
			}
		})
	}
}

func TestPyPINamesNormalizeAgainstPURL(t *testing.T) {
	gen := func(purl string) error {
		_, err := inventory.Generate(inventory.GenerateInput{
			SubjectKind: inventory.SubjectImage, SubjectName: imageRepo, SubjectDigest: imageDigest,
			Components: []inventory.Component{{Type: "library", Name: "typing_extensions", Version: "4.12.2", PURL: purl}},
		})
		return err
	}
	if err := gen("pkg:pypi/typing-extensions@4.12.2"); err != nil {
		t.Fatalf("normalized PyPI name rejected: %v", err)
	}
	for _, purl := range []string{"pkg:pypi/typing-extension@4.12.2", "pkg:pypi/typing-extensions@4.12.3", "pkg:npm/typing-extensions@4.12.2"} {
		if err := gen(purl); err == nil || !strings.Contains(err.Error(), "component_purl_mismatch") {
			t.Errorf("%s: err = %v; want component_purl_mismatch", purl, err)
		}
	}
}

func TestGenerateRejectsConflictingDuplicates(t *testing.T) {
	gen := func(components ...inventory.Component) error {
		_, err := inventory.Generate(inventory.GenerateInput{SubjectKind: inventory.SubjectImage, SubjectName: imageRepo, SubjectDigest: imageDigest, Components: components})
		return err
	}
	a := inventory.Component{Type: "library", Name: "x", Version: "1", PURL: "pkg:npm/x@1", Hashes: []inventory.Hash{{Alg: "SHA-256", Content: strings.Repeat("a", 64)}}}
	b := a
	b.Hashes = []inventory.Hash{{Alg: "SHA-256", Content: strings.Repeat("b", 64)}}
	if err := gen(a, a); err != nil {
		t.Fatalf("exact repeat rejected: %v", err)
	}
	for _, order := range [][]inventory.Component{{a, b}, {b, a}} {
		if err := gen(order...); err == nil || !strings.Contains(err.Error(), "conflicting definitions") {
			t.Errorf("conflicting definitions accepted: %v", err)
		}
	}
}

func TestExpectedNamesAreComparedUnclipped(t *testing.T) {
	plugin := func(name string) []byte {
		data, err := inventory.Generate(inventory.GenerateInput{SubjectKind: inventory.SubjectPlugin, SubjectName: name, SubjectDigest: imageDigest,
			Components: []inventory.Component{{Type: "library", Name: "x", Version: "1"}}})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	clipped := plugin(strings.Repeat("a", 509) + "...")
	r := inventory.Verify(clipped, inventory.Expectation{Kind: inventory.SubjectPlugin, Digest: imageDigest, Name: strings.Repeat("a", 600)})
	if r.Status != "failed" || code(r) != "expected_name_invalid" || utf8.RuneCountInString(r.Expected.Name) > 512 {
		t.Fatalf("over-long name: status %s code %q", r.Status, code(r))
	}
	if _, err := inventory.EncodeReport(r); err != nil {
		t.Fatal(err)
	}
	wide := strings.Repeat("界", 200)
	r = inventory.Verify(plugin(wide), inventory.Expectation{Kind: inventory.SubjectPlugin, Digest: imageDigest, Name: wide})
	if r.Status != "verified" || r.Expected.Name != wide {
		t.Fatalf("multi-byte name: status %s code %q", r.Status, code(r))
	}
	if _, err := inventory.EncodeReport(r); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateRejectsUnboundInventories(t *testing.T) {
	component := []inventory.Component{{Type: "library", Name: "x", Version: "1"}}
	for name, in := range map[string]inventory.GenerateInput{
		"tag":           {SubjectKind: inventory.SubjectImage, SubjectName: imageRepo, SubjectDigest: "latest", Components: component},
		"no components": {SubjectKind: inventory.SubjectImage, SubjectName: imageRepo, SubjectDigest: imageDigest},
		"no name":       {SubjectKind: inventory.SubjectPlugin, SubjectDigest: imageDigest, Components: component},
		"bad kind":      {SubjectKind: "chart", SubjectName: "x", SubjectDigest: imageDigest, Components: component},
	} {
		if _, err := inventory.Generate(in); err == nil {
			t.Errorf("%s: generation succeeded", name)
		}
	}
}

func TestCycloneDXImportKeepsProfileFields(t *testing.T) {
	src := []byte(`{"bomFormat":"CycloneDX","specVersion":"1.5","components":[
	 {"type":"library","name":"openssl","version":"3.0.15","purl":"pkg:deb/debian/openssl@3.0.15","licenses":[{"license":{"id":"Apache-2.0"}}],
	  "hashes":[{"alg":"SHA-1","content":"aa"},{"alg":"SHA-256","content":"` + strings.Repeat("A", 64) + `"}]},
	 {"type":"device","name":"weird"}]}`)
	components, err := inventory.CycloneDXComponents(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 2 || components[0].Hashes[0].Content != strings.Repeat("a", 64) || components[1].Type != "library" {
		t.Fatalf("components = %+v", components)
	}
	if _, err := inventory.CycloneDXComponents([]byte(`{"bomFormat":"SPDX"}`)); err == nil {
		t.Fatal("imported a non-CycloneDX document")
	}
}

type provenanceFixture struct {
	input    provenance.GenerateInput
	signer   provenance.Signer
	verifier provenance.SignatureVerifier
}

func newProvenanceFixture(t *testing.T) provenanceFixture {
	t.Helper()
	privatePEM, publicPEM, _, err := provenance.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := provenance.NewLocalSigner(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := provenance.NewLocalVerifier(publicPEM)
	if err != nil {
		t.Fatal(err)
	}
	return provenanceFixture{
		input: provenance.GenerateInput{
			Request:         read(t, "../../examples/runner/unit.json"),
			Manifest:        read(t, "../artifactmanifest/testdata/publication-manifest.json"),
			Objects:         map[string][]byte{"terminal.json": read(t, "../artifactmanifest/testdata/publication-terminal.json")},
			Policy:          []byte("{\"policy\":\"release\"}\n"),
			PolicyName:      "policy.json",
			PolicyMediaType: "application/json",
			WazaVersion:     "0.42.0",
			ImageReference:  imageRepo + ":0.42.0",
			ImageDigest:     imageDigest,
		},
		signer: signer, verifier: verifier,
	}
}

func (f provenanceFixture) bundle(t *testing.T, binding []byte) map[string][]byte {
	t.Helper()
	in := f.input
	if binding != nil {
		in.Bindings = []provenance.BindingInput{{Kind: inventory.BindingKind, Name: "runner-inventory.cdx.json", MediaType: inventory.MediaType, Data: binding}}
	}
	bundle, err := provenance.Generate(context.Background(), in, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	return bundle.Files
}

// rebind replaces the bound inventory and re-signs the statement over it, as a
// compromised or careless signer could, so verification must catch the
// subject mismatch rather than a member digest mismatch.
func (f provenanceFixture) rebind(t *testing.T, files map[string][]byte, replacement []byte) {
	t.Helper()
	var e provenance.Envelope
	if err := json.Unmarshal(files[provenance.EnvelopePath], &e); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	var s provenance.Statement
	if err := json.Unmarshal(payload, &s); err != nil {
		t.Fatal(err)
	}
	ref := &s.Predicate.Bindings[0].Ref
	files[ref.Path] = replacement
	ref.SHA256 = strings.TrimPrefix(inventory.DigestOf(replacement), "sha256:")
	if payload, err = json.Marshal(s); err != nil {
		t.Fatal(err)
	}
	sig, err := f.signer.Sign(context.Background(), provenance.PAE(e.PayloadType, payload))
	if err != nil {
		t.Fatal(err)
	}
	e.Payload = base64.StdEncoding.EncodeToString(payload)
	e.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sig)
	if files[provenance.EnvelopePath], err = json.Marshal(e); err != nil {
		t.Fatal(err)
	}
}

func (f provenanceFixture) verify(t *testing.T, files map[string][]byte, require bool) *provenance.Report {
	t.Helper()
	src, err := provenance.MapSource(files)
	if err != nil {
		t.Fatal(err)
	}
	opts := provenance.Options{Verifier: f.verifier}
	if require {
		opts.RequireBindingKinds = []string{inventory.BindingKind}
	}
	report := provenance.Verify(src, opts)
	if _, err := provenance.EncodeReport(report); err != nil {
		t.Fatal(err)
	}
	return report
}

func bindingCheck(r *provenance.Report) provenance.Check {
	for _, c := range r.Checks {
		if c.ID == "bindings" {
			return c
		}
	}
	return provenance.Check{}
}

func TestProvenanceBindsInventoryToSignedImageDigest(t *testing.T) {
	f := newProvenanceFixture(t)
	valid := imageInventory(t, imageDigest)
	if r := f.verify(t, f.bundle(t, valid), true); r.Status != "verified" {
		t.Fatalf("bound inventory did not verify: %+v", r.Failures())
	}
	if r := f.verify(t, f.bundle(t, nil), true); bindingCheck(r).Code != "binding_required_missing" {
		t.Fatalf("missing inventory = %+v", bindingCheck(r))
	}
	if r := f.verify(t, f.bundle(t, nil), false); r.Status != "verified" {
		t.Fatal("inventory must stay optional unless required")
	}

	files := f.bundle(t, valid)
	f.rebind(t, files, imageInventory(t, otherDigest))
	if r := f.verify(t, files, true); bindingCheck(r).Code != "subject_digest_mismatch" {
		t.Fatalf("inventory for another image = %+v", bindingCheck(r))
	}

	files = f.bundle(t, valid)
	files["bindings/runner-inventory.cdx.json"] = bytes.Replace(valid, []byte(`"bash"`), []byte(`"dash"`), 1)
	if c := bindingCheck(f.verify(t, files, true)); c.Status != "failed" || !strings.HasPrefix(c.Code, "binding_digest_") {
		t.Fatalf("inventory altered after signing = %+v", c)
	}
}

func TestProvenanceGenerationRefusesUnboundInventories(t *testing.T) {
	f := newProvenanceFixture(t)
	plugin, err := inventory.Generate(inventory.GenerateInput{
		SubjectKind: inventory.SubjectPlugin, SubjectName: "p", SubjectDigest: imageDigest,
		Components: []inventory.Component{{Type: "library", Name: "x", Version: "1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"other image": imageInventory(t, otherDigest), "plugin": plugin} {
		in := f.input
		in.Bindings = []provenance.BindingInput{{Kind: inventory.BindingKind, Name: "inv.json", MediaType: inventory.MediaType, Data: data}}
		if _, err := provenance.Generate(context.Background(), in, f.signer); err == nil {
			t.Errorf("%s: signed a bundle binding an inventory for another subject", name)
		}
	}
	files := f.bundle(t, imageInventory(t, imageDigest))
	f.rebind(t, files, plugin)
	if r := f.verify(t, files, true); bindingCheck(r).Code != "subject_kind_mismatch" {
		t.Fatalf("plugin inventory bound as image evidence: %+v", bindingCheck(r))
	}
}

// TestLabFixturesMatchVerification keeps the Lab e2e inventory and reports in
// sync with the verifier. Regenerate with WAZA_UPDATE_LAB_FIXTURES=1.
func TestLabFixturesMatchVerification(t *testing.T) {
	const dir = "../../web/e2e/fixtures/inventory/"
	osRelease := read(t, "testdata/os-release")
	osComponents, err := inventory.OSComponents(osRelease)
	if err != nil {
		t.Fatal(err)
	}
	id, version := inventory.OSRelease(osRelease)
	debs, err := inventory.DpkgComponents(read(t, "testdata/dpkg-status"), id, version)
	if err != nil {
		t.Fatal(err)
	}
	const repo = "ghcr.io/microsoft/waza-runner-reference"
	runner, err := inventory.Generate(inventory.GenerateInput{
		SubjectKind: inventory.SubjectImage, SubjectName: repo + ":0.42.0", SubjectDigest: imageDigest,
		Components: append(osComponents, debs...),
	})
	if err != nil {
		t.Fatal(err)
	}
	altered := bytes.Replace(runner, []byte(`"bom-ref": "pkg:deb/debian/bash@`), []byte(`"bom-ref": "pkg:deb/debian/bash@9.9-`), 1)
	expect := inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest, Name: repo}
	encode := func(data []byte, expect inventory.Expectation) []byte {
		out, err := inventory.EncodeReport(inventory.Verify(data, expect))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	files := map[string][]byte{
		"runner.cdx.json":        runner,
		"altered.cdx.json":       altered,
		"verified-report.json":   encode(runner, expect),
		"mismatched-report.json": encode(runner, inventory.Expectation{Kind: inventory.SubjectImage, Digest: otherDigest}),
		"missing-report.json":    encode(nil, expect),
		"altered-report.json":    encode(altered, expect),
	}
	for name, want := range files {
		path := filepath.FromSlash(dir + name)
		if os.Getenv("WAZA_UPDATE_LAB_FIXTURES") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got := read(t, dir+name); !bytes.Equal(got, want) {
			t.Errorf("%s is stale; regenerate with WAZA_UPDATE_LAB_FIXTURES=1 go test ./internal/inventory", name)
		}
	}
}

func TestInvalidExpectedKindFailsClosed(t *testing.T) {
	report := inventory.Verify(imageInventory(t, imageDigest), inventory.Expectation{Kind: "container", Digest: imageDigest})
	if report.Status != "failed" || report.ExitCode != 1 {
		t.Fatalf("invalid kind verified: %+v", report)
	}
	if got := code(report); got != "expected_kind_invalid" {
		t.Fatalf("code = %q", got)
	}
	if _, err := inventory.EncodeReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestDiskInputsMustBeRegularFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := inventory.GoBinaryComponents(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory accepted as Go binary: %v", err)
	}
	if _, err := inventory.FileComponent(dir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory accepted as file: %v", err)
	}
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	component, err := inventory.FileComponent(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimPrefix(inventory.DigestOf([]byte("payload")), "sha256:"); component.Hashes[0].Content != want {
		t.Fatalf("hash = %s, want %s", component.Hashes[0].Content, want)
	}
}

func TestEmbeddedComponentsRequirePresenceInBinary(t *testing.T) {
	dir := t.TempDir()
	payload := []byte("copilot cli payload bytes")
	for name, data := range map[string][]byte{
		"zcopilot_1.0.105_linux_amd64.zst":                             payload,
		"zcopilotruntimewrapper_1.0.105_linux-x64_copilot-runtime.zst": []byte("not embedded"),
		"zcopilot_1.0.105_linux_amd64.license":                         []byte("license"),
		"binary":                                                       append(append([]byte("prefix"), payload...), "suffix"...),
		"other-binary":                                                 []byte("nothing here"),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	components, err := inventory.EmbeddedComponents(dir, []string{filepath.Join(dir, "binary")})
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 1 || components[0].Name != "github-copilot-cli-linux_amd64" || components[0].Version != "1.0.105" ||
		components[0].Hashes[0].Content != strings.TrimPrefix(inventory.DigestOf(payload), "sha256:") {
		t.Fatalf("components = %+v", components)
	}
	if _, err := inventory.EmbeddedComponents(dir, []string{filepath.Join(dir, "other-binary")}); err == nil {
		t.Fatal("inventory claimed a Copilot CLI that is not embedded")
	}
}

// obj and list are checked JSON accessors for test edits.
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func TestReportSchemaRejectsContradictoryEvidence(t *testing.T) {
	encoded, err := inventory.EncodeReport(inventory.Verify(imageInventory(t, imageDigest), inventory.Expectation{Kind: inventory.SubjectImage, Digest: imageDigest}))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"verified with a failed check":  func(r map[string]any) { obj(list(r["checks"])[4])["status"] = "failed" },
		"verified with a skipped check": func(r map[string]any) { obj(list(r["checks"])[1])["status"] = "skipped" },
		"duplicate check ids":           func(r map[string]any) { obj(list(r["checks"])[3])["id"] = "inventory" },
		"failed without a failed check": func(r map[string]any) { r["status"], r["exit_code"] = "failed", 1 },
	} {
		var report map[string]any
		if err := json.Unmarshal(encoded, &report); err != nil {
			t.Fatal(err)
		}
		mutate(report)
		data, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if inventory.ValidateReport(data) == nil {
			t.Errorf("%s: schema accepted the report", name)
		}
	}
}
