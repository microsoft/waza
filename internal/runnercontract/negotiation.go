// Package runnercontract defines the offline, scheduler-neutral runner ABI.
// Negotiation never constructs an execution engine or claims work was executed.
package runnercontract

import (
	"encoding/json"
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
	NegotiationVersion = "1.0"
	MaxRequestBytes    = 4 << 20
)

type NegotiationRequest struct {
	SchemaVersion        string   `json:"schema_version"`
	Versions             []string `json:"versions"`
	RequiredCapabilities []string `json:"required_capabilities"`
	OptionalCapabilities []string `json:"optional_capabilities"`
}

type Selection struct {
	Shard   string `json:"shard"`
	Task    string `json:"task"`
	Trial   int    `json:"trial"`
	Attempt int    `json:"attempt"`
}

type Input struct {
	Shard string `json:"shard"`
	Eval  string `json:"eval"`
	Task  string `json:"task"`
}

// Request retains the captured source's execution request wire contract.
// Parsing it validates shape only; runnerunit owns semantic admission.
type Request struct {
	SchemaVersion        string    `json:"schema_version"`
	Versions             []string  `json:"versions"`
	RequiredCapabilities []string  `json:"required_capabilities"`
	OptionalCapabilities []string  `json:"optional_capabilities"`
	RunKey               string    `json:"run_key"`
	Selection            Selection `json:"selection"`
	Input                Input     `json:"input"`
	TimeoutMS            int       `json:"timeout_ms"`
	AllowPaidExecution   bool      `json:"allow_paid_execution"`
}

type Advertisement struct {
	SchemaVersion string   `json:"schema_version"`
	Versions      []string `json:"versions"`
	Capabilities  []string `json:"capabilities"`
}

func Capabilities() Advertisement {
	return Advertisement{
		SchemaVersion: NegotiationVersion,
		Versions:      []string{"1.0"},
		Capabilities:  []string{"negotiation", "exact-unit", "inline.mock-text", "terminal-result"},
	}
}

type Negotiation struct {
	Version              string   `json:"version,omitempty"`
	RequestedVersions    []string `json:"requested_versions"`
	RequiredCapabilities []string `json:"required_capabilities"`
	OptionalCapabilities []string `json:"optional_capabilities"`
	Available            []string `json:"available_capabilities"`
	Selected             []string `json:"selected_capabilities"`
	UnavailableOptional  []string `json:"unavailable_optional_capabilities"`
}

type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Kind          string       `json:"kind"`
	SchemaVersion string       `json:"schema_version"`
	Status        string       `json:"status"`
	Negotiation   Negotiation  `json:"negotiation"`
	Diagnostics   []Diagnostic `json:"diagnostics"`
}

func (r Response) ExitCode() int {
	switch r.Status {
	case "compatible":
		return 0
	case "unsupported_version", "unsupported_capability":
		return 5
	default:
		return 6
	}
}

func Rejected(message string) Response {
	if runes := []rune(message); len(runes) > 4096 {
		message = string(runes[:4096]) + " (diagnostic truncated; check the request against the published schema)"
	}
	response := Response{
		Kind: "waza.runner-negotiation", SchemaVersion: NegotiationVersion,
		Status: "invalid_request", Diagnostics: []Diagnostic{{Code: "invalid_request", Message: message}},
		Negotiation: Negotiation{
			RequestedVersions: []string{}, RequiredCapabilities: []string{}, OptionalCapabilities: []string{},
			Available: Capabilities().Capabilities, Selected: []string{}, UnavailableOptional: []string{},
		},
	}
	return response
}

func compileSchema(text string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("reading runner negotiation schema: %w", err)
	}
	if err := compiler.AddResource("runner-negotiation.json", doc); err != nil {
		return nil, fmt.Errorf("registering runner negotiation schema: %w", err)
	}
	schema, err := compiler.Compile("runner-negotiation.json")
	if err != nil {
		return nil, fmt.Errorf("compiling runner negotiation schema: %w", err)
	}
	return schema, nil
}

var requestSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSchema(schemas.RunnerRequestSchemaJSON)
})

var negotiationSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSchema(schemas.RunnerNegotiationRequestSchemaJSON)
})

