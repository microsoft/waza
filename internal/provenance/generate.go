package provenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// GenerateInput carries every material the statement binds. All claims are
// required: generation fails rather than emit a bundle with unavailable claims.
type GenerateInput struct {
	Request         []byte
	Manifest        []byte
	Objects         map[string][]byte
	Policy          []byte
	PolicyName      string
	PolicyMediaType string
	WazaVersion     string
	ImageReference  string
	ImageDigest     string
	Bindings        []BindingInput
}

// Bundle is an assembled, signed bundle keyed by slash-separated member path.
type Bundle struct {
	Files     map[string][]byte
	Statement Statement
	KeyID     string
	Mode      string
}

func validateDeclared(wazaVersion, reference, digest string) *Failure {
	if f := validateWazaVersion(wazaVersion); f != nil {
		return f
	}
	return validateImage(reference, digest)
}

func validateWazaVersion(wazaVersion string) *Failure {
	if strings.TrimSpace(wazaVersion) == "" || len(wazaVersion) > 128 {
		return fail("waza_version", "claim_missing", "Waza version is unavailable")
	}
	return nil
}

func validateImage(reference, digest string) *Failure {
	if strings.TrimSpace(reference) == "" || len(reference) > 512 || strings.ContainsAny(reference, " \t\r\n") {
		return fail("image", "claim_missing", "runner image reference is unavailable or invalid")
	}
	if !imageDigestPattern.MatchString(digest) {
		return fail("image", "image_digest_invalid", "runner image digest must be sha256:<64 lowercase hex>")
	}
	return nil
}

// BuildStatement derives and validates every claim without signing.
func BuildStatement(in GenerateInput) (*Statement, *Failure) {
	if f := validateDeclared(in.WazaVersion, in.ImageReference, in.ImageDigest); f != nil {
		return nil, f
	}
	if len(in.Policy) == 0 || len(in.Policy) > maxPolicyBytes {
		return nil, fail("policy", "claim_missing", "policy document is unavailable or exceeds %d bytes", maxPolicyBytes)
	}
	if !namePattern.MatchString(in.PolicyName) || in.PolicyMediaType == "" || len(in.PolicyMediaType) > 128 {
		return nil, fail("policy", "policy_invalid", "policy name or media type is invalid")
	}
	inputs, f := deriveInputs(in.Request)
	if f != nil {
		return nil, f
	}
	artifacts, f := deriveArtifacts(in.Manifest, in.Objects, inputs.admitted)
	if f != nil {
		return nil, f
	}
	if artifacts.mode != inputs.runtime.ExecutionMode {
		return nil, fail("runtime", "runtime_mode_mismatch", "manifest execution mode %q does not match executor %q", artifacts.mode, inputs.runtime.Executor)
	}
	if err := validateBindingInputs(in.Bindings); err != nil {
		return nil, fail("bindings", "binding_invalid", "%v", err)
	}
	predicate := Predicate{
		Profile: Profile,
		Waza:    WazaClaim{Version: in.WazaVersion},
		Runner: RunnerClaim{
			APIVersion: inputs.apiVersion, RequestSchemaVersion: inputs.requestSchemaVersion,
			Image: ImageClaim{Reference: in.ImageReference, Digest: in.ImageDigest},
		},
		Inputs:    inputs.inputs,
		Policy:    Ref{Path: PolicyDir + "/" + in.PolicyName, SHA256: sha256Hex(in.Policy), MediaType: in.PolicyMediaType},
		Runtime:   inputs.runtime,
		Artifacts: artifacts.artifacts,
		Outcome:   artifacts.outcome,
		Bindings:  []Binding{},
	}
	for _, b := range in.Bindings {
		predicate.Bindings = append(predicate.Bindings, Binding{Kind: b.Kind, Ref: Ref{Path: BindingsDir + "/" + b.Name, SHA256: sha256Hex(b.Data), MediaType: b.MediaType}})
	}
	statement := &Statement{Type: StatementType, Subject: subjects(predicate), PredicateType: PredicateType, Predicate: predicate}
	// Run each binding's verifier now so a bundle verification would reject is
	// never signed.
	for i, b := range in.Bindings {
		verifier, ok := lookupBindingVerifier(b.Kind)
		if !ok {
			return nil, fail("bindings", "binding_unsupported", "binding kind %s has no verifier in this build", b.Kind)
		}
		if err := verifier.Verify(predicate.Bindings[i], b.Data, statement); err != nil {
			return nil, fail("bindings", "binding_invalid", "%s binding %s: %v", b.Kind, predicate.Bindings[i].Path, err)
		}
	}
	return statement, nil
}

