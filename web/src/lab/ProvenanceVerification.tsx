import { useEffect, useRef, useState } from "react";
import { readBundleFiles, verifyProvenance, type ProvenanceReport } from "../api/provenance";
import { downloadText } from "../lib/export";

const checkLabels: Record<string, string> = {
  bundle: "Bundle index",
  signature: "Signature and trust",
  statement: "Signed statement",
  inputs: "Canonical inputs",
  runner: "Runner API version",
  runtime: "Runtime and model",
  policy: "Policy",
  image: "Runner image",
  waza_version: "Waza version",
  artifacts: "Artifact chain",
  outcome: "Terminal outcome",
  bindings: "Additional bindings",
  members: "Bundle members",
};

function short(value: string): string {
  return value.length > 24 ? `${value.slice(0, 21)}...` : value;
}

function ProvenanceReader() {
  const [files, setFiles] = useState<Map<string, Uint8Array>>(new Map());
  const [urls, setUrls] = useState<Map<string, string>>(new Map());
  const [publicKey, setPublicKey] = useState("");
  const [imageDigest, setImageDigest] = useState("");
  const [result, setResult] = useState<{ result: ProvenanceReport; json: string } | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const [loading, setLoading] = useState(false);
  const active = useRef<AbortController | null>(null);
  const generation = useRef(0);
  useEffect(() => () => active.current?.abort(), []);
  useEffect(() => () => urls.forEach((url) => URL.revokeObjectURL(url)), [urls]);

  function reset() {
    active.current?.abort();
    setResult(null);
    setError("");
    setPending(false);
  }

  async function select(list: FileList | null) {
    reset();
    const current = ++generation.current;
    setFiles(new Map());
    setUrls(new Map());
    setLoading(true);
    try {
      const selected = await readBundleFiles(list ? Array.from(list) : []);
      if (current !== generation.current) return;
      setFiles(selected);
      setUrls(new Map([...selected].map(([path, data]) => [path, URL.createObjectURL(new Blob([data as BlobPart]))])));
    } catch (error) {
      if (current === generation.current) setError(error instanceof Error ? error.message : "The bundle could not be read.");
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
      const value = await verifyProvenance(files, publicKey, { image_digest: imageDigest.trim() }, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : "Provenance could not be verified.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  function link(path: string, label = path) {
    const url = urls.get(path);
    return url ? <a href={url} download={path.split("/").pop()}>{label}</a> : <code>{label}</code>;
  }

  const report = result?.result;
  const failures = report?.checks.filter((check) => check.status === "failed") ?? [];
  const predicate = report?.predicate;
  return <>
    <label htmlFor="provenance-bundle">Provenance bundle directory</label>
    <input id="provenance-bundle" type="file" multiple
      ref={(input) => { input?.setAttribute("webkitdirectory", ""); }}
      onChange={(event) => void select(event.target.files)} />
    <p aria-live="polite">{loading ? "Reading bundle..." : files.size > 0 ? `${files.size} bundle file${files.size === 1 ? "" : "s"} selected` : "No bundle selected"}</p>
    <label htmlFor="provenance-key">Trusted public key (PEM)</label>
    <textarea id="provenance-key" spellCheck={false} rows={4} value={publicKey} placeholder="-----BEGIN PUBLIC KEY-----"
      onChange={(event) => { reset(); setPublicKey(event.target.value); }} />
    <label htmlFor="provenance-image">Expected runner image digest (optional)</label>
    <input id="provenance-image" spellCheck={false} autoComplete="off" value={imageDigest} placeholder="sha256:<64 hex>"
      onChange={(event) => { reset(); setImageDigest(event.target.value); }} />
    <div className="lab-actions">
      <button className="lab-button" disabled={pending || loading || files.size === 0} onClick={() => void verify()}>
        {pending ? "Verifying..." : "Verify provenance"}
      </button>
    </div>
    {error && <p role="alert">{error}</p>}
    {report && <div role="status" aria-label="Provenance verification result">
      <h3>{report.status === "verified" ? "Verified" : "Verification failed"}</h3>
      <dl>
        <dt>Signer</dt>
        <dd>{report.signer ? <>{report.signer.mode}, key <code title={report.signer.key_id}>{short(report.signer.key_id)}</code>{report.signer.trusted ? " (trusted)" : " (not trusted)"}</> : "Unavailable"}</dd>
        <dt>Statement</dt>
        <dd>{report.statement ? <>{link(report.statement.path)} <code title={report.statement.envelope_sha256}>sha256:{short(report.statement.envelope_sha256)}</code>{report.statement.predicate_type && <> ({report.statement.predicate_type})</>}</> : "Unavailable"}</dd>
        {predicate && <>
          <dt>Canonical inputs</dt><dd>{link(predicate.inputs.request.path)} <code title={predicate.inputs.identity.canonical_digest}>{short(predicate.inputs.identity.canonical_digest)}</code></dd>
          <dt>Runner</dt><dd>API {predicate.runner.api_version}, image <code title={`${predicate.runner.image.reference}@${predicate.runner.image.digest}`}>{predicate.runner.image.reference}@{short(predicate.runner.image.digest)}</code></dd>
          <dt>Waza version</dt><dd>{predicate.waza.version}</dd>
          <dt>Runtime</dt><dd>{predicate.runtime.executor} / {predicate.runtime.model} ({predicate.runtime.execution_mode})</dd>
          <dt>Policy</dt><dd>{link(predicate.policy.path)}</dd>
          <dt>Outcome</dt><dd>{predicate.outcome.outcome} (exit {predicate.outcome.exit_code}) from {link(predicate.outcome.artifact.path)}</dd>
        </>}
      </dl>
      {failures.length > 0 && <>
        <h3>Failure reasons</h3>
        <ul aria-label="Provenance failures">{failures.map((check) => <li key={check.id}>
          <strong>{checkLabels[check.id] ?? check.id}</strong> <code>{check.code}</code>: {check.message}
        </li>)}</ul>
      </>}
      <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Provenance checks">
        <table className="lab-table"><caption>Verification checks</caption>
          <thead><tr><th scope="col">Check</th><th scope="col">Status</th><th scope="col">Code</th><th scope="col">Detail</th></tr></thead>
          <tbody>{report.checks.map((check) => <tr key={check.id}>
            <th scope="row">{checkLabels[check.id] ?? check.id}</th><td>{check.status}</td><td><code>{check.code}</code></td><td>{check.message}</td>
          </tr>)}</tbody>
        </table>
      </div>
      <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Bound members">
        <table className="lab-table"><caption>Bundle members and bound digests</caption>
          <thead><tr><th scope="col">Member</th><th scope="col">Role</th><th scope="col">Bound sha256</th><th scope="col">Status</th></tr></thead>
          <tbody>{report.members.map((member) => <tr key={member.path}>
            <th scope="row">{link(member.path)}</th><td>{member.role}</td>
            <td>{member.sha256 ? <code title={member.sha256}>{short(member.sha256)}</code> : "None"}</td><td>{member.status}</td>
          </tr>)}</tbody>
        </table>
      </div>
      {report.members_omitted && <p>{report.members_omitted} unbound member{report.members_omitted === 1 ? "" : "s"} omitted from this report.</p>}
      <button className="lab-button" onClick={() => downloadText(result.json, "waza-provenance-verification.json", "application/json")}>
        Download verification report
      </button>
    </div>}
  </>;
}

export default function ProvenanceVerification({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Execution provenance verification">
    <div className="lab-panel-header"><div>
      <h2>Execution provenance</h2>
      <p>Verify a signed provenance bundle offline: the signature, canonical inputs, runner image, policy, runtime, artifact chain and terminal outcome.
        Trust comes only from the public key you supply; any altered, missing or unbound member fails closed.</p>
    </div></div>
    {demo ? <p>Provenance verification is unavailable in the synthetic demo. Use a running Waza server and a bundle from <code>waza runner provenance generate</code>.</p> : <ProvenanceReader />}
  </section>;
}
