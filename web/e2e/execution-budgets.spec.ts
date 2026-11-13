import { readFileSync } from "node:fs";
import { expect, test, type Page } from "@playwright/test";
import fixture from "./fixtures/execution-budgets.json" with { type: "json" };

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution budgets" });
}

async function readTerminal(page: Page, terminal: unknown) {
  await panel(page).getByRole("textbox", { name: "Runner terminal JSON" }).fill(JSON.stringify(terminal));
  await panel(page).getByRole("button", { name: "Read budget report" }).click();
}

function row(page: Page, resource: string) {
  return panel(page).getByRole("table", { name: "Budget usage by resource" }).getByRole("row", { name: new RegExp(`^${resource}`) });
}

// The terminated fixture stops on model_turns (limit 0, observed 1).
function withTurnUsage(used: number | undefined) {
  const resources = fixture.terminated.budget.resources.map((row) => {
    if (row.resource !== "model_turns") return row;
    const copy: Partial<typeof row> = { ...row };
    delete copy.used;
    delete copy.remaining;
    delete copy.exceeded;
    return used === undefined ? copy : { ...copy, used, remaining: 0, exceeded: true };
  });
  return { ...fixture.terminated, budget: { ...fixture.terminated.budget, resources } };
}

// The terminated report with its model turn kept within the limit instead.
function withinBudget() {
  const resources = fixture.terminated.budget.resources.map((row) =>
    row.resource === "model_turns" ? { ...row, used: 0, remaining: 0, exceeded: false } : row);
  const budget: Partial<typeof fixture.terminated.budget> = { ...fixture.terminated.budget, state: "within_budget", resources };
  delete budget.termination;
  return budget;
}

const terminatedLimits = { wall_time_ms: 60_000, model_turns: 0, tool_calls: 10 };

async function runLive(page: Page, request: unknown, terminal: unknown) {
  const ready = {
    ...fixture.rejected_validation, status: "ready", validation: "valid", diagnostics: [],
    resolved: fixture.terminated.selection, identity: fixture.terminated.identity,
  };
  await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: ready }));
  await page.route("**/api/v1/runner/run", (route) => route.fulfill({
    json: terminal, headers: { "X-Waza-Exit-Code": String((terminal as { exit_code: number }).exit_code) },
  }));
  await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(request));
  const outcome = page.getByRole("region", { name: "Runner terminal outcome" });
  await outcome.getByRole("button", { name: "Run selected unit", exact: true }).click();
  return outcome;
}

