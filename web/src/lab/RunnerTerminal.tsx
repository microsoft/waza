import { useEffect, useRef, useState } from "react";
import { runRunnerUnit, type RunnerTerminal as Terminal } from "../api/runner";
import { downloadText } from "../lib/export";
import { BudgetReportView } from "./ExecutionBudgets";

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
      const value = await runRunnerUnit(request, controller.signal);
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
      <h3>Execute one selected unit</h3>
      <p>Runs the captured request once. The Copilot profile requires explicit paid consent in the request and disables all tools. Repeating runs executes again and may incur charges.</p>
      <p>Success describes only the selected attempt. Eval metrics are uncomputed, identity-bound metadata: no aggregate metric values, threshold decisions, or whole-evaluation success are reported.</p>
      <button className="lab-button" disabled={pending} onClick={() => void run()}>
        {pending ? "Running selected unit..." : "Run selected unit"}
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
            <dt>Observed execution</dt><dd>{result.observation ? `${result.observation.execution_mode} / ${result.observation.status}` : "Unavailable"}</dd>
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
          {result.budget && <BudgetReportView budget={result.budget} />}
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
