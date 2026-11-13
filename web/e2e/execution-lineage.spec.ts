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

// Re-roots the retry under an unrecorded parent, optionally with the producer's diagnostic.
function orphanedParent(diagnosed: boolean) {
  const missing = `exec-${"d".repeat(64)}`;
  const attempts = fixture.attempts.map((attempt, index) =>
    index === 1 ? { ...attempt, depth: 0, parent: { execution_id: missing, relationship: "retry" } } : attempt);
  const diagnostics = diagnosed ? [...fixture.diagnostics, { code: "parent_unavailable",
    execution_ids: [fixture.attempts[1]!.execution_id], message: "Attempt names a parent that is not recorded for this logical work." }] : fixture.diagnostics;
  return { ...fixture, attempts, diagnostics };
}

// Turns the last attempt into an unfinished or unverified one, optionally with the producer's diagnostic.
function unfinishedAttempt(status: "dispatched" | "completion_unverified", code?: string) {
  const last = fixture.attempts[2]!;
  const attempts = fixture.attempts.map((attempt, index) => {
    if (index !== 2) return attempt;
    const unfinished: Partial<typeof attempt> = { ...attempt, status };
    delete unfinished.terminal;
    if (status === "dispatched") delete unfinished.manifest_digest;
    return unfinished;
  });
  const diagnostics = code ? [...fixture.diagnostics, { code, execution_ids: [last.execution_id], message: "Attempt is not verified." }] : fixture.diagnostics;
  return { ...fixture, attempts, diagnostics };
}

// Retries the third attempt under the first, so both retries share one parent.
function siblingRetries(ids?: string[]) {
  const parent = fixture.attempts[0]!.execution_id;
  const attempts = fixture.attempts.map((attempt, index) =>
    index === 2 ? { ...attempt, depth: 1, parent: { execution_id: parent, relationship: "retry" } } : attempt);
  const diagnostics = ids ? [{ code: "sibling_attempts", execution_ids: ids,
    message: "Multiple attempts name the same parent; concurrent retries each kept their own immutable evidence." }] : [];
  return { ...fixture, attempts, diagnostics };
}

// Adds a second distinct decision for the first attempt, optionally with the producer's diagnostic.
function duplicateDispatch(diagnosed: boolean) {
  const target = fixture.attempts[0]!.execution_id;
  const decisions = [...fixture.decisions, { decision_id: `decision-${"f".repeat(64)}`, execution_id: target,
    native_attempt: 2, outcome: "distinct", policy: "distinct" }];
  const diagnostics = diagnosed ? [...fixture.diagnostics, { code: "duplicate_dispatch_decision", execution_ids: [target],
    message: "More than one distinct decision names the same execution; all are preserved." }] : fixture.diagnostics;
  return { ...fixture, decisions, diagnostics };
}

