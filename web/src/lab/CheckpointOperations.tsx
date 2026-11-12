import { useEffect, useRef, useState } from "react";
import unit from "../../../examples/runner/unit.json";
import { runCheckpointOperation, type CheckpointOperationResponse } from "../api/checkpointResults";
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
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  function clear() {
    active.current?.abort();
    setResponse(null);
    setError("");
    setPending(false);
  }

  async function run(action: "create" | "resume") {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setResponse(null);
    setError("");
    setPending(true);
    try {
      const value = await runCheckpointOperation(action, request, digest, controller.signal);
      if (!controller.signal.aborted) setResponse(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(
        error instanceof Error ? error.message : "Checkpoint operation could not be loaded.",
      );
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
        {pending && <button className="lab-button" onClick={clear}>Stop waiting for checkpoint result</button>}
      </div>
      <p>Stopping this browser request is not proof the server worker stopped. Stop and join the original worker before retrying a before-unit source.</p>
      {error && <p role="alert">{error} No previous operation result is shown; execution may have occurred without a received response.</p>}
      {response && <>
        <CheckpointResult result={response.result} />
        <button className="lab-button" onClick={() => downloadText(
          response.json, "waza-checkpoint-result.json", "application/json",
        )}>Download checkpoint result JSON</button>
      </>}
    </>
  );
}

export default function CheckpointOperations({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Checkpoint creation and recovery">
      <div className="lab-panel-header"><div>
        <h2>Create or resume an exact-unit checkpoint</h2>
        <p>Creation executes one captured unit and publishes before-unit and finalized-unit references.
          Before-unit resume creates a fresh retry child; finalized-unit resume preserves the original terminal without new engine calls.</p>
        <p>Use the original private immutable store and a clean versioned Waza build.
          A failed or canceled original is not successful grading. No raw source-inspection endpoint is used.</p>
        <p>Creation and before-unit resume execute work and may incur charges. The Copilot profile requires explicit paid consent
          in the captured request and disables all tools. Finalized reuse reports new calls separately from the original terminal.</p>
      </div></div>
      {demo
        ? <p>Checkpoint operations are unavailable in the synthetic demo. Use a running Waza server and private immutable store.</p>
        : <Operations />}
    </section>
  );
}
