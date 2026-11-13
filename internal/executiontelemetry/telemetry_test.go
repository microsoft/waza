package executiontelemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/executionattempt"
	"github.com/microsoft/waza/internal/executionledger"
	"github.com/microsoft/waza/internal/manifestaggregate"
	"github.com/microsoft/waza/internal/runnerterminal"
	"github.com/microsoft/waza/internal/testutil/manifestfixture"
	"github.com/stretchr/testify/require"
)

const (
	secretPrompt = "SECRET-PROMPT-7f3a"
	secretOutput = "SECRET-OUTPUT-91c2"
	secretArgs   = "SECRET-ARGS-44d0"
	secretResult = "SECRET-RESULT-0be8"
)

var fixedNow = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

type fixture struct {
	Inputs
	original, retry manifestfixture.Source
}

// publishFixture runs the actual mock runner twice (an original and its retry)
// through the immutable store and ledger, then reads every public contract
// document the telemetry derivation consumes.
func publishFixture(t *testing.T) fixture {
	t.Helper()
	ctx := t.Context()
	backend, err := artifactmanifest.OpenLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	original := manifestfixture.PublishWith(t, backend, "../../examples/runner/unit.json", manifestfixture.Options{})
	retry := manifestfixture.PublishWith(t, backend, "../../examples/runner/unit.json", manifestfixture.Options{
		ChangeReport: manifestfixture.RecordedNonzero(1),
		Parent:       &executionattempt.Parent{ExecutionID: original.Execution, Relationship: "retry"},
	})

	completed, err := artifactmanifest.NewStore(backend).ReadCompleted(ctx, retry.Manifest)
	require.NoError(t, err)
	terminal := completed.ArtifactBytes[completed.Manifest.TerminalArtifact]
	envelope, err := runnerterminal.Validate(terminal, completed.Manifest.Binding.TerminalBinding())
	require.NoError(t, err)

	ledger, err := executionledger.New(backend)
	require.NoError(t, err)
	reservation, err := ledger.ReadReservation(ctx, retry.Execution)
	require.NoError(t, err)
	lineage, err := ledger.Lineage(ctx, executionattempt.LogicalFrom(*envelope.Identity), executionledger.LineageOptions{Now: fixedNow})
	require.NoError(t, err)
	lineageBytes, err := executionledger.EncodeLineage(*lineage)
	require.NoError(t, err)

	namespace, err := artifactmanifest.NewNamespace(backend, artifactmanifest.ExecutionLedgerNamespaceV1)
	require.NoError(t, err)
	reader, err := namespace.Reader(func(read artifactmanifest.ReadBackend) artifactmanifest.ReadBackend { return read })
	require.NoError(t, err)
	aggregate, err := manifestaggregate.Build(ctx, manifestaggregate.Sources{Objects: backend, Namespace: reader},
		[]manifestaggregate.Input{{Manifest: original.Manifest, Evidence: original.Evidence}, {Manifest: retry.Manifest, Evidence: retry.Evidence}},
		manifestaggregate.DefaultLimits())
	require.NoError(t, err)
	aggregateBytes, err := manifestaggregate.Encode(*aggregate)
	require.NoError(t, err)

	return fixture{Inputs: Inputs{
		Terminal: terminal, Manifest: completed.ManifestBytes, Attempt: reservation.Bytes,
		Lineage: lineageBytes, Report: reportFor(t, *envelope), Aggregate: aggregateBytes,
		SchedulerWorkID: "queue/job-42",
	}, original: original, retry: retry}
}

