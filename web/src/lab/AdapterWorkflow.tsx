import { useEffect, useRef, useState } from "react";
import { AdapterError, dispatchCall, translateDescriptor, type AdapterOutcome, type AdapterTranslation } from "../api/adapter";
import { downloadText } from "../lib/export";

const statusLabels: Record<string, string> = {
  terminal: "Terminal (returned manifest)",
  rejected: "Rejected input",
  operation_error: "Operation error (no terminal)",
  observed: "Observed (read-only lineage)",
  contract_error: "Contract error (reply unusable)",
};

function short(id?: string): string {
  if (!id) return "None";
  return id.length > 20 ? `${id.slice(0, 17)}...` : id;
}

function AdapterDispatch() {
  const [descriptor, setDescriptor] = useState("");
  const [translation, setTranslation] = useState<AdapterTranslation | null>(null);
  const [outcome, setOutcome] = useState<AdapterOutcome | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState("");
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  function reset() {
    active.current?.abort();
    setTranslation(null);
    setOutcome(null);
    setError("");
    setPending("");
  }

  async function run<T>(label: string, work: (signal: AbortSignal) => Promise<T>, done: (value: T) => void) {
    const controller = new AbortController();
    active.current = controller;
    setPending(label);
    setError("");
    try {
      const value = await work(controller.signal);
      if (!controller.signal.aborted) done(value);
    } catch (cause) {
      if (!controller.signal.aborted) setError(cause instanceof AdapterError ? cause.message : `${label} failed.`);
    } finally {
      if (!controller.signal.aborted) setPending("");
    }
  }

  const call = translation?.status === "translated" ? translation.call : undefined;
  const summary = translation?.summary;
  return <>
    <label htmlFor="adapter-descriptor">Work descriptor JSON</label>
    <textarea id="adapter-descriptor" rows={8} spellCheck={false} value={descriptor}
      placeholder='{"kind":"waza.adapter-work-descriptor","schema_version":"1.0","work_id":"...","operation":"submit","request":{...}}'
      onChange={(event) => { reset(); setDescriptor(event.target.value); }} />
    <div className="lab-actions">
      <button className="lab-button" disabled={pending !== "" || descriptor.trim() === ""}
        onClick={() => { setOutcome(null); void run("Translation", (signal) => translateDescriptor(descriptor, signal), setTranslation); }}>
        {pending === "Translation" ? "Translating..." : "Translate descriptor"}
      </button>
    </div>
    {error && <p role="alert">{error}</p>}
    {translation && <div role="status" aria-label="Adapter translation">
      <h3>{translation.status === "translated" ? `Translated ${translation.operation} for ${translation.work_id}` : "Descriptor rejected; nothing will be dispatched"}</h3>
      <p>Adapter <code>{translation.adapter.name}</code> {translation.adapter.version} (runner contract {translation.adapter.runner_versions.join(", ")})</p>
      {translation.diagnostics.length > 0 && <ul aria-label="Translation diagnostics">{translation.diagnostics.map((diagnostic) =>
        <li key={diagnostic.code}><strong>{diagnostic.code}</strong>: {diagnostic.message}</li>)}</ul>}
      {call && <dl aria-label="Public call">
        <dt>CLI</dt><dd><code>waza {call.command.join(" ")}</code></dd>
        <dt>HTTP</dt><dd><code>{call.http.method} {call.http.path}</code></dd>
        {call.input_sha256 && <><dt>Input SHA-256</dt><dd><code title={call.input_sha256}>{short(call.input_sha256)}</code></dd></>}
        {summary?.request_sha256 && <><dt>Request SHA-256</dt><dd><code title={summary.request_sha256}>{short(summary.request_sha256)}</code></dd></>}
        {summary?.selection && <><dt>Selection</dt><dd>{summary.selection.shard} / {summary.selection.task}, trial {summary.selection.trial}, attempt {summary.selection.attempt}</dd></>}
        {summary?.policy && <><dt>Policy</dt><dd>{summary.policy}{summary.parent ? ` (${summary.parent.relationship} of ${short(summary.parent.execution_id)})` : ""}{summary.source ? ` (source ${short(summary.source.execution_id)})` : ""}</dd></>}
        {summary?.checkpoint_sha256 && <><dt>Checkpoint</dt><dd><code title={summary.checkpoint_sha256}>{short(summary.checkpoint_sha256)}</code></dd></>}
      </dl>}
      {call && translation.operation && <div className="lab-actions">
        <button className="lab-button" disabled={pending !== ""}
          onClick={() => void run("Dispatch", (signal) => dispatchCall(translation.operation!, call, signal), setOutcome)}>
          {pending === "Dispatch" ? "Dispatching..." : "Dispatch through public contract"}
        </button>
      </div>}
      <p>Queueing, placement and retries belong to the external scheduler. The adapter makes one call and never retries.</p>
    </div>}
    {outcome && <div role="status" aria-label="Adapter result">
      <h3>{statusLabels[outcome.status] ?? outcome.status} (exit {outcome.exitCode})</h3>
      <dl aria-label="Returned manifest">
        <dt>Public status</dt><dd>{outcome.publicStatus ?? (outcome.status === "observed" ? "lineage" : "None")}</dd>
        {outcome.outcome && <><dt>Outcome</dt><dd>{outcome.outcome}</dd></>}
        {outcome.engineCalls !== undefined && <><dt>Engine calls</dt><dd>{outcome.engineCalls}</dd></>}
        {outcome.attempts !== undefined && <><dt>Recorded attempts</dt><dd>{outcome.attempts}</dd></>}
        <dt>Execution</dt><dd><code title={outcome.executionId}>{short(outcome.executionId)}</code></dd>
        <dt>Manifest</dt><dd><code title={outcome.manifestDigest}>{outcome.manifestDigest ?? "None"}</code></dd>
        {outcome.checkpointSHA256 && <><dt>Finalized checkpoint</dt><dd><code title={outcome.checkpointSHA256}>{short(outcome.checkpointSHA256)}</code></dd></>}
        {outcome.logical && <><dt>Logical work</dt><dd><code title={outcome.logical.work}>{short(outcome.logical.work)}</code></dd></>}
        <dt>Provenance</dt><dd>{outcome.manifestDigest ? "Not configured (optional host hook)" : "Not applicable"}</dd>
      </dl>
      {outcome.diagnostics.length > 0 && <ul aria-label="Result diagnostics">{outcome.diagnostics.map((diagnostic) =>
        <li key={diagnostic.code}><strong>{diagnostic.code}</strong>: {diagnostic.message}</li>)}</ul>}
      <button className="lab-button" onClick={() => downloadText(outcome.json, "waza-public-result.json", "application/json")}>
        Download public result JSON
      </button>
    </div>}
  </>;
}

export default function AdapterWorkflow({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="External adapter dispatch">
    <div className="lab-panel-header"><div>
      <h2>External execution adapter</h2>
      <p>Translate an external scheduler's work descriptor into one exact public runner call, dispatch it, and read the
        returned status and manifest. Ambiguous or scheduler-specific fields are rejected, never guessed.</p>
    </div></div>
    {demo ? <p>The adapter workflow is unavailable in the synthetic demo. Use a running Waza server with an existing local immutable store.</p> : <AdapterDispatch />}
  </section>;
}
