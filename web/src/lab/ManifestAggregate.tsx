import { useEffect, useRef, useState } from "react";
import { aggregateManifests, aggregateCSV, AggregateError, type EvidenceInput, type LoadedAggregate } from "../api/aggregates";
import { downloadText } from "../lib/export";

function AggregateInspector() {
  const [input, setInput] = useState("");
  const [evidenceInput, setEvidenceInput] = useState("");
  const [loaded, setLoaded] = useState<LoadedAggregate | null>(null);
  const report = loaded?.report;
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);

  async function inspect() {
    request.current?.abort();
    setLoaded(null);
    setError("");
    const manifests = input.trim().split(/\s+/);
    if (manifests.length > 128 || manifests.some((digest) => !/^[0-9a-f]{64}$/.test(digest))) {
      setError("Enter 1..128 full lowercase SHA-256 manifest digests, separated by whitespace.");
      return;
    }
    let evidence: EvidenceInput[] | undefined;
    if (evidenceInput.trim()) {
      const pairs = evidenceInput.trim().split(/\s+/);
      if (pairs.length > 128 || pairs.some((pair) => !/^[0-9a-f]{64}=[0-9a-f]{64}$/.test(pair) ||
          !manifests.includes(pair.slice(0, 64)))) {
        setError("Enter at most 128 MANIFEST_SHA256=EVIDENCE_SHA256 pairs; each origin must belong to the expected set.");
        return;
      }
      evidence = pairs.map((pair) => ({ manifest_sha256: pair.slice(0, 64), evidence_sha256: pair.slice(65) }));
    }
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const value = await aggregateManifests(manifests, controller.signal, evidence);
      if (!controller.signal.aborted) setLoaded(value);
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
      setLoaded(null);
      setError("");
      setPending(false);
    }} />
    <label htmlFor="aggregate-evidence">Optional explicit evidence pairs</label>
    <textarea id="aggregate-evidence" rows={3} spellCheck={false} value={evidenceInput} onChange={(event) => {
      request.current?.abort();
      setEvidenceInput(event.target.value);
      setLoaded(null);
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
      <button className="lab-button" onClick={() => loaded && downloadText(loaded.rawJSON, "manifest-aggregate.json", "application/json")}>Download aggregate JSON</button>
      <button className="lab-button" onClick={() => loaded && downloadText(aggregateCSV(loaded), "manifest-aggregate.csv", "text/csv")}>Download aggregate CSV</button>
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
      {report.evidence && <>
        <h3>Evidence declarations: {report.evidence.completion}</h3>
        <p>Content and declared context only. Category counts are declarations, not verified capture completeness.
          Usage totals are {report.evidence.usage_totals}; original recorded credits are not new-operation billing.</p>
        <h4>Declaration-only state counts</h4>
        <dl>{Object.entries(report.evidence.declaration_state_counts).sort(([a], [b]) => a.localeCompare(b)).map(([category, states]) =>
          <div key={category}><dt>{category}</dt><dd>{Object.entries(states).map(([state, count]) => `${state}: ${count}`).join("; ")}</dd></div>)}</dl>
        {report.evidence.members.map((member) => <div key={member.manifest_sha256}>
          <h4><code>{member.manifest_sha256}</code>: {member.state}</h4>
          {member.candidates.length === 0 && <p>No sidecar selected; category evidence unavailable.</p>}
          {member.state === "conflicting" && <p>All competing sidecars retained; no declaration or billing winner selected.</p>}
          {member.candidates.map((candidate) => <div key={candidate.evidence_sha256}>
            <p><a href={`/api/v1/evidence/${candidate.evidence_sha256}`} target="_blank" rel="noreferrer"><code>{candidate.evidence_sha256}</code></a>: {candidate.state}</p>
            <p>Current source links: {candidate.source_manifests.length === 0 ? "Unavailable" : candidate.source_manifests.map((digest) =>
              <a key={digest} href={`/api/v1/manifests/${digest}`} target="_blank" rel="noreferrer"><code>{digest} </code></a>)}</p>
            <dl>{candidate.declared_categories.map((declaration) => <div key={declaration.category}>
              <dt>{declaration.category}</dt><dd>{declaration.declared_state}; source: {declaration.declared_source};
                context: {declaration.declared_context}; count: {declaration.declared_observed_count ?? "Unavailable"}
                {declaration.declared_reason && `; reason: ${declaration.declared_reason}`}
                {declaration.declared_limit && `; limit observed: ${declaration.declared_limit.observed}; total: ${declaration.declared_limit.total ?? "Unavailable"}`}
              </dd>
            </div>)}</dl>
            <p>Recorded usage: {candidate.usage?.state ?? "Unavailable"}; verification: {candidate.usage?.verification ?? "unverified"}.
              {candidate.usage?.reason && ` Reason: ${candidate.usage.reason}.`}</p>
            {loaded?.exactUsage[candidate.evidence_sha256] && <dl>
              {Object.entries(loaded.exactUsage[candidate.evidence_sha256]!).map(([name, value]) =>
                <div key={name}><dt>{name}</dt><dd><code>{value}</code></dd></div>)}
            </dl>}
            {candidate.diagnostics.map((diagnostic, index) => <p key={`${diagnostic.code}-${index}`}>
              {diagnostic.code}: {diagnostic.message}</p>)}
          </div>)}
        </div>)}
      </>}
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
