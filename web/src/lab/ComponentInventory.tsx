import { useEffect, useRef, useState } from "react";
import { maxInventoryBytes, verifyInventory, type InventoryReport, type InventorySubjectKind } from "../api/inventory";
import { downloadText } from "../lib/export";

const checkLabels: Record<string, string> = {
  inventory: "Inventory document",
  profile: "Inventory profile",
  subject: "Bound subject",
  binding: "Consumed digest",
  components: "Components",
};

function short(value: string): string {
  return value.length > 24 ? `${value.slice(0, 21)}...` : value;
}

function counts(values: Record<string, number>): string {
  return Object.entries(values).sort(([a], [b]) => a.localeCompare(b)).map(([key, count]) => `${key} ${count}`).join(", ") || "None";
}

function InventoryReader() {
  const [file, setFile] = useState<{ name: string; data: Uint8Array } | null>(null);
  const [kind, setKind] = useState<InventorySubjectKind>("image");
  const [digest, setDigest] = useState("");
  const [name, setName] = useState("");
  const [result, setResult] = useState<{ result: InventoryReport; json: string } | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(false);
  const active = useRef<AbortController | null>(null);
  const generation = useRef(0);
  useEffect(() => () => { active.current?.abort(); generation.current++; }, []);

  function reset() {
    active.current?.abort();
    setResult(null);
    setError("");
    setPending(false);
  }

  async function select(list: FileList | null) {
    reset();
    const current = ++generation.current;
    setFile(null);
    const selected = list?.[0];
    if (!selected) return;
    if (selected.size > maxInventoryBytes) {
      setError("This inventory exceeds the 16 MiB limit and cannot be verified.");
      return;
    }
    setLoading(true);
    try {
      const data = new Uint8Array(await selected.arrayBuffer());
      if (current === generation.current) setFile({ name: selected.name, data });
    } catch (error) {
      if (current === generation.current) setError(error instanceof Error ? error.message : "The inventory could not be read.");
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }

  async function verify() {
    reset();
    const controller = new AbortController();
    active.current = controller;
    setPending(true);
    try {
      const value = await verifyInventory(file?.data ?? null, { kind, digest, name }, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : "The inventory could not be verified.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  const report = result?.result;
  const failures = report?.checks.filter((check) => check.status === "failed") ?? [];
  const missing = failures.some((check) => check.code === "inventory_missing");
  return <>
    <label htmlFor="inventory-file">Component inventory (CycloneDX JSON)</label>
    <input id="inventory-file" type="file" accept=".json,application/json,application/vnd.cyclonedx+json"
      onChange={(event) => void select(event.target.files)} />
    <p aria-live="polite">{loading ? "Reading inventory..." : file ? `${file.name} selected` : "No inventory selected"}</p>
    <label htmlFor="inventory-kind">Consumed artifact</label>
    <select id="inventory-kind" value={kind} onChange={(event) => { reset(); setKind(event.target.value as InventorySubjectKind); }}>
      <option value="image">Runner image</option>
      <option value="plugin">Plugin</option>
    </select>
    <label htmlFor="inventory-digest">Consumed {kind === "image" ? "image manifest" : "plugin artifact"} digest</label>
    <input id="inventory-digest" spellCheck={false} autoComplete="off" value={digest} placeholder="sha256:<64 hex>"
      onChange={(event) => { reset(); setDigest(event.target.value); }} />
    <label htmlFor="inventory-name">{kind === "image" ? "Image repository" : "Plugin name"} (optional)</label>
    <input id="inventory-name" spellCheck={false} autoComplete="off" value={name}
      placeholder={kind === "image" ? "ghcr.io/microsoft/waza-runner-reference" : "example-plugin"}
      onChange={(event) => { reset(); setName(event.target.value); }} />
    <div className="lab-actions">
      <button className="lab-button" disabled={pending || loading} onClick={() => void verify()}>
        {pending ? "Verifying..." : "Verify inventory"}
      </button>
    </div>
    {error && <p role="alert">{error}</p>}
    {report && <div role="status" aria-label="Inventory verification result">
      <h3>{report.status === "verified" ? "Verified" : missing ? "Inventory unavailable" : "Verification failed"}</h3>
      <dl>
        <dt>Availability</dt>
        <dd>{missing ? "No inventory supplied" : report.inventory_sha256 ? <>Inventory <code title={report.inventory_sha256}>sha256:{short(report.inventory_sha256)}</code></> : "Unreadable"}</dd>
        <dt>Bound subject</dt>
        <dd>{report.subject ? <>{report.subject.kind} <code>{report.subject.name}</code> at <code title={report.subject.digest}>{short(report.subject.digest)}</code></> : "Not bound"}</dd>
        <dt>Consumed</dt>
        <dd>{report.expected.kind} {report.expected.digest ? <code title={report.expected.digest}>{short(report.expected.digest)}</code> : "digest not supplied"}</dd>
        {report.summary && <>
          <dt>Components</dt><dd>{report.summary.total}</dd>
          <dt>By type</dt><dd>{counts(report.summary.by_type)}</dd>
          <dt>By ecosystem</dt><dd>{counts(report.summary.by_ecosystem)}</dd>
        </>}
      </dl>
      {failures.length > 0 && <>
        <h3>Mismatch diagnostics</h3>
        <ul aria-label="Inventory failures">{failures.map((check) => <li key={check.id}>
          <strong>{checkLabels[check.id] ?? check.id}</strong> <code>{check.code}</code>: {check.message}
        </li>)}</ul>
      </>}
      <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Inventory checks">
        <table className="lab-table"><caption>Verification checks</caption>
          <thead><tr><th scope="col">Check</th><th scope="col">Status</th><th scope="col">Code</th><th scope="col">Detail</th></tr></thead>
          <tbody>{report.checks.map((check) => <tr key={check.id}>
            <th scope="row">{checkLabels[check.id] ?? check.id}</th><td>{check.status}</td><td><code>{check.code}</code></td><td>{check.message}</td>
          </tr>)}</tbody>
        </table>
      </div>
      <button className="lab-button" onClick={() => downloadText(result.json, "waza-inventory-verification.json", "application/json")}>
        Download report JSON
      </button>
    </div>}
  </>;
}

export default function ComponentInventory({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Component inventory verification">
    <div className="lab-panel-header"><div>
      <h2>Component inventory</h2>
      <p>Verify offline that a component inventory is bound to the runner image or plugin digest you consumed.
        A missing, altered or mismatched inventory fails closed. The result is evidence, not a vulnerability or policy decision.</p>
    </div></div>
    {demo ? <p>Inventory verification is unavailable in the synthetic demo. Use a running Waza server and an inventory from <code>waza runner inventory generate</code>.</p> : <InventoryReader />}
  </section>;
}
