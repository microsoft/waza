import { useState } from "react";
import example from "../../../examples/runner/unit.json";
import { validateRunnerSelection, type RunnerSelectionReport, type WorkSelection } from "../api/runner";

function tuple(value: WorkSelection | undefined): string {
  return value ? `${value.shard} / ${value.task} / trial ${value.trial} / attempt ${value.attempt}` : "Not resolved";
}

export default function RunnerSelection({ demo }: { demo: boolean }) {
  const [request, setRequest] = useState(JSON.stringify(example, null, 2));
  const [result, setResult] = useState<RunnerSelectionReport | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  async function check() {
    setResult(null);
    setError("");
    setPending(true);
    try {
      setResult(await validateRunnerSelection(request));
    } catch (error) {
      setError(error instanceof Error ? error.message : "Selection validation could not be loaded.");
    } finally {
      setPending(false);
    }
  }

  return (
    <section className="lab-panel lab-runner-negotiation" aria-label="Exact runner selection">
      <div className="lab-panel-header">
        <div>
          <h2>Validate exact work selection</h2>
          <p>Pre-execution validation of one captured inline mock/text unit. No engine is launched here.</p>
        </div>
      </div>
      {demo ? (
        <p>Selection validation is unavailable in the synthetic demo.</p>
      ) : (
        <>
          <label htmlFor="runner-unit-request">Exact work request (JSON)</label>
          <textarea id="runner-unit-request" rows={14} spellCheck={false} value={request} disabled={pending}
            onChange={(event) => {
              setRequest(event.target.value);
              setResult(null);
              setError("");
            }}
          />
          <button className="lab-button" onClick={() => void check()} disabled={pending}>
            {pending ? "Validating selection..." : "Validate selection"}
          </button>
          {error && <p role="alert">{error} No previous selection validation is shown.</p>}
          {result && (
            <div role="status" aria-label="Selection validation result">
              <h3>{result.status === "ready" ? "Valid selection (not executed)" : "Selection rejected"}</h3>
              <dl>
                <dt>Requested work</dt><dd>{tuple(result.requested)}</dd>
                <dt>Resolved work</dt><dd>{tuple(result.resolved)}</dd>
                <dt>Pre-execution validation</dt><dd>{result.validation}</dd>
                <dt>Adjacent work selected</dt><dd>No</dd>
                <dt>Engine calls</dt><dd>{result.engine_calls}</dd>
              </dl>
              {[...result.compatibility.diagnostics, ...result.diagnostics].map((diagnostic, index) => (
                <p key={`${diagnostic.code}-${index}`}>{diagnostic.code}: {diagnostic.message}</p>
              ))}
            </div>
          )}
        </>
      )}
    </section>
  );
}