func reportFor(t *testing.T, envelope runnerterminal.Envelope) []byte {
	t.Helper()
	report := map[string]any{"tasks": []any{map[string]any{
		"test_id": envelope.Selection.Task,
		"runs": []any{
			map[string]any{"run_number": envelope.Observation.RunNumber + 1, "prompt": "other run must not be selected"},
			map[string]any{
				"run_number": envelope.Observation.RunNumber, "prompt": secretPrompt, "final_output": secretOutput,
				"session_digest": map[string]any{
					"usage": map[string]any{"model_metrics": map[string]any{"gpt-test": map[string]any{
						"input_tokens": 10, "output_tokens": 4, "cache_read_tokens": 2, "cache_write_tokens": 1, "request_count": 1,
					}}},
					"tool_calls": []any{
						map[string]any{"id": "call-1", "name": "bash", "arguments": map[string]any{"command": secretArgs}, "result": map[string]any{"content": secretResult}, "success": true},
						map[string]any{"name": "view", "success": false},
					},
				},
			},
		},
	}}}
	data, err := json.Marshal(report)
	require.NoError(t, err)
	return data
}

func build(t *testing.T, in Inputs, opts Options) *Bundle {
	t.Helper()
	src, err := Load(in)
	require.NoError(t, err)
	if opts.Now.IsZero() {
		opts.Now = fixedNow
	}
	opts.ServiceVersion = "test"
	return Build(src, opts)
}

func marshalAll(t *testing.T, bundle *Bundle) string {
	t.Helper()
	var out bytes.Buffer
	for _, doc := range []any{bundle.Traces, bundle.Metrics, bundle.Logs, bundle.Report} {
		data, err := json.Marshal(doc)
		require.NoError(t, err)
		out.Write(data)
	}
	return out.String()
}

func spansByName(bundle *Bundle) map[string][]Span {
	out := map[string][]Span{}
	for _, s := range bundle.Traces.ResourceSpans[0].ScopeSpans[0].Spans {
		out[s.Name] = append(out[s.Name], s)
	}
	return out
}

func attrValue(attrs []KeyValue, key string) (AnyValue, bool) {
	for _, kv := range attrs {
		if kv.Key == key {
			return kv.Value, true
		}
	}
	return AnyValue{}, false
}

func TestConformanceCorrelatesEverySignal(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, f.Inputs, Options{})
	report := bundle.Report

	encoded, err := EncodeReport(report)
	require.NoError(t, err, "report must satisfy its published schema")
	_, err = ParseReport(encoded)
	require.NoError(t, err)

	src, err := Load(f.Inputs)
	require.NoError(t, err)
	identity := src.Terminal.Identity
	require.Equal(t, TraceID(identity.Run, identity.Trial), report.Correlation.TraceID)
	require.Equal(t, WorkSpanID(identity.Run, identity.Trial), report.Correlation.RootSpanID)
	require.Equal(t, "queue/job-42", report.Resource[KeySchedulerWorkID])
	for _, s := range report.Sources {
		require.Equal(t, SourcePresent, s.State, s.Source)
	}

	// Every span and log shares the trace; every non-root span has a known parent.
	spans := bundle.Traces.ResourceSpans[0].ScopeSpans[0].Spans
	ids := map[string]Span{}
	for _, s := range spans {
		require.Equal(t, report.Correlation.TraceID, s.TraceID)
		require.NotContains(t, ids, s.SpanID, "span IDs are unique")
		ids[s.SpanID] = s
	}
	for _, s := range spans {
		if s.Name == SpanWork {
			require.Empty(t, s.ParentSpanID)
			continue
		}
		require.Contains(t, ids, s.ParentSpanID, "%s parent must exist", s.Name)
	}
	for _, l := range bundle.Logs.ResourceLogs[0].ScopeLogs[0].LogRecords {
		require.Equal(t, report.Correlation.TraceID, l.TraceID)
		require.Contains(t, ids, l.SpanID, "logs correlate to an emitted span")
	}

	// Attempts: original and retry, the retry parented by the original and selected.
	byName := spansByName(bundle)
	require.Len(t, byName[SpanAttempt], 2)
	require.Len(t, report.Correlation.Attempts, 2)
	var selected AttemptCorrelation
	for _, a := range report.Correlation.Attempts {
		if a.Selected {
			selected = a
		}
	}
	require.Equal(t, f.retry.Execution, selected.ExecutionID)
	require.Equal(t, f.original.Execution, selected.ParentExecutionID)
	require.Equal(t, "retry", selected.Relationship)
	require.Equal(t, SpanID(SpanAttempt, f.original.Execution), ids[selected.SpanID].ParentSpanID)
	require.Equal(t, selected.SpanID, ids[report.Correlation.TaskSpanID].ParentSpanID)
	task := ids[report.Correlation.TaskSpanID]
	execution, ok := attrValue(task.Attributes, KeyExecutionID)
	require.True(t, ok)
	require.Equal(t, f.retry.Execution, *execution.StringValue)

	require.Len(t, byName[SpanArtifact], len(report.Correlation.Artifacts))
	require.NotEmpty(t, report.Correlation.Artifacts)
	require.Len(t, byName[SpanModel], 1)
	require.Len(t, byName[SpanTool], 2)

	// Aggregation links to each member's deterministic work trace.
	require.NotNil(t, report.Correlation.Aggregation)
	require.Len(t, byName[SpanAggregation], 1)
	links := byName[SpanAggregation][0].Links
	require.Len(t, links, 2)
	for _, link := range links {
		require.Equal(t, report.Correlation.TraceID, link.TraceID, "both members share the logical work trace")
		require.Equal(t, report.Correlation.RootSpanID, link.SpanID)
	}

	// Metric data points never carry identifiers.
	for _, m := range bundle.Metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		require.Contains(t, MetricNames, m.Name)
		for _, p := range m.Sum.DataPoints {
			for _, kv := range p.Attributes {
				entry, ok := catalogIndex[kv.Key]
				require.True(t, ok, kv.Key)
				require.NotEqual(t, ClassIdentifier, entry.Class, "metric %s carries identifier %s", m.Name, kv.Key)
			}
		}
	}
}

