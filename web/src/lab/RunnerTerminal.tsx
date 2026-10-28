import { useEffect, useRef, useState } from "react";
import { runRunnerUnit, type RunnerTerminal as Terminal } from "../api/runner";
import { downloadText } from "../lib/export";

export default function RunnerTerminal({ request }: { request: string }) {
  const [result, setResult] = useState<Terminal | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  async function run() {
    setResult(null);
    setError("");
    setPending(true);
    const controller = new AbortController();
    active.current = controller;
    try {
      const value = await runRunnerUnit(request, AbortSignal.any([controller.signal, AbortSignal.timeout(65_000)]));
      if (!controller.signal.aborted) setResult(value);
    } catch (error) {
      if (!controller.signal.aborted) {
        setError(error instanceof Error ? error.message : "Terminal response unavailable.");
      }
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <section aria-label="Runner terminal outcome">
      <h3>Execute one mock unit</h3>
      <p>Runs the captured request once, without native or paid execution. Repeating runs executes again.</p>
      <button className="lab-button" disabled={pending} onClick={() => void run()}>
        {pending ? "Running mock unit..." : "Run mock unit"}
      </button>
      {error && <p role="alert">{error} No previous terminal success is shown; execution may have occurred without a received response.</p>}
      {result && (
        <div role="status" aria-label="Terminal result">
          <dl>
            <dt>Terminal outcome</dt><dd>{result.outcome}</dd>
            <dt>Exit category</dt><dd>{result.exit_code}</dd>
            <dt>Failure boundary</dt><dd>{result.failure_boundary}</dd>
            <dt>Input validation</dt><dd>{result.validation}</dd>
            <dt>Envelope validation</dt><dd>{result.identity
              ? "Schema checked; input identity bound. Producer checks observed grader bindings."
              : "Schema checked; no execution identity claimed."}</dd>
            <dt>Observed execution</dt><dd>{result.observation ? `mock / ${result.observation.status}` : "Unavailable"}</dd>
            <dt>Engine calls</dt><dd>{result.engine_calls}</dd>
            <dt>Billing and scores</dt><dd>Unavailable in this terminal summary; no inferred zeros.</dd>
            <dt>Attempt identity</dt><dd><code>{result.identity?.attempt ?? "Unavailable (not admitted)"}</code></dd>
          </dl>
          {result.observation && (
            <ul aria-label="Observed grader verdicts">
              {result.observation.validations.map((verdict) => (
                <li key={verdict.identifier}>{verdict.identifier}: {verdict.passed ? "passed" : "failed"}</li>
              ))}
            </ul>
          )}
          {result.observation?.error_msg && <p>{result.observation.error_msg}</p>}
          <ol aria-label="Terminal diagnostics">
            {result.diagnostics.map((diagnostic, index) => (
              <li id={`terminal-diagnostic-${index}`} key={`${diagnostic.code}-${index}`}>
                <a href={`#terminal-diagnostic-${index}`} onClick={(event) => {
                  event.preventDefault();
                  document.getElementById(`terminal-diagnostic-${index}`)?.scrollIntoView();
                }}>{diagnostic.code}</a>: {diagnostic.message}
              </li>
            ))}
          </ol>
          <button className="lab-button" onClick={() => downloadText(JSON.stringify(result, null, 2), "runner-terminal.json", "application/json")}>
            Export terminal JSON
          </button>
        </div>
      )}
    </section>
  );
}
