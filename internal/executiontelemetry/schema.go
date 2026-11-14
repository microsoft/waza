package executiontelemetry

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/microsoft/waza/internal/jsonutil"
	"github.com/microsoft/waza/internal/schemaloader"
	"github.com/microsoft/waza/schemas"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const reportSchemaURL = "https://microsoft.github.io/waza/schemas/execution-telemetry-1.0.schema.json"

var reportSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaloader.Offline{})
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(schemas.ExecutionTelemetrySchemaJSON))
	if err != nil {
		return nil, fmt.Errorf("reading execution telemetry schema: %w", err)
	}
	if err := compiler.AddResource(reportSchemaURL, doc); err != nil {
		return nil, fmt.Errorf("registering execution telemetry schema: %w", err)
	}
	return compiler.Compile(reportSchemaURL)
})

// EncodeReport validates a report against its published schema.
func EncodeReport(report Report) ([]byte, error) {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding execution telemetry report: %w", err)
	}
	if _, err := ParseReport(data); err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// ParseReport admits a report only when it matches the published schema.
func ParseReport(data []byte) (*Report, error) {
	doc, err := jsonutil.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parsing execution telemetry report: %w", err)
	}
	schema, err := reportSchema()
	if err != nil {
		return nil, err
	}
	if err := schema.Validate(doc); err != nil {
		return nil, fmt.Errorf("execution telemetry report schema: %w", err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("decoding execution telemetry report: %w", err)
	}
	return &report, nil
}
