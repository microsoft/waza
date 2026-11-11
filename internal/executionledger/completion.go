// Package executionledger admits execution completion references against original
// attempt and manifest bytes. Pure admission does not confer storage ownership,
// verify final publication or artifact content, or attest runtime execution.
package executionledger

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const Version = "1.0"

var (
	ErrInvalid = errors.New("invalid execution completion declaration")
	ErrContext = errors.New("execution completion context mismatch")
)

type Completion struct {
	Kind              string `json:"kind"`
	SchemaVersion     string `json:"schema_version"`
	ExecutionID       string `json:"execution_id"`
	ReservationDigest string `json:"reservation_digest"`
	ManifestDigest    string `json:"manifest_digest"`
}

var completionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-completion-1.0.schema.json": schemas.ExecutionCompletionSchemaJSON,
		"execution-attempt-1.0.schema.json":    schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":    schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":      schemas.RunnerTerminalSchemaJSON,
	} {
		resource := "https://microsoft.github.io/waza/schemas/" + name
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading completion schema %s: %w", name, err)
		}
		if err := compiler.AddResource(resource, doc); err != nil {
			return nil, fmt.Errorf("registering completion schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/execution-completion-1.0.schema.json")
})

func ParseCompletion(data []byte) (*Completion, error) {
	if len(data) > executionattempt.MaxSidecarBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, executionattempt.MaxSidecarBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: parsing: %w", ErrInvalid, err)
	}
	schema, err := completionSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling completion schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var completion Completion
	if err := json.Unmarshal(data, &completion); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	return &completion, nil
}

func EncodeCompletion(completion Completion) ([]byte, error) {
	data, err := json.Marshal(completion)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	if _, err := ParseCompletion(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ValidateContext checks canonical whole-byte hashes and the original native
// binding. The caller must separately verify source artifacts and final marker
// through the real manifest provider before any publication or reuse decision.
func ValidateContext(completion Completion, reservationBytes, manifestBytes []byte) error {
	_, err := admitContext(completion, reservationBytes, manifestBytes)
	return err
}

func admitContext(completion Completion, reservationBytes, manifestBytes []byte) (*executionattempt.Attempt, error) {
	if _, err := EncodeCompletion(completion); err != nil {
		return nil, err
	}
	attempt, err := executionattempt.ParseAttempt(reservationBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: reservation: %w", ErrContext, err)
	}
	canonicalAttempt, err := executionattempt.EncodeAttempt(*attempt)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding reservation: %w", ErrContext, err)
	}
	if !bytes.Equal(canonicalAttempt, reservationBytes) ||
		artifactmanifest.Hash(reservationBytes) != completion.ReservationDigest ||
		attempt.ExecutionID != completion.ExecutionID {
		return nil, fmt.Errorf("%w: reservation bytes, digest, or execution ID differ", ErrContext)
	}
	manifest, err := artifactmanifest.Parse(manifestBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: manifest: %w", ErrContext, err)
	}
	canonicalManifest, err := artifactmanifest.Encode(*manifest)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding manifest: %w", ErrContext, err)
	}
	if !bytes.Equal(canonicalManifest, manifestBytes) ||
		artifactmanifest.Hash(manifestBytes) != completion.ManifestDigest {
		return nil, fmt.Errorf("%w: manifest bytes or digest differ", ErrContext)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Name == "execution-attempt.json" &&
			(artifact.SHA256 != completion.ReservationDigest || artifact.SizeBytes != int64(len(reservationBytes)) ||
				artifact.MediaType != "application/json") {
			return nil, fmt.Errorf("%w: declared execution-attempt artifact contradicts original reservation", ErrContext)
		}
	}
	original, source := attempt.Binding, manifest.Binding
	if original.Identity != source.Identity || original.Selection != source.Selection ||
		original.ExecutionMode != source.ExecutionMode || !slices.Equal(original.GraderIDs, source.GraderIDs) {
		return nil, fmt.Errorf("%w: manifest contradicts original reservation binding", ErrContext)
	}
	return attempt, nil
}

// ValidateReuseContext admits a reused decision against the original source
// reservation and completion. The caller's declared attempt ordinal may differ;
// source terminal/manifest bytes must never be retagged to that ordinal.
// This remains pure admission, not provider finalization/content verification.
func ValidateReuseContext(decision executionattempt.Decision, reservationBytes, completionBytes, manifestBytes []byte) error {
	if _, err := executionattempt.EncodeDecision(decision); err != nil {
		return fmt.Errorf("%w: decision: %w", ErrContext, err)
	}
	if decision.Policy != "reuse" || decision.Outcome != "reused" || decision.Source == nil {
		return fmt.Errorf("%w: decision does not declare reuse", ErrContext)
	}
	completion, err := ParseCompletion(completionBytes)
	if err != nil {
		return fmt.Errorf("%w: completion: %w", ErrContext, err)
	}
	canonical, err := EncodeCompletion(*completion)
	if err != nil {
		return fmt.Errorf("%w: encoding completion: %w", ErrContext, err)
	}
	if !bytes.Equal(canonical, completionBytes) || completion.ExecutionID != decision.Source.ExecutionID ||
		completion.ManifestDigest != decision.Source.ManifestDigest {
		return fmt.Errorf("%w: decision source differs from original completion", ErrContext)
	}
	attempt, err := admitContext(*completion, reservationBytes, manifestBytes)
	if err != nil {
		return err
	}
	return ValidateReuseBinding(decision, *attempt)
}

// ValidateReuseBinding checks caller/source compatibility without requiring
// manifest bytes. Only the native attempt ordinal and its identity may differ.
func ValidateReuseBinding(decision executionattempt.Decision, attempt executionattempt.Attempt) error {
	if _, err := executionattempt.EncodeDecision(decision); err != nil {
		return fmt.Errorf("%w: decision: %w", ErrContext, err)
	}
	if _, err := executionattempt.EncodeAttempt(attempt); err != nil {
		return fmt.Errorf("%w: reservation: %w", ErrContext, err)
	}
	if decision.Policy != "reuse" || decision.Outcome != "reused" || decision.Source == nil ||
		decision.Source.ExecutionID != attempt.ExecutionID {
		return fmt.Errorf("%w: decision does not reuse the original execution", ErrContext)
	}
	caller, source := decision.Binding, attempt.Binding
	graders := slices.Clone(caller.GraderIDs)
	slices.Sort(graders)
	if decision.Logical != attempt.Logical || caller.Identity.Version != source.Identity.Version ||
		caller.Identity.Run != source.Identity.Run || caller.Identity.Shard != source.Identity.Shard ||
		caller.Identity.Task != source.Identity.Task || caller.Identity.Trial != source.Identity.Trial ||
		caller.Selection.Shard != source.Selection.Shard || caller.Selection.Task != source.Selection.Task ||
		caller.Selection.Trial != source.Selection.Trial || caller.ExecutionMode != source.ExecutionMode ||
		!slices.Equal(graders, source.GraderIDs) {
		return fmt.Errorf("%w: caller differs from source logical work or execution context", ErrContext)
	}
	return nil
}