func TestDerivationIsDeterministic(t *testing.T) {
	f := publishFixture(t)
	require.Equal(t, marshalAll(t, build(t, f.Inputs, Options{})), marshalAll(t, build(t, f.Inputs, Options{})))
}

func TestPayloadsAreExcludedByDefault(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, f.Inputs, Options{})
	all := marshalAll(t, bundle)
	for _, secret := range []string{secretPrompt, secretOutput, secretArgs, secretResult} {
		require.NotContains(t, all, secret)
	}
	for _, key := range []string{KeyPromptSHA256, KeyCompletionSHA256, KeyToolArgsSHA256, KeyToolResultSHA256} {
		require.NotContains(t, all, key)
	}
	require.Equal(t, PayloadExclude, bundle.Report.PayloadPolicy)
	require.Contains(t, bundle.Report.Dropped, Drop{Signal: SignalTraces, Reason: DropPayloadExcluded, Count: 4},
		"prompt, completion, one tool's arguments and result are counted, not emitted")
}

func TestDigestPolicyEmitsOnlySurrogates(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, f.Inputs, Options{Policy: PayloadDigest})
	all := marshalAll(t, bundle)
	for _, secret := range []string{secretPrompt, secretOutput, secretArgs, secretResult} {
		require.NotContains(t, all, secret)
	}
	task := spansByName(bundle)[SpanTask][0]
	digest, ok := attrValue(task.Attributes, KeyPromptSHA256)
	require.True(t, ok)
	require.Equal(t, artifactmanifest.Hash([]byte(secretPrompt)), *digest.StringValue)
	length, ok := attrValue(task.Attributes, KeyPromptLength)
	require.True(t, ok)
	require.Equal(t, "18", *length.IntValue)
	tool := spansByName(bundle)[SpanTool][0]
	_, ok = attrValue(tool.Attributes, KeyToolArgsSHA256)
	require.True(t, ok)
	_, ok = attrValue(spansByName(bundle)[SpanTool][1].Attributes, KeyToolArgsSHA256)
	require.False(t, ok, "absent arguments produce no surrogate")
}