func validate(data []byte, load func() (*jsonschema.Schema, error)) error {
	if len(data) > MaxRequestBytes {
		return fmt.Errorf("runner request exceeds %d bytes", MaxRequestBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("parsing runner request: %w", err)
	}
	schema, err := load()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("runner request schema: %w", err)
	}
	return nil
}

func Parse(data []byte) (*Request, error) {
	if err := validate(data, requestSchema); err != nil {
		return nil, err
	}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("decoding runner negotiation request: %w", err)
	}
	return &request, nil
}

func parseNegotiation(data []byte) (*NegotiationRequest, error) {
	if err := validate(data, negotiationSchema); err != nil {
		return nil, err
	}
	var request NegotiationRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("decoding runner negotiation request: %w", err)
	}
	return &request, nil
}

// Negotiate preserves offered order but chooses the server's highest mutually
// supported version. Optional capabilities never broaden required capabilities.
func Negotiate(data []byte) Response {
	request, err := parseNegotiation(data)
	if err != nil {
		// A complete execution request can be checked without interpreting its
		// captured sources or constructing a runtime. Partial forms stay invalid.
		if full, fullErr := Parse(data); fullErr == nil {
			request = &NegotiationRequest{
				SchemaVersion: full.SchemaVersion, Versions: full.Versions,
				RequiredCapabilities: full.RequiredCapabilities, OptionalCapabilities: full.OptionalCapabilities,
			}
			err = nil
		}
	}
	if err != nil {
		return Rejected(err.Error())
	}
	response := Rejected("pending negotiation")
	response.Diagnostics = []Diagnostic{}
	n := &response.Negotiation
	n.RequestedVersions = slices.Clone(request.Versions)
	n.RequiredCapabilities = slices.Clone(request.RequiredCapabilities)
	n.OptionalCapabilities = slices.Clone(request.OptionalCapabilities)
	required := make(map[string]bool, len(request.RequiredCapabilities))
	for _, capability := range request.RequiredCapabilities {
		required[capability] = true
	}
	conflicts := 0
	for _, capability := range request.OptionalCapabilities {
		if required[capability] {
			conflicts++
			if conflicts <= 64 {
				response.Diagnostics = append(response.Diagnostics, Diagnostic{
					Code: "conflicting_capabilities", Message: fmt.Sprintf("capability %q cannot be both required and optional", capability),
				})
			}
		}
	}
	if conflicts > 64 {
		response.Diagnostics = append(response.Diagnostics, Diagnostic{
			Code: "additional_conflicting_capabilities", Message: fmt.Sprintf("%d additional capabilities are both required and optional", conflicts-64),
		})
	}
	if len(response.Diagnostics) != 0 {
		return response
	}
	for _, version := range Capabilities().Versions {
		if slices.Contains(request.Versions, version) {
			n.Version = version
			break
		}
	}
	if n.Version == "" {
		response.Status = "unsupported_version"
		response.Diagnostics = append(response.Diagnostics, Diagnostic{
			Code: "unsupported_version", Message: "no supported runner version; offer 1.0",
		})
		return response
	}
	missing := 0
	for _, capability := range request.RequiredCapabilities {
		if !slices.Contains(n.Available, capability) {
			missing++
			if missing <= 64 {
				response.Diagnostics = append(response.Diagnostics, Diagnostic{
					Code:    "unsupported_capability",
					Message: fmt.Sprintf("required capability %q is unavailable; available: %s", capability, strings.Join(n.Available, ", ")),
				})
			}
		}
	}
	if missing > 64 {
		response.Diagnostics = append(response.Diagnostics, Diagnostic{
			Code:    "additional_unsupported_capabilities",
			Message: fmt.Sprintf("%d additional required capabilities are unavailable; compare required_capabilities with available_capabilities", missing-64),
		})
	}
	for _, capability := range request.OptionalCapabilities {
		if !slices.Contains(n.Available, capability) {
			n.UnavailableOptional = append(n.UnavailableOptional, capability)
		}
	}
	if len(response.Diagnostics) != 0 {
		response.Status = "unsupported_capability"
		return response
	}
	for _, capability := range append(slices.Clone(request.RequiredCapabilities), request.OptionalCapabilities...) {
		if slices.Contains(n.Available, capability) {
			n.Selected = append(n.Selected, capability)
		}
	}
	slices.Sort(n.Selected)
	response.Status = "compatible"
	return response
}
