// Package executionattempt defines pure attempt and duplicate-decision sidecars.
// Declarations do not reserve storage, verify publication, or attest execution.
package executionattempt

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version         = "1.0"
	MaxSidecarBytes = 1 << 20
	MaxLineage      = 1024
)

var ErrInvalid = errors.New("invalid execution attempt declaration")

type Logical struct {
	Run  string `json:"run"`
	Work string `json:"work"`
}

func LogicalFrom(identity runneridentity.Identity) Logical {
	return Logical{Run: identity.Run, Work: identity.Trial}
}

type Parent struct {
	ExecutionID  string `json:"execution_id"`
	Relationship string `json:"relationship"`
}

type Attempt struct {
	Kind          string                   `json:"kind"`
	SchemaVersion string                   `json:"schema_version"`
	Logical       Logical                  `json:"logical"`
	ExecutionID   string                   `json:"execution_id"`
	Binding       artifactmanifest.Binding `json:"binding"`
	Parent        *Parent                  `json:"parent,omitempty"`
}

// Source names the original execution and manifest, never a retagged terminal.
// Its presence alone is not proof of finalization, content, or context integrity.
type Source struct {
	ExecutionID    string `json:"execution_id"`
	ManifestDigest string `json:"manifest_digest"`
}

type Decision struct {
	Kind          string                   `json:"kind"`
	SchemaVersion string                   `json:"schema_version"`
	DecisionID    string                   `json:"decision_id"`
	Logical       Logical                  `json:"logical"`
	Binding       artifactmanifest.Binding `json:"binding"`
	Policy        string                   `json:"policy"`
	Outcome       string                   `json:"outcome"`
	ExecutionID   string                   `json:"execution_id,omitempty"`
	Source        *Source                  `json:"source,omitempty"`
}

var profileSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-attempt-1.0.schema.json": schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json": schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":   schemas.RunnerTerminalSchemaJSON,
	} {
		resource := "https://microsoft.github.io/waza/schemas/" + name
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading attempt schema %s: %w", name, err)
		}
		if err := compiler.AddResource(resource, doc); err != nil {
			return nil, fmt.Errorf("registering attempt schema %s: %w", name, err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/execution-attempt-1.0.schema.json")
})

func parse(data []byte, kind string, target any) error {
	if len(data) > MaxSidecarBytes {
		return fmt.Errorf("%w: sidecar exceeds %d bytes", ErrInvalid, MaxSidecarBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("%w: parsing: %w", ErrInvalid, err)
	}
	schema, err := profileSchema()
	if err != nil {
		return fmt.Errorf("compiling attempt schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	object, ok := doc.(map[string]any)
	if !ok || object["kind"] != kind {
		return fmt.Errorf("%w: expected %s", ErrInvalid, kind)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	return nil
}

func bindingConsistent(logical Logical, binding artifactmanifest.Binding) error {
	if logical != LogicalFrom(binding.Identity) {
		return fmt.Errorf("%w: logical run/work contradicts original binding", ErrInvalid)
	}
	for _, id := range append(slices.Clone(binding.GraderIDs), binding.Selection.Shard, binding.Selection.Task) {
		if len(id) > runnerterminal.MaxIdentifierBytes {
			return fmt.Errorf("%w: binding identifier exceeds %d UTF-8 bytes", ErrInvalid, runnerterminal.MaxIdentifierBytes)
		}
	}
	return nil
}

func ParseAttempt(data []byte) (*Attempt, error) {
	var attempt Attempt
	if err := parse(data, "waza.execution-attempt", &attempt); err != nil {
		return nil, err
	}
	if err := bindingConsistent(attempt.Logical, attempt.Binding); err != nil {
		return nil, err
	}
	if attempt.Parent != nil && attempt.Parent.ExecutionID == attempt.ExecutionID {
		return nil, fmt.Errorf("%w: execution cannot parent itself", ErrInvalid)
	}
	return &attempt, nil
}

func ParseDecision(data []byte) (*Decision, error) {
	var decision Decision
	if err := parse(data, "waza.duplicate-decision", &decision); err != nil {
		return nil, err
	}
	if err := bindingConsistent(decision.Logical, decision.Binding); err != nil {
		return nil, err
	}
	return &decision, nil
}

func encode(value any, binding artifactmanifest.Binding) ([]byte, error) {
	for _, text := range append(slices.Clone(binding.GraderIDs), binding.Selection.Shard, binding.Selection.Task) {
		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("%w: binding is not UTF-8", ErrInvalid)
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	return data, nil
}

// EncodeAttempt uses typed field order, compact Go JSON escaping, sorted graders,
// and no trailing newline. Parent links have no implicit ordinal or timestamp.
func EncodeAttempt(attempt Attempt) ([]byte, error) {
	attempt.Binding.GraderIDs = slices.Clone(attempt.Binding.GraderIDs)
	slices.Sort(attempt.Binding.GraderIDs)
	data, err := encode(attempt, attempt.Binding)
	if err != nil {
		return nil, err
	}
	if _, err := ParseAttempt(data); err != nil {
		return nil, err
	}
	return data, nil
}

func EncodeDecision(decision Decision) ([]byte, error) {
	decision.Binding.GraderIDs = slices.Clone(decision.Binding.GraderIDs)
	slices.Sort(decision.Binding.GraderIDs)
	data, err := encode(decision, decision.Binding)
	if err != nil {
		return nil, err
	}
	if _, err := ParseDecision(data); err != nil {
		return nil, err
	}
	return data, nil
}

// ValidateLineage checks a complete, bounded graph without ordering concurrent
// siblings. It neither appends records nor guarantees storage immutability.
func ValidateLineage(attempts []Attempt) error {
	if len(attempts) == 0 || len(attempts) > MaxLineage {
		return fmt.Errorf("%w: lineage requires 1..%d attempts", ErrInvalid, MaxLineage)
	}
	byID := make(map[string]Attempt, len(attempts))
	for _, attempt := range attempts {
		if _, err := EncodeAttempt(attempt); err != nil {
			return err
		}
		if _, exists := byID[attempt.ExecutionID]; exists {
			return fmt.Errorf("%w: repeated execution ID %s", ErrInvalid, attempt.ExecutionID)
		}
		byID[attempt.ExecutionID] = attempt
	}
	for _, attempt := range attempts {
		if attempt.Parent == nil {
			continue
		}
		parent, ok := byID[attempt.Parent.ExecutionID]
		if !ok || parent.Logical != attempt.Logical {
			return fmt.Errorf("%w: missing or incomparable parent for %s", ErrInvalid, attempt.ExecutionID)
		}
	}
	// Each node has at most one parent. Finished paths are reused so validation
	// stays linear even when many branches share a long retry history.
	state := make(map[string]uint8, len(attempts))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("%w: cyclic lineage at %s", ErrInvalid, id)
		case 2:
			return nil
		}
		state[id] = 1
		if parent := byID[id].Parent; parent != nil {
			if err := visit(parent.ExecutionID); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, attempt := range attempts {
		if err := visit(attempt.ExecutionID); err != nil {
			return err
		}
	}
	return nil
}