func TestSanitizerEnforcesCatalog(t *testing.T) {
	s := newSanitizer(PayloadExclude, Limits{MaxAttributes: 3, MaxStringLength: 4})
	clean, dropped := s.clean(SignalTraces, OnMetric, []KeyValue{
		str("unregistered.key", "x"),
		str(KeyRunID, "run-1"),             // identifier is not allowed on metrics
		str(KeyGenAIPrompt, secretPrompt),  // payload class is never emitted
		str(KeyOutcome, "success"),         // truncated to 4 bytes
		str(KeyAttemptStatus, "completed"), // truncated
		str(KeyGenAITokenType, "out"),      // fits
		str(KeyGenAIModel, "gpt"),          // over the attribute limit
		str(KeyMemberState, "admitted"),    // over the attribute limit
	})
	require.Len(t, clean, 3)
	require.Equal(t, 5, dropped)
	require.Equal(t, "succ", *clean[0].Value.StringValue)
	require.Equal(t, map[string]int{
		DropUnregisteredAttribute: 1, DropPlacement: 1, DropPayloadExcluded: 1,
		DropAttributeLimit: 2, DropAttributeTruncated: 2,
	}, s.dropped[SignalTraces])
	require.Equal(t, "na", truncateUTF8("naïve", 3), "truncation never splits a rune")
}

func TestSafeguardsBoundCardinality(t *testing.T) {
	f := publishFixture(t)
	var report map[string]any
	require.NoError(t, json.Unmarshal(f.Report, &report))
	runs := asSlice(t, asMap(t, asSlice(t, report["tasks"])[0])["runs"])
	digest := asMap(t, asMap(t, runs[1])["session_digest"])
	var calls []any
	for i := range 40 {
		calls = append(calls, map[string]any{"name": "tool-" + strconv.Itoa(i), "success": true})
	}
	digest["tool_calls"] = calls
	f.Report = mustMarshal(t, report)

	limits := DefaultLimits()
	limits.MaxToolSpansPerTask = 5
	limits.MaxSeriesPerMetric = 8
	bundle := build(t, f.Inputs, Options{Limits: limits})
	require.Len(t, spansByName(bundle)[SpanTool], 5)
	require.Contains(t, bundle.Report.Dropped, Drop{Signal: SignalTraces, Reason: DropToolSpanLimit, Count: 35})
	for _, m := range bundle.Metrics.ResourceMetrics[0].ScopeMetrics[0].Metrics {
		if m.Name != MetricToolCalls {
			continue
		}
		require.LessOrEqual(t, len(m.Sum.DataPoints), 8)
		var total int
		overflow := false
		for _, p := range m.Sum.DataPoints {
			total += mustInt(t, p.AsInt)
			if _, ok := attrValue(p.Attributes, KeyMetricOverflow); ok {
				overflow = true
			}
		}
		require.True(t, overflow, "excess series fold into the overflow series")
		require.Equal(t, 40, total, "no measurement is lost")
	}
	require.Contains(t, bundle.Report.Dropped, Drop{Signal: SignalMetrics, Reason: DropMetricCardinality, Count: 33})
	require.Equal(t, 8, bundle.Report.Signals.Metrics.Names[MetricToolCalls])

	limits = DefaultLimits()
	limits.MaxSpans = 3
	limits.MaxLogs = 2
	bundle = build(t, f.Inputs, Options{Limits: limits})
	require.Equal(t, 3, bundle.Report.Signals.Traces.Emitted)
	require.Equal(t, 2, bundle.Report.Signals.Logs.Emitted)
	require.Positive(t, bundle.Report.Signals.Traces.Dropped)
	_, err := EncodeReport(bundle.Report)
	require.NoError(t, err)
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	require.True(t, ok, "expected object, got %T", v)
	return m
}

