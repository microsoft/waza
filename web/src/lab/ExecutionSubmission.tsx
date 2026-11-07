import { useEffect, useRef, useState } from "react";
import unit from "../../../examples/runner/unit.json";
import { submitExecution, type SubmissionResponse } from "../api/submissions";
import { downloadText } from "../lib/export";

const example = JSON.stringify({
  kind: "waza.execution-submission", schema_version: "1.0", request: unit,
}, null, 2);

function Submission() {
  const [request, setRequest] = useState(example);
  const [response, setResponse] = useState<SubmissionResponse | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  async function submit() {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setResponse(null);
    setError("");
    setPending(true);
    try {
      const value = await submitExecution(request, controller.signal);
      if (!controller.signal.aborted) setResponse(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(
        error instanceof Error ? error.message : "Submission could not be loaded.",
      );
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  const result = response?.result;
  return (
    <>
      <label htmlFor="execution-submission-request">Captured execution submission (JSON)</label>
      <textarea id="execution-submission-request" rows={14} spellCheck={false} value={request}
        onChange={(event) => {
          active.current?.abort();
          setResponse(null); setError(""); setPending(false); setRequest(event.target.value);
        }} />
      <button className="lab-button" disabled={pending} onClick={() => void submit()}>
        {pending ? "Submission running..." : "Submit captured execution policy"}
      </button>
      {error && <p role="alert">{error} No previous submission result is shown.</p>}
      {response && result && <div role={result.status === "submission_error" || result.status === "rejected" ? "alert" : "status"}
        aria-label="Owned execution submission result">
        <h3>Execution result and declared lineage</h3>
        <p>Finalized evidence does not mean successful grading. Source ordinals and failed/canceled
          originals remain unchanged; missing billing is unavailable, not zero.</p>
        <dl>
          <dt>Operation status</dt><dd>{result.status}</dd>
          <dt>Exit code</dt><dd>{result.exit_code}</dd>
          <dt>Logical run</dt><dd><code>{result.reservation?.logical.run ?? "Unavailable"}</code></dd>
          <dt>Logical work</dt><dd><code>{result.reservation?.logical.work ?? "Unavailable"}</code></dd>
          <dt>Original execution ID</dt><dd><code>{result.reservation?.execution_id ?? "Unavailable"}</code></dd>
          <dt>Current caller attempt identity</dt><dd><code>{result.caller?.attempt ?? "Unavailable"}</code></dd>
          <dt>Original native attempt ordinal</dt><dd>{result.terminal?.selection?.attempt ?? "Unavailable"}</dd>
          <dt>New engine calls</dt><dd>{result.engine_calls}</dd>
          <dt>Original terminal outcome</dt><dd>{result.terminal?.outcome ?? "Unavailable"}</dd>
          <dt>Original terminal engine calls</dt><dd>{result.terminal?.engine_calls ?? "Unavailable"}</dd>
          <dt>Parent execution</dt><dd><code>{result.reservation?.parent?.execution_id ?? "No admitted parent"}</code></dd>
          <dt>Parent relationship</dt><dd>{result.reservation?.parent?.relationship ?? "No admitted parent"}</dd>
          <dt>Original manifest digest</dt><dd><code>{result.completion?.manifest_digest ?? "Unavailable"}</code></dd>
        </dl>
        <p>Parent links are append-only declarations. Independent roots and sibling branches are incomparable;
          no global order or exactly-once guarantee is inferred.</p>
        {result.diagnostics.map((item, index) => <p key={`${item.code}-${index}`}>{item.code}: {item.message}</p>)}
        <button className="lab-button" onClick={() => downloadText(
          response.json, "waza-execution-submission.json", "application/json",
        )}>Download exact submission JSON</button>
      </div>}
    </>
  );
}

export default function ExecutionSubmission({ demo }: { demo: boolean }) {
  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Owned execution and lineage">
      <div className="lab-panel-header"><div>
        <h2>Submit distinct work or reuse its original</h2>
        <p>Omitted policy creates a distinct execution. Explicit reuse requires the original execution ID
          and manifest digest in the same private store; a digest alone is not approved evidence.</p>
        <p>Native requests require explicit paid-execution consent and may incur charges.
          This action never migrates historical results or exposes raw reports.</p>
      </div></div>
      {demo
        ? <p>Execution submission is unavailable in the synthetic demo. Use a running Waza server and private immutable store.</p>
        : <Submission />}
    </section>
  );
}
