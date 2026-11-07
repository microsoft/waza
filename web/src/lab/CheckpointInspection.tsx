import { useEffect, useRef, useState } from "react";
import {
  CheckpointInspectionError,
  inspectCheckpoint,
  type CheckpointInspection as Inspection,
} from "../api/checkpoints";

function CheckpointInspector() {
  const [digest, setDigest] = useState("");
  const [result, setResult] = useState<Inspection | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);

  useEffect(() => () => request.current?.abort(), []);

  async function inspect() {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setResult(null);
    setError("");
    setPending(true);
    try {
      const value = await inspectCheckpoint(digest, controller.signal);
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof CheckpointInspectionError ? error.message : "Checkpoint inspection could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <>
      <label htmlFor="checkpoint-digest">Checkpoint blob SHA-256 digest</label>
      <input id="checkpoint-digest" type="text" spellCheck={false} autoComplete="off" value={digest}
        onChange={(event) => {
          request.current?.abort();
          setDigest(event.target.value);
          setResult(null);
          setError("");
          setPending(false);
        }}
      />
      <button className="lab-button" onClick={() => void inspect()} disabled={pending}>
        {pending ? "Inspecting checkpoint..." : "Inspect checkpoint"}
      </button>
      {error && <p role="alert">{error} No previous checkpoint metadata is shown.</p>}
      {result && (
        <div role="status" aria-label="Checkpoint source metadata">
          <h3>{result.availability === "blob-verified" ? "Blob verified (declaration only)" : "Checkpoint unavailable"}</h3>
          <dl>
            <dt>Checkpoint digest</dt><dd><code>{result.checkpoint_sha256}</code></dd>
            <dt>Declared boundary</dt><dd>{result.checkpoint?.boundary ?? "Unavailable"}</dd>
            <dt>Created (UTC)</dt><dd>{result.checkpoint
              ? <time dateTime={result.checkpoint.created_at}>{result.checkpoint.created_at}</time>
              : "Unavailable"}</dd>
            <dt>Original execution reference</dt><dd><code>{result.checkpoint?.execution_id ?? "Unavailable"}</code></dd>
            <dt>Declared runner revision</dt><dd><code>{result.checkpoint?.compatibility.runner_revision ?? "Unavailable"}</code></dd>
            <dt>Current compatibility</dt><dd>Unavailable - source not authenticated</dd>
            <dt>Restored units</dt><dd>Unavailable</dd>
            <dt>Remaining work</dt><dd>Unavailable</dd>
          </dl>
          <h3>Source inspection diagnostics</h3>
          <ul>{result.diagnostics.map((diagnostic, index) => (
            <li key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</li>
          ))}</ul>
        </div>
      )}
    </>
  );
}

export default function CheckpointInspection({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Checkpoint source inspection">
      <div className="lab-panel-header">
        <div>
          <h2>Inspect a stored checkpoint declaration</h2>
          <p>Read-only current blob inspection. It does not create checkpoints, authenticate ownership or resume work.</p>
          <p>References and declared boundaries are not completed execution evidence or restored progress.</p>
        </div>
      </div>
      {demo
        ? <p>Checkpoint inspection is unavailable in the synthetic demo. Open the saved-results Lab with a running Waza server.</p>
        : <CheckpointInspector />}
    </section>
  );
}