func asSlice(t *testing.T, v any) []any {
	t.Helper()
	s, ok := v.([]any)
	require.True(t, ok, "expected array, got %T", v)
	return s
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func mustInt(t *testing.T, value string) int {
	t.Helper()
	var n int
	require.NoError(t, json.Unmarshal([]byte(value), &n))
	return n
}

func TestLoadRejectsUncorrelatedSources(t *testing.T) {
	f := publishFixture(t)
	other := map[string]any{}
	require.NoError(t, json.Unmarshal(f.Lineage, &other))
	asMap(t, other["logical"])["work"] = "trial-" + strings.Repeat("0", 64)
	f.Lineage = mustMarshal(t, other)
	_, err := Load(f.Inputs)
	require.ErrorIs(t, err, ErrUncorrelated)

	f = publishFixture(t)
	f.Terminal = []byte(`{"kind":"waza.runner-terminal"}`)
	_, err = Load(f.Inputs)
	require.Error(t, err)

	_, err = Load(Inputs{Terminal: publishFixture(t).Terminal, SchedulerWorkID: "bad id with spaces"})
	require.ErrorIs(t, err, ErrInvalid)
}

func TestTerminalOnlyAndUnmatchedReport(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, Inputs{Terminal: f.Terminal, Report: []byte(`{"tasks":[{"test_id":"other","runs":[{"run_number":1,"prompt":"x"}]}]}`)}, Options{})
	require.Len(t, bundle.Report.Correlation.Attempts, 1)
	require.Equal(t, AttemptStatusObserved, bundle.Report.Correlation.Attempts[0].Status)
	states := map[string]string{}
	for _, s := range bundle.Report.Sources {
		states[s.Source] = s.State
	}
	require.Equal(t, SourceUnmatched, states["report"], "an unmatched report never falls back to another run")
	require.Equal(t, SourceUnavailable, states["lineage"])
	require.Empty(t, spansByName(bundle)[SpanModel])
	_, err := EncodeReport(bundle.Report)
	require.NoError(t, err)
}

