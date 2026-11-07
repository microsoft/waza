import { useEffect, useRef, useState } from "react";
import { ExecutionEvidenceError, inspectExecutionEvidence, type EvidenceReceipt } from "../api/executionEvidence";

function EvidenceInspector() {
  const [digest, setDigest] = useState("");
  const [receipt, setReceipt] = useState<EvidenceReceipt | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => request.current?.abort(), []);

  async function inspect() {
    request.current?.abort();
    setReceipt(null);
    setError("");
    if (!/^[0-9a-f]{64}$/.test(digest)) {
      setError("Enter a full lowercase SHA-256 evidence digest.");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const result = await inspectExecutionEvidence(digest, controller.signal);
      if (!controller.signal.aborted) setReceipt(result);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof ExecutionEvidenceError ? error.message : "Evidence inspection could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <>
      <label htmlFor="execution-evidence-digest">Evidence SHA-256 digest</label>
      <input id="execution-evidence-digest" type="text" spellCheck={false} autoComplete="off" value={digest}
        onChange={(event) => {
          request.current?.abort();
          setDigest(event.target.value);
          setReceipt(null);
          setError("");
          setPending(false);
        }} />
      <button className="lab-button" onClick={() => void inspect()} disabled={pending}>
        {pending ? "Inspecting evidence..." : "Inspect evidence"}
      </button>
      {error && <p role="alert">{error} Previous evidence has been cleared.</p>}
      {receipt && <div aria-label="Execution evidence receipt">
        <p>Current content integrity: {receipt.integrity}. Publication: derived.</p>
        {receipt.diagnostics.map((diagnostic, index) => <p key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</p>)}
        {!receipt.document ? <p>Category evidence is withheld because source verification did not complete.</p> : <>
          <p>Source manifest: <a href={`/api/v1/manifests/${receipt.document.origin_manifest}`}><code>{receipt.document.origin_manifest}</code></a></p>
          <p>Source execution: <code>{receipt.document.execution_source?.execution_id ?? "No verified source-attempt reference recorded"}</code></p>
          <div style={{ overflowX: "auto" }}>
            <table>
              <caption>Declared per-category availability and completeness</caption>
              <thead><tr><th scope="col">Category</th><th scope="col">State</th><th scope="col">Recorded count</th><th scope="col">Source</th><th scope="col">Context</th><th scope="col">Reason / limits</th></tr></thead>
              <tbody>{receipt.document.categories.map((record) => <tr key={record.category}>
                <th scope="row">{record.category}</th>
                <td>{record.state}{record.state === "present" && record.observed_count === "0" ? " (observed empty declaration)" : ""}</td>
                <td>{record.observed_count ?? "Unavailable"}</td>
                <td>{record.source}</td><td>{record.context} (declaration)</td>
                <td>{record.reason ?? "None"}{record.limit && <>; measured lower bound {record.limit.observed}; total {record.limit.total ?? "unknown"}</>}</td>
              </tr>)}</tbody>
            </table>
          </div>
          {receipt.document.lineage.length === 0 ? <p>No source-manifest lineage recorded.</p> : <ul aria-label="Source-manifest lineage">
            {receipt.document.lineage.map((link) => <li key={`${link.manifest}-${link.relationship}`}>{link.relationship}: <a href={`/api/v1/manifests/${link.manifest}`}><code>{link.manifest}</code></a></li>)}
          </ul>}
          <p><a href={`/api/v1/evidence/${receipt.evidence_sha256}/document`} download>Download evidence JSON</a>{" "}
            <a href={`/api/v1/evidence/${receipt.evidence_sha256}/csv`} download>Download evidence CSV</a></p>
          <p>Downloads recheck current sources; damaged or unfinalized sources are withheld.</p>
        </>}
      </div>}
    </>
  );
}

export default function ExecutionEvidence({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel" aria-label="Execution evidence inspection">
      <h2>Review execution evidence states</h2>
      <p>Read-only, separately digested evidence metadata. Completeness and provenance remain producer declarations; verified bytes are not execution attestation or assurance acceptance.</p>
      <p>Unavailable, incomplete, redacted, and conflicting evidence never implies a pass.</p>
      {demo ? <p>Evidence inspection is unavailable in the synthetic demo; no recorded evidence is reconstructed.</p> : <EvidenceInspector />}
    </section>
  );
}
