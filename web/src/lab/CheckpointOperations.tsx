import { useEffect, useRef, useState } from "react";
import unit from "../../../examples/runner/unit.json";
import { runCheckpointOperation, type CheckpointOperationResponse } from "../api/checkpointResults";
import { inspectCheckpoint, type CheckpointInspection } from "../api/checkpoints";
import { downloadText } from "../lib/export";
import CheckpointResult from "./CheckpointResult";

const example = JSON.stringify({
  kind: "waza.execution-submission", schema_version: "1.0", request: unit,
}, null, 2);

function Operations() {
  const [request, setRequest] = useState(example);
  const [digest, setDigest] = useState("");
  const [response, setResponse] = useState<CheckpointOperationResponse | null>(null);
  const [error, setError] = useState("");
  const [metadata, setMetadata] = useState<CheckpointInspection | null>(null);
  const [metadataError, setMetadataError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  function clear() {
    active.current?.abort();
    setResponse(null);
    setError("");
    setMetadata(null);
    setMetadataError("");
    setPending(false);
  }

  async function run(action: "create" | "resume") {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setResponse(null);
    setError("");
    setMetadata(null);
    setMetadataError("");
    setPending(true);
    try {
      const value = await runCheckpointOperation(action, request, digest, controller.signal);
      if (!controller.signal.aborted) {
        setResponse(value);
        if (value.result.finalized_checkpoint_sha256) {
          try {
            const current = await inspectCheckpoint(value.result.finalized_checkpoint_sha256, controller.signal);
            if (!controller.signal.aborted) setMetadata(current);
          } catch (error) {
            if (!controller.signal.aborted) setMetadataError(
              error instanceof Error ? error.message : "Published checkpoint metadata could not be loaded.",
            );
          }
        }
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof Error ? error.message : "Checkpoint operation could not be loaded.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <>
      <label htmlFor="checkpoint-operation-request">Captured checkpoint submission (JSON)</label>
      <textarea id="checkpoint-operation-request" rows={14} spellCheck={false} value={request}
        onChange={(event) => { clear(); setRequest(event.target.value); }} />
      <label htmlFor="checkpoint-operation-digest">Checkpoint digest to resume</label>
      <input id="checkpoint-operation-digest" spellCheck={false} autoComplete="off" value={digest}
        onChange={(event) => { clear(); setDigest(event.target.value); }} />
      <div className="lab-actions">
        <button className="lab-button" disabled={pending} onClick={() => void run("create")}>
          {pending ? "Checkpoint operation running..." : "Create checkpoints and run unit"}
        </button>
        <button className="lab-button" disabled={pending} onClick={() => void run("resume")}>Resume checkpoint</button>
      </div>
      {error && <p role="alert">{error} No previous operation result is shown.</p>}
      {response && (
        <>
          <CheckpointResult result={response.result} />
          {metadataError && <p role="alert">{metadataError} No previous published-checkpoint metadata is shown.</p>}
          {metadata && <div role="status" aria-label="Published checkpoint current metadata">
            <h3>Published checkpoint current metadata (declaration only)</h3>
            <p>This separate blob read does not grant execution ownership or replace the operation result.</p>
            <dl>
              <dt>Current availability</dt><dd>{metadata.availability}</dd>
              <dt>Published boundary</dt><dd>{metadata.checkpoint?.boundary ?? "Unavailable"}</dd>
              <dt>Created (UTC)</dt><dd>{metadata.checkpoint
                ? <time dateTime={metadata.checkpoint.created_at}>{metadata.checkpoint.created_at}</time> : "Unavailable"}</dd>
            </dl>
            {metadata.diagnostics.map((item, index) => <p key={`${item.code}-${index}`}>{item.code}: {item.message}</p>)}
          </div>}
          <button className="lab-button" onClick={() => downloadText(
            response.json, "waza-checkpoint-result.json", "application/json",
          )}>Download checkpoint result JSON</button>
        </>
      )}
    </>
  );
}

export default function CheckpointOperations({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Checkpoint creation and recovery">
      <div className="lab-panel-header">
        <div>
          <h2>Create or resume an exact-unit checkpoint</h2>
          <p>Creation executes one captured unit and publishes before-unit and finalized-unit references.
            Before-unit resume creates a fresh retry child; finalized-unit resume preserves the original terminal without new engine calls.</p>
          <p>Use the original immutable store and a clean versioned Waza build. Source inspection alone does not authorize recovery.
            A failed or canceled original is not successful grading.</p>
        </div>
      </div>
      {demo
        ? <p>Checkpoint operations are unavailable in the synthetic demo. Use a running Waza server and private immutable store.</p>
        : <Operations />}
    </section>
  );
}
