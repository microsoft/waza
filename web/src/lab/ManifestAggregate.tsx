import { useEffect, useRef, useState } from "react";
import { aggregateManifests, AggregateError, type ManifestAggregate as Aggregate } from "../api/aggregates";
import { downloadText } from "../lib/export";

function AggregateInspector() {
  const [input, setInput] = useState("");
  const [report, setReport] = useState<Aggregate | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);

  async function inspect() {
    request.current?.abort();
    setReport(null);
    setError("");
    const manifests = input.trim().split(/\s+/);
    if (manifests.length > 128 || manifests.some((digest) => !/^[0-9a-f]{64}$/.test(digest))) {
      setError("Enter 1..128 full lowercase SHA-256 manifest digests, separated by whitespace.");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const value = await aggregateManifests(manifests, controller.signal);
      if (!controller.signal.aborted) setReport(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof AggregateError ? error.message : "Aggregation could not be loaded.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return <>
    <label htmlFor="aggregate-manifests">Expected manifest digests</label>
    <textarea id="aggregate-manifests" rows={5} spellCheck={false} value={input} onChange={(event) => {
      request.current?.abort();
      setInput(event.target.value);
      setReport(null);
      setError("");
      setPending(false);
    }} />
    <button className="lab-button" disabled={pending} onClick={() => void inspect()}>
      {pending ? "Aggregating..." : "Aggregate manifests"}
    </button>
    {error && <p role="alert">{error} No previous aggregate is shown.</p>}
    {report && <div role="status" aria-label="Aggregate composition">
      <h3>Membership: {report.completion}</h3>
      <p>Compatibility: {report.compatibility}. Evidence assurance and aggregate scores are not inferred.</p>
      <p>Usage and billing: unavailable without category-admitted source usage. Missing evidence is not zero or a pass.</p>
      <button className="lab-button" onClick={() => downloadText(JSON.stringify(report, null, 2), "manifest-aggregate.json", "application/json")}>Download aggregate JSON</button>
      <h3>Input state counts</h3>
      <dl>{Object.entries(report.state_counts).sort(([a], [b]) => a.localeCompare(b)).map(([state, count]) =>
        <div key={state}><dt>{state}</dt><dd>{count}</dd></div>)}</dl>
      <div className="lab-table-scroll"><table>
        <caption>Aggregate membership and omissions</caption>
        <thead><tr><th scope="col">Source manifest</th><th scope="col">Input state</th><th scope="col">Recorded outcome</th><th scope="col">Logical work</th><th scope="col">Recorded grader verdicts</th><th scope="col">Diagnostics</th></tr></thead>
        <tbody>{report.members.map((member) => <tr key={member.manifest_sha256}>
          <th scope="row"><a href={`/api/v1/manifests/${member.manifest_sha256}`} target="_blank" rel="noreferrer"><code>{member.manifest_sha256}</code></a></th>
          <td>{member.state}</td><td>{member.outcome ?? "Unavailable"}</td>
          <td><code>{member.logical?.work ?? "Unavailable"}</code></td>
          <td>{member.verdicts === undefined ? "Unavailable" : member.verdicts.length === 0 ? "Recorded empty" : member.verdicts.map((verdict) =>
            <p key={verdict.identifier}>{verdict.identifier}: {verdict.passed ? "passed" : "failed"}</p>)}</td>
          <td>{member.diagnostics.length === 0 ? "None" : member.diagnostics.map((diagnostic, index) =>
            <p key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</p>)}</td>
        </tr>)}</tbody>
      </table></div>
      <h3>Conflicts</h3>
      {report.conflicts.length === 0 ? <p>No logical-work conflicts reported.</p> : report.conflicts.map((conflict) =>
        <div key={`${conflict.logical.run}-${conflict.logical.work}`}>
          <p><code>{conflict.logical.work}</code>: no winning attempt selected.</p>
          <ul>{conflict.manifests.map((digest) => <li key={digest}><code>{digest}</code></li>)}</ul>
        </div>)}
      <h3>Compatibility diagnostics</h3>
      {report.diagnostics.length === 0 ? <p>No set diagnostics reported.</p> : report.diagnostics.map((diagnostic, index) =>
        <p key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</p>)}
    </div>}
  </>;
}

export default function ManifestAggregate({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Completed manifest aggregation">
    <div className="lab-panel-header"><div>
      <h2>Aggregate completed manifests</h2>
      <p>Read-only, explicit expected membership. Duplicate digests count once; missing or competing sources remain explicit.</p>
      <p>Derived content and declared context only; not execution attestation. No workers or agents are started.</p>
    </div></div>
    {demo ? <p>Aggregation is unavailable in the synthetic demo. Use a running saved-results server and its immutable store.</p> : <AggregateInspector />}
  </section>;
}
