import { useEffect, useRef, useState } from "react";
import { inspectLifecycle, type LifecycleSnapshot } from "../api/lifecycle";
import { downloadText } from "../lib/export";

export default function ExecutionLifecycle({ demo }: { demo: boolean }) {
  const [context, setContext] = useState("");
  const [result, setResult] = useState<LifecycleSnapshot | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  async function inspect() {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setResult(null);
    setError("");
    setPending(true);
    try {
      const value = await inspectLifecycle(context, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof Error ? error.message : "Lifecycle inspection is unavailable.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Execution lifecycle declarations">
      <div className="lab-panel-header">
        <div>
          <h2>Inspect execution lifecycle declarations</h2>
          <p>Caller-declared, unverified projection only. Verified worker state, progress and ownership are unavailable.</p>
          <p>This panel does not retry, cancel, reclaim or restore work. No live state or latest pointer is inferred.</p>
        </div>
      </div>
      {demo ? <p>Lifecycle inspection is unavailable in the synthetic demo.</p> : (
        <>
          <label htmlFor="lifecycle-context">Retained lifecycle inspection context (JSON)</label>
          <textarea id="lifecycle-context" rows={10} spellCheck={false} value={context}
            onChange={(event) => {
              active.current?.abort();
              setContext(event.target.value);
              setResult(null);
              setError("");
              setPending(false);
            }} />
          <button className="lab-button" disabled={pending || !context.trim()} onClick={() => void inspect()}>
            {pending ? "Inspecting declarations..." : "Inspect lifecycle declarations"}
          </button>
          {error && <p role="alert">{error} No previous projection is shown.</p>}
          {result && (
            <div role="status" aria-label="Declared lifecycle projection">
              <h3>Declared state: {result.state}</h3>
              <dl>
                <dt>Evidence</dt><dd>{result.evidence}</dd>
                <dt>Verified worker progress</dt><dd>Unavailable</dd>
                <dt>Declared execution ID</dt><dd><code>{result.execution_id}</code></dd>
                <dt>Original reservation digest</dt><dd><code>{result.reservation_digest}</code></dd>
                <dt>Declared completed units</dt><dd>{result.completed_units ?? "Unknown"}</dd>
                <dt>Declared terminal outcome</dt><dd>{result.terminal?.outcome ?? "Unknown"}</dd>
                <dt>Declared terminal exit</dt><dd>{result.terminal?.exit_code ?? "Unknown"}</dd>
                <dt>Declared execution verdict</dt><dd>{result.terminal?.execution_verdict ?? "Unknown"}</dd>
                <dt>Declared failure boundary</dt><dd>{result.terminal?.failure_boundary ?? "Unknown"}</dd>
                <dt>Original terminal digest</dt><dd><code>{result.terminal?.digest ?? "Unknown"}</code></dd>
                <dt>Cancellation observation phase</dt><dd>{result.cancellation}</dd>
                <dt>Configured deadline</dt><dd>{result.deadline ?? "Unknown"}</dd>
                <dt>Declared deadline expiry</dt><dd>{result.deadline_expired ? "Observed in declaration" : "Not recorded"}</dd>
                <dt>Declared heartbeat</dt><dd>{result.heartbeat ?? "Unknown"}</dd>
                <dt>Inspection as-of</dt><dd>{result.as_of ?? "Unknown"}</dd>
                <dt>Declared heartbeat age (ms)</dt><dd>{result.heartbeat_age_ms ?? "Unknown"}</dd>
                <dt>Suspected orphan (declaration only)</dt><dd>{result.suspected_orphan === undefined ? "Unknown" : result.suspected_orphan ? "Suspected; investigate, never reclaim" : "Not suspected; not proof of liveness"}</dd>
                <dt>Retry advice (not authorization)</dt><dd>{result.retry.advice}: {result.retry.reason}</dd>
              </dl>
              <h3>Advisory next steps only</h3>
              <ul>{result.actions.map((action) => <li key={action}>{action}</li>)}</ul>
              {result.diagnostics.map((diagnostic) => <p key={diagnostic}>{diagnostic}</p>)}
              <table>
                <caption>Declared observation sequence</caption>
                <thead><tr><th scope="col">Sequence</th><th scope="col">Timestamp</th><th scope="col">Event</th><th scope="col">Concrete handling</th></tr></thead>
                <tbody>{result.observations.map((event) => (
                  <tr key={event.sequence}><th scope="row">{event.sequence}</th><td>{event.observed_at}</td><td>{event.event}</td><td>{event.handling ?? event.stop ?? "Not recorded"}</td></tr>
                ))}</tbody>
              </table>
              <button className="lab-button" onClick={() => downloadText(JSON.stringify(result, null, 2), "lifecycle-declarations.json", "application/json")}>Download declared projection JSON</button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
