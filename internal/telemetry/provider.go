package telemetry

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TracerName is the OpenTelemetry instrumentation scope used by waza spans.
const TracerName = "github.com/microsoft/waza"

// Provider wraps a configured tracer plus its Shutdown hook. Callers should
// always call Shutdown (best-effort, on a fresh context) to flush spans
// before the process exits.
type Provider struct {
	cfg      Config
	tp       trace.TracerProvider
	tracer   trace.Tracer
	shutdown func(context.Context) error
}

// Tracer returns the tracer that should be used to start spans. When
// telemetry is disabled, the returned tracer is a no-op so callers can
// always wrap their work in spans without conditionals.
func (p *Provider) Tracer() trace.Tracer {
	if p == nil || p.tracer == nil {
		return noop.NewTracerProvider().Tracer(TracerName)
	}
	return p.tracer
}

// Config returns the configuration the provider was built with. Useful for
// downstream code that needs to know whether payloads should be included
// (Config.IncludePayloads).
func (p *Provider) Config() Config {
	if p == nil {
		return Config{}
	}
	return p.cfg
}

// Enabled reports whether the provider is exporting spans to a real backend
// (as opposed to a no-op tracer).
func (p *Provider) Enabled() bool {
	return p != nil && p.cfg.Enabled()
}

// Shutdown flushes pending spans and releases exporter resources. Safe to
// call when telemetry is disabled (it is a no-op in that case).
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// New initializes telemetry from cfg and returns a Provider. When cfg is
// disabled (Exporter == ExporterNone) a no-op provider is returned with a
// nil shutdown function — calls to Tracer() still succeed and emit nothing.
//
// New does not register the provider globally; callers that want spans
// created elsewhere in the process (e.g. by the Copilot SDK) can opt in by
// calling SetGlobal on the returned provider.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled() {
		noopTP := noop.NewTracerProvider()
		return &Provider{cfg: cfg, tp: noopTP, tracer: noopTP.Tracer(TracerName)}, nil
	}

	exp, err := newExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create exporter: %w", err)
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		// Closing the exporter here is best-effort; we are about to return an
		// error and the caller never sees the provider.
		_ = exp.Shutdown(ctx) //nolint:errcheck
		return nil, fmt.Errorf("telemetry: create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)

	return &Provider{
		cfg:      cfg,
		tp:       tp,
		tracer:   tp.Tracer(TracerName),
		shutdown: tp.Shutdown,
	}, nil
}

// SetGlobal registers p as the global OTel TracerProvider. Optional; only
// useful when downstream libraries (Copilot SDK, etc.) read from the global
// provider rather than receiving a tracer explicitly. No-op when telemetry
// is disabled.
func (p *Provider) SetGlobal() {
	if p == nil || p.tp == nil || !p.Enabled() {
		return
	}
	otel.SetTracerProvider(p.tp)
}

func newExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch cfg.Exporter {
	case ExporterOTLP:
		opts := []otlptracehttp.Option{}
		if cfg.Endpoint != "" {
			// otlptracehttp accepts either a bare host:port via WithEndpoint
			// or a full URL via WithEndpointURL. Prefer URL when the user
			// passed one so the path/scheme are preserved.
			if hasScheme(cfg.Endpoint) {
				opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
			} else {
				opts = append(opts, otlptracehttp.WithEndpoint(cfg.Endpoint), otlptracehttp.WithInsecure())
			}
		}
		if len(cfg.Headers) > 0 {
			opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
		}
		return otlptracehttp.New(ctx, opts...)
	case ExporterStdout:
		// Newline-delimited JSON (one span per line) so the output can be
		// piped to tools like `jq -c` without the multi-line indentation
		// added by stdouttrace.WithPrettyPrint().
		return stdouttrace.New()
	case ExporterFile:
		f, err := os.OpenFile(cfg.FilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, err
		}
		// stdouttrace closes its writer on Shutdown via WithWriter; we wrap
		// the file so the descriptor is released when the provider stops.
		// No WithPrettyPrint so the file is newline-delimited JSON.
		return stdouttrace.New(stdouttrace.WithWriter(f))
	default:
		return nil, fmt.Errorf("unsupported exporter %q", string(cfg.Exporter))
	}
}

func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	name := cfg.ServiceName
	if name == "" {
		name = "waza"
	}
	attrs := []resource.Option{
		resource.WithAttributes(
			SemConvServiceName.String(name),
		),
	}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs,
			resource.WithAttributes(SemConvServiceVersion.String(cfg.ServiceVersion)),
		)
	}
	return resource.New(ctx, attrs...)
}

func hasScheme(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ':':
			return i+2 < len(s) && s[i+1] == '/' && s[i+2] == '/'
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.':
			continue
		default:
			return false
		}
	}
	return false
}
