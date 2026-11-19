// Package runneradapter is the portable external execution adapter. It
// translates one external work descriptor into exactly one public runner
// contract call and maps the public reply back to a status and returned
// manifest. It is translation only: it owns no queue, placement, fleet state,
// retry policy or scheduling decision, and it never imports the execution
// runtime. The TestArchitecture* tests enforce this boundary.
package runneradapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Name          = "waza-reference-adapter"
	Version       = "1.0"
	SchemaVersion = "1.0"

	DescriptorKind  = "waza.adapter-work-descriptor"
	TranslationKind = "waza.adapter-translation"
	ResultKind      = "waza.adapter-result"

	// MaxDescriptorBytes matches the public submission limit: a descriptor
	// carries one runner request plus bounded routing fields.
	MaxDescriptorBytes = runnercontract.MaxRequestBytes + 8192
)

const (
	OperationSubmit     = "submit"
	OperationCheckpoint = "checkpoint"
	OperationResume     = "resume"
	OperationStatus     = "status"
)

// Diagnostic codes for descriptors that cannot be translated exactly.
const (
	CodeDescriptorTooLarge    = "descriptor_too_large"
	CodeDescriptorUnavailable = "descriptor_unavailable"
	CodeDescriptorInvalid     = "descriptor_invalid"
	CodeDescriptorUnsupported = "descriptor_field_unsupported"
	CodeDescriptorAmbiguous   = "descriptor_ambiguous"
	CodeDescriptorIncomplete  = "descriptor_incomplete"
	CodeRequestInvalid        = "request_invalid"
)

var descriptorFields = []string{"kind", "schema_version", "work_id", "operation", "request", "policy", "checkpoint_sha256", "logical"}

// fieldsByOperation lists the routing fields each operation requires and
// accepts; any other present field is ambiguous rather than silently dropped.
var fieldsByOperation = map[string]struct{ required, allowed []string }{
	OperationSubmit:     {required: []string{"request"}, allowed: []string{"request", "policy"}},
	OperationCheckpoint: {required: []string{"request"}, allowed: []string{"request", "policy"}},
	OperationResume:     {required: []string{"request", "checkpoint_sha256"}, allowed: []string{"request", "policy", "checkpoint_sha256"}},
	OperationStatus:     {required: []string{"logical"}, allowed: []string{"logical"}},
}

type Identity struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	RunnerVersions []string `json:"runner_versions"`
}

func Adapter() Identity {
	return Identity{Name: Name, Version: Version, RunnerVersions: slices.Clone(runnercontract.Capabilities().Versions)}
}

type Diagnostic = runnercontract.Diagnostic

type HTTPRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Call is the single public contract call for one descriptor. Command is the
// public CLI argv after the executable (store flags are appended by the host);
// HTTP is the equivalent public route. Input is the exact stdin/body text.
type Call struct {
	Command     []string  `json:"command"`
	HTTP        HTTPRoute `json:"http"`
	Input       string    `json:"input,omitempty"`
	InputSHA256 string    `json:"input_sha256,omitempty"`
}

type Selection struct {
	Shard   string `json:"shard"`
	Task    string `json:"task"`
	Trial   int    `json:"trial"`
	Attempt int    `json:"attempt"`
}

type Logical struct {
	Run  string `json:"run"`
	Work string `json:"work"`
}

type Parent struct {
	ExecutionID  string `json:"execution_id"`
	Relationship string `json:"relationship"`
}

type Source struct {
	ExecutionID    string `json:"execution_id"`
	ManifestDigest string `json:"manifest_digest"`
}

type Summary struct {
	RunKey               string     `json:"run_key,omitempty"`
	Selection            *Selection `json:"selection,omitempty"`
	Versions             []string   `json:"versions,omitempty"`
	RequiredCapabilities []string   `json:"required_capabilities,omitempty"`
	OptionalCapabilities []string   `json:"optional_capabilities,omitempty"`
	TimeoutMS            int        `json:"timeout_ms,omitempty"`
	AllowPaidExecution   *bool      `json:"allow_paid_execution,omitempty"`
	RequestSHA256        string     `json:"request_sha256,omitempty"`
	Policy               string     `json:"policy,omitempty"`
	Parent               *Parent    `json:"parent,omitempty"`
	Source               *Source    `json:"source,omitempty"`
	CheckpointSHA256     string     `json:"checkpoint_sha256,omitempty"`
	Logical              *Logical   `json:"logical,omitempty"`
}

