import { useEffect, useRef, useState } from "react";
import { AggregateError, buildAggregate, parseAggregateReferences, type AggregateResult } from "../api/aggregates";
import { downloadText } from "../lib/export";

function AggregateBuilder() {
  const [references, setReferences] = useState("");
  const [result, setResult] = useState<AggregateResult | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);

  async function aggregate() {
    request.current?.abort();
    setResult(null);
    setError("");
    let inputs;
    try {
      inputs = parseAggregateReferences(references);
    } catch (error) {
      setError(error instanceof AggregateError ? error.message : "Invalid aggregate references.");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const evaluated = await buildAggregate(inputs, controller.signal);
      if (!controller.signal.aborted) setResult(evaluated);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof AggregateError ? error.message : "Aggregate could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return <>
    <label htmlFor="aggregate-references">Manifest references (one manifest digest and optional evidence digest per line)</label>
    <textarea id="aggregate-references" rows={5} value={references} spellCheck={false} autoComplete="off"
      onChange={(event) => {
        request.current?.abort();
        setReferences(event.target.value);
        setResult(null);
        setError("");
        setPending(false);
      }} />
    <button className="lab-button" disabled={pending} onClick={() => void aggregate()}>
      {pending ? "Reading aggregate sources..." : "Build aggregate"}
    </button>
    {error && <p role="alert">{error} No previous aggregate is shown.</p>}
    {result && <div role="status" aria-label="Evaluated manifest aggregate">
      <h3>Aggregate admission: {result.report.state}</h3>
      <p>Admission completeness is not a pass verdict or execution attestation. Failed and cancelled source outcomes remain unchanged.</p>
      <dl>
        <dt>Input members (unique reference pairs)</dt><dd>{result.report.counts.input_members}</dd>
        <dt>Unique verified manifests</dt><dd>{result.report.counts.unique_verified_manifests}</dd>
        <dt>Unique verified execution records</dt><dd>{result.report.counts.unique_verified_execution_records}</dd>
        <dt>Display profiles (no pooled scores)</dt><dd>{result.report.groups.length}</dd>
        <dt>Historical usage completeness</dt><dd>{result.report.usage.state}</dd>
        <dt>Recorded historical AI credits</dt><dd>{result.report.usage.recorded_historical_usage?.ai_credits ?? "Unavailable"}</dd>
      </dl>
      <p>Aggregation incurs no SDK billing. Historical totals require complete, compatible, non-cached recorded usage for every eligible execution.</p>
      <h3>Member state counts</h3>
      <ul>{Object.entries(result.report.counts.member_states).map(([state, count]) => <li key={state}>{state}: {count}</li>)}</ul>
      <h3>Original member outcomes</h3>
      <ul>{Object.entries(result.report.counts.member_outcomes).map(([state, count]) => <li key={state}>{state}: {count}</li>)}</ul>
      <h3>Composition and compatibility</h3>
      {result.report.members.map((member) => <article key={`${member.manifest_sha256}:${member.evidence_sha256 ?? ""}`}>
        <h4>{member.state}: <code>{member.manifest_sha256}</code></h4>
        <a href={`/api/v1/manifests/${member.manifest_sha256}`} target="_blank" rel="noreferrer">Inspect source manifest</a>
        <dl>
          <dt>Evidence digest</dt><dd><code>{member.evidence_sha256 ?? "Optional sidecar not supplied"}</code></dd>
          <dt>Original outcome</dt><dd>{member.outcome ?? "Unavailable"}</dd>
          <dt>Execution record</dt><dd><code>{member.execution_id ?? "Unknown; not inferred from attempt ordinal"}</code></dd>
          {member.binding && <>
            <dt>Task / trial / attempt</dt><dd>{member.binding.selection.task} / {member.binding.selection.trial} / {member.binding.selection.attempt}</dd>
            <dt>Mode</dt><dd>{member.binding.execution_mode}</dd>
          </>}
        </dl>
        {member.diagnostics.length > 0 && <ul aria-label="Member diagnostics">{member.diagnostics.map((code) => <li key={code}>{code}</li>)}</ul>}
        {member.categories.length > 0 && <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Aggregate category states">
          <table><caption>Per-view evidence states, not payloads or assurance</caption>
            <thead><tr><th scope="col">Category</th><th scope="col">State</th><th scope="col">Source</th><th scope="col">Observed count</th><th scope="col">Verification</th></tr></thead>
            <tbody>{member.categories.map((category) => <tr key={category.category}>
              <th scope="row">{category.category}</th><td>{category.state}</td><td>{category.source}</td>
              <td>{category.observed_count ?? "Unavailable"}</td><td>{category.verification}</td>
            </tr>)}</tbody>
          </table>
        </div>}
      </article>)}
      {result.report.usage.diagnostics.length > 0 && <ul aria-label="Historical usage diagnostics">
        {result.report.usage.diagnostics.map((code) => <li key={code}>{code}</li>)}
      </ul>}
      <button className="lab-button" onClick={() => downloadText(result.canonical, "waza-manifest-aggregate.json", "application/json")}>
        Download aggregate JSON
      </button>
    </div>}
  </>;
}

export default function ManifestAggregate({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Standalone manifest aggregation">
    <div className="lab-panel-header"><div>
      <h2>Aggregate completed public manifests</h2>
      <p>Explicit input sets only. Current source bytes and original record context are verified without agents or the original worker.</p>
    </div></div>
    {demo ? <p>Aggregation is unavailable in the synthetic demo. Use the saved-results Lab with an existing local immutable store.</p> : <AggregateBuilder />}
  </section>;
}