test.describe("Lab execution budgets", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows the host matrix with enforced and unsupported resources", async ({ page }) => {
    await page.route("**/api/v1/runner/budgets", (route) => route.fulfill({ json: fixture.capabilities }));
    await panel(page).getByRole("button", { name: "Load host budget capabilities" }).click();
    const table = panel(page).getByRole("table", { name: /^Budget capabilities for / });
    await expect(table.getByRole("row")).toHaveCount(8);
    await expect(table.getByRole("row", { name: /^wall_time_ms Enforced Measured \(attempt\)/ })).toBeVisible();
    await expect(table.getByRole("row", { name: /^disk_bytes Unsupported Unsupported/ })).toBeVisible();
    await expect(table.getByRole("row", { name: /^process_count Unsupported Unsupported/ })).toBeVisible();
  });

  test("rejects an invalid capability matrix", async ({ page }) => {
    const resources = fixture.capabilities.resources.slice(0, 6);
    await page.route("**/api/v1/runner/budgets", (route) => route.fulfill({ json: { ...fixture.capabilities, resources } }));
    await panel(page).getByRole("button", { name: "Load host budget capabilities" }).click();
    await expect(panel(page).getByRole("alert")).toHaveText("Runner API returned an invalid budget capability matrix.");
  });

  test("reports configured limits, measured usage and enforced termination", async ({ page }) => {
    await readTerminal(page, fixture.terminated);
    const report = panel(page).getByLabel("Execution budget report");
    await expect(report).toContainText("Terminated by an enforced limit");
    await expect(report).toContainText("model_turns: observed 1 beyond limit 0");
    await expect(report).toContainText("cancelled (exit 3)");
    await expect(report).toContainText(fixture.terminated.identity!.attempt);
    await expect(row(page, "model_turns")).toContainText("Yes");
    await expect(row(page, "model_turns")).toContainText("Enforced");
    await expect(row(page, "wall_time_ms")).toContainText("60,000");
    await expect(row(page, "disk_bytes")).toContainText("Unsupported (no limit set)");
  });

  test("shows unsupported limits rejected before execution", async ({ page }) => {
    await readTerminal(page, fixture.rejected);
    const report = panel(page).getByLabel("Execution budget report");
    await expect(report).toContainText("Rejected before execution");
    await expect(report).toContainText("unsupported_capability (exit 5)");
    await expect(report).toContainText("Unavailable (not admitted)");
    await expect(row(page, "disk_bytes")).toContainText("Unsupported: limit not enforced");
    await expect(row(page, "model_turns")).toContainText("Not measured");
  });

  test("refuses envelopes without a budget or with a contradictory state", async ({ page }) => {
    const plain: Partial<typeof fixture.terminated> = structuredClone(fixture.terminated);
    delete plain.budget;
    await readTerminal(page, plain);
    await expect(panel(page).getByRole("alert")).toContainText("has no budget report");
    await readTerminal(page, { ...fixture.terminated, outcome: "timeout", exit_code: 4 });
    await expect(panel(page).getByRole("alert")).toHaveText("Budget state contradicts the terminal outcome.");
    const resources = fixture.terminated.budget.resources.map((row) =>
      row.resource === "wall_time_ms" ? { ...row, remaining: 1 } : row);
    await readTerminal(page, { ...fixture.terminated, budget: { ...fixture.terminated.budget, resources } });
    await expect(panel(page).getByRole("alert")).toHaveText("Budget wall_time_ms remaining does not equal limit minus used.");
    await readTerminal(page, withTurnUsage(undefined));
    await expect(panel(page).getByRole("alert")).toHaveText("Budget termination has no measured usage.");
    await readTerminal(page, withTurnUsage(0));
    await expect(panel(page).getByRole("alert")).toHaveText("Budget termination contradicts its measured usage.");
  });

  test("refuses a live terminated report that understates the triggering usage", async ({ page }) => {
    const ready = {
      ...fixture.rejected_validation, status: "ready", validation: "valid", diagnostics: [],
      resolved: fixture.terminated.selection, identity: fixture.terminated.identity,
    };
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: ready }));
    await page.route("**/api/v1/runner/run", (route) =>
      route.fulfill({ json: withTurnUsage(0), headers: { "X-Waza-Exit-Code": "3" } }));
    await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(fixture.rejected_request));
    const outcome = page.getByRole("region", { name: "Runner terminal outcome" });
    await outcome.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(outcome.getByRole("alert")).toContainText(
      "Runner API returned an inconsistent budget report: Budget termination contradicts its measured usage.");
    await expect(outcome.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
  });

  test("binds a live budget report to the submitted limits", async ({ page }) => {
    const request = { ...fixture.rejected_request, budgets: { limits: terminatedLimits } };
    const accepted = await runLive(page, request, fixture.terminated);
    await expect(accepted.getByRole("status", { name: "Terminal result" })).toBeVisible();

    const plain: Partial<typeof fixture.terminated> = structuredClone(fixture.terminated);
    delete plain.budget;
    for (const [terminal, sent, message] of [
      [plain, request, "The terminal is missing the requested budget report."],
      [fixture.terminated, fixture.rejected_request, "The terminal budget limits do not match the request."],
      [fixture.terminated, { ...request, budgets: { ...request.budgets, unsupported: "report" } },
        "The terminal budget policy does not match the request."],
      [fixture.terminated, { ...fixture.rejected_request, budgets: undefined },
        "The terminal reports budgets the request did not declare."],
    ] as const) {
      await page.unrouteAll();
      await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
      const outcome = await runLive(page, sent, terminal);
      await expect(outcome.getByRole("alert")).toContainText(`Runner API returned an inconsistent budget report: ${message}`);
      await expect(outcome.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
    }
  });

  test("accepts a live pre-planning rejection without a budget report", async ({ page }) => {
    const rejected: Partial<typeof fixture.rejected> = {
      ...fixture.rejected, outcome: "invalid_input", exit_code: 6,
      diagnostics: [{ code: "invalid_input", message: "request does not match the runner request schema" }],
    };
    delete rejected.budget;
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: fixture.rejected_validation }));
    await page.route("**/api/v1/runner/run", (route) =>
      route.fulfill({ json: rejected, headers: { "X-Waza-Exit-Code": "6" } }));
    await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(fixture.rejected_request));
    const outcome = page.getByRole("region", { name: "Runner terminal outcome" });
    await outcome.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(outcome.getByRole("status", { name: "Terminal result" })).toBeVisible();
  });

  test("refuses an unstarted budget on a terminal that executed", async ({ page }) => {
    const unstarted = {
      ...fixture.terminated.budget, state: "not_started", termination: undefined,
      resources: fixture.terminated.budget.resources.map(({ resource, enforcement, measurement, scope, detail, limit }) =>
        ({ resource, enforcement, measurement, scope, detail, limit })),
    };
    for (const [outcome, status] of [["success", "passed"], ["evaluation_failure", "failed"]]) {
      await readTerminal(page, {
        ...fixture.terminated, outcome, exit_code: outcome === "success" ? 0 : 1, failure_boundary: "none",
        diagnostics: outcome === "success" ? [] : fixture.terminated.diagnostics,
        observation: {
          ...fixture.terminated.observation, status, error_msg: undefined,
          validations: [{ identifier: "check", passed: status === "passed" }],
        },
        budget: unstarted,
      });
      await expect(panel(page).getByRole("alert")).toHaveText("Budget state contradicts the terminal outcome.");
    }
    await readTerminal(page, {
      ...fixture.terminated, outcome: "infrastructure_failure", exit_code: 2, failure_boundary: "execution", budget: unstarted,
    });
    await expect(panel(page).getByRole("alert")).toHaveText("Budget reports an unstarted attempt but the terminal reports execution.");
  });

  test("checks budget reports embedded in checkpoint results", async ({ page }) => {
    const source = JSON.parse(readFileSync(new URL("./checkpoint-operation-error-source.json", import.meta.url), "utf8"));
    const reused = structuredClone(source);
    reused.schema_version = "1.0";
    reused.status = "reused";
    reused.exit_code = 0;
    delete reused.error_code;
    reused.compatibility_result = "compatible";
    reused.restored_units = 1;
    reused.remaining_work = 0;
    const reader = page.getByRole("region", { name: "Checkpoint result reader" });
    const parse = async (budget: unknown) => {
      const result = structuredClone(reused);
      result.submission.terminal.budget = budget;
      await reader.getByLabel("Checkpoint result JSON").fill(JSON.stringify(result));
      await reader.getByRole("button", { name: "Parse checkpoint result" }).click();
    };
    await parse(withinBudget());
    await expect(reader.getByRole("status", { name: "Checkpoint operation result" })).toBeVisible();
    await parse(fixture.terminated.budget);
    await expect(reader.getByRole("alert")).toContainText(
      "Checkpoint retained submission terminal has an inconsistent budget report: Budget state contradicts the terminal outcome.");
    const arithmetic = withinBudget();
    arithmetic.resources = arithmetic.resources!.map((row) => row.resource === "wall_time_ms" ? { ...row, remaining: 1 } : row);
    await parse(arithmetic);
    await expect(reader.getByRole("alert")).toContainText("Budget wall_time_ms remaining does not equal limit minus used.");
  });

  test("refuses a contradictory budget report from a live run", async ({ page }) => {
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: fixture.rejected_validation }));
    const contradictory = { ...fixture.rejected, budget: { ...fixture.rejected.budget, unsupported: "report" } };
    await page.route("**/api/v1/runner/run", (route) =>
      route.fulfill({ json: contradictory, headers: { "X-Waza-Exit-Code": "5" } }));
    await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(fixture.rejected_request));
    const outcome = page.getByRole("region", { name: "Runner terminal outcome" });
    await outcome.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(outcome.getByRole("alert")).toContainText(
      "Runner API returned an inconsistent budget report: Budget state contradicts the terminal outcome.");
    await expect(outcome.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
  });
});