test.describe("Execution attempt lineage in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows ordered retries, reuse and concurrent-writer diagnostics", async ({ page }) => {
    const queries: string[] = [];
    const served = `${JSON.stringify(fixture, null, 1)}\n`;
    await page.route("**/api/v1/lineage**", (route) => {
      queries.push(new URL(route.request().url()).search);
      return route.fulfill({ body: served, contentType: "application/json" });
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
    expect(await readFile(saved, "utf8")).toBe(served);
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

  test("accepts unresolved references only with their diagnostics", async ({ page }) => {
    await page.route("**/api/v1/lineage**", (route) => route.fulfill({ body: JSON.stringify(orphanedParent(true)), contentType: "application/json" }));
    await fill(page);
    await load(page);
    const result = panel(page).getByRole("status", { name: "Loaded attempt lineage" });
    await expect(result.getByRole("list", { name: "Lineage diagnostics" })).toContainText("parent_unavailable");
  });

  for (const [name, body, code] of [
    ["sibling retries", siblingRetries(fixture.attempts.map((attempt) => attempt.execution_id)), "sibling_attempts"],
    ["duplicate dispatch decisions", duplicateDispatch(true), "duplicate_dispatch_decision"],
  ] as const) {
    test(`shows ${name} with the complete ${code} diagnostic`, async ({ page }) => {
      await page.route("**/api/v1/lineage**", (route) => route.fulfill({ body: JSON.stringify(body), contentType: "application/json" }));
      await fill(page);
      await load(page);
      const result = panel(page).getByRole("status", { name: "Loaded attempt lineage" });
      await expect(result.getByRole("list", { name: "Lineage diagnostics" })).toContainText(code);
    });
  }

  for (const [status, code] of [
    ["dispatched", "dispatched_without_completion"],
    ["completion_unverified", "completion_verification_interrupted"],
  ] as const) {
    test(`shows a ${status} attempt with its ${code} diagnostic`, async ({ page }) => {
      await page.route("**/api/v1/lineage**", (route) => route.fulfill({ body: JSON.stringify(unfinishedAttempt(status, code)), contentType: "application/json" }));
      await fill(page);
      await load(page);
      const result = panel(page).getByRole("status", { name: "Loaded attempt lineage" });
      await expect(result.getByRole("list", { name: "Lineage diagnostics" })).toContainText(code);
    });
  }

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
    ["distinct decision with reused outcome", 200, JSON.stringify({ ...fixture, decisions: fixture.decisions.map((decision, index) =>
      index === 0 ? { ...decision, outcome: "reused" } : decision) })],
    ["distinct decision with a different native attempt", 200, JSON.stringify({ ...fixture, decisions: fixture.decisions.map((decision, index) =>
      index === 0 ? { ...decision, native_attempt: 5 } : decision) })],
    ["reserved attempt with a recorded dispatch", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) => {
      if (index !== 2) return attempt;
      const reserved: Partial<typeof attempt> = { ...attempt, status: "reserved" };
      delete reserved.dispatch_decision;
      delete reserved.manifest_digest;
      delete reserved.terminal;
      return reserved;
    }) })],
    ["success terminal with a failure exit code", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, terminal: { ...attempt.terminal, exit_code: 2 } } : attempt) })],
    ["terminal with more than one engine call", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, terminal: { ...attempt.terminal, engine_calls: 7 } } : attempt) })],
    ["success terminal without an engine call", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, terminal: { ...attempt.terminal, engine_calls: 0 } } : attempt) })],
    ["cancelled terminal at the output boundary", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, terminal: { outcome: "cancelled", exit_code: 3, failure_boundary: "output", engine_calls: 1 } } : attempt) })],
    ["startup failure with an engine call", 200, JSON.stringify({ ...fixture, attempts: fixture.attempts.map((attempt, index) =>
      index === 0 ? { ...attempt, terminal: { outcome: "infrastructure_failure", exit_code: 2, failure_boundary: "startup", engine_calls: 1 } } : attempt) })],
    ["undiagnosed dispatched attempt", 200, JSON.stringify(unfinishedAttempt("dispatched"))],
    ["undiagnosed unverified completion", 200, JSON.stringify(unfinishedAttempt("completion_unverified"))],
    ["undiagnosed missing parent", 200, JSON.stringify(orphanedParent(false))],
    ["undiagnosed missing decision target", 200, JSON.stringify({ ...fixture, decisions: [...fixture.decisions,
      { decision_id: `decision-${"e".repeat(64)}`, execution_id: `exec-${"e".repeat(64)}`, native_attempt: 1, outcome: "distinct", policy: "distinct" }] })],
    ["undiagnosed missing reuse source", 200, JSON.stringify({ ...fixture, decisions: [...fixture.decisions,
      { decision_id: `decision-${"e".repeat(64)}`, native_attempt: 4, outcome: "reused", policy: "reuse",
        source: { execution_id: `exec-${"e".repeat(64)}`, manifest_digest: "0".repeat(64) } }] })],
    ["undiagnosed concurrent roots", 200, JSON.stringify({ ...fixture, diagnostics: [] })],
    ["incomplete concurrent-root diagnostic", 200, JSON.stringify({ ...fixture, diagnostics: [{ ...fixture.diagnostics[0]!,
      execution_ids: [fixture.attempts[0]!.execution_id] }] })],
    ["undiagnosed sibling attempts", 200, JSON.stringify(siblingRetries())],
    ["incomplete sibling diagnostic", 200, JSON.stringify(siblingRetries(fixture.attempts.slice(1).map((attempt) => attempt.execution_id)))],
    ["undiagnosed duplicate dispatch", 200, JSON.stringify(duplicateDispatch(false))],
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
