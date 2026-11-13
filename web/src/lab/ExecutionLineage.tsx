import { useEffect, useRef, useState } from "react";
import { LineageError, loadLineage, parseLogical, type LineageResult } from "../api/lineage";
import { downloadText } from "../lib/export";

const statusLabels: Record<string, string> = {
  reserved: "Reserved (not dispatched)",
  dispatched: "Dispatched; no completion recorded",
  completed: "Completed (verified terminal)",
  completion_unverified: "Completion recorded but unverified",
};

function short(id: string): string {
  return id.length > 20 ? `${id.slice(0, 17)}...` : id;
}

function LineageReader() {
  const [run, setRun] = useState("");
  const [work, setWork] = useState("");
  const [result, setResult] = useState<LineageResult | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  function clear() {
    active.current?.abort();
    setResult(null);
    setError("");
    setPending(false);
  }

  async function load() {
    clear();
    let logical;
    try {
      logical = parseLogical(run, work);
    } catch (error) {
      setError(error instanceof LineageError ? error.message : "Invalid logical work IDs.");
      return;
    }
    const controller = new AbortController();
    active.current = controller;
    setPending(true);
    try {
      const value = await loadLineage(logical, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof LineageError ? error.message : "Lineage could not be loaded.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  const lineage = result?.lineage;
  return <>
    <label htmlFor="lineage-run">Logical run ID</label>
    <input id="lineage-run" spellCheck={false} autoComplete="off" value={run} placeholder="run-<sha256>"
      onChange={(event) => { clear(); setRun(event.target.value); }} />
    <label htmlFor="lineage-work">Logical work ID</label>
    <input id="lineage-work" spellCheck={false} autoComplete="off" value={work} placeholder="trial-<sha256>"
      onChange={(event) => { clear(); setWork(event.target.value); }} />
    <div className="lab-actions">
      <button className="lab-button" disabled={pending} onClick={() => void load()}>
        {pending ? "Reading lineage..." : "Load attempt lineage"}
      </button>
    </div>
    {error && <p role="alert">{error}</p>}
    {lineage && <div role="status" aria-label="Loaded attempt lineage">
      <h3>{lineage.attempts.length} attempt{lineage.attempts.length === 1 ? "" : "s"}, {lineage.decisions.length} decision{lineage.decisions.length === 1 ? "" : "s"}</h3>
      <p>Order follows recorded retry/branch parents, not wall-clock time. Each attempt keeps its own immutable evidence; reuse never reruns or rewrites the source.</p>
      {lineage.attempts.length > 0 && <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Ordered attempts">
        <table className="lab-table"><caption>Ordered execution attempts</caption>
          <thead><tr>
            <th scope="col">Order</th><th scope="col">Execution</th><th scope="col">Native attempt</th>
            <th scope="col">Parent</th><th scope="col">Status</th><th scope="col">Terminal</th><th scope="col">Reused by</th>
          </tr></thead>
          <tbody>{lineage.attempts.map((attempt) => <tr key={attempt.execution_id}>
            <th scope="row">{attempt.order}</th>
            <td style={{ paddingInlineStart: `${0.5 + attempt.depth * 1.25}rem` }}>
              <code title={attempt.execution_id}>{short(attempt.execution_id)}</code>
            </td>
            <td>{attempt.native_attempt}</td>
            <td>{attempt.parent ? <>{attempt.parent.relationship} of <code title={attempt.parent.execution_id}>{short(attempt.parent.execution_id)}</code></> : "None"}</td>
            <td>{statusLabels[attempt.status] ?? attempt.status}</td>
            <td>{attempt.terminal ? `${attempt.terminal.outcome} (exit ${attempt.terminal.exit_code})` : "Unavailable"}</td>
            <td>{attempt.reused_by.length > 0 ? attempt.reused_by.map((id) => <code key={id} title={id}>{short(id)} </code>) : "None"}</td>
          </tr>)}</tbody>
        </table>
      </div>}
      {lineage.decisions.length > 0 && <>
        <h3>Duplicate and reuse decisions</h3>
        <ul aria-label="Lineage decisions">{lineage.decisions.map((decision) => <li key={decision.decision_id}>
          <code title={decision.decision_id}>{short(decision.decision_id)}</code>: {decision.policy} / {decision.outcome}, native attempt {decision.native_attempt}
          {decision.execution_id && <> for <code title={decision.execution_id}>{short(decision.execution_id)}</code></>}
          {decision.source && <> from source <code title={decision.source.execution_id}>{short(decision.source.execution_id)}</code> {decision.outcome === "reused" ? " (zero new engine calls)" : " (not reused; source not linked)"}</>}
        </li>)}</ul>
      </>}
      <h3>Writer and record diagnostics</h3>
      {lineage.diagnostics.length === 0
        ? <p>No concurrent-writer, missing-parent or unfinished-dispatch diagnostics.</p>
        : <ul aria-label="Lineage diagnostics">{lineage.diagnostics.map((diagnostic) => <li key={`${diagnostic.code}:${diagnostic.execution_ids.join(",")}`}>
          <strong>{diagnostic.code}</strong>: {diagnostic.message} {diagnostic.execution_ids.map((id) => <code key={id} title={id}>{short(id)} </code>)}
        </li>)}</ul>}
      <button className="lab-button" onClick={() => downloadText(result.json, "waza-execution-lineage.json", "application/json")}>
        Download lineage JSON
      </button>
    </div>}
  </>;
}

export default function ExecutionLineage({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Execution attempt lineage">
    <div className="lab-panel-header"><div>
      <h2>Logical work and execution attempts</h2>
      <p>Inspect every recorded attempt for one logical work item: retry parents, reuse decisions and concurrent-writer diagnostics.
        Read-only; nothing is executed or repaired. IDs come from <code>reservation.logical</code> in a submission result.</p>
    </div></div>
    {demo ? <p>Lineage is unavailable in the synthetic demo. Use a running Waza server with an existing local immutable store.</p> : <LineageReader />}
  </section>;
}
