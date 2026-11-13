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
