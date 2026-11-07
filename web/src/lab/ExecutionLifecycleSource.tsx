import { useEffect, useRef, useState } from "react";
import { inspectLifecycleSource, type LifecycleSource } from "../api/lifecycleSource";
import { downloadText } from "../lib/export";

export default function ExecutionLifecycleSource({ demo }: { demo: boolean }) {
  const [query, setQuery] = useState("");
  const [receipt, setReceipt] = useState<{ value: LifecycleSource; json: string } | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  async function inspect() {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setReceipt(null);
    setError("");
    setPending(true);
    try {
      const result = await inspectLifecycleSource(query, controller.signal);
      if (!controller.signal.aborted) setReceipt(result);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : "Lifecycle source is unavailable.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  const source = receipt?.value;
  const snapshot = source?.snapshot;
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Original lifecycle source inspection">
      <div className="lab-panel-header">
        <div>
          <h2>Inspect original finalized lifecycle source</h2>
          <p>Verified nonsnapshot content and original source declarations, not authenticated live worker state.</p>
          <p>Missing completion is unavailable, not a dead worker or permission to reclaim. No retry, cancellation or restore is performed.</p>
        </div>
      </div>
      {demo ? <p>Original lifecycle source inspection is unavailable in the synthetic demo.</p> : (
        <>
          <label htmlFor="lifecycle-source-query">Original lifecycle source selection (JSON)</label>
          <textarea id="lifecycle-source-query" rows={8} spellCheck={false} value={query}
            onChange={(event) => {
              active.current?.abort();
              setQuery(event.target.value);
              setReceipt(null);
              setError("");
              setPending(false);
            }} />
          <button className="lab-button" disabled={pending || !query.trim()} onClick={() => void inspect()}>
            {pending ? "Inspecting original source..." : "Inspect original lifecycle source"}
          </button>
          {error && <p role="alert">{error} No previous or partial projection is shown.</p>}
          {source && snapshot && receipt && (
            <div role="status" aria-label="Original lifecycle source metadata">
              <h3>Original terminal state: {snapshot.state}</h3>
              <dl>
                <dt>Content verification</dt><dd>{source.verification}</dd>
                <dt>Context qualification</dt><dd>{source.context}</dd>
                <dt>Projection evidence</dt><dd>{snapshot.evidence}</dd>
                <dt>Original execution ID</dt><dd><code>{source.source.execution_id}</code></dd>
                <dt>Original manifest digest</dt><dd><code>{source.source.manifest_digest}</code></dd>
                <dt>Original reservation digest</dt><dd><code>{snapshot.reservation_digest}</code></dd>
                <dt>Original completed units</dt><dd>{snapshot.completed_units ?? "Unknown"}</dd>
                <dt>Original terminal outcome</dt><dd>{snapshot.terminal?.outcome ?? "Unknown"}</dd>
                <dt>Original terminal exit</dt><dd>{snapshot.terminal?.exit_code ?? "Unknown"}</dd>
                <dt>Original execution verdict</dt><dd>{snapshot.terminal?.execution_verdict ?? "Unknown"}</dd>
                <dt>Original failure boundary</dt><dd>{snapshot.terminal?.failure_boundary ?? "Unknown"}</dd>
                <dt>Original terminal digest</dt><dd><code>{snapshot.terminal?.digest ?? "Unknown"}</code></dd>
                <dt>Cancellation observation phase</dt><dd>{snapshot.cancellation}</dd>
                <dt>Retained invocation deadline</dt><dd>{snapshot.deadline ?? "Unknown"}</dd>
                <dt>Declared expiry observation</dt><dd>{snapshot.deadline_expired ? "Recorded" : "Not recorded"}</dd>
                <dt>Recorded heartbeat</dt><dd>{snapshot.heartbeat ?? "Unknown"}</dd>
                <dt>Inspection as-of</dt><dd>{snapshot.as_of ?? "Unknown"}</dd>
                <dt>Recorded heartbeat age (ms)</dt><dd>{snapshot.heartbeat_age_ms ?? "Unknown"}</dd>
                <dt>Declared orphan suspicion</dt><dd>{snapshot.suspected_orphan === undefined ? "Unknown" : snapshot.suspected_orphan ? "Suspected; never reclaim" : "Not suspected; not liveness proof"}</dd>
                <dt>Worker liveness / reclaim authority</dt><dd>Unavailable / none</dd>
                <dt>Native observation sink</dt><dd>{source.runtime_observations.owned_engine_bound ? "Bound to original native engine" : "Not bound; no native observations claimed"}</dd>
                <dt>Native process exit</dt><dd>{source.runtime_observations.process_exit}</dd>
                <dt>Native dropped observations / pending at seal</dt><dd>{source.runtime_observations.dropped} / {source.runtime_observations.pending_at_seal}</dd>
                <dt>Native late-observation policy</dt><dd>{source.runtime_observations.late_observation_policy}</dd>
                <dt>Retry advice, not permission</dt><dd>{snapshot.retry.advice}: {snapshot.retry.reason}</dd>
              </dl>
              <h3>Advisory operator actions only</h3>
              <ul>{snapshot.actions.map((action) => <li key={action}>{action}</li>)}</ul>
              {snapshot.diagnostics.map((diagnostic) => <p key={diagnostic}>{diagnostic}</p>)}
              <table>
                <caption>Original lifecycle declaration sequence</caption>
                <thead><tr><th scope="col">Sequence</th><th scope="col">Timestamp</th><th scope="col">Event</th><th scope="col">Handling</th></tr></thead>
                <tbody>{snapshot.observations.map((event) => (
                  <tr key={event.sequence}><th scope="row">{event.sequence}</th><td>{event.observed_at}</td><td>{event.event}</td><td>{event.handling ?? event.stop ?? "Not recorded"}</td></tr>
                ))}</tbody>
              </table>
              <table>
                <caption>Scoped original native runtime observations, not process-exit proof</caption>
                <thead><tr><th scope="col">Sequence</th><th scope="col">Timestamp</th><th scope="col">Operation</th><th scope="col">Scope</th><th scope="col">Result</th></tr></thead>
                <tbody>{source.runtime_observations.observations.map((event) => (
                  <tr key={event.sequence}><th scope="row">{event.sequence}</th><td>{event.observed_at}</td><td>{event.operation}</td><td>{event.scope}</td><td>{event.result}</td></tr>
                ))}</tbody>
              </table>
              <button className="lab-button" onClick={() => downloadText(receipt.json, "lifecycle-original-source.json", "application/json")}>Download exact lifecycle source JSON</button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
