// Package executioncheckpoint admits exact-unit safe-boundary declarations and
// verifies immutable checkpoint blobs. It does not reserve attempts, restore
// runtimes or authenticate boundary ownership and completed execution evidence.
package executioncheckpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version       = "1.0"
	MaxBytes      = 1 << 20
	BeforeUnit    = "before-unit"
	FinalizedUnit = "finalized-unit"
)

var (
	ErrInvalid      = errors.New("invalid execution checkpoint declaration")
	ErrContext      = errors.New("execution checkpoint context mismatch")
	ErrIncompatible = errors.New("incompatible execution checkpoint")
	ErrStale        = errors.New("stale execution checkpoint")
)

type Compatibility struct {
	RunnerRevision      string `json:"runner_revision"`
	RequestVersion      string `json:"request_version"`
	IdentityVersion     string `json:"identity_version"`
	TerminalVersion     string `json:"terminal_version"`
	ManifestVersion     string `json:"manifest_version"`
	AttemptVersion      string `json:"attempt_version"`
	CompletionVersion   string `json:"completion_version"`
	Work                string `json:"work"`
	RequestPolicyDigest string `json:"request_policy_digest"`
}

type RequestDeclaration struct {
	compatibility Compatibility
	selection     runnercontract.Selection
	identity      runneridentity.Identity
}

// Compatibility returns the immutable request compatibility declaration.
func (d RequestDeclaration) Compatibility() Compatibility { return d.compatibility }

// CompatibilityError exposes only differing field names, not captured inputs.
type CompatibilityError struct {
	Fields []string
}

func (e *CompatibilityError) Error() string {
	return fmt.Sprintf("%s: changed %s; preserve the checkpoint and start a distinct attempt",
		ErrIncompatible, strings.Join(e.Fields, ", "))
}

func (e *CompatibilityError) Unwrap() error { return ErrIncompatible }

type Checkpoint struct {
	Kind              string        `json:"kind"`
	SchemaVersion     string        `json:"schema_version"`
	Boundary          string        `json:"boundary"`
	CreatedAt         string        `json:"created_at"`
	ExecutionID       string        `json:"execution_id"`
	ReservationDigest string        `json:"reservation_digest"`
	Compatibility     Compatibility `json:"compatibility"`
	CompletionDigest  string        `json:"completion_digest,omitempty"`
	ManifestDigest    string        `json:"manifest_digest,omitempty"`
}

var checkpointSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-checkpoint-1.0.schema.json": schemas.ExecutionCheckpointSchemaJSON,
		"execution-attempt-1.0.schema.json":    schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":    schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":      schemas.RunnerTerminalSchemaJSON,
	} {
		resource := "https://microsoft.github.io/waza/schemas/" + name
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading checkpoint schema %s: %w", name, err)
		}
		if err := compiler.AddResource(resource, document); err != nil {
			return nil, fmt.Errorf("registering checkpoint schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/execution-checkpoint-1.0.schema.json")
})

func Parse(data []byte) (*Checkpoint, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxBytes)
	}
	document, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing: %w", ErrInvalid, err)
	}
	schema, err := checkpointSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling checkpoint schema: %w", err)
	}
	if err := schema.Validate(document); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	created, err := time.Parse(time.RFC3339Nano, checkpoint.CreatedAt)
	if err != nil || created.Year() < 1 || created.Format(time.RFC3339Nano) != checkpoint.CreatedAt {
		return nil, fmt.Errorf("%w: created_at must be a canonical UTC RFC3339 timestamp", ErrInvalid)
	}
	return &checkpoint, nil
}

// Encode uses typed field order, compact Go JSON escaping and no trailing LF.
func Encode(checkpoint Checkpoint) ([]byte, error) {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

// DeclareRequest validates and derives one immutable request declaration for
// both compatibility checks and checkpoint publication.
func DeclareRequest(data []byte, runnerRevision string) (RequestDeclaration, error) {
	request, err := runnercontract.Parse(data)
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: request: %w", ErrIncompatible, err)
	}
	negotiation := runnercontract.NegotiateValidatedRequest(request)
	if negotiation.Status != "compatible" {
		return RequestDeclaration{}, fmt.Errorf("%w: negotiation: %s", ErrIncompatible, negotiation.Status)
	}
	identity, err := runneridentity.DeriveValidated(request)
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: identity: %w", ErrIncompatible, err)
	}
	versions := slices.Clone(request.Versions)
	required := slices.Clone(request.RequiredCapabilities)
	optional := slices.Clone(request.OptionalCapabilities)
	slices.Sort(versions)
	slices.Sort(required)
	slices.Sort(optional)
	policy, err := json.Marshal(struct {
		Versions []string `json:"versions"`
		Required []string `json:"required"`
		Optional []string `json:"optional"`
		Paid     bool     `json:"paid"`
	}{versions, required, optional, request.AllowPaidExecution})
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: request policy: %w", ErrIncompatible, err)
	}
	compatibility := Compatibility{
		RunnerRevision: runnerRevision, RequestVersion: request.SchemaVersion,
		IdentityVersion: runneridentity.Version, TerminalVersion: runnerterminal.Version,
		ManifestVersion: artifactmanifest.Version, AttemptVersion: executionattempt.Version,
		CompletionVersion: executionledger.Version, Work: identity.Trial,
		RequestPolicyDigest: artifactmanifest.Hash(policy),
	}
	wire, err := json.Marshal(compatibility)
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: encoding compatibility: %w", ErrIncompatible, err)
	}
	document, err := jsonutil.Parse(wire)
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: parsing compatibility: %w", ErrIncompatible, err)
	}
	schema, err := checkpointSchema()
	if err != nil {
		return RequestDeclaration{}, fmt.Errorf("compiling checkpoint schema: %w", err)
	}
	if err := schema.Properties["compatibility"].Validate(document); err != nil {
		return RequestDeclaration{}, fmt.Errorf("%w: compatibility: %w", ErrIncompatible, err)
	}
	return RequestDeclaration{compatibility: compatibility, selection: request.Selection, identity: *identity}, nil
}

