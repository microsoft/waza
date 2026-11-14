import { useState } from "react";
import { exportHealth, parseExecutionTelemetry, type ExecutionTelemetry as Report, type TelemetrySignal } from "../api/telemetry";

const signals: TelemetrySignal[] = ["traces", "metrics", "logs"];

function short(value: string) {
  return value.length > 24 ? `${value.slice(0, 24)}…` : value;
}

function ManifestLink({ digest }: { digest: string }) {
  return <a href={`/api/v1/manifests/${digest}`} target="_blank" rel="noreferrer"><code title={digest}>{short(digest)}</code></a>;
}

export default function ExecutionTelemetry() {
  const [wire, setWire] = useState("");
  const [report, setReport] = useState<Report | null>(null);
  const [error, setError] = useState("");

  function parse() {
    setReport(null);
    setError("");
    try {
      setReport(parseExecutionTelemetry(wire));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Telemetry report could not be parsed.");
    }
  }

  const health = report ? exportHealth(report) : undefined;
  return (
    <section className="lab-panel lab-execution-telemetry" aria-label="Execution telemetry">
      <div className="lab-panel-header">
        <div>
          <h2>Execution telemetry correlation</h2>
          <p>
            Read a <code>waza runner telemetry</code> report to see the trace and span identifiers that correlate work,
            attempts, artifacts and aggregation, plus safeguard drops and export health. Reports never contain payloads.
          </p>
        </div>
      </div>
      <label htmlFor="execution-telemetry-json">Telemetry report JSON</label>
      <textarea id="execution-telemetry-json" rows={8} spellCheck={false} value={wire}
        onChange={(event) => {
          setWire(event.target.value);
          setReport(null);
          setError("");
        }}
      />
      <button className="lab-button" onClick={parse}>Read telemetry report</button>
      {error && <p role="alert">{error}</p>}
      {report && (
        <div role="status" aria-label="Execution telemetry report">
          <h3>Correlation</h3>
          <dl aria-label="Correlation identifiers">
            <dt>Trace ID</dt><dd><code>{report.correlation.trace_id}</code></dd>
            <dt>Work span ID</dt><dd><code>{report.correlation.root_span_id}</code></dd>
            <dt>Task attempt span ID</dt><dd><code>{report.correlation.task_span_id}</code></dd>
            <dt>Run</dt><dd><code title={report.correlation.run}>{short(report.correlation.run)}</code></dd>
            <dt>Work</dt><dd><code title={report.correlation.work}>{short(report.correlation.work)}</code></dd>
            {report.correlation.scheduler_work_id && <><dt>Scheduler work ID</dt><dd><code>{report.correlation.scheduler_work_id}</code></dd></>}
            <dt>Payload policy</dt><dd>{report.payload_policy === "exclude" ? "exclude (no payload-derived attributes)" : "digest (SHA-256 and length surrogates only)"}</dd>
          </dl>

          <h3>Sources</h3>
          <ul aria-label="Telemetry sources">
            {report.sources.map((source) => <li key={source.source}>{source.source}: {source.state}</li>)}
          </ul>

          <h3>Attempts</h3>
          <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Attempt spans table">
          <table className="lab-table" aria-label="Attempt spans">
            <thead><tr><th>Span</th><th>Execution</th><th>Parent</th><th>Status</th><th>Manifest</th><th>Selected</th></tr></thead>
            <tbody>
              {report.correlation.attempts.map((attempt) => (
                <tr key={attempt.span_id}>
                  <td><code>{attempt.span_id}</code></td>
                  <td>{attempt.execution_id ? <code title={attempt.execution_id}>{short(attempt.execution_id)}</code> : "—"}</td>
                  <td>{attempt.parent_execution_id ? <>{attempt.relationship}: <code title={attempt.parent_execution_id}>{short(attempt.parent_execution_id)}</code></> : "—"}</td>
                  <td>{attempt.status}{attempt.outcome ? ` (${attempt.outcome})` : ""}</td>
                  <td>{attempt.manifest_sha256 ? <ManifestLink digest={attempt.manifest_sha256} /> : "—"}</td>
                  <td>{attempt.selected ? "yes" : "no"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>

          <h3>Artifacts</h3>
          <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Artifact spans table">
          <table className="lab-table" aria-label="Artifact spans">
            <thead><tr><th>Span</th><th>Name</th><th>SHA-256</th><th>Bytes</th></tr></thead>
            <tbody>
              {report.correlation.artifacts.map((artifact) => (
                <tr key={artifact.span_id}>
                  <td><code>{artifact.span_id}</code></td>
                  <td>{artifact.name}{artifact.terminal ? " (terminal)" : ""}</td>
                  <td><code title={artifact.sha256}>{short(artifact.sha256)}</code></td>
                  <td>{artifact.size_bytes}</td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>

          {report.correlation.aggregation && (
            <>
              <h3>Aggregation</h3>
              <p>Aggregation span <code>{report.correlation.aggregation.span_id}</code> ({report.correlation.aggregation.state})</p>
              <ul aria-label="Aggregation members">
                {report.correlation.aggregation.members.map((member) => (
                  <li key={member.manifest_sha256}>
                    <ManifestLink digest={member.manifest_sha256} /> {member.state}
                    {member.trace_id && <> — trace <code>{member.trace_id}</code> span <code>{member.span_id}</code></>}
                  </li>
                ))}
              </ul>
            </>
          )}

          <h3>Signals</h3>
          <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Signal counts table">
          <table className="lab-table" aria-label="Signal counts">
            <thead><tr><th>Signal</th><th>Emitted</th><th>Dropped</th></tr></thead>
            <tbody>
              {signals.map((signal) => (
                <tr key={signal}><td>{signal}</td><td>{report.signals[signal].emitted}</td><td>{report.signals[signal].dropped}</td></tr>
              ))}
            </tbody>
          </table>
          </div>
          {report.dropped.length > 0 && (
            <ul aria-label="Safeguard drops">
              {report.dropped.map((drop) => <li key={`${drop.signal}-${drop.reason}`}>{drop.signal}: {drop.reason} × {drop.count}</li>)}
            </ul>
          )}

          <h3>Export diagnostics</h3>
          <p>Export health: {health === "healthy" ? "all exports succeeded" : health === "degraded" ? "one or more exports did not succeed" : "not exported"}</p>
          {report.exports.length > 0 && (
            <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Export diagnostics table">
            <table className="lab-table" aria-label="Export diagnostics">
              <thead><tr><th>Signal</th><th>Destination</th><th>Target</th><th>State</th><th>Detail</th></tr></thead>
              <tbody>
                {report.exports.map((entry) => (
                  <tr key={`${entry.signal}-${entry.destination}`}>
                    <td>{entry.signal}</td>
                    <td>{entry.destination}</td>
                    <td><code>{entry.target}</code></td>
                    <td>{entry.state}</td>
                    <td>{[entry.http_status ? `HTTP ${entry.http_status}` : "", entry.error_code ?? ""].filter(Boolean).join(" · ") || "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )}
        </div>
      )}
    </section>
  );
}
