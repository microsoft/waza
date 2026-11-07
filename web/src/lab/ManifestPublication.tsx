import { useEffect, useRef, useState } from "react";
import { discoverManifests, inspectManifest, ManifestInspectionError, type ManifestCatalog, type ManifestInspection } from "../api/manifests";
import ExecutionEvidence from "./ExecutionEvidence";

function ManifestInspector() {
  const [digest, setDigest] = useState("");
  const [result, setResult] = useState<ManifestInspection | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  const catalogRequest = useRef<AbortController | null>(null);
  const [catalog, setCatalog] = useState<ManifestCatalog | null>(null);
  const [catalogError, setCatalogError] = useState("");
  const [discovering, setDiscovering] = useState(false);

  useEffect(() => () => {
    request.current?.abort();
    catalogRequest.current?.abort();
  }, []);

  async function discover() {
    catalogRequest.current?.abort();
    const controller = new AbortController();
    catalogRequest.current = controller;
    setCatalog(null);
    setCatalogError("");
    setDiscovering(true);
    try {
      const value = await discoverManifests(controller.signal);
      if (!controller.signal.aborted) setCatalog(value);
    } catch (error) {
      if (!controller.signal.aborted) {
        setCatalogError(error instanceof ManifestInspectionError ? error.message : "Manifest discovery could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setDiscovering(false);
    }
  }

  async function inspect() {
    request.current?.abort();
    setResult(null);
    setError("");
    if (!/^[0-9a-f]{64}$/.test(digest)) {
      setError("Enter a full lowercase SHA-256 digest (64 hexadecimal characters).");
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    try {
      const inspection = await inspectManifest(digest, controller.signal);
      if (!controller.signal.aborted) setResult(inspection);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof ManifestInspectionError ? error.message : "Manifest inspection could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <>
      <button className="lab-button" onClick={() => void discover()} disabled={discovering}>
        {discovering ? "Finding manifests..." : "Find stored manifests"}
      </button>
      {catalogError && <p role="alert">{catalogError} No previous catalog is shown.</p>}
      {catalog && (
        <div aria-label="Stored manifest pointers">
          <p>Unverified declaration pointers. Select a digest, then inspect its current content.</p>
          {catalog.truncated && <p>Discovery is truncated; this is not a complete result inventory.</p>}
          {catalog.manifest_sha256.length === 0 && <p>No manifest pointers were returned.</p>}
          <ul>{catalog.manifest_sha256.map((value) => (
            <li key={value}><button className="lab-button lab-manifest-pointer" onClick={() => {
              request.current?.abort();
              setDigest(value);
              setResult(null);
              setError("");
              setPending(false);
            }}><code>{value}</code></button></li>
          ))}</ul>
          {catalog.diagnostics.map((d, i) => <p key={`${d.code}-${i}`}>{d.code}: {d.message}</p>)}
        </div>
      )}
      <label htmlFor="manifest-digest">Manifest SHA-256 digest</label>
      <input id="manifest-digest" type="text" spellCheck={false} autoComplete="off" value={digest}
        onChange={(event) => {
          request.current?.abort();
          setDigest(event.target.value);
          setResult(null);
          setError("");
          setPending(false);
        }}
      />
      <button className="lab-button" onClick={() => void inspect()} disabled={pending}>
        {pending ? "Inspecting manifest..." : "Inspect manifest"}
      </button>
      {error && <p role="alert">{error} No previous receipt is shown.</p>}
      {result && (
        <div role="status" aria-label="Manifest inspection receipt">
          <h3>Publication: {result.receipt.publication}</h3>
          <dl>
            <dt>Manifest digest</dt><dd><code>{result.receipt.manifest_sha256}</code></dd>
            <dt>Declared identity attempt</dt><dd><code>{result.receipt.attempt ?? "Not available"}</code></dd>
            <dt>Publication state</dt><dd>{result.receipt.publication}</dd>
            <dt>Content integrity</dt><dd>{result.receipt.integrity}</dd>
            <dt>Inspection HTTP status</dt><dd>{result.status}</dd>
          </dl>
          <h3>Declared artifacts</h3>
          {result.receipt.artifacts.length === 0 ? <p>No artifact declarations available.</p> : (
            <div className="lab-table-scroll" tabIndex={0} role="region" aria-label="Artifact inspection details">
            <table>
              <caption>Per-artifact content integrity</caption>
              <thead><tr><th scope="col">Logical name</th><th scope="col">Integrity</th><th scope="col">SHA-256</th><th scope="col">Size (bytes)</th><th scope="col">Media type</th></tr></thead>
              <tbody>
                {result.receipt.artifacts.map((artifact, index) => (
                  <tr key={`${artifact.name}-${index}`}>
                    <th scope="row">{artifact.name}</th>
                    <td>{artifact.integrity}</td>
                    <td><code>{artifact.sha256}</code></td>
                    <td>{artifact.size_bytes}</td>
                    <td>{artifact.media_type}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )}
          <h3>Logical-name diagnostics</h3>
          {result.receipt.diagnostics.length === 0 ? <p>No diagnostics reported.</p> : (
            <ul>{result.receipt.diagnostics.map((diagnostic, index) => (
              <li key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</li>
            ))}</ul>
          )}
        </div>
      )}
    </>
  );
}

export default function ManifestPublication({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Manifest publication inspection">
      <div className="lab-panel-header">
        <div>
          <h2>Inspect immutable manifest publication</h2>
          <p>Read-only inspection rereads current stored content. This panel does not upload or finalize artifacts.</p>
          <p>Content and declared context only; not execution attestation.</p>
        </div>
      </div>
      {demo ? <p>Manifest inspection is unavailable in the synthetic demo. Open the saved-results Lab with a running Waza server.</p> : <ManifestInspector />}
      <ExecutionEvidence demo={demo} />
    </section>
  );
}
