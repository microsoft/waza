import type { CheckpointOperationResult } from "../api/checkpointResults";

export default function CheckpointResult({ result }: { result: CheckpointOperationResult }) {
  const submission = result.submission;
  const terminal = submission?.terminal;
  const failed = result.status === "rejected" || result.status === "submission_error" ||
    result.status === "checkpoint_error" || result.status === "operation_error";
  return (
    <div role={failed ? "alert" : "status"} aria-label="Checkpoint operation result">
      <h3>{failed ? "Checkpoint operation did not complete" : "Checkpoint operation completed"}</h3>
      <p>This describes one selected exact unit. Original terminal failure or cancellation is preserved, not successful grading.</p>
      <dl>
        <dt>Operation status</dt><dd>{result.status}</dd>
        <dt>Exit code</dt><dd>{result.exit_code}</dd>
        {result.error_code && <><dt>Sanitized operation error</dt><dd>{result.error_code}</dd></>}
        <dt>Source boundary</dt><dd>{result.source?.boundary ?? "Unavailable"}</dd>
        <dt>Source disclosure</dt><dd>{result.source_withheld
          ? "Withheld - capture admission did not succeed" : result.source ? "Admitted original declaration" : "Unavailable"}</dd>
        <dt>Source created (UTC)</dt><dd>{result.source
          ? <time dateTime={result.source.created_at}>{result.source.created_at}</time> : "Unavailable"}</dd>
        <dt>Compatibility</dt><dd>{result.compatibility_result}</dd>
        <dt>Differing fields</dt><dd>{result.compatibility_differences.join(", ") || "None reported"}</dd>
        <dt>Restored units</dt><dd>{result.restored_units ?? "Unavailable"}</dd>
        <dt>Remaining work</dt><dd>{result.remaining_work ?? "Unavailable"}</dd>
        <dt>New engine calls</dt><dd>{submission?.engine_calls ?? "Unavailable"}</dd>
        <dt>Original terminal outcome</dt><dd>{terminal?.outcome ?? "Unavailable"}</dd>
        <dt>Original terminal engine calls</dt><dd>{terminal?.engine_calls ?? "Unavailable"}</dd>
        <dt>Original native attempt</dt><dd>{terminal?.selection?.attempt ?? "Unavailable"}</dd>
        <dt>Execution ID</dt><dd><code>{submission?.reservation?.execution_id ?? "Unavailable"}</code></dd>
        <dt>Retry parent</dt><dd><code>{submission?.reservation?.parent?.execution_id ?? "Unavailable"}</code></dd>
        <dt>Parent relationship</dt><dd>{submission?.reservation?.parent?.relationship ?? "Unavailable"}</dd>
        <dt>Decision policy</dt><dd>{submission?.decision?.policy ?? "Unavailable"}</dd>
        <dt>Reused original execution</dt><dd><code>{submission?.decision?.source?.execution_id ?? "Unavailable"}</code></dd>
        <dt>Before checkpoint reference</dt><dd><code>{result.before_checkpoint_sha256 ?? "Unavailable"}</code></dd>
        <dt>Finalized checkpoint reference</dt><dd><code>{result.finalized_checkpoint_sha256 ?? "Unavailable"}</code></dd>
      </dl>
      {failed && <p>References may locate partial or unverified residue. Preserve source records; a digest is not successful recovery.</p>}
      {result.diagnostics.length > 0 && <>
        <h3>Operation diagnostics</h3>
        <ul>{result.diagnostics.map((item, index) => <li key={`${item.code}-${index}`}>{item.code}: {item.message}</li>)}</ul>
      </>}
      {submission && submission.diagnostics.length > 0 && <>
        <h3>Submission diagnostics</h3>
        <ul>{submission.diagnostics.map((item, index) => <li key={`${item.code}-${index}`}>{item.code}: {item.message}</li>)}</ul>
      </>}
    </div>
  );
}