// ForRequest retains the shared declared logical-work identity, including
// captured model/config/tool/policy values and timeout. Only the attempt ordinal
// may change. Negotiation/consent declarations not covered by that identity are
// hashed separately. These hashes are not runtime attestation.
func ForRequest(data []byte, runnerRevision string) (Compatibility, error) {
	declaration, err := DeclareRequest(data, runnerRevision)
	if err != nil {
		return Compatibility{}, err
	}
	return declaration.Compatibility(), nil
}

func ValidateCompatibility(checkpoint Checkpoint, current Compatibility) error {
	if _, err := Encode(checkpoint); err != nil {
		return err
	}
	source := checkpoint.Compatibility
	var changed []string
	for _, field := range []struct{ name, before, after string }{
		{"runner_revision", source.RunnerRevision, current.RunnerRevision},
		{"request_version", source.RequestVersion, current.RequestVersion},
		{"identity_version", source.IdentityVersion, current.IdentityVersion},
		{"terminal_version", source.TerminalVersion, current.TerminalVersion},
		{"manifest_version", source.ManifestVersion, current.ManifestVersion},
		{"attempt_version", source.AttemptVersion, current.AttemptVersion},
		{"completion_version", source.CompletionVersion, current.CompletionVersion},
		{"work", source.Work, current.Work},
		{"request_policy_digest", source.RequestPolicyDigest, current.RequestPolicyDigest},
	} {
		if field.before != field.after {
			changed = append(changed, field.name)
		}
	}
	if len(changed) != 0 {
		return &CompatibilityError{Fields: changed}
	}
	return nil
}

// ValidateContext requires the independently retained original admission binding
// and canonical original sidecars. Original must not be inferred from these
// sidecars or replaced with the destination admission. The caller still must
// verify the source owner, final marker, artifact bytes and terminal outcome via
// the actual provider before skipping any work.
func ValidateContext(checkpoint Checkpoint, original artifactmanifest.Binding, reservation, completion, manifest []byte) error {
	if _, err := Encode(checkpoint); err != nil {
		return err
	}
	attempt, err := executionattempt.ParseAttempt(reservation)
	if err != nil {
		return fmt.Errorf("%w: reservation: %w", ErrContext, err)
	}
	canonical, err := executionattempt.EncodeAttempt(*attempt)
	if err != nil {
		return fmt.Errorf("%w: encoding reservation: %w", ErrContext, err)
	}
	if !bytes.Equal(canonical, reservation) || artifactmanifest.Hash(reservation) != checkpoint.ReservationDigest ||
		attempt.ExecutionID != checkpoint.ExecutionID || attempt.Logical.Work != checkpoint.Compatibility.Work {
		return fmt.Errorf("%w: reservation bytes, execution ID or logical work differ", ErrContext)
	}
	graders := slices.Clone(original.GraderIDs)
	slices.Sort(graders)
	source := attempt.Binding
	if source.Identity != original.Identity || source.Selection != original.Selection ||
		source.ExecutionMode != original.ExecutionMode || !slices.Equal(source.GraderIDs, graders) {
		return fmt.Errorf("%w: source reservation differs from original admitted identity, selection, mode or graders; do not retag source evidence", ErrContext)
	}
	if checkpoint.Boundary == BeforeUnit {
		if len(completion) != 0 || len(manifest) != 0 {
			return fmt.Errorf("%w: before-unit cannot declare completed evidence", ErrContext)
		}
		return nil
	}
	value, err := executionledger.ParseCompletion(completion)
	if err != nil {
		return fmt.Errorf("%w: completion: %w", ErrContext, err)
	}
	canonical, err = executionledger.EncodeCompletion(*value)
	if err != nil {
		return fmt.Errorf("%w: encoding completion: %w", ErrContext, err)
	}
	if !bytes.Equal(canonical, completion) || artifactmanifest.Hash(completion) != checkpoint.CompletionDigest ||
		value.ExecutionID != checkpoint.ExecutionID || value.ReservationDigest != checkpoint.ReservationDigest ||
		value.ManifestDigest != checkpoint.ManifestDigest {
		return fmt.Errorf("%w: completion bytes or source references differ", ErrContext)
	}
	if err := executionledger.ValidateContext(*value, reservation, manifest); err != nil {
		return fmt.Errorf("%w: original completion context: %w", ErrContext, err)
	}
	return nil
}
