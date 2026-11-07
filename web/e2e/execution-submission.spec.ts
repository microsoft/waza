import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import fixture from "./fixtures/execution-submission.json" with { type: "json" };

// Exact protected HTTP bytes emitted by TestPublicSubmissionHTTPBrowserFixtureUsesActualProducerBytes.
// These mock-execution consumer controls do not attest a native runtime or storage in the browser.
const created = JSON.parse(fixture.created);
const reused = JSON.parse(fixture.reused);

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
  await page.goto("/#/lab/runner");
});

test("displays original ownership and reuse without retagging and downloads exact producer JSON", async ({ page }) => {
  const panel = page.getByRole("region", { name: "Owned execution and lineage" });
  const requests: string[] = [];
  await page.route("**/api/v1/runner/submit", (route) => {
    expect(route.request().method()).toBe("POST");
    requests.push(route.request().postData() ?? "");
    const body = requests.length === 1 ? fixture.created : fixture.reused;
    return route.fulfill({ body, contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" } });
  });
  const input = panel.getByRole("textbox", { name: "Captured execution submission (JSON)" });
  const firstRequest = await input.inputValue();
  await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
  const result = panel.getByRole("status", { name: "Owned execution submission result" });
  await expect(result).toContainText(created.reservation.execution_id);
  await expect(result.locator("dd").nth(6)).toHaveText("2");
  await expect(result.locator("dd").nth(7)).toHaveText("1");
  const next = JSON.parse(firstRequest);
  next.request.selection.attempt = 3;
  next.policy = { mode: "reuse", source: {
    execution_id: created.reservation.execution_id, manifest_digest: created.completion.manifest_digest,
  } };
  const reuseRequest = JSON.stringify(next, null, 2);
  await input.fill(reuseRequest);
  await expect(result).toHaveCount(0);
  await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
  await expect(result).toContainText("reused");
  await expect(result.locator("dd").nth(5)).toHaveText(reused.caller.attempt);
  await expect(result.locator("dd").nth(6)).toHaveText("2");
  await expect(result.locator("dd").nth(7)).toHaveText("0");
  const downloaded = page.waitForEvent("download");
  await panel.getByRole("button", { name: "Download exact submission JSON" }).click();
  const download = await downloaded;
  const path = await download.path();
  if (path === null) throw new Error("Submission download was not materialized.");
  expect(await readFile(path, "utf8")).toBe(fixture.reused);
  expect(requests).toEqual([firstRequest, reuseRequest]);
});

for (const control of ["retagged-terminal", "different-source", "raw-report", "null-call-count", "duplicate-key", "orphan-terminal"]) {
  test(`rejects ${control} without displaying an approved source`, async ({ page }) => {
    const panel = page.getByRole("region", { name: "Owned execution and lineage" });
    const changed = JSON.parse(fixture.reused);
    if (control === "retagged-terminal") changed.terminal.selection.attempt = 3;
    if (control === "different-source") changed.decision.source.execution_id = `exec-${"a".repeat(64)}`;
    if (control === "raw-report") changed.report = { private: "unapproved-provider-control" };
    if (control === "null-call-count") changed.engine_calls = null;
    if (control === "orphan-terminal") {
      Object.assign(changed, { status: "submission_error", exit_code: 2, engine_calls: 1,
        terminal: created.terminal, diagnostics: [{ code: "operation_failed", message: "Original context unavailable." }] });
      for (const key of ["caller", "reservation", "decision", "completion"]) Reflect.deleteProperty(changed, key);
    }
    const body = control === "duplicate-key"
      ? fixture.reused.replace('"engine_calls":0', '"engine_calls":0,"engine_calls":0')
      : JSON.stringify(changed);
    expect(body).not.toBe(fixture.reused);
    await page.route("**/api/v1/runner/submit", (route) => route.fulfill({
      body, contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" },
    }));
    await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
    await expect(panel.getByRole("alert")).toBeVisible();
    await expect(panel.getByRole("status", { name: "Owned execution submission result" })).toHaveCount(0);
    await expect(panel).not.toContainText("unapproved-provider-control");
  });
}

test("clears old results on transport failure and disables synthetic demo submission", async ({ page }) => {
  let requests = 0;
  await page.route("**/api/v1/runner/submit", (route) => {
    requests++;
    return route.fulfill(requests === 1
      ? { body: fixture.created, contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" } }
      : { status: 503, body: "private-provider-control" });
  });
  const panel = page.getByRole("region", { name: "Owned execution and lineage" });
  await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
  await expect(panel.getByRole("status", { name: "Owned execution submission result" })).toBeVisible();
  await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
  await expect(panel.getByRole("alert")).toBeVisible();
  await expect(panel.getByRole("status", { name: "Owned execution submission result" })).toHaveCount(0);
  await expect(panel).not.toContainText("private-provider-control");
  await page.goto("/#/lab/demo/runner");
  await expect(panel).toContainText("unavailable in the synthetic demo");
  await expect(panel.getByRole("textbox")).toHaveCount(0);
});
