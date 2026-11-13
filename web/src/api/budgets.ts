import Ajv2020 from "ajv/dist/2020";
import budgetSchema from "../../../schemas/execution-budget-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
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

const validateCapabilities = new Ajv2020({ strict: false }).compile<BudgetCapabilities>(budgetSchema);
const validateTerminal = new Ajv2020({ strict: false }).compile<RunnerTerminal>(terminalSchema);

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
  for (const row of budget.resources) {
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
  }
  switch (budget.state) {
    case "rejected_unsupported":
      return outcome === "unsupported_capability" && !anyUsed && !termination && budget.unsupported === "reject"
        ? null : "Budget state contradicts the terminal outcome.";
    case "not_started":
      return anyUsed || termination ? "Budget state contradicts the terminal outcome." : null;
    case "within_budget":
      return anyExceeded || termination ? "Budget state contradicts its resources." : null;
    case "exceeded":
      return !anyExceeded || termination ? "Budget state contradicts its resources." : null;
    case "terminated":
      return termination && terminationOutcome[termination.resource] === outcome ? null : "Budget state contradicts the terminal outcome.";
  }
}

// Reads a saved or exported terminal envelope. The browser checks the schema
// and the same budget consistency rules as the native reader; it does not
// re-derive identity or re-measure usage.
export function readTerminalBudget(text: string): RunnerTerminal & { budget: BudgetReport } {
  const value = parseStrictJSON(text);
  if (!validateTerminal(value)) throw new Error("Input is not a valid waza.runner-terminal 1.0 envelope.");
  if (!value.budget) throw new Error("Terminal envelope has no budget report; the request declared no budgets.");
  const problem = checkBudget(value.budget, value.outcome);
  if (problem) throw new Error(problem);
  if (value.budget.state === "rejected_unsupported" && (value.identity || value.engine_calls !== 0)) {
    throw new Error("Budget state contradicts the terminal outcome.");
  }
  return value as RunnerTerminal & { budget: BudgetReport };
}
