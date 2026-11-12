import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import fixture from "./fixtures/execution-lineage.json" with { type: "json" };

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution attempt lineage" });
}

async function fill(page: Page, run = fixture.logical.run, work = fixture.logical.work) {
  await panel(page).getByRole("textbox", { name: "Logical run ID" }).fill(run);
  await panel(page).getByRole("textbox", { name: "Logical work ID" }).fill(work);
}

function load(page: Page) {
  return panel(page).getByRole("button", { name: "Load attempt lineage" }).click();
}

test.describe("Execution attempt lineage in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows ordered retries, reuse and concurrent-writer diagnostics", async ({ page }) => {
    const queries: string[] = [];
    await page.route("**/api/v1/lineage**", (route) => {
      queries.push(new URL(route.request().url()).search);
      return route.fulfill({ body: JSON.stringify(fixture), contentType: "application/json" });
    });
    await fill(page);
    await load(page);
    const result = panel(page).getByRole("status", { name: "Loaded attempt lineage" });
    await expect(result).toContainText("3 attempts, 4 decisions");
    expect(queries).toEqual([`?run=${fixture.logical.run}&work=${fixture.logical.work}`]);
    const rows = result.getByRole("region", { name: "Ordered attempts" }).getByRole("row");
    await expect(rows).toHaveCount(4);
    await expect(rows.nth(1)).toContainText("Completed (verified terminal)");
    await expect(rows.nth(2)).toContainText("retry of");
    await expect(rows.nth(1).getByRole("cell").last()).not.toHaveText("None");
    await expect(result.getByRole("list", { name: "Lineage decisions" })).toContainText("zero new engine calls");
    await expect(result.getByRole("list", { name: "Lineage diagnostics" })).toContainText("concurrent_distinct_attempts");

    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Download lineage JSON" }).click();
    const saved = await (await download).path();
    expect(JSON.parse(await readFile(saved, "utf8"))).toEqual(fixture);
  });

  test("lists unfinished reuse decisions without linking them", async ({ page }) => {
    const unfinished = { decision_id: `decision-${"f".repeat(64)}`, native_attempt: 4, outcome: "unfinished", policy: "reuse",
      source: { execution_id: fixture.attempts[1]!.execution_id, manifest_digest: "0".repeat(64) } };
    await page.route("**/api/v1/lineage**", (route) => route.fulfill({
      body: JSON.stringify({ ...fixture, decisions: [...fixture.decisions, unfinished] }), contentType: "application/json",
    }));
    await fill(page);
    await load(page);
    const result = panel(page).getByRole("status", { name: "Loaded attempt lineage" });
    await expect(result).toContainText("3 attempts, 5 decisions");
    await expect(result.getByRole("list", { name: "Lineage decisions" })).toContainText("reuse / unfinished");
    await expect(result.getByRole("list", { name: "Lineage decisions" })).toContainText("not reused; source not linked");
  });

  test("rejects malformed IDs without a request", async ({ page }) => {
    let requests = 0;
    await page.route("**/api/v1/lineage**", (route) => { requests++; return route.abort(); });
    await fill(page, "run-1", fixture.logical.work);
    await load(page);
    await expect(panel(page).getByRole("alert")).toContainText("full run-<sha256>");
    expect(requests).toBe(0);
  });

  for (const [name, status, body] of [
    ["storage failure", 503, "{}"],
    ["inconsistent order", 200, JSON.stringify({ ...fixture, attempts: [...fixture.attempts].reverse() })],
    ["mismatched logical work", 200, JSON.stringify({ ...fixture, logical: { ...fixture.logical, work: `trial-${"0".repeat(64)}` } })],
    ["forward parent reference", 200, JSON.stringify({ ...fixture, attempts: [
      { ...fixture.attempts[0]!, parent: { execution_id: fixture.attempts[2]!.execution_id, relationship: "retry" } },
      ...fixture.attempts.slice(1),
    ] })],
    ["terminal on an unfinished attempt", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, status: "dispatched" } : attempt) })],
    ["misattributed reuse", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 2 ? { ...attempt, reused_by: fixture.attempts[0]!.reused_by } : attempt) })],
    ["duplicate keys", 200, `{"kind":"waza.execution-lineage",${JSON.stringify(fixture).slice(1)}`],
  ] as const) {
    test(`fails closed on ${name}`, async ({ page }) => {
      await page.route("**/api/v1/lineage**", (route) => route.fulfill({ status, body, contentType: "application/json" }));
      await fill(page);
      await load(page);
      await expect(panel(page).getByRole("alert")).toBeVisible();
      await expect(panel(page).getByRole("status", { name: "Loaded attempt lineage" })).toHaveCount(0);
    });
  }

  test("is unavailable in demo mode", async ({ page }) => {
    await page.goto("/#/lab/demo/runner");
    await expect(panel(page)).toContainText("unavailable in the synthetic demo");
    await expect(panel(page).getByRole("button", { name: "Load attempt lineage" })).toHaveCount(0);
  });
});