func TestIdentifiersAreValidOTLP(t *testing.T) {
	trace := TraceID("run-a", "trial-b")
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{32}$`), trace)
	require.NotEqual(t, strings.Repeat("0", 32), trace)
	require.Regexp(t, regexp.MustCompile(`^[0-9a-f]{16}$`), SpanID(SpanTool, "x", "1"))
	require.NotEqual(t, SpanID(SpanTool, "x", "1"), SpanID(SpanTool, "x1", ""), "key boundaries are unambiguous")
	require.NotEqual(t, TraceID("run-a", "trial-b"), TraceID("run-a", "trial-c"))
}

type receiver struct {
	mu       sync.Mutex
	paths    []string
	types    []string
	bodies   []string
	auth     []string
	status   int
	response string
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.paths = append(r.paths, req.URL.Path)
	r.types = append(r.types, req.Header.Get("Content-Type"))
	r.bodies = append(r.bodies, string(body))
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.mu.Unlock()
	if r.status != 0 {
		w.WriteHeader(r.status)
	}
	_, _ = io.WriteString(w, r.response)
}

func TestDeliverFilesAndOTLP(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, f.Inputs, Options{})
	recv := &receiver{response: `{}`}
	server := httptest.NewServer(recv)
	defer server.Close()
	dir := filepath.Join(t.TempDir(), "out")
	err := Deliver(context.Background(), bundle, ExportOptions{
		Dir: dir, OTLPEndpoint: server.URL + "/otlp/", Headers: map[string]string{"Authorization": "Bearer TOKEN-SECRET"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"/otlp/v1/traces", "/otlp/v1/metrics", "/otlp/v1/logs"}, recv.paths)
	require.Equal(t, []string{"application/json", "application/json", "application/json"}, recv.types)
	require.Equal(t, "Bearer TOKEN-SECRET", recv.auth[0])
	for _, body := range recv.bodies {
		require.NotContains(t, body, secretPrompt)
		require.True(t, json.Valid([]byte(body)))
	}
	require.Len(t, bundle.Report.Exports, 6)
	for _, e := range bundle.Report.Exports {
		require.Equal(t, ExportExported, e.State, "%+v", e)
	}

	written, err := os.ReadFile(filepath.Join(dir, ReportFile))
	require.NoError(t, err)
	require.NotContains(t, string(written), "TOKEN-SECRET")
	parsed, err := ParseReport(written)
	require.NoError(t, err)
	require.Equal(t, bundle.Report.Correlation, parsed.Correlation)
	for _, name := range []string{TracesFile, MetricsFile, LogsFile} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		require.True(t, json.Valid(data))
	}

	// A second export never overwrites existing files.
	again := build(t, f.Inputs, Options{})
	err = Deliver(context.Background(), again, ExportOptions{Dir: dir})
	require.ErrorIs(t, err, ErrExport)
	require.Equal(t, ErrorOutputExists, again.Report.Exports[0].ErrorCode)
}

func TestDeliverFailureNeverEchoesReceiverContent(t *testing.T) {
	f := publishFixture(t)
	bundle := build(t, f.Inputs, Options{})
	recv := &receiver{status: http.StatusUnauthorized, response: "RECEIVER-SECRET-BODY"}
	server := httptest.NewServer(recv)
	defer server.Close()
	err := Deliver(context.Background(), bundle, ExportOptions{OTLPEndpoint: server.URL, Headers: map[string]string{"X-Key": "HEADER-SECRET"}})
	require.ErrorIs(t, err, ErrExport)
	require.NotContains(t, err.Error(), "RECEIVER-SECRET-BODY")
	encoded, encodeErr := EncodeReport(bundle.Report)
	require.NoError(t, encodeErr)
	require.NotContains(t, string(encoded), "RECEIVER-SECRET-BODY")
	require.NotContains(t, string(encoded), "HEADER-SECRET")
	for _, e := range bundle.Report.Exports {
		require.Equal(t, ExportFailed, e.State)
		require.Equal(t, ErrorHTTPStatus, e.ErrorCode)
		require.Equal(t, http.StatusUnauthorized, e.HTTPStatus)
	}

	partial := build(t, f.Inputs, Options{})
	recv = &receiver{response: `{"partialSuccess":{"rejectedSpans":"2","errorMessage":"RECEIVER-SECRET-BODY"}}`}
	server2 := httptest.NewServer(recv)
	defer server2.Close()
	require.NoError(t, Deliver(context.Background(), partial, ExportOptions{OTLPEndpoint: server2.URL}))
	for _, e := range partial.Report.Exports {
		require.Equal(t, ExportPartial, e.State)
		require.Equal(t, ErrorPartialSuccess, e.ErrorCode)
	}

	unreachable := build(t, f.Inputs, Options{})
	err = Deliver(context.Background(), unreachable, ExportOptions{OTLPEndpoint: "http://127.0.0.1:1", Timeout: time.Second})
	require.ErrorIs(t, err, ErrExport)
	require.Contains(t, []string{ErrorTransport, ErrorTimeout}, unreachable.Report.Exports[0].ErrorCode)

	for _, bad := range []string{"ftp://x", "http://user:pass@host", "http://host/?token=1", "relative/path"} {
		_, err := ValidateEndpoint(bad)
		require.Error(t, err, bad)
	}
}

// TestDocumentationListsStableContract keeps docs/EXECUTION_TELEMETRY.md in
// lockstep with the stable catalog: every key and signal name is documented.
func TestDocumentationListsStableContract(t *testing.T) {
	doc, err := os.ReadFile("../../docs/EXECUTION_TELEMETRY.md")
	require.NoError(t, err)
	text := string(doc)
	var missing []string
	for _, entry := range Catalog() {
		if !strings.Contains(text, "`"+entry.Key+"`") {
			missing = append(missing, entry.Key)
		}
	}
	for _, names := range [][]string{SpanNames, EventNames, MetricNames, DropReasons} {
		for _, name := range names {
			if !strings.Contains(text, "`"+name+"`") {
				missing = append(missing, name)
			}
		}
	}
	slices.Sort(missing)
	require.Empty(t, missing, "document every stable key and name")
}

const webFixture = "../../web/e2e/fixtures/execution-telemetry.json"

// TestWebFixtureIsConformant keeps the Lab e2e fixture a real derivation.
// Regenerate with WAZA_UPDATE_TELEMETRY_FIXTURE=1.
func TestWebFixtureIsConformant(t *testing.T) {
	if os.Getenv("WAZA_UPDATE_TELEMETRY_FIXTURE") == "1" {
		fx := publishFixture(t)
		bundle := build(t, fx.Inputs, Options{})
		receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/v1/logs") {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer receiver.Close()
		err := Deliver(t.Context(), bundle, ExportOptions{Dir: filepath.Join(t.TempDir(), "otel"), OTLPEndpoint: receiver.URL})
		require.ErrorIs(t, err, ErrExport)
		for i := range bundle.Report.Exports {
			if bundle.Report.Exports[i].Destination == DestinationOTLP {
				bundle.Report.Exports[i].Target = "http://collector.example:4318/v1/" + bundle.Report.Exports[i].Signal
			}
		}
		data, err := EncodeReport(bundle.Report)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(webFixture, data, 0o644))
	}
	data, err := os.ReadFile(webFixture)
	require.NoError(t, err)
	report, err := ParseReport(data)
	require.NoError(t, err)
	require.NotEmpty(t, report.Correlation.Attempts)
	require.NotNil(t, report.Correlation.Aggregation)
	for _, secret := range []string{secretPrompt, secretOutput, secretArgs, secretResult} {
		require.NotContains(t, string(data), secret)
	}
}

func TestLoadRejectsUnrelatedAttemptBindingAndAggregate(t *testing.T) {
	f := publishFixture(t)
	attempt := map[string]any{}
	require.NoError(t, json.Unmarshal(f.Attempt, &attempt))
	asMap(t, attempt["binding"])["grader_ids"] = []string{"unrelated-grader"}
	f.Attempt = mustMarshal(t, attempt)
	_, err := Load(f.Inputs)
	require.ErrorIs(t, err, ErrUncorrelated, "an attempt with a different binding is not merged under a manifest")

	f = publishFixture(t)
	f.Aggregate = publishFixture(t).Aggregate
	_, err = Load(f.Inputs)
	require.ErrorIs(t, err, ErrUncorrelated, "an aggregate without this manifest is not attached to the work trace")
}

func TestToolEventsArePreferredOverLegacyToolCalls(t *testing.T) {
	f := publishFixture(t)
	report := map[string]any{}
	require.NoError(t, json.Unmarshal(f.Report, &report))
	run := asMap(t, asSlice(t, asMap(t, asSlice(t, report["tasks"])[0])["runs"])[1])
	delete(asMap(t, run["session_digest"]), "tool_calls")
	run["tool_events"] = []any{
		map[string]any{"turn": 1, "sequence": 1, "tool_call_id": "call-9", "tool_name": "grep",
			"args": map[string]any{"pattern": secretArgs}, "result": secretResult, "success": true},
	}
	f.Report = mustMarshal(t, report)
	bundle := build(t, f.Inputs, Options{})
	tools := spansByName(bundle)[SpanTool]
	require.Len(t, tools, 1)
	name, ok := attrValue(tools[0].Attributes, KeyToolName)
	require.True(t, ok)
	require.Equal(t, "grep", *name.StringValue)
	all := marshalAll(t, bundle)
	require.NotContains(t, all, secretArgs)
	require.NotContains(t, all, secretResult)
}
