import type { CheckpointAvailability } from "../api/checkpointAvailability";

const actions = {
  fresh_retry_child: "Resume creates a fresh retry child and executes the unit again.",
  reuse_original_terminal: "Resume reuses the original terminal with no new engine calls.",
};

export default function CheckpointAvailabilityResult({ result }: { result: CheckpointAvailability }) {
  return (
    <div role="status" aria-label="Checkpoint availability result">
      <h3>Resume availability: {result.status}</h3>
      <dl>
        {result.source && <>
          <dt>Boundary</dt><dd>{result.source.boundary}</dd>
          <dt>Created</dt><dd>{result.source.created_at}</dd>
          <dt>Original execution</dt><dd>{result.source.execution_id}</dd>
        </>}
        <dt>Compatibility</dt><dd>{result.compatibility_result}</dd>
        {result.compatibility_differences.length > 0 && <>
          <dt>Differences</dt><dd>{result.compatibility_differences.join(", ")}</dd>
        </>}
        {result.resume_action && <><dt>Resume action</dt><dd>{actions[result.resume_action]}</dd></>}
        {result.restored_units !== null && <><dt>Restored units</dt><dd>{result.restored_units}</dd></>}
        {result.remaining_work !== null && <><dt>Remaining work</dt><dd>{result.remaining_work}</dd></>}
      </dl>
      {result.diagnostics.length > 0 && (
        <ul aria-label="Checkpoint availability diagnostics">
          {result.diagnostics.map((diagnostic) => (
            <li key={diagnostic.code}><code>{diagnostic.code}</code>: {diagnostic.message}</li>
          ))}
        </ul>
      )}
      <p>Nothing was executed, reserved or repaired by this check. Availability can change before resume.</p>
    </div>
  );
}
