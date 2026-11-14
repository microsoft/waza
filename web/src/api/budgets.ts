import Ajv2020 from "ajv/dist/2020";
import budgetSchema from "../../../schemas/execution-budget-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { lazyValidator } from "../lib/lazyValidator";
import { parseStrictJSON } from "../lib/strictJson";
import type { RunnerTerminal } from "./runner";

export type BudgetResource =
  | "wall_time_ms" | "model_turns" | "tool_calls" | "cpu_time_ms" | "memory_bytes" | "disk_bytes" | "process_count";

export interface BudgetCapability {
  resource: BudgetResource;
  enforcement: "enforced" | "unsupported";
  measurement: "measured" | "unsupported";
  scope?: "attempt" | "runner_process";
  detail: string;
}

export interface BudgetCapabilities {
  kind: "waza.execution-budget-capabilities";
  schema_version: "1.0";
  platform: { os: string; arch: string };
  resources: BudgetCapability[];
}

export interface BudgetUsage extends BudgetCapability {
  limit?: number;
  used?: number;
  remaining?: number;
  exceeded?: boolean;
}

export interface BudgetReport {
  schema_version: "1.0";
  platform: { os: string; arch: string };
  unsupported: "reject" | "report";
  state: "rejected_unsupported" | "not_started" | "within_budget" | "exceeded" | "terminated";
  resources: BudgetUsage[];
  termination?: { resource: "wall_time_ms" | "model_turns" | "tool_calls"; limit: number; observed: number };
}

const validateCapabilities = lazyValidator(() => new Ajv2020({ strict: false }).compile<BudgetCapabilities>(budgetSchema));
const validateTerminal = lazyValidator(() => new Ajv2020({ strict: false }).compile<RunnerTerminal>(terminalSchema));

// The schema allows any row order; the server emits exactly one row per resource.
function uniqueResources(rows: { resource: string }[]): boolean {
  return new Set(rows.map((row) => row.resource)).size === rows.length && rows.length === 7;
}

