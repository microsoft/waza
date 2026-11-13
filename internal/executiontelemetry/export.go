package executiontelemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// File names written by the file exporter. Each signal file is one OTLP/JSON
// export request that can be replayed to any OTLP/HTTP JSON receiver.
const (
	TracesFile  = "traces.otlp.json"
	MetricsFile = "metrics.otlp.json"
	LogsFile    = "logs.otlp.json"
	ReportFile  = "telemetry-report.json"
)

// Export destinations and sanitized error codes.
const (
	DestinationFile = "file"
	DestinationOTLP = "otlp_http"

	ExportPartial = "partial"

	ErrorOutputExists   = "output_exists"
	ErrorWrite          = "write_error"
	ErrorTransport      = "transport_error"
	ErrorTimeout        = "timeout"
	ErrorHTTPStatus     = "http_status"
	ErrorPartialSuccess = "partial_success"
)

// ErrExport reports at least one failed signal export.
var ErrExport = errors.New("telemetry export failed")

// ExportOptions select destinations. Header values are sent but never
// recorded in the report.
type ExportOptions struct {
	Dir          string
	OTLPEndpoint string
	Headers      map[string]string
	Client       *http.Client
	Timeout      time.Duration
}

// ValidateEndpoint admits an absolute http(s) OTLP base URL without
// credentials, query or fragment and returns the redacted target.
func ValidateEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("OTLP endpoint must be an absolute http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("OTLP endpoint must not contain credentials, a query or a fragment; use headers")
	}
	return u, nil
}

func signalDocuments(bundle *Bundle) []struct {
	signal, file, path string
	doc                any
} {
	return []struct {
		signal, file, path string
		doc                any
	}{
		{SignalTraces, TracesFile, "/v1/traces", bundle.Traces},
		{SignalMetrics, MetricsFile, "/v1/metrics", bundle.Metrics},
		{SignalLogs, LogsFile, "/v1/logs", bundle.Logs},
	}
}

// Deliver writes and/or sends every signal and records per-signal export
// health in bundle.Report.Exports. When Dir is set the report is written last.
func Deliver(ctx context.Context, bundle *Bundle, opts ExportOptions) error {
	var failed []string
	var endpoint *url.URL
	if opts.OTLPEndpoint != "" {
		var err error
		if endpoint, err = ValidateEndpoint(opts.OTLPEndpoint); err != nil {
			return err
		}
	}
	for _, s := range signalDocuments(bundle) {
		data, err := json.Marshal(s.doc)
		if err != nil {
			return fmt.Errorf("encoding %s: %w", s.signal, err)
		}
		if opts.Dir != "" {
			result := Export{Signal: s.signal, Destination: DestinationFile, Target: s.file, State: ExportExported}
			if code := writeExclusive(opts.Dir, s.file, data); code != "" {
				result.State, result.ErrorCode = ExportFailed, code
				failed = append(failed, s.signal+" file")
			}
			bundle.Report.Exports = append(bundle.Report.Exports, result)
		}
		if endpoint != nil {
			result := postOTLP(ctx, endpoint, s.path, data, opts)
			if result.State == ExportFailed {
				failed = append(failed, s.signal+" otlp_http")
			}
			result.Signal = s.signal
			bundle.Report.Exports = append(bundle.Report.Exports, result)
		}
	}
	if opts.Dir != "" {
		report, err := EncodeReport(bundle.Report)
		if err != nil {
			return err
		}
		if code := writeExclusive(opts.Dir, ReportFile, report); code != "" {
			failed = append(failed, "report file ("+code+")")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%w: %s", ErrExport, strings.Join(failed, ", "))
	}
	return nil
}

func writeExclusive(dir, name string, data []byte) string {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ErrorWrite
	}
	file, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrorOutputExists
		}
		return ErrorWrite
	}
	_, writeErr := file.Write(data)
	if errors.Join(writeErr, file.Close()) != nil {
		return ErrorWrite
	}
	return ""
}

type partialSuccess struct {
	PartialSuccess *struct {
		RejectedSpans      json.Number `json:"rejectedSpans"`
		RejectedDataPoints json.Number `json:"rejectedDataPoints"`
		RejectedLogRecords json.Number `json:"rejectedLogRecords"`
	} `json:"partialSuccess"`
}

func postOTLP(ctx context.Context, endpoint *url.URL, path string, data []byte, opts ExportOptions) Export {
	target := *endpoint
	target.Path = strings.TrimRight(target.Path, "/") + path
	result := Export{Destination: DestinationOTLP, Target: target.Scheme + "://" + target.Host + target.Path, State: ExportExported}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(data))
	if err != nil {
		result.State, result.ErrorCode = ExportFailed, ErrorTransport
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range opts.Headers {
		req.Header.Set(key, value)
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		result.State, result.ErrorCode = ExportFailed, ErrorTransport
		if errors.Is(err, context.DeadlineExceeded) {
			result.ErrorCode = ErrorTimeout
		}
		return result
	}
	defer resp.Body.Close() //nolint:errcheck // response body is drained best-effort
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	result.HTTPStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		result.State, result.ErrorCode = ExportFailed, ErrorHTTPStatus
		return result
	}
	var partial partialSuccess
	if json.Unmarshal(body, &partial) == nil && partial.PartialSuccess != nil {
		for _, n := range []json.Number{partial.PartialSuccess.RejectedSpans, partial.PartialSuccess.RejectedDataPoints, partial.PartialSuccess.RejectedLogRecords} {
			if v, err := strconv.ParseInt(string(n), 10, 64); err == nil && v > 0 {
				result.State, result.ErrorCode = ExportPartial, ErrorPartialSuccess
			}
		}
	}
	return result
}
