import { useRef, useState } from "react";
import { parseCheckpointOperationResult, type CheckpointOperationResult } from "../api/checkpoint-result";

export default function CheckpointResult() {
  const [wire, setWire] = useState("");
  const [result, setResult] = useState<CheckpointOperationResult | null>(null);
  const [error, setError] = useState("");
  const parseGeneration = useRef(0);
  const unverified = result?.schema_version === "1.0" && result.submission?.report !== undefined;

  async function parse() {
    const generation = ++parseGeneration.current;
    const input = wire;
    setResult(null);
    setError("");
    try {
      const parsed = await parseCheckpointOperationResult(input);
      if (generation === parseGeneration.current) setResult(parsed);
    } catch (cause) {
      if (generation === parseGeneration.current) {
        setError(cause instanceof Error ? cause.message : "Checkpoint result could not be parsed.");
      }
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
          parseGeneration.current++;
          setWire(event.target.value);
          setResult(null);
          setError("");
        }}
      />
      <button className="lab-button" onClick={parse}>Parse checkpoint result</button>
      {error && <p role="alert">{error}</p>}
      {result && (
        <div role="status" aria-label="Checkpoint operation result">
          <h3>{unverified ? "Checkpoint result: UNVERIFIED CLAIMS" : result.status === "operation_error" ? "Operation error" : `Checkpoint result: ${result.status}`}</h3>
          {unverified && <p>Native canonical-report checks are not performed in-browser. Statuses and exit codes below are unverified claims, not verified outcomes.</p>}
          <dl>
            <dt>Result schema</dt><dd>{result.schema_version}</dd>
            <dt>{unverified ? "Operation status (UNVERIFIED CLAIM)" : "Operation status"}</dt>
            <dd>{unverified ? `UNVERIFIED CLAIM: ${result.status}` : result.status}</dd>
            <dt>{unverified ? "Operation exit code (UNVERIFIED CLAIM)" : "Operation exit code"}</dt>
            <dd>{unverified ? `UNVERIFIED CLAIM: ${result.exit_code}` : result.exit_code}</dd>
            {result.error_code && <><dt>Sanitized error code</dt><dd>{result.error_code}</dd></>}
            {result.submission && <>
              <dt>{unverified ? "Submission status (UNVERIFIED CLAIM)" : "Underlying submission status (not overall operation status)"}</dt>
              <dd>{unverified ? "UNVERIFIED CLAIM: " : ""}{result.submission.status} (exit {result.submission.exit_code})</dd>
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
