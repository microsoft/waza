package executiontelemetry

// Report is the waza.execution-telemetry 1.0 document: correlation
// identifiers, source availability, signal counts, drop diagnostics and export
// health. It never carries payloads, prompts, outputs, headers or credentials.
type Report struct {
	Kind          string            `json:"kind"`
	SchemaVersion string            `json:"schema_version"`
	PayloadPolicy PayloadPolicy     `json:"payload_policy"`
	Resource      map[string]string `json:"resource"`
	Correlation   Correlation       `json:"correlation"`
	Sources       []SourceState     `json:"sources"`
	Signals       Signals           `json:"signals"`
	Dropped       []Drop            `json:"dropped"`
	Exports       []Export          `json:"exports"`
}

type Correlation struct {
	TraceID         string                  `json:"trace_id"`
	RootSpanID      string                  `json:"root_span_id"`
	Run             string                  `json:"run"`
	Work            string                  `json:"work"`
	SchedulerWorkID string                  `json:"scheduler_work_id,omitempty"`
	IdentityDigest  string                  `json:"identity_digest"`
	Shard           string                  `json:"shard"`
	Task            string                  `json:"task"`
	Trial           string                  `json:"trial"`
	Attempt         string                  `json:"attempt"`
	TaskSpanID      string                  `json:"task_span_id"`
	Attempts        []AttemptCorrelation    `json:"attempts"`
	Artifacts       []ArtifactCorrelation   `json:"artifacts"`
	Aggregation     *AggregationCorrelation `json:"aggregation,omitempty"`
}

type AttemptCorrelation struct {
	SpanID            string `json:"span_id"`
	ExecutionID       string `json:"execution_id,omitempty"`
	ParentExecutionID string `json:"parent_execution_id,omitempty"`
	Relationship      string `json:"relationship,omitempty"`
	NativeAttempt     int    `json:"native_attempt"`
	Status            string `json:"status"`
	Outcome           string `json:"outcome,omitempty"`
	ManifestSHA256    string `json:"manifest_sha256,omitempty"`
	Selected          bool   `json:"selected"`
}

type ArtifactCorrelation struct {
	SpanID    string `json:"span_id"`
	Name      string `json:"name"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Terminal  bool   `json:"terminal"`
}

type AggregationCorrelation struct {
	SpanID  string       `json:"span_id"`
	SHA256  string       `json:"sha256"`
	State   string       `json:"state"`
	Members []MemberLink `json:"members"`
}

type MemberLink struct {
	ManifestSHA256 string `json:"manifest_sha256"`
	State          string `json:"state"`
	TraceID        string `json:"trace_id,omitempty"`
	SpanID         string `json:"span_id,omitempty"`
	ExecutionID    string `json:"execution_id,omitempty"`
}

type SourceState struct {
	Source string `json:"source"`
	State  string `json:"state"`
}

type SignalCount struct {
	Emitted int            `json:"emitted"`
	Dropped int            `json:"dropped"`
	Names   map[string]int `json:"names"`
}

type Signals struct {
	Traces  SignalCount `json:"traces"`
	Metrics SignalCount `json:"metrics"`
	Logs    SignalCount `json:"logs"`
}

type Drop struct {
	Signal string `json:"signal"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// Export states.
const (
	ExportExported = "exported"
	ExportFailed   = "failed"
	ExportSkipped  = "skipped"
)

type Export struct {
	Signal      string `json:"signal"`
	Destination string `json:"destination"`
	Target      string `json:"target"`
	State       string `json:"state"`
	HTTPStatus  int    `json:"http_status,omitempty"`
	ErrorCode   string `json:"error_code,omitempty"`
}

// Signal identifiers used in reports, drops and exports.
const (
	SignalTraces  = "traces"
	SignalMetrics = "metrics"
	SignalLogs    = "logs"
)
