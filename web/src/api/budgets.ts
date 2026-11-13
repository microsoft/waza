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

// Reads a saved or exported terminal envelope. The browser checks the schema
// and internal consistency only; it does not re-derive identity or usage.
export function readTerminalBudget(text: string): RunnerTerminal & { budget: BudgetReport } {
  const value = parseStrictJSON(text);
  if (!validateTerminal(value)) throw new Error("Input is not a valid waza.runner-terminal 1.0 envelope.");
  if (!value.budget) throw new Error("Terminal envelope has no budget report; the request declared no budgets.");
  const budget = value.budget;
  if (!uniqueResources(budget.resources)) throw new Error("Budget report must list each resource exactly once.");
  if ((budget.state === "rejected_unsupported") !== (value.outcome === "unsupported_capability" && !value.identity)) {
    throw new Error("Budget state contradicts the terminal outcome.");
  }
  return value as RunnerTerminal & { budget: BudgetReport };
}
