// Package artifactmanifest verifies immutable publication declarations and bytes.
// Integrity and declared-context consistency are not execution attestation.
package artifactmanifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/runneridentity"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version          = "1.0"
	MaxManifestBytes = 1 << 20
	MaxArtifactBytes = 64 << 20
	MaxTotalBytes    = 256 << 20
)

var (
	ErrInvalid  = errors.New("invalid manifest declaration")
	ErrMissing  = errors.New("missing immutable object")
	ErrCorrupt  = errors.New("corrupt immutable object")
	ErrConflict = errors.New("conflicting immutable publication")
)

type Artifact struct {
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	MediaType string `json:"media_type"`
}

// Binding is caller-declared context. It must not be presented as trusted
// admission or a record of observed runtime behavior.
type Binding struct {
	Identity      runneridentity.Identity  `json:"identity"`
	Selection     runnercontract.Selection `json:"selection"`
	ExecutionMode string                   `json:"execution_mode"`
	GraderIDs     []string                 `json:"grader_ids"`
}

func (b Binding) TerminalBinding() *runnerterminal.Binding {
	return &runnerterminal.Binding{
		Identity: &b.Identity, Selection: b.Selection,
		ExecutionMode: b.ExecutionMode, GraderIDs: slices.Clone(b.GraderIDs),
	}
}

type Manifest struct {
	Kind             string     `json:"kind"`
	SchemaVersion    string     `json:"schema_version"`
	Binding          Binding    `json:"binding"`
	TerminalArtifact string     `json:"terminal_artifact"`
	Artifacts        []Artifact `json:"artifacts"`
}

var manifestSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	c := jsonschema.NewCompiler()
	c.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"https://microsoft.github.io/waza/schemas/artifact-manifest-1.0.schema.json": schemas.ArtifactManifestSchemaJSON,
		"https://microsoft.github.io/waza/schemas/runner-terminal-1.0.schema.json":   schemas.RunnerTerminalSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, err
		}
		if err := c.AddResource(name, doc); err != nil {
			return nil, err
		}
	}
	return c.Compile("https://microsoft.github.io/waza/schemas/artifact-manifest-1.0.schema.json")
})

// Parse validates original JSON before decoding; omission, null and malformed
// wire types never become valid zero values.
func Parse(data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxManifestBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	schema, err := manifestSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling manifest schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: decoding: %w", ErrInvalid, err)
	}
	for _, identifier := range append(slices.Clone(m.Binding.GraderIDs), m.Binding.Selection.Shard, m.Binding.Selection.Task) {
		if len(identifier) > runnerterminal.MaxIdentifierBytes {
			return nil, fmt.Errorf("%w: binding identifiers exceed %d UTF-8 bytes", ErrInvalid, runnerterminal.MaxIdentifierBytes)
		}
	}
	seen := make(map[string]bool, len(m.Artifacts))
	found := false
	var total int64
	for _, a := range m.Artifacts {
		if seen[a.Name] {
			return nil, fmt.Errorf("%w: duplicate artifact name %q", ErrInvalid, a.Name)
		}
		seen[a.Name] = true
		total += a.SizeBytes
		if a.Name == m.TerminalArtifact {
			found = true
			if a.SizeBytes > runnerterminal.MaxEnvelopeBytes || a.MediaType != "application/json" {
				return nil, fmt.Errorf("%w: terminal artifact must be bounded application/json", ErrInvalid)
			}
		}
	}
	if !found || total > MaxTotalBytes {
		return nil, fmt.Errorf("%w: missing terminal artifact or total size exceeds %d", ErrInvalid, MaxTotalBytes)
	}
	return &m, nil
}

// Encode is the v1 canonical byte profile: typed field order, sorted unique
// names/graders, compact Go JSON string escaping, and no trailing newline.
func Encode(m Manifest) ([]byte, error) {
	m.Artifacts = slices.Clone(m.Artifacts)
	m.Binding.GraderIDs = slices.Clone(m.Binding.GraderIDs)
	slices.SortFunc(m.Artifacts, func(a, b Artifact) int { return strings.Compare(a.Name, b.Name) })
	slices.Sort(m.Binding.GraderIDs)
	stringsToCheck := append(slices.Clone(m.Binding.GraderIDs), m.Binding.Selection.Shard, m.Binding.Selection.Task,
		m.Kind, m.SchemaVersion, m.TerminalArtifact, m.Binding.ExecutionMode,
		m.Binding.Identity.Version, m.Binding.Identity.CanonicalDigest, m.Binding.Identity.Run,
		m.Binding.Identity.Shard, m.Binding.Identity.Task, m.Binding.Identity.Trial, m.Binding.Identity.Attempt)
	for _, a := range m.Artifacts {
		stringsToCheck = append(stringsToCheck, a.Name, a.SHA256, a.MediaType)
	}
	for _, s := range stringsToCheck {
		if !utf8.ValidString(s) {
			return nil, fmt.Errorf("%w: invalid UTF-8", ErrInvalid)
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("%w: encoding: %w", ErrInvalid, err)
	}
	if _, err := Parse(data); err != nil {
		return nil, err
	}
	return data, nil
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func Digest(m Manifest) (string, error) {
	data, err := Encode(m)
	if err != nil {
		return "", err
	}
	return Hash(data), nil
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, c := range digest {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func verifyBytes(data []byte, digest string, size int64) error {
	if int64(len(data)) != size || Hash(data) != digest {
		return ErrCorrupt
	}
	return nil
}

func verifyObject(key string, data []byte) error {
	if strings.HasPrefix(key, "final-") {
		if !validDigest(string(data)) {
			return ErrCorrupt
		}
		if string(data) != strings.TrimPrefix(key, "final-") {
			return ErrConflict
		}
		return nil
	}
	_, digest, _ := strings.Cut(key, "-")
	if Hash(data) != digest {
		return ErrCorrupt
	}
	return nil
}

func canonicalStored(data []byte, digest string) (*Manifest, error) {
	if Hash(data) != digest {
		return nil, ErrCorrupt
	}
	m, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	canonical, err := Encode(*m)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: noncanonical manifest", ErrCorrupt)
	}
	return m, nil
}
