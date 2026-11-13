package provenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/runnerunit"
)

const testImageDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

type fixture struct {
	input     GenerateInput
	signer    Signer
	verifier  SignatureVerifier
	publicPEM []byte
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	privatePEM, publicPEM, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewLocalSigner(privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewLocalVerifier(publicPEM)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{
		input: GenerateInput{
			Request:         readFixture(t, "../../examples/runner/unit.json"),
			Manifest:        readFixture(t, "../artifactmanifest/testdata/publication-manifest.json"),
			Objects:         map[string][]byte{"terminal.json": readFixture(t, "../artifactmanifest/testdata/publication-terminal.json")},
			Policy:          []byte("{\"policy\":\"release\"}\n"),
			PolicyName:      "policy.json",
			PolicyMediaType: "application/json",
			WazaVersion:     "0.42.0",
			ImageReference:  "ghcr.io/microsoft/waza-runner:0.42.0",
			ImageDigest:     testImageDigest,
		},
		signer: signer, verifier: verifier, publicPEM: publicPEM,
	}
}

func (f fixture) bundle(t *testing.T) map[string][]byte {
	t.Helper()
	b, err := Generate(context.Background(), f.input, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	return maps.Clone(b.Files)
}

func verifyFiles(t *testing.T, files map[string][]byte, opts Options) *Report {
	t.Helper()
	src, err := MapSource(files)
	if err != nil {
		t.Fatal(err)
	}
	report := Verify(src, opts)
	if _, err := EncodeReport(report); err != nil {
		t.Fatalf("report violates schema: %v\n%+v", err, report)
	}
	return report
}

func failure(r *Report, id string) Check {
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	return Check{}
}

func TestGenerateAndVerifyDirectoryBundle(t *testing.T) {
	f := newFixture(t)
	b, err := Generate(context.Background(), f.input, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	if err := b.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteDir(dir); err == nil {
		t.Fatal("WriteDir overwrote an existing bundle")
	}
	src, closeSrc, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeSrc() }()
	report := Verify(src, Options{Verifier: f.verifier, ExpectImageDigest: testImageDigest, ExpectWazaVersion: "0.42.0", ExpectPolicySHA256: b.Statement.Predicate.Policy.SHA256})
	if report.Status != "verified" || report.ExitCode != 0 || !report.Signer.Trusted {
		t.Fatalf("expected verified bundle, got %+v", report.Failures())
	}
	p := report.Predicate
	if p.Inputs.Identity.CanonicalDigest != "46c427b5aa8bfbb61ab297b8e7b780c31ea7ca831ddaa633099498ed749e8dc5" ||
		p.Runtime.Executor != "mock" || p.Runtime.Model != "mock-model" || p.Outcome.Outcome != "success" ||
		p.Runner.APIVersion != "1.0" || len(p.Bindings) != 0 {
		t.Fatalf("unexpected claims: %+v", p)
	}
	for _, c := range report.Checks {
		if c.ID == "image" && c.Code != "pinned" {
			t.Fatalf("image expectation was not pinned: %+v", c)
		}
	}
	if _, err := EncodeReport(report); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyTamperFailsClosed(t *testing.T) {
	f := newFixture(t)
	tamper := func(files map[string][]byte, name string) {
		files[name] = append(bytes.Clone(files[name][:len(files[name])-2]), ' ', files[name][len(files[name])-1])
	}
	cases := []struct {
		name  string
		edit  func(map[string][]byte)
		check string
		code  string
	}{
		{"artifact", func(m map[string][]byte) { tamper(m, "artifacts/objects/terminal.json") }, "artifacts", "artifact_digest_mismatch"},
		{"input", func(m map[string][]byte) { tamper(m, RequestPath) }, "inputs", "input_digest_mismatch"},
		{"policy", func(m map[string][]byte) { tamper(m, "policy/policy.json") }, "policy", "policy_digest_mismatch"},
		{"manifest digest", func(m map[string][]byte) { tamper(m, ManifestPath) }, "artifacts", "manifest_digest_mismatch"},
		{"missing member", func(m map[string][]byte) { delete(m, RequestPath) }, "inputs", "input_digest_missing"},
		{"unbound member", func(m map[string][]byte) { m["notes.txt"] = []byte("x") }, "members", "member_unbound"},
		{"undeclared object", func(m map[string][]byte) { m["artifacts/objects/extra.json"] = []byte("{}") }, "artifacts", "artifact_undeclared"},
		{"signature", func(m map[string][]byte) {
			var e Envelope
			if err := json.Unmarshal(m[EnvelopePath], &e); err != nil {
				t.Fatal(err)
			}
			sig, _ := base64.StdEncoding.DecodeString(e.Signatures[0].Sig)
			sig[0] ^= 0xff
			e.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sig)
			m[EnvelopePath], _ = json.Marshal(e)
		}, "signature", "signature_invalid"},
		{"outcome", func(m map[string][]byte) {
			resignPredicate(t, f, m, func(p *Predicate) { p.Outcome.Outcome = "evaluation_failure"; p.Outcome.ExitCode = 1 })
		}, "outcome", "outcome_mismatch"},
		{"input identity", func(m map[string][]byte) {
			resignPredicate(t, f, m, func(p *Predicate) { p.Inputs.Identity.Run = "run-" + strings.Repeat("0", 64) })
		}, "inputs", "input_identity_mismatch"},
		{"runtime model", func(m map[string][]byte) { resignPredicate(t, f, m, func(p *Predicate) { p.Runtime.Model = "gpt-4o" }) }, "runtime", "runtime_mismatch"},
		{"blank waza version", func(m map[string][]byte) { resignPredicate(t, f, m, func(p *Predicate) { p.Waza.Version = " " }) }, "waza_version", "claim_missing"},
		{"bundle index", func(m map[string][]byte) { m[IndexPath] = []byte("{}") }, "bundle", "bundle_index_invalid"},
		{"empty policy", func(m map[string][]byte) {
			m["policy/policy.json"] = []byte{}
			empty := sha256.Sum256(nil)
			resignPredicate(t, f, m, func(p *Predicate) { p.Policy.SHA256 = hex.EncodeToString(empty[:]) })
		}, "policy", "claim_missing"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := f.bundle(t)
			tc.edit(files)
			report := verifyFiles(t, files, Options{Verifier: f.verifier})
			got := failure(report, tc.check)
			if report.Status != "failed" || report.ExitCode != 1 || got.Status != "failed" || got.Code != tc.code {
				t.Fatalf("expected %s/%s failure, got %+v", tc.check, tc.code, report.Failures())
			}
		})
	}
}

// resignPredicate re-signs an altered statement with the trusted key, proving
// recomputation (not only the signature) catches false claims.
func resignPredicate(t *testing.T, f fixture, files map[string][]byte, edit func(*Predicate)) {
	t.Helper()
	var e Envelope
	if err := json.Unmarshal(files[EnvelopePath], &e); err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.StdEncoding.DecodeString(e.Payload)
	var s Statement
	if err := json.Unmarshal(payload, &s); err != nil {
		t.Fatal(err)
	}
	edit(&s.Predicate)
	payload, _ = json.Marshal(s)
	signed, err := signEnvelope(context.Background(), f.signer, payload)
	if err != nil {
		t.Fatal(err)
	}
	files[EnvelopePath], _ = json.Marshal(signed)
}

func TestVerifyTrustAndSignerStates(t *testing.T) {
	f := newFixture(t)
	_, otherPublic, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewLocalVerifier(otherPublic)
	if err != nil {
		t.Fatal(err)
	}
	files := f.bundle(t)
	for _, tc := range []struct {
		name string
		opts Options
		code string
	}{
		{"no trust anchor", Options{}, "trust_anchor_missing"},
		{"untrusted key", Options{Verifier: other}, "key_untrusted"},
	} {
		report := verifyFiles(t, files, tc.opts)
		if got := failure(report, "signature"); got.Code != tc.code || report.Predicate != nil {
			t.Fatalf("%s: got %+v", tc.name, got)
		}
	}
	for _, mode := range []string{ModeSigstoreKeyless, ModeManagedKMS} {
		if _, err := NewSigner(mode, nil); !errors.Is(err, ErrSignerUnavailable) {
			t.Fatalf("%s signer should be unavailable, got %v", mode, err)
		}
		changed := maps.Clone(files)
		changed[IndexPath] = bytes.Replace(files[IndexPath], []byte(ModeLocalEd25519), []byte(mode), 1)
		if got := failure(verifyFiles(t, changed, Options{Verifier: f.verifier}), "signature"); got.Code != "signer_unavailable" {
			t.Fatalf("%s verification should be unavailable, got %+v", mode, got)
		}
	}
	report := verifyFiles(t, files, Options{Verifier: f.verifier, ExpectImageDigest: "sha256:" + strings.Repeat("2", 64), ExpectWazaVersion: "9.9.9"})
	if failure(report, "image").Code != "image_digest_unexpected" || failure(report, "waza_version").Code != "waza_version_unexpected" {
		t.Fatalf("declared claim pins were not enforced: %+v", report.Failures())
	}
}

func TestGenerateRequiresEveryClaim(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*GenerateInput)
		code string
	}{
		{"image digest", func(in *GenerateInput) { in.ImageDigest = "latest" }, "image_digest_invalid"},
		{"waza version", func(in *GenerateInput) { in.WazaVersion = " " }, "claim_missing"},
		{"policy", func(in *GenerateInput) { in.Policy = nil }, "claim_missing"},
		{"model", func(in *GenerateInput) {
			in.Request = bytes.Replace(in.Request, []byte(`model: mock-model\n`), []byte(`model: \"\"\n`), 1)
		}, "runtime_model_unavailable"},
		{"artifact", func(in *GenerateInput) { in.Objects = map[string][]byte{} }, "artifact_missing"},
		{"binding kind", func(in *GenerateInput) {
			in.Bindings = []BindingInput{{Kind: "sbom.unknown", Name: "x.json", MediaType: "application/json", Data: []byte("{}")}}
		}, "binding_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			tc.edit(&f.input)
			_, err := Generate(context.Background(), f.input, f.signer)
			var failure *Failure
			if !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestBindingExtensionPoint(t *testing.T) {
	const kind = "test.inventory"
	var seen []byte
	if err := RegisterBindingVerifier(kind, BindingVerifierFunc(func(b Binding, data []byte, s *Statement) error {
		seen = data
		if !bytes.Contains(data, []byte(s.Predicate.Inputs.Identity.CanonicalDigest)) {
			return errors.New("inventory does not name the canonical inputs")
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unregisterBindingVerifier(kind) })
	if err := RegisterBindingVerifier(kind, BindingVerifierFunc(nil)); err == nil {
		t.Fatal("duplicate binding kind registered")
	}
	f := newFixture(t)
	f.input.Bindings = []BindingInput{{Kind: kind, Name: "inventory.json", MediaType: "application/json",
		Data: []byte(`{"subject":"46c427b5aa8bfbb61ab297b8e7b780c31ea7ca831ddaa633099498ed749e8dc5"}`)}}
	files := f.bundle(t)
	pristine := maps.Clone(files)
	report := verifyFiles(t, files, Options{Verifier: f.verifier})
	if report.Status != "verified" || len(seen) == 0 || report.Predicate.Bindings[0].Path != "bindings/inventory.json" {
		t.Fatalf("binding did not verify: %+v", report.Failures())
	}
	files["bindings/inventory.json"] = []byte(`{"subject":"other"}`)
	if got := failure(verifyFiles(t, files, Options{Verifier: f.verifier}), "bindings"); got.Code != "binding_digest_mismatch" {
		t.Fatalf("tampered binding accepted: %+v", got)
	}
	unregisterBindingVerifier(kind)
	if got := failure(verifyFiles(t, pristine, Options{Verifier: f.verifier}), "bindings"); got.Code != "binding_unsupported" {
		t.Fatalf("unknown binding kind accepted: %+v", got)
	}
}

func TestMemberCountIsBounded(t *testing.T) {
	f := newFixture(t)
	b, err := Generate(context.Background(), f.input, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := b.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	for i := range MaxMembers {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("extra-%d.txt", i)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	src, closeSrc, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeSrc() }()
	report := Verify(src, Options{Verifier: f.verifier})
	if got := failure(report, "members"); got.Code != "members_unlisted" {
		t.Fatalf("oversized bundle accepted: %+v", got)
	}
	if _, err := EncodeReport(report); err != nil {
		t.Fatalf("report for oversized bundle does not encode: %v", err)
	}
	files := map[string][]byte{}
	for i := range MaxMembers + 1 {
		files[fmt.Sprintf("m%d", i)] = nil
	}
	if _, err := MapSource(files); err == nil {
		t.Fatal("MapSource accepted more than MaxMembers entries")
	}
}

func TestDirectoryRejectsSymlinkMembers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	f := newFixture(t)
	b, err := Generate(context.Background(), f.input, f.signer)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := b.WriteDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, IndexPath), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	src, closeSrc, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeSrc() }()
	if got := failure(Verify(src, Options{Verifier: f.verifier}), "members"); got.Code != "member_irregular" {
		t.Fatalf("symlink member accepted: %+v", got)
	}
}

// The Lab e2e fixtures are produced by the real CLI; this keeps the committed
// report in sync with what verification of the committed bundle emits.
func TestLabFixtureReportMatchesVerification(t *testing.T) {
	const fixtures = "../../web/e2e/fixtures/provenance/"
	verifier, err := NewLocalVerifier(readFixture(t, fixtures+"signing.pub.pem"))
	if err != nil {
		t.Fatal(err)
	}
	src, closeSrc, err := OpenDir(filepath.FromSlash(fixtures + "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeSrc() }()
	report := Verify(src, Options{Verifier: verifier, ExpectImageDigest: "sha256:4f2b8c9e1d0a7b6c5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d"})
	got, err := EncodeReport(report)
	if err != nil {
		t.Fatal(err)
	}
	if want := readFixture(t, fixtures+"verified-report.json"); !bytes.Equal(got, want) {
		t.Fatalf("committed verified-report.json is stale; regenerate with waza runner provenance verify\n%s", got)
	}
	if err := ValidateReport(readFixture(t, fixtures+"tampered-report.json")); err != nil {
		t.Fatalf("tampered-report.json violates the report schema: %v", err)
	}
}

func TestManifestMustMatchAdmittedBinding(t *testing.T) {
	f := newFixture(t)
	admitted, ok := runnerunit.AdmittedBinding(f.input.Request)
	if !ok {
		t.Fatal("fixture request was not admitted")
	}
	objects := f.input.Objects
	if _, failure := deriveArtifacts(f.input.Manifest, objects, admitted); failure != nil {
		t.Fatalf("admitted manifest rejected: %v", failure)
	}
	changed := admitted
	changed.GraderIDs = append(slices.Clone(admitted.GraderIDs), "extra-grader")
	if _, failure := deriveArtifacts(f.input.Manifest, objects, changed); failure == nil || failure.Code != "manifest_binding_mismatch" {
		t.Fatalf("grader mismatch accepted: %v", failure)
	}
}

func TestBundleBoundsEnforcedAtGeneration(t *testing.T) {
	f := newFixture(t)
	statement, failure := BuildStatement(f.input)
	if failure != nil {
		t.Fatal(failure)
	}
	statement.Predicate.Runtime.Model = strings.Repeat("m", 257)
	var invalid *Failure
	if _, err := encodeStatement(statement); !errors.As(err, &invalid) || invalid.Code != "statement_invalid" {
		t.Fatalf("out-of-profile statement accepted for signing: %v", err)
	}
	files := map[string][]byte{}
	for i := range MaxMembers + 1 {
		files[fmt.Sprintf("m-%d", i)] = nil
	}
	if failure := checkBundleBounds(files); failure == nil || failure.Code != "bundle_too_large" {
		t.Fatalf("oversized bundle accepted: %v", failure)
	}
}

func TestReportStaysWithinSchemaBounds(t *testing.T) {
	f := newFixture(t)
	report := verifyFiles(t, f.bundle(t), Options{Verifier: f.verifier})
	members := map[string]*Member{}
	for _, m := range report.Members {
		members[m.Path] = &m
	}
	for i := range maxReportMembers {
		p := fmt.Sprintf("extra-%d", i)
		members[p] = &Member{Path: p, Role: "unbound", Status: "unbound"}
	}
	long := strings.Repeat("a", maxReportPathLength+1)
	members[long] = &Member{Path: long, Role: "unbound", Status: "unbound"}
	report.Members, report.MembersOmitted = boundedMembers(members)
	if len(report.Members) != maxReportMembers || report.MembersOmitted != len(members)-maxReportMembers || report.Members[0].Role == "unbound" {
		t.Fatalf("members not bounded: %d listed, %d omitted", len(report.Members), report.MembersOmitted)
	}
	message := truncateMessage(strings.Repeat("\u00e9", maxReportMessage))
	if len(message) > maxReportMessage || !utf8.ValidString(message) {
		t.Fatalf("message not truncated safely: %d bytes", len(message))
	}
	report.Checks[0].Message = message
	if _, err := EncodeReport(report); err != nil {
		t.Fatalf("bounded report violates schema: %v", err)
	}
}
