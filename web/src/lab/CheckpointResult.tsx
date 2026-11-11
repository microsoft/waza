import { useState } from "react";
import { parseCheckpointOperationResult, type CheckpointOperationResult } from "../api/checkpoint-result";

export default function CheckpointResult() {
  const [wire, setWire] = useState("");
  const [result, setResult] = useState<CheckpointOperationResult | null>(null);
  const [error, setError] = useState("");

  async function parse() {
    setResult(null);
    setError("");
    try {
      setResult(await parseCheckpointOperationResult(wire));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Checkpoint result could not be parsed.");
    }
  }

  return (
    <section className="lab-panel" aria-label="Checkpoint result reader">
      <div className="lab-panel-header">
        <div>
          <h2>Read checkpoint operation result</h2>
          <p>Validate captured CLI or HTTP output against the supported result schemas. An execution status is not the overall operation status.</p>
        </div>
      </div>
      <label htmlFor="checkpoint-result-json">Checkpoint result JSON</label>
      <textarea id="checkpoint-result-json" rows={8} spellCheck={false} value={wire}
        onChange={(event) => {
          setWire(event.target.value);
          setResult(null);
          setError("");
        }}
      />
      <button className="lab-button" onClick={parse}>Parse checkpoint result</button>
      {error && <p role="alert">{error}</p>}
      {result && (
        <div role="status" aria-label="Checkpoint operation result">
          <h3>{result.status === "operation_error" ? "Operation error" : `Checkpoint result: ${result.status}`}</h3>
          <dl>
            <dt>Result schema</dt><dd>{result.schema_version}</dd>
            <dt>Operation status</dt><dd>{result.status}</dd>
            <dt>Operation exit code</dt><dd>{result.exit_code}</dd>
            {result.error_code && <><dt>Sanitized error code</dt><dd>{result.error_code}</dd></>}
            {result.submission && <>
              <dt>Underlying submission status (not overall operation status)</dt>
              <dd>{result.submission.status} (exit {result.submission.exit_code})</dd>
            </>}
            {result.source && <><dt>Declared source (not independently verified)</dt><dd>{result.source.boundary}: {result.source.execution_id}</dd></>}
            {result.before_checkpoint_sha256 && <><dt>Before checkpoint reference</dt><dd><code>{result.before_checkpoint_sha256}</code></dd></>}
            {result.finalized_checkpoint_sha256 && <><dt>Finalized checkpoint reference</dt><dd><code>{result.finalized_checkpoint_sha256}</code></dd></>}
          </dl>
        </div>
      )}
    </section>
  );
}