type Translation struct {
	Kind             string       `json:"kind"`
	SchemaVersion    string       `json:"schema_version"`
	Adapter          Identity     `json:"adapter"`
	Status           string       `json:"status"`
	DescriptorSHA256 string       `json:"descriptor_sha256"`
	WorkID           string       `json:"work_id,omitempty"`
	Operation        string       `json:"operation,omitempty"`
	Call             *Call        `json:"call,omitempty"`
	Summary          *Summary     `json:"summary,omitempty"`
	Diagnostics      []Diagnostic `json:"diagnostics"`
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func rejected(descriptorSHA string, code, message string) Translation {
	return Translation{
		Kind: TranslationKind, SchemaVersion: SchemaVersion, Adapter: Adapter(), Status: "rejected",
		DescriptorSHA256: descriptorSHA, Diagnostics: []Diagnostic{{Code: code, Message: truncate(message)}},
	}
}

func truncate(message string) string {
	if runes := []rune(message); len(runes) > 4000 {
		return string(runes[:4000]) + " (truncated)"
	}
	return message
}

// Unreadable reports a descriptor the host could not read in full.
func Unreadable(data []byte, cause error) Translation {
	return rejected(digest(data), CodeDescriptorUnavailable, fmt.Sprintf("work descriptor could not be read: %v", cause))
}

// Translate maps one descriptor to exactly one public call. It is offline:
// it executes nothing, opens no store and never injects defaults. The runner
// request and optional policy are forwarded as their original bytes.
func Translate(data []byte) Translation {
	descriptorSHA := digest(data)
	if len(data) > MaxDescriptorBytes {
		return rejected(descriptorSHA, CodeDescriptorTooLarge, fmt.Sprintf("work descriptor exceeds %d bytes", MaxDescriptorBytes))
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return rejected(descriptorSHA, CodeDescriptorInvalid, fmt.Sprintf("work descriptor is not strict JSON (duplicate keys, invalid Unicode and trailing data are rejected): %v", err))
	}
	fields, ok := doc.(map[string]any)
	if !ok {
		return rejected(descriptorSHA, CodeDescriptorInvalid, "work descriptor must be a JSON object")
	}
	var unknown []string
	for key := range fields {
		if !slices.Contains(descriptorFields, key) {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return rejected(descriptorSHA, CodeDescriptorUnsupported, fmt.Sprintf(
			"fields %s cannot be translated to the runner contract and would be lost; queueing, placement, priority, fleet and retry policy stay with the external scheduler",
			strings.Join(unknown, ", ")))
	}
	operation, _ := fields["operation"].(string)
	if rules, ok := fieldsByOperation[operation]; ok {
		for _, key := range rules.required {
			if _, present := fields[key]; !present {
				return rejected(descriptorSHA, CodeDescriptorIncomplete, fmt.Sprintf("operation %q requires %q; no value is inferred", operation, key))
			}
		}
		for _, key := range []string{"request", "policy", "checkpoint_sha256", "logical"} {
			if _, present := fields[key]; present && !slices.Contains(rules.allowed, key) {
				return rejected(descriptorSHA, CodeDescriptorAmbiguous, fmt.Sprintf("operation %q does not use %q; remove it rather than have it ignored", operation, key))
			}
		}
	}
	schema, err := descriptorSchema()
	if err != nil {
		return rejected(descriptorSHA, CodeDescriptorInvalid, err.Error())
	}
	if err := schema.Validate(doc); err != nil {
		return rejected(descriptorSHA, CodeDescriptorInvalid, fmt.Sprintf("work descriptor schema: %v", err))
	}
	var head struct {
		WorkID     string   `json:"work_id"`
		Checkpoint string   `json:"checkpoint_sha256"`
		Logical    *Logical `json:"logical"`
		Policy     *struct {
			Mode   string  `json:"mode"`
			Parent *Parent `json:"parent"`
			Source *Source `json:"source"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return rejected(descriptorSHA, CodeDescriptorInvalid, fmt.Sprintf("decoding work descriptor: %v", err))
	}
	translation := Translation{
		Kind: TranslationKind, SchemaVersion: SchemaVersion, Adapter: Adapter(), Status: "translated",
		DescriptorSHA256: descriptorSHA, WorkID: head.WorkID, Operation: operation, Diagnostics: []Diagnostic{},
	}
	if operation == OperationStatus {
		query := url.Values{"run": {head.Logical.Run}, "work": {head.Logical.Work}}
		translation.Call = &Call{
			Command: []string{"runner", "lineage", "--run", head.Logical.Run, "--work", head.Logical.Work},
			HTTP:    HTTPRoute{Method: "GET", Path: "/api/v1/lineage?" + query.Encode()},
		}
		translation.Summary = &Summary{Logical: head.Logical}
		return translation
	}
	rawRequest, err := member(data, "request")
	if err != nil {
		return rejected(descriptorSHA, CodeDescriptorInvalid, err.Error())
	}
	request, err := runnercontract.Parse(rawRequest)
	if err != nil {
		return rejected(descriptorSHA, CodeRequestInvalid, fmt.Sprintf("request is not a complete runner request: %v", err))
	}
	var input bytes.Buffer
	input.WriteString(`{"kind":"waza.execution-submission","schema_version":"1.0","request":`)
	input.Write(rawRequest)
	summary := &Summary{
		RunKey:               request.RunKey,
		Selection:            &Selection{Shard: request.Selection.Shard, Task: request.Selection.Task, Trial: request.Selection.Trial, Attempt: request.Selection.Attempt},
		Versions:             request.Versions,
		RequiredCapabilities: request.RequiredCapabilities,
		OptionalCapabilities: request.OptionalCapabilities,
		TimeoutMS:            request.TimeoutMS,
		AllowPaidExecution:   &request.AllowPaidExecution,
		RequestSHA256:        digest(rawRequest),
		Policy:               "omitted",
		CheckpointSHA256:     head.Checkpoint,
	}
	if _, present := fields["policy"]; present {
		rawPolicy, err := member(data, "policy")
		if err != nil {
			return rejected(descriptorSHA, CodeDescriptorInvalid, err.Error())
		}
		input.WriteString(`,"policy":`)
		input.Write(rawPolicy)
		summary.Policy, summary.Parent, summary.Source = head.Policy.Mode, head.Policy.Parent, head.Policy.Source
	}
	input.WriteByte('}')
	call := &Call{Input: input.String(), InputSHA256: digest(input.Bytes())}
	switch operation {
	case OperationSubmit:
		call.Command, call.HTTP = []string{"runner", "submit"}, HTTPRoute{Method: "POST", Path: "/api/v1/submissions"}
	case OperationCheckpoint:
		call.Command, call.HTTP = []string{"runner", "checkpoint", "create"}, HTTPRoute{Method: "POST", Path: "/api/v1/checkpoints"}
	case OperationResume:
		call.Command = []string{"runner", "checkpoint", "resume", head.Checkpoint}
		call.HTTP = HTTPRoute{Method: "POST", Path: "/api/v1/checkpoints/" + head.Checkpoint + "/resume"}
	}
	translation.Call, translation.Summary = call, summary
	return translation
}

func member(data []byte, name string) ([]byte, error) {
	start, end, found, err := jsonutil.ObjectMemberRange(data, name)
	if err != nil {
		return nil, fmt.Errorf("locating %q: %w", name, err)
	}
	if !found {
		return nil, fmt.Errorf("work descriptor has no %q member", name)
	}
	return data[start:end], nil
}

// EncodeTranslation validates the document against its published schema.
func EncodeTranslation(t Translation) ([]byte, error) {
	return encodeValidated(t, translationSchema)
}

func encodeValidated(value any, load func() (*jsonschema.Schema, error)) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := bytes.TrimRight(buffer.Bytes(), "\n")
	if err := validate(data, load); err != nil {
		return nil, fmt.Errorf("adapter output: %w", err)
	}
	return data, nil
}

func validate(data []byte, load func() (*jsonschema.Schema, error)) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return err
	}
	schema, err := load()
	if err != nil {
		return err
	}
	return schema.Validate(doc)
}

const schemaBase = "https://microsoft.github.io/waza/schemas/"

var schemaFiles = map[string]*string{
	"adapter-work-descriptor-1.0.schema.json":     &schemas.AdapterWorkDescriptorSchemaJSON,
	"adapter-translation-1.0.schema.json":         &schemas.AdapterTranslationSchemaJSON,
	"adapter-result-1.0.schema.json":              &schemas.AdapterResultSchemaJSON,
	"runner-request-1.0.schema.json":              &schemas.RunnerRequestSchemaJSON,
	"execution-submission-1.0.schema.json":        &schemas.ExecutionSubmissionSchemaJSON,
	"execution-submission-result-1.0.schema.json": &schemas.ExecutionSubmissionResultSchemaJSON,
	"execution-attempt-1.0.schema.json":           &schemas.ExecutionAttemptSchemaJSON,
	"execution-completion-1.0.schema.json":        &schemas.ExecutionCompletionSchemaJSON,
	"execution-checkpoint-1.0.schema.json":        &schemas.ExecutionCheckpointSchemaJSON,
	"execution-lineage-1.0.schema.json":           &schemas.ExecutionLineageSchemaJSON,
	"execution-lifecycle-1.0.schema.json":         &schemas.ExecutionLifecycleSchemaJSON,
	"artifact-manifest-1.0.schema.json":           &schemas.ArtifactManifestSchemaJSON,
	"runner-terminal-1.0.schema.json":             &schemas.RunnerTerminalSchemaJSON,
	"runner-unit-1.0.schema.json":                 &schemas.RunnerUnitSchemaJSON,
	"runner-checkpoint-result-1.0.schema.json":    &schemas.RunnerCheckpointResultSchemaJSON,
	"runner-checkpoint-result-1.1.schema.json":    &schemas.RunnerCheckpointResultV11SchemaJSON,
	"runner-negotiation-request-1.0.schema.json":  &schemas.RunnerNegotiationRequestSchemaJSON,
	"runner-negotiation-response-1.0.schema.json": &schemas.RunnerNegotiationResponseSchemaJSON,
	"runner-capabilities-1.0.schema.json":         &schemas.RunnerCapabilitiesSchemaJSON,
}

// schemaSet compiles every adapter and public-reply schema once, offline.
var schemaSet = sync.OnceValues(func() (map[string]*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range schemaFiles {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(*text))
		if err != nil {
			return nil, fmt.Errorf("reading adapter schema %s: %w", name, err)
		}
		if err := compiler.AddResource(schemaBase+name, doc); err != nil {
			return nil, fmt.Errorf("registering adapter schema %s: %w", name, err)
		}
	}
	compiledSchemas := map[string]*jsonschema.Schema{}
	for name := range schemaFiles {
		schema, err := compiler.Compile(schemaBase + name)
		if err != nil {
			return nil, fmt.Errorf("compiling adapter schema %s: %w", name, err)
		}
		compiledSchemas[name] = schema
	}
	return compiledSchemas, nil
})

func compiled(name string) func() (*jsonschema.Schema, error) {
	return func() (*jsonschema.Schema, error) {
		set, err := schemaSet()
		if err != nil {
			return nil, err
		}
		return set[name], nil
	}
}

var (
	descriptorSchema       = compiled("adapter-work-descriptor-1.0.schema.json")
	translationSchema      = compiled("adapter-translation-1.0.schema.json")
	resultSchema           = compiled("adapter-result-1.0.schema.json")
	submissionResultSchema = compiled("execution-submission-result-1.0.schema.json")
	checkpointResultSchema = compiled("runner-checkpoint-result-1.0.schema.json")
	checkpointErrorSchema  = compiled("runner-checkpoint-result-1.1.schema.json")
	lineageSchema          = compiled("execution-lineage-1.0.schema.json")
)
