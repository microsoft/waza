import type { CheckpointOperationResult } from "../api/checkpointResults";

export default function CheckpointResult({ result }: { result: CheckpointOperationResult }) {
  const terminal = result.submission?.terminal;
  const failed = result.status === "rejected" || result.status === "submission_error" || result.status === "checkpoint_error";
  return (
    <div role={failed ? "alert" : "status"} aria-label="Checkpoint operation result">
      <h3>{failed ? "Checkpoint operation did not complete" : "Checkpoint operation completed"}</h3>
      <p>This result describes the selected exact unit. Original terminal failure or cancellation is preserved, not relabeled as successful grading.</p>
      <dl>
        <dt>Operation status</dt><dd>{result.status}</dd>
        <dt>Exit code</dt><dd>{result.exit_code}</dd>
        <dt>Source boundary</dt><dd>{result.source?.boundary ?? "Unavailable"}</dd>
        <dt>Source disclosure</dt><dd>{result.source_withheld
          ? "Withheld - capture admission did not succeed" : result.source ? "Admitted original declaration" : "Unavailable"}</dd>
        <dt>Source created (UTC)</dt><dd>{result.source
          ? <time dateTime={result.source.created_at}>{result.source.created_at}</time>
          : "Unavailable"}</dd>
        <dt>Compatibility</dt><dd>{result.compatibility_result}</dd>
        <dt>Differing fields</dt><dd>{result.compatibility_differences.length
          ? result.compatibility_differences.join(", ") : "None reported"}</dd>
        <dt>Restored units</dt><dd>{result.restored_units ?? "Unavailable"}</dd>
        <dt>Remaining work</dt><dd>{result.remaining_work ?? "Unavailable"}</dd>
        <dt>New engine calls</dt><dd>{result.submission?.engine_calls ?? "Unavailable"}</dd>
        <dt>Original terminal outcome</dt><dd>{terminal?.outcome ?? "Unavailable"}</dd>
        <dt>Original terminal engine calls</dt><dd>{terminal?.engine_calls ?? "Unavailable"}</dd>
        <dt>Original native attempt</dt><dd>{terminal?.selection?.attempt ?? "Unavailable"}</dd>
        <dt>Before checkpoint reference</dt><dd><code>{result.before_checkpoint_sha256 ?? "Unavailable"}</code></dd>
        <dt>Finalized checkpoint reference</dt><dd><code>{result.finalized_checkpoint_sha256 ?? "Unavailable"}</code></dd>
      </dl>
      {failed && <p>References may locate partial or unverified residue. Preserve source records; a digest is not successful recovery.</p>}
      <h3>Operation diagnostics</h3>
      <ul>{result.diagnostics.map((item, index) => <li key={`${item.code}-${index}`}>{item.code}: {item.message}</li>)}</ul>
      {result.submission && (
        <>
          <h3>Submission diagnostics</h3>
          <ul>{result.submission.diagnostics.map((item, index) => <li key={`${item.code}-${index}`}>{item.code}: {item.message}</li>)}</ul>
        </>
      )}
    </div>
  );
}
