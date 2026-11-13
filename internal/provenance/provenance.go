// Package provenance generates, signs and offline-verifies execution
// provenance bundles. A bundle binds the canonical runner inputs, the declared
// runner image digest, the Waza and runner API versions, the policy document,
// the runtime/model identity, the immutable artifact chain and the terminal
// outcome into one in-toto statement signed inside a DSSE envelope.
//
// Claims fall into two classes. Derived claims (inputs, API version, runtime,
// policy, artifacts, outcome) are recomputed from bundle members during
// verification. Declared claims (image digest, Waza version) are covered by the
// signature only and can be pinned with verification expectations.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/microsoft/waza/internal/runneridentity"
)

const (
	StatementType = "https://in-toto.io/Statement/v1"
	PredicateType = "https://microsoft.github.io/waza/provenance/execution/v1"
	Profile       = "waza.execution-provenance/1.0"
	PayloadType   = "application/vnd.in-toto+json"

	BundleKind    = "waza.provenance-bundle"
	BundleVersion = "1.0"
	ReportKind    = "waza.provenance-verification"
	ReportVersion = "1.0"

	IndexPath    = "bundle.json"
	EnvelopePath = "statement.dsse.json"
	RequestPath  = "inputs/request.json"
	ManifestPath = "artifacts/manifest.json"
	ObjectsDir   = "artifacts/objects"
	PolicyDir    = "policy"
	BindingsDir  = "bindings"
)

const (
	maxIndexBytes    = 64 << 10
	maxEnvelopeBytes = 8 << 20
	maxPolicyBytes   = 1 << 20
	maxBindingBytes  = 16 << 20
	maxBindings      = 16
	// MaxBundleBytes bounds the total member bytes read for one verification.
	MaxBundleBytes = 320 << 20
)

// Statement is an in-toto v1 statement with the Waza execution predicate.
type Statement struct {
	Type          string    `json:"_type"`
	Subject       []Subject `json:"subject"`
	PredicateType string    `json:"predicateType"`
	Predicate     Predicate `json:"predicate"`
}

type Subject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

// Ref binds one bundle member path to its SHA-256 digest.
type Ref struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	MediaType string `json:"media_type"`
}

type Predicate struct {
	Profile   string         `json:"profile"`
	Waza      WazaClaim      `json:"waza"`
	Runner    RunnerClaim    `json:"runner"`
	Inputs    InputsClaim    `json:"inputs"`
	Policy    Ref            `json:"policy"`
	Runtime   RuntimeClaim   `json:"runtime"`
	Artifacts ArtifactsClaim `json:"artifacts"`
	Outcome   OutcomeClaim   `json:"outcome"`
	// Bindings is the extension point for additional evidence bound by digest,
	// such as component inventories. Every kind must have a registered verifier.
	Bindings []Binding `json:"bindings"`
}

type WazaClaim struct {
	Version string `json:"version"`
}

type RunnerClaim struct {
	APIVersion           string     `json:"api_version"`
	RequestSchemaVersion string     `json:"request_schema_version"`
	Image                ImageClaim `json:"image"`
}

type ImageClaim struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type InputsClaim struct {
	Request  Ref                     `json:"request"`
	Identity runneridentity.Identity `json:"identity"`
}

type RuntimeClaim struct {
	ExecutionMode string `json:"execution_mode"`
	Executor      string `json:"executor"`
	Model         string `json:"model"`
}

type ArtifactsClaim struct {
	Manifest Ref   `json:"manifest"`
	Objects  []Ref `json:"objects"`
}

type OutcomeClaim struct {
	Artifact        Ref    `json:"artifact"`
	Outcome         string `json:"outcome"`
	ExitCode        int    `json:"exit_code"`
	FailureBoundary string `json:"failure_boundary"`
}

// Binding is one additional bundle member bound into the signed statement.
type Binding struct {
	Kind string `json:"kind"`
	Ref
}

// Index is the unsigned bundle entry point. It only locates the envelope and
// names the signer mode; every security-relevant value comes from the signed
// statement and the verifier's trust configuration.
type Index struct {
	Kind          string     `json:"kind"`
	SchemaVersion string     `json:"schema_version"`
	Envelope      string     `json:"envelope"`
	Signer        SignerInfo `json:"signer"`
}

type SignerInfo struct {
	Mode  string `json:"mode"`
	KeyID string `json:"key_id"`
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
