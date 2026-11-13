package provenance

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/artifactmanifest"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Check identifiers in report order.
var checkIDs = []string{"bundle", "signature", "statement", "inputs", "runner", "runtime", "policy", "image", "waza_version", "artifacts", "outcome", "bindings", "members"}

// Options configure trust and optional pins for declared claims.
type Options struct {
	// Verifier is the trust anchor. A nil verifier fails closed.
	Verifier SignatureVerifier
	// ExpectImageDigest binds the declared image to the consumed image digest.
	ExpectImageDigest string
	// ExpectWazaVersion pins the declared Waza version.
	ExpectWazaVersion string
	// ExpectPolicySHA256 pins the policy document digest.
	ExpectPolicySHA256 string
}

type Report struct {
	Kind          string          `json:"kind"`
	SchemaVersion string          `json:"schema_version"`
	Status        string          `json:"status"`
	ExitCode      int             `json:"exit_code"`
	Signer        *ReportSigner   `json:"signer,omitempty"`
	Statement     *ReportEnvelope `json:"statement,omitempty"`
	Predicate     *Predicate      `json:"predicate,omitempty"`
	Members       []Member        `json:"members"`
	// MembersOmitted counts unbound entries left out of Members to keep the
	// report within its schema bounds.
	MembersOmitted int     `json:"members_omitted,omitempty"`
	Checks         []Check `json:"checks"`
}

type ReportSigner struct {
	Mode    string `json:"mode"`
	KeyID   string `json:"key_id"`
	Trusted bool   `json:"trusted"`
}

type ReportEnvelope struct {
	Path           string `json:"path"`
	EnvelopeSHA256 string `json:"envelope_sha256"`
	PayloadSHA256  string `json:"payload_sha256,omitempty"`
	PredicateType  string `json:"predicate_type,omitempty"`
}

type Member struct {
	Path   string `json:"path"`
	Role   string `json:"role"`
	SHA256 string `json:"sha256,omitempty"`
	Status string `json:"status"`
}

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Failures returns failed checks.
func (r *Report) Failures() []Check {
	var out []Check
	for _, c := range r.Checks {
		if c.Status == "failed" {
			out = append(out, c)
		}
	}
	return out
}

var compiledSchemas = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	base := "https://microsoft.github.io/waza/schemas/"
	for name, text := range map[string]string{
		"runner-terminal-1.0.schema.json":         schemas.RunnerTerminalSchemaJSON,
		"provenance-statement-1.0.schema.json":    schemas.ProvenanceStatementSchemaJSON,
		"provenance-bundle-1.0.schema.json":       schemas.ProvenanceBundleSchemaJSON,
		"provenance-verification-1.0.schema.json": schemas.ProvenanceVerificationSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(base+name, doc); err != nil {
			return nil, err
		}
	}
	out := map[string]*jsonschema.Schema{}
	for _, name := range []string{"statement", "bundle", "verification"} {
		s, err := c.Compile(base + "provenance-" + name + "-1.0.schema.json")
		if err != nil {
			return nil, err
		}
		out[name] = s
	}
	return out, nil
})

// decodeStrict rejects duplicate keys, invalid escapes and schema violations
// before decoding into typed values.
func decodeStrict(data []byte, schema string, target any) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	compiled, err := compiledSchemas()
	if err != nil {
		return fmt.Errorf("compiling provenance schemas: %w", err)
	}
	if err := compiled[schema].Validate(doc); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return json.Unmarshal(data, target)
}

// ValidateReport checks a report against the published schema.
func ValidateReport(data []byte) error {
	var r Report
	return decodeStrict(data, "verification", &r)
}

type verification struct {
	src     Source
	opts    Options
	report  *Report
	checks  map[string]*Check
	members map[string]*Member
	data    map[string][]byte
	read    int64
	// admitted is set once the canonical inputs verify.
	admitted *artifactmanifest.Binding
}

