// Package telemetry provides opt-in OpenTelemetry trace export for waza
// runs. It is off by default. When disabled, the package returns no-op
// tracers and adds no runtime cost to users who do not enable it.
//
// Spans follow the OpenTelemetry GenAI semantic conventions
// (https://opentelemetry.io/docs/specs/semconv/gen-ai/). The span
// hierarchy emitted by waza is:
//
//	eval (root)
//	└── task (per test case)
//	    └── turn (per Execute call: initial + follow-ups + responder)
//	        ├── model_call (one per recorded model invocation)
//	        └── tool_call  (one per recorded tool invocation)
//
// Payload redaction is on by default: prompt text, tool arguments, and
// model output content are dropped. The --otel-include-payloads flag
// (Config.IncludePayloads) opts back in.
package telemetry

import (
	"fmt"
	"strings"
)

// ExporterKind selects the span exporter implementation.
type ExporterKind string

const (
	// ExporterNone disables telemetry. This is the default.
	ExporterNone ExporterKind = ""
	// ExporterOTLP exports via OTLP/HTTP to an OpenTelemetry collector.
	ExporterOTLP ExporterKind = "otlp"
	// ExporterStdout writes spans to stdout (for debugging).
	ExporterStdout ExporterKind = "stdout"
	// ExporterFile writes spans as JSON to a file (for debugging / CI artifacts).
	ExporterFile ExporterKind = "file"
)

// Config controls OpenTelemetry trace export. The zero value disables
// telemetry; callers must explicitly set Exporter for spans to be emitted.
type Config struct {
	// Exporter selects which span exporter to use. When empty, telemetry is off.
	Exporter ExporterKind

	// Endpoint is the OTLP/HTTP endpoint (e.g. "localhost:4318" or
	// "https://collector.example.com/v1/traces"). Only meaningful when
	// Exporter == ExporterOTLP.
	Endpoint string

	// Headers carries arbitrary OTLP headers (e.g. for authentication).
	// Parsed from --otel-headers=k1=v1,k2=v2.
	Headers map[string]string

	// FilePath is the destination when Exporter == ExporterFile.
	FilePath string

	// IncludePayloads disables payload redaction so prompt text, tool
	// arguments, and model output content are written into span attributes.
	// Default: false (payloads are redacted).
	IncludePayloads bool

	// ServiceName overrides the OTel resource service.name attribute.
	// Defaults to "waza".
	ServiceName string

	// ServiceVersion sets the OTel resource service.version attribute.
	ServiceVersion string
}

// Enabled reports whether telemetry should be initialized.
func (c Config) Enabled() bool {
	return c.Exporter != ExporterNone
}

// Validate returns an error when the configuration is internally inconsistent.
// A disabled config is always valid.
func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	switch c.Exporter {
	case ExporterOTLP, ExporterStdout, ExporterFile:
	default:
		return fmt.Errorf("unsupported --otel-exporter %q (expected otlp|stdout|file)", string(c.Exporter))
	}
	if c.Exporter == ExporterFile && strings.TrimSpace(c.FilePath) == "" {
		return fmt.Errorf("--otel-exporter=file requires --otel-file <path>")
	}
	return nil
}

// ParseHeaders parses a comma-separated list of key=value pairs into a map.
// Whitespace around keys and values is trimmed. Empty input returns nil.
// An empty key or a missing '=' produces an error.
func ParseHeaders(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make(map[string]string, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			return nil, fmt.Errorf("invalid header %q: expected key=value", p)
		}
		key := strings.TrimSpace(p[:eq])
		val := strings.TrimSpace(p[eq+1:])
		if key == "" {
			return nil, fmt.Errorf("invalid header %q: empty key", p)
		}
		out[key] = val
	}
	return out, nil
}
