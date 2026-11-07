package ownedsubmission

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionlifecycle"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type LifecycleSourceRequest struct {
	Kind          string                  `json:"kind"`
	SchemaVersion string                  `json:"schema_version"`
	Source        executionattempt.Source `json:"source"`
	AsOf          string                  `json:"as_of,omitempty"`
	StaleAfterMS  int64                   `json:"stale_after_ms"`
}

type lifecycleSourceSchemaPair struct {
	request *jsonschema.Schema
	result  *jsonschema.Schema
}

var lifecycleSourceSchemas = sync.OnceValues(func() (lifecycleSourceSchemaPair, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-lifecycle-source-inspection-1.0.schema.json": schemas.ExecutionLifecycleSourceInspectionSchemaJSON,
		"execution-lifecycle-source-1.0.schema.json":            schemas.ExecutionLifecycleSourceSchemaJSON,
		"execution-attempt-1.0.schema.json":                     schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":                     schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":                       schemas.RunnerTerminalSchemaJSON,
		"execution-lifecycle-1.0.schema.json":                   schemas.ExecutionLifecycleSchemaJSON,
		"execution-lifecycle-observation-1.0.schema.json":       schemas.ExecutionLifecycleObservationSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return lifecycleSourceSchemaPair{}, fmt.Errorf("reading lifecycle source schema %s: %w", name, err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return lifecycleSourceSchemaPair{}, fmt.Errorf("registering lifecycle source schema %s: %w", name, err)
		}
	}
	request, err := compiler.Compile("https://microsoft.github.io/waza/schemas/execution-lifecycle-source-inspection-1.0.schema.json")
	if err != nil {
		return lifecycleSourceSchemaPair{}, err
	}
	result, err := compiler.Compile("https://microsoft.github.io/waza/schemas/execution-lifecycle-source-1.0.schema.json")
	return lifecycleSourceSchemaPair{request: request, result: result}, err
})

func ParseLifecycleSourceRequest(data []byte) (*LifecycleSourceRequest, error) {
	if len(data) > executionlifecycle.MaxBytes {
		return nil, fmt.Errorf("%w: lifecycle source request exceeds byte bound", executionlifecycle.ErrInvalid)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: lifecycle source request JSON: %w", executionlifecycle.ErrInvalid, err)
	}
	pair, err := lifecycleSourceSchemas()
	if err != nil {
		return nil, fmt.Errorf("compiling lifecycle source schema: %w", err)
	}
	if err := pair.request.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: lifecycle source request schema: %w", executionlifecycle.ErrInvalid, err)
	}
	var request LifecycleSourceRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("%w: decoding lifecycle source request: %w", executionlifecycle.ErrInvalid, err)
	}
	if request.AsOf != "" {
		at, err := time.Parse(time.RFC3339Nano, request.AsOf)
		if err != nil || at.Year() < 1 || at.UTC().Format(time.RFC3339Nano) != request.AsOf {
			return nil, fmt.Errorf("%w: lifecycle source as_of must be canonical UTC", executionlifecycle.ErrInvalid)
		}
	}
	return &request, nil
}

func EncodeLifecycleInspection(value LifecycleInspection) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encoding lifecycle source: %w", err)
	}
	if len(data) > executionlifecycle.MaxBytes {
		return nil, fmt.Errorf("%w: lifecycle source response exceeds byte bound", executionlifecycle.ErrInvalid)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("validating lifecycle source response JSON: %w", err)
	}
	pair, err := lifecycleSourceSchemas()
	if err != nil {
		return nil, fmt.Errorf("compiling lifecycle source response schema: %w", err)
	}
	if err := pair.result.Validate(doc); err != nil {
		return nil, fmt.Errorf("%w: lifecycle source response schema: %w", executionlifecycle.ErrInvalid, err)
	}
	if value.Source.ExecutionID != value.Snapshot.ExecutionID {
		return nil, fmt.Errorf("%w: lifecycle source execution contradicts projection", executionlifecycle.ErrContext)
	}
	return data, nil
}