// subjects lists the produced artifacts: the manifest and every object.
func subjects(p Predicate) []Subject {
	out := []Subject{{Name: p.Artifacts.Manifest.Path, Digest: map[string]string{"sha256": p.Artifacts.Manifest.SHA256}}}
	for _, o := range p.Artifacts.Objects {
		out = append(out, Subject{Name: o.Path, Digest: map[string]string{"sha256": o.SHA256}})
	}
	return out
}

// Generate builds, signs and assembles a bundle.
func Generate(ctx context.Context, in GenerateInput, signer Signer) (*Bundle, error) {
	if signer == nil {
		return nil, fmt.Errorf("%w: no signer configured", ErrSignerUnavailable)
	}
	statement, f := BuildStatement(in)
	if f != nil {
		return nil, f
	}
	payload, err := encodeStatement(statement)
	if err != nil {
		return nil, err
	}
	envelope, err := signEnvelope(ctx, signer, payload)
	if err != nil {
		return nil, fmt.Errorf("signing statement: %w", err)
	}
	envelopeJSON, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding envelope: %w", err)
	}
	index, err := json.MarshalIndent(Index{
		Kind: BundleKind, SchemaVersion: BundleVersion, Envelope: EnvelopePath,
		Signer: SignerInfo{Mode: signer.Mode(), KeyID: signer.KeyID()},
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding bundle index: %w", err)
	}
	files := map[string][]byte{
		IndexPath:                       append(index, '\n'),
		EnvelopePath:                    append(envelopeJSON, '\n'),
		RequestPath:                     in.Request,
		ManifestPath:                    in.Manifest,
		statement.Predicate.Policy.Path: in.Policy,
	}
	for name, data := range in.Objects {
		files[ObjectsDir+"/"+name] = data
	}
	for _, b := range in.Bindings {
		files[BindingsDir+"/"+b.Name] = b.Data
	}
	if f := checkBundleBounds(files); f != nil {
		return nil, f
	}
	return &Bundle{Files: files, Statement: *statement, KeyID: signer.KeyID(), Mode: signer.Mode()}, nil
}

// encodeStatement refuses to sign a statement that verification would reject.
func encodeStatement(statement *Statement) ([]byte, error) {
	payload, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("encoding statement: %w", err)
	}
	var check Statement
	if err := decodeStrict(payload, "statement", &check); err != nil {
		return nil, fail("statement", "statement_invalid", "statement does not match the provenance profile: %v", err)
	}
	return payload, nil
}

// checkBundleBounds rejects bundles that verification would refuse to read.
func checkBundleBounds(files map[string][]byte) *Failure {
	if len(files) > MaxMembers {
		return fail("members", "bundle_too_large", "bundle has %d members; at most %d are verifiable", len(files), MaxMembers)
	}
	var total int64
	for _, data := range files {
		total += int64(len(data))
	}
	if total > MaxBundleBytes {
		return fail("members", "bundle_too_large", "bundle members total %d bytes; at most %d are verifiable", total, int64(MaxBundleBytes))
	}
	return nil
}

// WriteDir writes the bundle into a new or empty directory without following
// or replacing existing members.
func (b *Bundle) WriteDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating bundle directory: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading bundle directory: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("bundle directory must be empty")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening bundle directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, path := range slices.Sorted(maps.Keys(b.Files)) {
		if parent := filepath.Dir(filepath.FromSlash(path)); parent != "." {
			if err := root.MkdirAll(parent, 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", parent, err)
			}
		}
		file, err := root.OpenFile(filepath.FromSlash(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		_, writeErr := file.Write(b.Files[path])
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}
