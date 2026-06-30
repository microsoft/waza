package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestNewDisabled(t *testing.T) {
	p, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.Enabled() {
		t.Fatal("disabled config should yield disabled provider")
	}
	// Shutdown should be a no-op (shutdown func is nil).
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown disabled: %v", err)
	}
	// SetGlobal on a disabled provider must not change the global.
	prev := otel.GetTracerProvider()
	p.SetGlobal()
	if otel.GetTracerProvider() != prev {
		t.Fatal("SetGlobal on disabled provider mutated global")
	}
	// Tracer still works.
	if p.Tracer() == nil {
		t.Fatal("Tracer() is nil")
	}
}

func TestNewValidationError(t *testing.T) {
	if _, err := New(context.Background(), Config{Exporter: ExporterKind("garbage")}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestNewFileExporter(t *testing.T) {
	// Use os.MkdirTemp + best-effort cleanup so that any Windows file-handle
	// retention by the stdouttrace exporter doesn't fail the test during
	// teardown (t.TempDir surfaces RemoveAll errors).
	dir, err := os.MkdirTemp("", "waza-telemetry-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "traces.jsonl")
	cfg := Config{Exporter: ExporterFile, FilePath: path, ServiceName: "svc", ServiceVersion: "v1"}
	p, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.Enabled() {
		t.Fatal("Enabled should be true")
	}
	if p.Config().FilePath != path {
		t.Fatalf("Config.FilePath = %q", p.Config().FilePath)
	}

	// Emit a span and shut down so the exporter flushes to disk.
	_, span := p.Tracer().Start(context.Background(), "test-span")
	span.End()
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("trace file is empty")
	}
}

func TestNewFileExporterBadPath(t *testing.T) {
	cfg := Config{Exporter: ExporterFile, FilePath: filepath.Join(t.TempDir(), "no-such-dir", "x.jsonl")}
	// Validate passes; OpenFile should fail because the parent directory does not exist.
	if _, err := New(context.Background(), cfg); err == nil {
		t.Fatal("expected file open error")
	}
}

func TestNewStdoutExporter(t *testing.T) {
	p, err := New(context.Background(), Config{Exporter: ExporterStdout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !p.Enabled() {
		t.Fatal("Enabled false for stdout")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestNewOTLPExporterVariants(t *testing.T) {
	// All of these construct the exporter without dialing — exporter dial
	// happens lazily on first export.
	cases := []Config{
		{Exporter: ExporterOTLP},
		{Exporter: ExporterOTLP, Endpoint: "localhost:4318"},
		{Exporter: ExporterOTLP, Endpoint: "http://collector.example.com/v1/traces"},
		{Exporter: ExporterOTLP, Endpoint: "https://collector.example.com/v1/traces", Headers: map[string]string{"x-key": "v"}},
	}
	for _, cfg := range cases {
		p, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
		_ = p.Shutdown(context.Background())
	}
}

func TestSetGlobalEnabled(t *testing.T) {
	prev := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	p, err := New(context.Background(), Config{Exporter: ExporterStdout})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Shutdown(context.Background()) })

	p.SetGlobal()
	if otel.GetTracerProvider() == prev {
		t.Fatal("SetGlobal did not change global provider")
	}
}

func TestNilProviderSafe(t *testing.T) {
	var p *Provider
	if p.Enabled() {
		t.Fatal("nil should be disabled")
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
	if got := p.Config(); got.Exporter != "" || got.Endpoint != "" || got.IncludePayloads {
		t.Fatalf("nil Config should be zero, got %+v", got)
	}
	tr := p.Tracer()
	if tr == nil {
		t.Fatal("nil Tracer() returned nil")
	}
	// nil provider's tracer is a noop tracer; just confirm we can call Start.
	_, span := tr.Start(context.Background(), "noop")
	span.End()
	// And SetGlobal must not panic.
	p.SetGlobal()
	_ = noop.NewTracerProvider() // sanity import use
}

func TestHasScheme(t *testing.T) {
	cases := map[string]bool{
		"":                      false,
		"http://example.com":    true,
		"https://example.com":   true,
		"otlp+grpc://x":         true,
		"localhost:4318":        false,
		"://":                   true, // empty scheme but :// pattern is detected
		"badscheme":             false,
		"a.b.c:1234":            false,
		"h+t-t.p://ok":          true,
		"1http://numeric-start": true, // digits are allowed in scheme chars
	}
	for in, want := range cases {
		if got := hasScheme(in); got != want {
			t.Errorf("hasScheme(%q) = %v, want %v", in, got, want)
		}
	}
}