export async function loadBudgetCapabilities(signal?: AbortSignal): Promise<BudgetCapabilities> {
  const timeout = AbortSignal.timeout(10_000);
  const response = await fetch("/api/v1/runner/budgets", { signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
  if (!response.ok) throw new Error(`Runner API returned HTTP ${response.status}.`);
  const value = parseStrictJSON(await response.text());
  if (!validateCapabilities(value) || !uniqueResources(value.resources)) {
    throw new Error("Runner API returned an invalid budget capability matrix.");
  }
  return value;
}

const terminationOutcome = { wall_time_ms: "timeout", model_turns: "cancelled", tool_calls: "cancelled" } as const;

// Mirrors the native executionbudget.CheckTerminal cross-field rules.
export function checkBudget(budget: BudgetReport, outcome: string): string | null {
  if (!uniqueResources(budget.resources)) return "Budget report must list each resource exactly once.";
  const termination = budget.termination;
  let anyUsed = false;
  let anyExceeded = false;
  let missingUsage = false;
  for (const row of budget.resources) {
    missingUsage ||= row.measurement === "measured" && row.used === undefined;
    anyUsed ||= row.used !== undefined;
    anyExceeded ||= row.exceeded === true;
    const derived = row.remaining !== undefined || row.exceeded !== undefined;
    if (row.used === undefined || row.limit === undefined) {
      if (derived) return `Budget ${row.resource} reports remaining/exceeded without a limit and measured usage.`;
      if (row.used !== undefined && row.measurement !== "measured") return `Budget ${row.resource} reports usage for an unmeasured resource.`;
      continue;
    }
    if (row.measurement !== "measured") return `Budget ${row.resource} reports usage for an unmeasured resource.`;
    if (row.remaining !== Math.max(row.limit - row.used, 0)) return `Budget ${row.resource} remaining does not equal limit minus used.`;
    if (row.exceeded !== (row.used > row.limit || termination?.resource === row.resource)) {
      return `Budget ${row.resource} exceeded flag contradicts its usage.`;
    }
  }
  if (termination) {
    const row = budget.resources.find((candidate) => candidate.resource === termination.resource);
    if (row?.enforcement !== "enforced" || row.limit !== termination.limit) return "Budget termination does not match an enforced limit.";
    const over = termination.resource === "wall_time_ms" ? termination.observed >= termination.limit : termination.observed > termination.limit;
    if (!over) return "Budget termination does not exceed its limit.";
    // Counted usage includes the triggering start; wall time may round below a deadline recorded as observed == limit.
    const used = row.used;
    if (used === undefined) return "Budget termination has no measured usage.";
    const consistent = termination.resource === "wall_time_ms"
      ? used === termination.observed || (used < termination.limit && termination.observed === termination.limit)
      : used >= termination.observed;
    if (!consistent) return "Budget termination contradicts its measured usage.";
  }
  const started = budget.state === "within_budget" || budget.state === "exceeded" || budget.state === "terminated";
  if (started && missingUsage) return "Budget report omits usage for a measured resource.";
  switch (budget.state) {
    case "rejected_unsupported":
      return outcome === "unsupported_capability" && !anyUsed && !termination && budget.unsupported === "reject"
        ? null : "Budget state contradicts the terminal outcome.";
    case "not_started":
      return anyUsed || termination || outcome === "success" || outcome === "evaluation_failure"
        ? "Budget state contradicts the terminal outcome." : null;
    case "within_budget":
      return anyExceeded || termination ? "Budget state contradicts its resources." : null;
    case "exceeded":
      return !anyExceeded || termination ? "Budget state contradicts its resources." : null;
    case "terminated":
      return termination && terminationOutcome[termination.resource] === outcome ? null : "Budget state contradicts the terminal outcome.";
  }
}

// Checks a terminal's budget report against its outcome and admission, for
// both live and saved envelopes. Returns null when there is no budget.
export function terminalBudgetProblem(value: RunnerTerminal): string | null {
  if (!value.budget) return null;
  const problem = checkBudget(value.budget, value.outcome);
  if (problem) return problem;
  if (value.budget.state === "rejected_unsupported" && (value.identity || value.engine_calls !== 0)) {
    return "Budget state contradicts the terminal outcome.";
  }
  if (value.budget.state === "not_started" && (value.observation || value.engine_calls !== 0)) {
    return "Budget reports an unstarted attempt but the terminal reports execution.";
  }
  return null;
}

// Budgets are excluded from logical identity, so identity checks alone cannot
// tell whether a terminal answers this request's limits. A request that fails
// before budget planning (for example a schema rejection) is never admitted
// and carries no report.
export function requestBudgetProblem(request: string, value: RunnerTerminal): string | null {
  let captured: unknown;
  try {
    captured = parseStrictJSON(request);
  } catch {
    captured = undefined;
  }
  const declared = typeof captured === "object" && captured !== null && "budgets" in captured
    ? (captured as { budgets: unknown }).budgets : undefined;
  if (declared === undefined) return value.budget ? "The terminal reports budgets the request did not declare." : null;
  if (!value.budget) return value.identity ? "The terminal is missing the requested budget report." : null;
  const budgets = declared as { limits?: Record<string, unknown>; unsupported?: unknown };
  if (value.budget.unsupported !== (budgets.unsupported ?? "reject")) {
    return "The terminal budget policy does not match the request.";
  }
  const limits = budgets.limits ?? {};
  if (Object.keys(limits).some((key) => !value.budget!.resources.some((row) => row.resource === key)) ||
      value.budget.resources.some((row) => row.limit !== limits[row.resource])) {
    return "The terminal budget limits do not match the request.";
  }
  return null;
}

// Reads a saved or exported terminal envelope. The browser checks the schema
// and the same budget consistency rules as the native reader; it does not
// re-derive identity or re-measure usage.
export function readTerminalBudget(text: string): RunnerTerminal & { budget: BudgetReport } {
  const value = parseStrictJSON(text);
  if (!validateTerminal(value)) throw new Error("Input is not a valid waza.runner-terminal 1.0 envelope.");
  if (!value.budget) throw new Error("Terminal envelope has no budget report; the request declared no budgets.");
  const problem = terminalBudgetProblem(value);
  if (problem) throw new Error(problem);
  return value as RunnerTerminal & { budget: BudgetReport };
}
