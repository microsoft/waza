// Package executionisolation defines portable execution isolation
// capabilities: what an execution requests, what host/runtime providers can
// prove they enforce, and the effective posture admitted before execution.
//
// Waza enforces no boundary itself. Providers (an outer host or container
// runtime declared by the operator, or the Copilot-native sandbox tracked in
// #544 when present) own enforcement; this package only discovers their
// claims, runs a conformance probe that attempts each boundary violation, and
// rejects requested boundaries that cannot be shown to hold.
package executionisolation

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Version       = "1.0"
	RequestKind   = "waza.execution-isolation-request"
	HostKind      = "waza.execution-isolation-host"
	PreflightKind = "waza.execution-isolation-preflight"
)

// ErrInvalid marks documents that fail the isolation schema.
var ErrInvalid = errors.New("invalid isolation document")

// Dimension is one isolation boundary.
type Dimension string

const (
	Filesystem  Dimension = "filesystem"
	Environment Dimension = "environment"
	Network     Dimension = "network"
	Subprocess  Dimension = "subprocess"
	RemoteTools Dimension = "remote_tools"
)

// Dimensions lists every boundary in canonical report order.
var Dimensions = []Dimension{Filesystem, Environment, Network, Subprocess, RemoteTools}

// Posture is what an execution requests for one dimension.
type Posture string

const (
	Unrestricted Posture = "unrestricted"
	Restricted   Posture = "restricted"
)

// Request names the boundaries an execution requires. Omitted dimensions are
// unrestricted, which preserves existing behavior.
type Request struct {
	Kind          string                `json:"kind"`
	SchemaVersion string                `json:"schema_version"`
	Dimensions    map[Dimension]Posture `json:"dimensions"`
}

// Posture returns the requested posture for a dimension.
func (r Request) Posture(d Dimension) Posture {
	if r.Dimensions[d] == Restricted {
		return Restricted
	}
	return Unrestricted
}

// HostDeclaration is an operator's claim that an outer host or runtime
// enforces the listed boundaries around the Waza process and everything it
// launches. Claims are probed, never trusted.
type HostDeclaration struct {
	Kind          string         `json:"kind"`
	SchemaVersion string         `json:"schema_version"`
	EnforcedBy    string         `json:"enforced_by"`
	Dimensions    HostDimensions `json:"dimensions"`
}

// HostDimensions holds the per-dimension probe parameters of a declaration.
type HostDimensions struct {
	Filesystem  *FilesystemSpec  `json:"filesystem,omitempty"`
	Environment *EnvironmentSpec `json:"environment,omitempty"`
	Network     *EndpointSpec    `json:"network,omitempty"`
	Subprocess  *struct{}        `json:"subprocess,omitempty"`
	RemoteTools *EndpointSpec    `json:"remote_tools,omitempty"`
}

// FilesystemSpec lists paths outside the workspace that must not be writable.
type FilesystemSpec struct {
	OutsidePaths []string `json:"outside_paths,omitempty"`
}

// EnvironmentSpec lists exact secret variable names that must be absent in
// addition to the built-in secret-name patterns.
type EnvironmentSpec struct {
	SecretNames []string `json:"secret_names,omitempty"`
}

// EndpointSpec lists host:port targets that are reachable without the
// boundary and must be unreachable with it.
type EndpointSpec struct {
	Targets []string `json:"targets"`
}

var schema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	const id = "https://microsoft.github.io/waza/schemas/execution-isolation-1.0.schema.json"
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ExecutionIsolationSchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("reading isolation schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	if err := compiler.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("registering isolation schema: %w", err)
	}
	return compiler.Compile(id)
})

func admit(data []byte, kind string, target any) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	compiled, err := schema()
	if err != nil {
		return err
	}
	if err := compiled.Validate(doc); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if object, ok := doc.(map[string]any); !ok || object["kind"] != kind {
		return fmt.Errorf("%w: expected kind %s", ErrInvalid, kind)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

// ParseRequest admits a schema-valid isolation request.
func ParseRequest(data []byte) (Request, error) {
	var request Request
	err := admit(data, RequestKind, &request)
	return request, err
}

// ParseHostDeclaration admits a schema-valid host isolation declaration.
func ParseHostDeclaration(data []byte) (*HostDeclaration, error) {
	var host HostDeclaration
	if err := admit(data, HostKind, &host); err != nil {
		return nil, err
	}
	return &host, nil
}

// ParseReport admits a schema-valid preflight report.
func ParseReport(data []byte) (*Report, error) {
	var report Report
	if err := admit(data, PreflightKind, &report); err != nil {
		return nil, err
	}
	if err := checkEvidence(&report); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return &report, nil
}

// checkEvidence enforces the cross-object rules JSON Schema cannot express:
// dimensions are canonical, every probe comes from an available provider and
// each diagnostic or enforcement claim follows from the recorded probes.
func checkEvidence(report *Report) error {
	available := map[string]bool{}
	for _, provider := range report.Providers {
		if _, seen := available[provider.Name]; seen {
			return fmt.Errorf("provider %q is duplicated", provider.Name)
		}
		available[provider.Name] = provider.Status == ProviderAvailable
	}
	for i, entry := range report.Dimensions {
		if entry.Dimension != Dimensions[i] {
			return fmt.Errorf("dimension %d is %q, want %q", i, entry.Dimension, Dimensions[i])
		}
		outcomes := map[Outcome]bool{}
		for _, probe := range entry.Probes {
			if !available[probe.Provider] {
				return fmt.Errorf("%s has a probe from %q, which is not an available provider", entry.Dimension, probe.Provider)
			}
			outcomes[probe.Outcome] = true
		}
		want := CodeNoProvider
		switch {
		case outcomes[Violated]:
			want = CodeProbeViolated
		case outcomes[Enforced]:
			want = ""
		case len(entry.Probes) > 0:
			want = CodeProbeInconclusive
		}
		if entry.Available {
			if want != "" || !slices.ContainsFunc(entry.Probes, func(p ProbeReport) bool {
				return p.Provider == entry.EnforcementSource && p.Outcome == Enforced
			}) {
				return fmt.Errorf("%s is reported enforced without a passing probe from %q", entry.Dimension, entry.EnforcementSource)
			}
		} else if entry.Diagnostic == nil || entry.Diagnostic.Code != want {
			return fmt.Errorf("%s diagnostic does not match its probes", entry.Dimension)
		}
	}
	return nil
}

// Encode returns the indented, schema-valid JSON of a preflight report.
func (r *Report) Encode() ([]byte, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding isolation report: %w", err)
	}
	if _, err := ParseReport(data); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