const (
	maxReportMembers    = 512
	maxReportPathLength = 512
	maxReportMessage    = 4096
)

func (v *verification) set(id, status, code, format string, args ...any) {
	c := v.checks[id]
	c.Status, c.Code, c.Message = status, code, truncateMessage(fmt.Sprintf(format, args...))
}

// truncateMessage keeps diagnostics within the report schema bound on a
// UTF-8 boundary.
func truncateMessage(s string) string {
	const marker = " ... (truncated)"
	if len(s) <= maxReportMessage {
		return s
	}
	cut := maxReportMessage - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

func (v *verification) failed(f *Failure) { v.set(f.Check, "failed", f.Code, "%s", f.Message) }

func (v *verification) passed(id, code, format string, args ...any) {
	if v.checks[id].Status == "failed" {
		return
	}
	v.set(id, "passed", code, format, args...)
}

// readMember reads and digest-checks one bound member exactly once.
func (v *verification) readMember(ref Ref, role string, limit int64) ([]byte, string) {
	m := &Member{Path: ref.Path, Role: role, SHA256: ref.SHA256}
	v.members[ref.Path] = m
	data, err := v.src.ReadFile(ref.Path, min(limit, MaxBundleBytes-v.read))
	// Every byte read counts against the budget, whether or not it matches.
	v.read += int64(len(data))
	switch {
	case errors.Is(err, errMemberMissing):
		m.Status = "missing"
	case err != nil:
		m.Status = "unreadable"
	case sha256Hex(data) != ref.SHA256:
		m.Status = "mismatch"
	default:
		m.Status = "verified"
		v.data[ref.Path] = data
	}
	return v.data[ref.Path], m.Status
}

// Verify checks one bundle offline. It never trusts the bundle index for key
// material and reports every failure; the bundle is verified only when every
// check passes.
func Verify(src Source, opts Options) *Report {
	v := &verification{
		src: src, opts: opts,
		report:  &Report{Kind: ReportKind, SchemaVersion: ReportVersion},
		checks:  map[string]*Check{},
		members: map[string]*Member{},
		data:    map[string][]byte{},
	}
	for _, id := range checkIDs {
		v.checks[id] = &Check{ID: id, Status: "skipped", Code: "blocked", Message: "not evaluated because an earlier check failed"}
	}
	if statement := v.verifyEnvelope(); statement != nil {
		v.report.Predicate = &statement.Predicate
		v.verifyClaims(statement)
		v.verifyMembers()
	}
	v.finish()
	return v.report
}

func (v *verification) verifyEnvelope() *Statement {
	indexData, err := v.src.ReadFile(IndexPath, maxIndexBytes)
	if err != nil {
		v.members[IndexPath] = &Member{Path: IndexPath, Role: "index", Status: map[bool]string{true: "missing", false: "unreadable"}[errors.Is(err, errMemberMissing)]}
		v.set("bundle", "failed", "bundle_index_missing", "bundle.json is missing or unreadable: %v", err)
		return nil
	}
	v.members[IndexPath] = &Member{Path: IndexPath, Role: "index", SHA256: sha256Hex(indexData), Status: "present"}
	var index Index
	if err := decodeStrict(indexData, "bundle", &index); err != nil {
		v.set("bundle", "failed", "bundle_index_invalid", "bundle.json is invalid: %v", err)
		return nil
	}
	v.passed("bundle", "ok", "bundle index %s %s locates %s", index.Kind, index.SchemaVersion, index.Envelope)
	v.report.Signer = &ReportSigner{Mode: index.Signer.Mode, KeyID: index.Signer.KeyID}

	envelopeData, err := v.src.ReadFile(EnvelopePath, maxEnvelopeBytes)
	if err != nil {
		v.members[EnvelopePath] = &Member{Path: EnvelopePath, Role: "envelope", Status: "missing"}
		v.set("signature", "failed", "envelope_missing", "statement envelope is missing or unreadable: %v", err)
		return nil
	}
	v.members[EnvelopePath] = &Member{Path: EnvelopePath, Role: "envelope", SHA256: sha256Hex(envelopeData), Status: "present"}
	v.report.Statement = &ReportEnvelope{Path: EnvelopePath, EnvelopeSHA256: sha256Hex(envelopeData)}

	switch index.Signer.Mode {
	case ModeLocalEd25519:
	case ModeSigstoreKeyless, ModeManagedKMS:
		v.set("signature", "failed", "signer_unavailable", "%s verification has no configured integration in this build", index.Signer.Mode)
		return nil
	}
	if v.opts.Verifier == nil {
		v.set("signature", "failed", "trust_anchor_missing", "no trusted public key was supplied; provenance cannot be verified")
		return nil
	}
	if v.opts.Verifier.Mode() != index.Signer.Mode {
		v.set("signature", "failed", "signer_mode_mismatch", "bundle signer mode %s does not match verifier mode %s", index.Signer.Mode, v.opts.Verifier.Mode())
		return nil
	}
	var envelope Envelope
	if _, err := jsonutil.Parse(envelopeData); err != nil {
		v.set("signature", "failed", "envelope_invalid", "statement envelope is not strict JSON: %v", err)
		return nil
	}
	if err := json.Unmarshal(envelopeData, &envelope); err != nil {
		v.set("signature", "failed", "envelope_invalid", "statement envelope is invalid: %v", err)
		return nil
	}
	if envelope.PayloadType != PayloadType || len(envelope.Signatures) != 1 {
		v.set("signature", "failed", "envelope_invalid", "envelope must carry one signature over %s", PayloadType)
		return nil
	}
	sig := envelope.Signatures[0]
	if sig.KeyID != index.Signer.KeyID {
		v.set("signature", "failed", "key_id_mismatch", "envelope key %s does not match bundle signer %s", sig.KeyID, index.Signer.KeyID)
		return nil
	}
	payload, err := base64.StdEncoding.Strict().DecodeString(envelope.Payload)
	if err != nil {
		v.set("signature", "failed", "envelope_invalid", "envelope payload is not base64: %v", err)
		return nil
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(sig.Sig)
	if err != nil {
		v.set("signature", "failed", "signature_invalid", "signature is not base64: %v", err)
		return nil
	}
	v.report.Statement.PayloadSHA256 = sha256Hex(payload)
	if err := v.opts.Verifier.Verify(sig.KeyID, PAE(envelope.PayloadType, payload), signature); err != nil {
		code := "signature_invalid"
		if errors.Is(err, ErrKeyUntrusted) {
			code = "key_untrusted"
		}
		v.set("signature", "failed", code, "%v", err)
		return nil
	}
	v.report.Signer.Trusted = true
	v.passed("signature", "ok", "DSSE signature verified with trusted key %s", sig.KeyID)

	var statement Statement
	if err := decodeStrict(payload, "statement", &statement); err != nil {
		v.set("statement", "failed", "statement_invalid", "signed statement does not match the provenance profile: %v", err)
		return nil
	}
	v.report.Statement.PredicateType = statement.PredicateType
	if !reflect.DeepEqual(statement.Subject, subjects(statement.Predicate)) {
		v.set("statement", "failed", "statement_subject_mismatch", "statement subjects do not match the bound artifact chain")
		return nil
	}
	v.passed("statement", "ok", "%s statement with profile %s", statement.PredicateType, statement.Predicate.Profile)
	return &statement
}

func (v *verification) verifyClaims(s *Statement) {
	p := s.Predicate

	// Inputs, runner API version and runtime are recomputed from the request.
	request, status := v.readMember(p.Inputs.Request, "input", 4<<20)
	if p.Inputs.Request.Path != RequestPath {
		v.set("inputs", "failed", "input_path_invalid", "request must be bound at %s", RequestPath)
	} else if status != "verified" {
		v.set("inputs", "failed", "input_digest_"+status, "canonical request %s is %s", RequestPath, status)
	} else if derived, f := deriveInputs(request); f != nil {
		v.failed(f)
	} else {
		v.admitted = &derived.admitted
		if derived.inputs != p.Inputs {
			v.set("inputs", "failed", "input_identity_mismatch", "recomputed canonical identity does not match the signed identity")
		}
		v.passed("inputs", "ok", "canonical digest %s recomputed from the request", p.Inputs.Identity.CanonicalDigest)
		if derived.apiVersion != p.Runner.APIVersion || derived.requestSchemaVersion != p.Runner.RequestSchemaVersion {
			v.set("runner", "failed", "runner_api_mismatch", "recomputed runner API %s (request %s) does not match the signed claim", derived.apiVersion, derived.requestSchemaVersion)
		}
		v.passed("runner", "ok", "runner API %s negotiated from request schema %s", p.Runner.APIVersion, p.Runner.RequestSchemaVersion)
		if derived.runtime != p.Runtime {
			v.set("runtime", "failed", "runtime_mismatch", "recomputed runtime %s/%s does not match the signed claim", derived.runtime.Executor, derived.runtime.Model)
		}
		v.passed("runtime", "ok", "executor %s, model %s, execution mode %s", p.Runtime.Executor, p.Runtime.Model, p.Runtime.ExecutionMode)
	}
	if v.checks["inputs"].Status == "failed" {
		for _, id := range []string{"runner", "runtime"} {
			if v.checks[id].Status == "skipped" {
				v.set(id, "skipped", "blocked", "canonical inputs did not verify")
			}
		}
	}

	policy, status := v.readMember(p.Policy, "policy", maxPolicyBytes)
	switch {
	case !strings.HasPrefix(p.Policy.Path, PolicyDir+"/"):
		v.set("policy", "failed", "policy_path_invalid", "policy must be bound under %s/", PolicyDir)
	case status != "verified":
		v.set("policy", "failed", "policy_digest_"+status, "policy %s is %s", p.Policy.Path, status)
	case len(policy) == 0:
		v.set("policy", "failed", "claim_missing", "policy %s is empty; a policy document is required", p.Policy.Path)
	case v.opts.ExpectPolicySHA256 != "" && v.opts.ExpectPolicySHA256 != p.Policy.SHA256:
		v.set("policy", "failed", "policy_unexpected", "policy digest %s does not match the expected %s", p.Policy.SHA256, v.opts.ExpectPolicySHA256)
	default:
		v.passed("policy", pinned(v.opts.ExpectPolicySHA256), "policy %s sha256 %s", p.Policy.Path, p.Policy.SHA256)
	}

	if f := validateImage(p.Runner.Image.Reference, p.Runner.Image.Digest); f != nil {
		v.failed(f)
	} else if v.opts.ExpectImageDigest != "" && v.opts.ExpectImageDigest != p.Runner.Image.Digest {
		v.set("image", "failed", "image_digest_unexpected", "signed image digest %s does not match the consumed image %s", p.Runner.Image.Digest, v.opts.ExpectImageDigest)
	} else {
		v.passed("image", pinned(v.opts.ExpectImageDigest), "%s@%s", p.Runner.Image.Reference, p.Runner.Image.Digest)
	}
	if f := validateWazaVersion(p.Waza.Version); f != nil {
		v.failed(f)
	} else if v.opts.ExpectWazaVersion != "" && v.opts.ExpectWazaVersion != p.Waza.Version {
		v.set("waza_version", "failed", "waza_version_unexpected", "signed Waza version %s does not match the expected %s", p.Waza.Version, v.opts.ExpectWazaVersion)
	} else {
		v.passed("waza_version", pinned(v.opts.ExpectWazaVersion), "Waza %s", p.Waza.Version)
	}

	v.verifyArtifacts(p)
	v.verifyBindings(s)
}

func pinned(expected string) string {
	if expected != "" {
		return "pinned"
	}
	return "declared"
}

func (v *verification) verifyArtifacts(p Predicate) {
	manifest, manifestStatus := v.readMember(p.Artifacts.Manifest, "manifest", 1<<20)
	objects := map[string][]byte{}
	bad := ""
	for _, ref := range p.Artifacts.Objects {
		role := "artifact"
		if ref.Path == p.Outcome.Artifact.Path {
			role = "terminal"
		}
		data, status := v.readMember(ref, role, 64<<20)
		name, ok := underDir(ref.Path, ObjectsDir)
		if !ok {
			v.set("artifacts", "failed", "artifact_path_invalid", "artifact %s is not under %s/", ref.Path, ObjectsDir)
			return
		}
		if status != "verified" {
			if bad == "" {
				bad = ref.Path + " is " + status
			}
			if role == "terminal" {
				v.set("outcome", "failed", "outcome_digest_"+status, "terminal outcome %s is %s", ref.Path, status)
			}
			continue
		}
		objects[name] = data
	}
	switch {
	case p.Artifacts.Manifest.Path != ManifestPath:
		v.set("artifacts", "failed", "manifest_path_invalid", "manifest must be bound at %s", ManifestPath)
		return
	case manifestStatus != "verified":
		v.set("artifacts", "failed", "manifest_digest_"+manifestStatus, "artifact manifest is %s", manifestStatus)
	case bad != "":
		v.set("artifacts", "failed", "artifact_digest_mismatch", "artifact chain is broken: %s", bad)
	}
	if v.checks["artifacts"].Status == "failed" || v.checks["inputs"].Status != "passed" || v.admitted == nil {
		if v.checks["outcome"].Status == "skipped" {
			v.set("outcome", "skipped", "blocked", "artifact chain or inputs did not verify")
		}
		return
	}
	// Unlisted files under the objects directory must also be declared.
	regular, _, _ := v.src.List()
	for _, name := range regular {
		if rest, ok := underDir(name, ObjectsDir); ok {
			if _, bound := objects[rest]; !bound {
				objects[rest] = nil
			}
		}
	}
	derived, f := deriveArtifacts(manifest, objects, *v.admitted)
	if f != nil {
		v.failed(f)
		if f.Check == "outcome" {
			v.passed("artifacts", "ok", "manifest and %d objects match their digests", len(p.Artifacts.Objects))
		} else if v.checks["outcome"].Status == "skipped" {
			v.set("outcome", "skipped", "blocked", "artifact chain did not verify")
		}
		return
	}
	if !reflect.DeepEqual(derived.artifacts, p.Artifacts) {
		v.set("artifacts", "failed", "artifact_chain_mismatch", "signed artifact chain does not match the canonical manifest")
	} else if derived.mode != p.Runtime.ExecutionMode {
		v.set("artifacts", "failed", "runtime_mode_mismatch", "manifest execution mode %s does not match signed runtime %s", derived.mode, p.Runtime.ExecutionMode)
	}
	v.passed("artifacts", "ok", "manifest %s and %d objects verified", p.Artifacts.Manifest.SHA256, len(p.Artifacts.Objects))
	if derived.outcome != p.Outcome {
		v.set("outcome", "failed", "outcome_mismatch", "terminal outcome %s (exit %d) does not match the signed outcome %s (exit %d)",
			derived.outcome.Outcome, derived.outcome.ExitCode, p.Outcome.Outcome, p.Outcome.ExitCode)
	}
	v.passed("outcome", "ok", "%s (exit %d, boundary %s)", p.Outcome.Outcome, p.Outcome.ExitCode, p.Outcome.FailureBoundary)
}

func (v *verification) verifyBindings(s *Statement) {
	if len(s.Predicate.Bindings) == 0 {
		v.passed("bindings", "none", "no additional bindings")
		return
	}
	for _, b := range s.Predicate.Bindings {
		data, status := v.readMember(b.Ref, "binding", maxBindingBytes)
		if _, ok := underDir(b.Path, BindingsDir); !ok {
			v.set("bindings", "failed", "binding_path_invalid", "binding %s is not under %s/", b.Path, BindingsDir)
			return
		}
		if status != "verified" {
			v.set("bindings", "failed", "binding_digest_"+status, "%s binding %s is %s", b.Kind, b.Path, status)
			return
		}
		verifier, ok := lookupBindingVerifier(b.Kind)
		if !ok {
			v.set("bindings", "failed", "binding_unsupported", "binding kind %s has no verifier in this build", b.Kind)
			return
		}
		if err := verifier.Verify(b, data, s); err != nil {
			v.set("bindings", "failed", "binding_invalid", "%s binding %s: %v", b.Kind, b.Path, err)
			return
		}
	}
	v.passed("bindings", "ok", "%d bindings verified", len(s.Predicate.Bindings))
}

func (v *verification) verifyMembers() {
	regular, irregular, err := v.src.List()
	if err != nil {
		v.set("members", "failed", "members_unlisted", "bundle members cannot be listed: %v", err)
		return
	}
	var unbound []string
	for _, name := range regular {
		if _, ok := v.members[name]; !ok {
			v.members[name] = &Member{Path: name, Role: "unbound", Status: "unbound"}
			unbound = append(unbound, name)
		}
	}
	for _, name := range irregular {
		v.members[name] = &Member{Path: name, Role: "unbound", Status: "irregular"}
	}
	switch {
	case len(irregular) > 0:
		slices.Sort(irregular)
		v.set("members", "failed", "member_irregular", "bundle contains non-regular entries: %s", strings.Join(irregular, ", "))
	case len(unbound) > 0:
		slices.Sort(unbound)
		v.set("members", "failed", "member_unbound", "bundle contains members not bound by the statement: %s", strings.Join(unbound, ", "))
	default:
		v.passed("members", "ok", "every bundle member is bound by the signed statement")
	}
}

func (v *verification) finish() {
	r := v.report
	failed := false
	for _, id := range checkIDs {
		c := *v.checks[id]
		failed = failed || c.Status != "passed"
		r.Checks = append(r.Checks, c)
	}
	r.Members, r.MembersOmitted = boundedMembers(v.members)
	if failed {
		r.Status, r.ExitCode = "failed", 1
		if !slices.ContainsFunc(r.Checks, func(c Check) bool { return c.Status == "failed" }) {
			// A skipped check alone never verifies; surface it as a failure.
			for i := range r.Checks {
				if r.Checks[i].Status == "skipped" {
					r.Checks[i].Status = "failed"
					break
				}
			}
		}
		r.Predicate = predicateIfTrusted(r)
		return
	}
	r.Status, r.ExitCode = "verified", 0
}

// boundedMembers lists bound members first, then unbound entries, within the
// report's member and path bounds; anything left out is counted as omitted.
func boundedMembers(members map[string]*Member) ([]Member, int) {
	var bound, unbound []string
	for p, m := range members {
		if m.Role == "unbound" {
			unbound = append(unbound, p)
		} else {
			bound = append(bound, p)
		}
	}
	slices.Sort(bound)
	slices.Sort(unbound)
	out := []Member{}
	omitted := 0
	for _, p := range append(bound, unbound...) {
		if len(out) == maxReportMembers || len(p) > maxReportPathLength || !utf8.ValidString(p) {
			omitted++
			continue
		}
		out = append(out, *members[p])
	}
	return out, omitted
}

func predicateIfTrusted(r *Report) *Predicate {
	if r.Signer == nil || !r.Signer.Trusted {
		return nil
	}
	return r.Predicate
}

// EncodeReport encodes a report and validates it against its schema.
func EncodeReport(r *Report) ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := ValidateReport(data); err != nil {
		return nil, fmt.Errorf("verification report violates its schema: %w", err)
	}
	return append(data, '\n'), nil
}
