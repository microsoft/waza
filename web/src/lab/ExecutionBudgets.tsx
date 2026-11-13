import { useEffect, useRef, useState } from "react";
import {
  loadBudgetCapabilities, readTerminalBudget,
  type BudgetCapabilities, type BudgetReport, type BudgetUsage,
} from "../api/budgets";
import type { RunnerTerminal } from "../api/runner";

const stateLabels: Record<BudgetReport["state"], string> = {
  rejected_unsupported: "Rejected before execution: a declared limit is unsupported on this host",
  not_started: "Not started",
  within_budget: "Within budget",
  exceeded: "Exceeded a measured (unenforced) limit",
  terminated: "Terminated by an enforced limit",
};

function amount(value: number | undefined): string {
  return value === undefined ? "Not measured" : value.toLocaleString("en-US");
}

function enforcement(row: BudgetUsage): string {
  if (row.limit === undefined) return row.enforcement === "enforced" ? "Enforceable (no limit set)" : "Unsupported (no limit set)";
  return row.enforcement === "enforced" ? "Enforced" : "Unsupported: limit not enforced";
}

export function BudgetReportView({ budget, terminal }: { budget: BudgetReport; terminal?: RunnerTerminal }) {
  return (
    <div aria-label="Execution budget report">
      <dl>
        <dt>Budget state</dt><dd>{stateLabels[budget.state]}</dd>
        <dt>Unsupported-limit policy</dt><dd>{budget.unsupported}</dd>
        <dt>Host platform</dt><dd>{budget.platform.os}/{budget.platform.arch}</dd>
        <dt>Termination</dt><dd>{budget.termination
          ? `${budget.termination.resource}: observed ${amount(budget.termination.observed)} beyond limit ${amount(budget.termination.limit)}`
          : "None"}</dd>
        {terminal && <>
          <dt>Terminal outcome</dt><dd>{terminal.outcome} (exit {terminal.exit_code})</dd>
          <dt>Run identity</dt><dd><code>{terminal.identity?.run ?? "Unavailable (not admitted)"}</code></dd>
          <dt>Attempt identity</dt><dd><code>{terminal.identity?.attempt ?? "Unavailable (not admitted)"}</code></dd>
          <dt>Selection</dt><dd>{terminal.selection
            ? `${terminal.selection.shard} / ${terminal.selection.task} / trial ${terminal.selection.trial} / attempt ${terminal.selection.attempt}`
            : "Unavailable (not admitted)"}</dd>
        </>}
      </dl>
      <div className="lab-table-scroll"><table className="lab-table" aria-label="Budget usage by resource">
        <thead>
          <tr><th>Resource</th><th>Limit</th><th>Used</th><th>Remaining</th><th>Exceeded</th><th>Enforcement</th><th>Measurement</th><th>Detail</th></tr>
        </thead>
        <tbody>
          {budget.resources.map((row) => (
            <tr key={row.resource}>
              <th scope="row">{row.resource}</th>
              <td>{row.limit === undefined ? "No limit" : amount(row.limit)}</td>
              <td>{amount(row.used)}</td>
              <td>{row.remaining === undefined ? "-" : amount(row.remaining)}</td>
              <td>{row.exceeded === undefined ? "-" : row.exceeded ? "Yes" : "No"}</td>
              <td>{enforcement(row)}</td>
              <td>{row.measurement === "measured" ? `Measured (${row.scope})` : "Unsupported"}</td>
              <td>{row.detail}</td>
            </tr>
          ))}
        </tbody>
      </table></div>
    </div>
  );
}

function HostMatrix() {
  const [matrix, setMatrix] = useState<BudgetCapabilities | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const active = useRef<AbortController | null>(null);
  useEffect(() => () => active.current?.abort(), []);

  async function load() {
    active.current?.abort();
    const controller = new AbortController();
    active.current = controller;
    setMatrix(null);
    setError("");
    setPending(true);
    try {
      const value = await loadBudgetCapabilities(controller.signal);
      if (!controller.signal.aborted) setMatrix(value);
    } catch (error) {
      if (!controller.signal.aborted) setError(error instanceof Error ? error.message : "Budget capabilities unavailable.");
    } finally {
      if (!controller.signal.aborted) setPending(false);
    }
  }

  return (
    <div>
      <button className="lab-button" disabled={pending} onClick={() => void load()}>
        {pending ? "Loading host budgets..." : "Load host budget capabilities"}
      </button>
      {error && <p role="alert">{error}</p>}
      {matrix && (
        <div className="lab-table-scroll"><table className="lab-table" aria-label={`Budget capabilities for ${matrix.platform.os}/${matrix.platform.arch}`}>
          <thead><tr><th>Resource</th><th>Enforcement</th><th>Measurement</th><th>Detail</th></tr></thead>
          <tbody>
            {matrix.resources.map((row) => (
              <tr key={row.resource}>
                <th scope="row">{row.resource}</th>
                <td>{row.enforcement === "enforced" ? "Enforced" : "Unsupported"}</td>
                <td>{row.measurement === "measured" ? `Measured (${row.scope})` : "Unsupported"}</td>
                <td>{row.detail}</td>
              </tr>
            ))}
          </tbody>
        </table></div>
      )}
    </div>
  );
}

function TerminalBudgetReader() {
  const [text, setText] = useState("");
  const [terminal, setTerminal] = useState<ReturnType<typeof readTerminalBudget> | null>(null);
  const [error, setError] = useState("");

  function read() {
    setTerminal(null);
    setError("");
    try {
      setTerminal(readTerminalBudget(text));
    } catch (error) {
      setError(error instanceof Error ? error.message : "Terminal envelope could not be read.");
    }
  }

  return (
    <div>
      <label htmlFor="budget-terminal-json">Runner terminal JSON</label>
      <textarea id="budget-terminal-json" rows={6} spellCheck={false} value={text}
        onChange={(event) => { setText(event.target.value); setTerminal(null); setError(""); }} />
      <button className="lab-button" onClick={read}>Read budget report</button>
      {error && <p role="alert">{error}</p>}
      {terminal && <BudgetReportView budget={terminal.budget} terminal={terminal} />}
    </div>
  );
}

export default function ExecutionBudgets({ demo }: { demo: boolean }) {
  return <section className="lab-panel lab-runner-negotiation" aria-label="Execution budgets">
    <div className="lab-panel-header"><div>
      <h2>Execution budgets</h2>
      <p>See which wall-time, model-turn, tool-call, CPU, memory, disk and process limits this host enforces or measures,
        then read the budget report from a saved runner terminal envelope. Unsupported limits are rejected before execution
        unless the request sets <code>budgets.unsupported</code> to <code>report</code>. Read-only; nothing is executed.</p>
    </div></div>
    {demo
      ? <p>Budget capabilities are unavailable in the synthetic demo. Use a running Waza server.</p>
      : <HostMatrix />}
    <TerminalBudgetReader />
  </section>;
}
