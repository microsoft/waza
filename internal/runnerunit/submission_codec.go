package runnerunit

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const MaxSubmissionBytes = runnercontract.MaxRequestBytes + 8192

type SubmissionPolicy struct {
	Mode   string                   `json:"mode"`
	Parent *executionattempt.Parent `json:"parent,omitempty"`
	Source *executionattempt.Source `json:"source,omitempty"`
}

type SubmissionRequest struct {
	Kind          string            `json:"kind"`
	SchemaVersion string            `json:"schema_version"`
	Request       json.RawMessage   `json:"request"`
	Policy        *SubmissionPolicy `json:"policy,omitempty"`
}

var submissionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSubmissionSchema("execution-submission-1.0.schema.json")
})

var runnerUnitReportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	return compileSubmissionSchema("runner-unit-1.0.schema.json")
})

func validateRunnerUnitReport(data []byte) error {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return fmt.Errorf("parsing source runner-unit report: %w", err)
	}
	schema, err := runnerUnitReportSchema()
	if err != nil {
		return err
	}
	if err := schema.Validate(doc); err != nil {
		return fmt.Errorf("source runner-unit report schema: %w", err)
	}
	return nil
}

func compileSubmissionSchema(name string) (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	for name, text := range map[string]string{
		"execution-submission-1.0.schema.json":        schemas.ExecutionSubmissionSchemaJSON,
		"runner-request-1.0.schema.json":              schemas.RunnerRequestSchemaJSON,
		"execution-attempt-1.0.schema.json":           schemas.ExecutionAttemptSchemaJSON,
		"artifact-manifest-1.0.schema.json":           schemas.ArtifactManifestSchemaJSON,
		"runner-terminal-1.0.schema.json":             schemas.RunnerTerminalSchemaJSON,
		"execution-submission-result-1.0.schema.json": schemas.ExecutionSubmissionResultSchemaJSON,
		"execution-completion-1.0.schema.json":        schemas.ExecutionCompletionSchemaJSON,
		"runner-unit-1.0.schema.json":                 schemas.RunnerUnitSchemaJSON,
		"runner-negotiation-response-1.0.schema.json": schemas.RunnerNegotiationResponseSchemaJSON,
	} {
		doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
		if err != nil {
			return nil, fmt.Errorf("reading submission schema: %w", err)
		}
		if err := compiler.AddResource("https://microsoft.github.io/waza/schemas/"+name, doc); err != nil {
			return nil, fmt.Errorf("registering submission schema: %w", err)
		}
	}
	return compiler.Compile("https://microsoft.github.io/waza/schemas/" + name)
}

func ParseSubmission(data []byte) (*SubmissionRequest, error) {
	if len(data) > MaxSubmissionBytes {
		return nil, fmt.Errorf("execution submission exceeds %d bytes", MaxSubmissionBytes)
	}
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing execution submission: %w", err)
	}
	schema, err := submissionSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("execution submission schema: %w", err)
	}
	var request SubmissionRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return nil, fmt.Errorf("decoding execution submission: %w", err)
	}
	return &request, nil
}
