import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";
import fixture from "./fixtures/execution-submission.json" with { type: "json" };
import historyFixture from "./fixtures/execution-submission-history.json" with { type: "json" };

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

for (const control of ["retagged-terminal", "different-source", "raw-report", "null-call-count", "duplicate-key", "orphan-terminal", "original-binding-run", "original-binding-work"]) {
  test(`rejects ${control} without displaying an approved source`, async ({ page }) => {
    const panel = page.getByRole("region", { name: "Owned execution and lineage" });
    const changed = JSON.parse(fixture.reused);
    if (control === "retagged-terminal") changed.terminal.selection.attempt = 3;
    if (control === "different-source") changed.decision.source.execution_id = `exec-${"a".repeat(64)}`;
    if (control === "raw-report") changed.report = { private: "unapproved-provider-control" };
    if (control === "null-call-count") changed.engine_calls = null;
    if (control === "original-binding-run") {
      changed.reservation.binding.identity.run = `run-${"a".repeat(64)}`;
      changed.terminal.identity.run = changed.reservation.binding.identity.run;
    }
    if (control === "original-binding-work") {
      changed.reservation.binding.identity.trial = `trial-${"a".repeat(64)}`;
      changed.terminal.identity.trial = changed.reservation.binding.identity.trial;
    }
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
    await expect(panel.getByRole("region", { name: "Historical submission receipts" })).toHaveCount(0);
    if (control === "original-binding-run" || control === "original-binding-work") {
      await expect(panel.getByRole("alert")).toContainText("original binding contradicts its logical work");
    }
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
  await expect(panel.getByRole("region", { name: "Historical submission receipts" })).toContainText("Historical protected responses, not reread");
  await expect(panel.getByRole("table").locator("tbody tr")).toHaveCount(1);
  await page.goto("/#/lab/demo/runner");
  await expect(panel).toContainText("unavailable in the synthetic demo");
  await expect(panel.getByRole("textbox")).toHaveCount(0);
});

test("retains independent attempts, parent links, repeated reuse and actor/operation errors across Lab navigation", async ({ page }) => {
  const bodies = [
    historyFixture.created, historyFixture.distinct, historyFixture.retry,
    historyFixture.reused, historyFixture.repeated_reuse, historyFixture.failed,
    historyFixture.canceled, historyFixture.canceled_reuse, historyFixture.close_failed, historyFixture.rejected,
  ];
  const originals = bodies.map((body) => JSON.parse(body));
  expect(originals[0].reservation.execution_id).not.toBe(originals[1].reservation.execution_id);
  expect(originals[0].reservation.logical).toEqual(originals[1].reservation.logical);
  expect(originals[3].decision.decision_id).not.toBe(originals[4].decision.decision_id);
  expect(originals[3].reservation).toEqual(originals[0].reservation);
  let calls = 0;
  await page.route("**/api/v1/runner/submit", (route) => {
    const body = bodies[calls++];
    if (body === undefined) throw new Error("Unexpected extra history submission.");
    const value = JSON.parse(body);
    return route.fulfill({ body, status: value.status === "submission_error" ? 503 : value.status === "rejected" ? 400 : 200,
      contentType: "application/json", headers: { "X-Waza-Exit-Code": String(value.exit_code) } });
  });
  const panel = page.getByRole("region", { name: "Owned execution and lineage" });
  const input = panel.getByRole("textbox", { name: "Captured execution submission (JSON)" });
  const first = JSON.parse(await input.inputValue());
  const rows = panel.getByRole("table", { name: "Recorded attempts and reuse decisions" }).locator("tbody tr");
  for (let index = 0; index < bodies.length; index++) {
    const request = structuredClone(first);
    if (index === 2) request.policy = { mode: "distinct",
      parent: { execution_id: originals[0].reservation.execution_id, relationship: "retry" } };
    if (index === 3 || index === 4 || index === 7) {
      request.request.selection.attempt = 3;
      const source = originals[index === 7 ? 6 : 0];
      request.policy = { mode: "reuse", source: {
        execution_id: source.reservation.execution_id, manifest_digest: source.completion.manifest_digest,
      } };
    }
    if (index === 5) request.request.input.task = request.request.input.task.replace("- hello\n", "- history-never-match\n");
    await input.fill(index === 9 ? '{"request":{"timeout_ms":60000}}' : JSON.stringify(request));
    await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
    await expect(rows).toHaveCount(index + 1);
    await expect(rows.nth(index)).toContainText(originals[index].decision?.decision_id ?? "No admitted decision");
    await expect(rows.nth(index).locator("td").nth(0)).toHaveText(String(index + 1));
    await expect(rows.nth(index).locator("td").nth(7)).toHaveText(String(originals[index].engine_calls));
  }
  await expect(rows.nth(0).locator("td").nth(2)).toContainText(originals[0].reservation.execution_id);
  await expect(rows.nth(1).locator("td").nth(2)).toContainText(originals[1].reservation.execution_id);
  await expect(rows.nth(2).locator("td").nth(4)).toContainText(`retry: ${originals[0].reservation.execution_id}`);
  await expect(rows.nth(2)).toContainText("Parent appears in these receipts");
  await expect(rows.nth(3).locator("td").nth(2)).toContainText(originals[0].reservation.execution_id);
  await expect(rows.nth(3).locator("td").nth(5)).toContainText(originals[0].completion.manifest_digest);
  await expect(rows.nth(5)).toContainText("completed / evaluation_failure");
  await expect(rows.nth(6)).toContainText("completed / cancelled");
  await expect(rows.nth(7)).toContainText("reused / cancelled");
  await expect(rows.nth(8)).toContainText("submission_error / success");
  await expect(rows.nth(8)).toContainText("No operation completion claim");
  await expect(rows.nth(9)).toContainText("No admitted reservation");
  await page.getByRole("link", { name: "Run history", exact: true }).click();
  await expect(panel).toHaveCount(0);
  await page.getByRole("link", { name: "Runner contract", exact: true }).click();
  await expect(rows).toHaveCount(bodies.length);
  await expect(panel.getByRole("status", { name: "Owned execution submission result" })).toHaveCount(0);
  await expect(panel).toContainText("Receipt order is not global execution order");
  const downloaded = page.waitForEvent("download");
  await panel.getByRole("button", { name: "Download exact historical receipts" }).click();
  const path = await (await downloaded).path();
  if (path === null) throw new Error("Historical receipt download was not materialized.");
  const exported = JSON.parse(await readFile(path, "utf8"));
  expect(exported).toEqual(bodies.map((json, index) => ({ sequence: index + 1, json })));
  await input.fill("{}");
  await expect(rows).toHaveCount(bodies.length);
  await panel.getByRole("button", { name: "Clear historical view (does not delete stored evidence)" }).click();
  await expect(rows).toHaveCount(0);
  expect(calls).toBe(bodies.length);
});

test("stops at exactly 32 receipts without dropping or overwriting and permits export/edit/explicit clear", async ({ page }) => {
  let calls = 0;
  await page.route("**/api/v1/runner/submit", (route) => {
    calls++;
    return route.fulfill({ body: historyFixture.reused, contentType: "application/json",
      headers: { "X-Waza-Exit-Code": "0" } });
  });
  const panel = page.getByRole("region", { name: "Owned execution and lineage" });
  const submit = panel.getByRole("button", { name: "Submit captured execution policy" });
  const rows = panel.getByRole("table").locator("tbody tr");
  const reuseRequest = JSON.parse(await panel.getByRole("textbox").inputValue());
  reuseRequest.request.selection.attempt = 3;
  const source = JSON.parse(historyFixture.created);
  reuseRequest.policy = { mode: "reuse", source: {
    execution_id: source.reservation.execution_id, manifest_digest: source.completion.manifest_digest,
  } };
  await panel.getByRole("textbox").fill(JSON.stringify(reuseRequest));
  for (let count = 1; count <= 32; count++) {
    await submit.click();
    await expect(rows).toHaveCount(count);
  }
  await expect(submit).toBeDisabled();
  await expect(panel).toContainText("No records were dropped");
  const input = panel.getByRole("textbox");
  await input.fill("{}");
  await expect(input).toBeEnabled();
  await expect(submit).toBeDisabled();
  await expect(rows).toHaveCount(32);
  const downloaded = page.waitForEvent("download");
  await panel.getByRole("button", { name: "Download exact historical receipts" }).click();
  const path = await (await downloaded).path();
  if (path === null) throw new Error("Capacity receipt download was not materialized.");
  expect(JSON.parse(await readFile(path, "utf8"))).toEqual(
    Array.from({ length: 32 }, (_, index) => ({ sequence: index + 1, json: historyFixture.reused })),
  );
  await panel.getByRole("button", { name: "Clear historical view (does not delete stored evidence)" }).click();
  await expect(rows).toHaveCount(0);
  await expect(submit).toBeEnabled();
  expect(calls).toBe(32);
});

test("retains the alternative-valid wire receipt that crosses the byte budget and stops further submissions", async ({ page }) => {
  // Production Go parsing separately admits this trailing-whitespace control.
  const body = `${historyFixture.created}${" ".repeat(4 * 1024 * 1024)}`;
  let calls = 0;
  await page.route("**/api/v1/runner/submit", (route) => {
    calls++;
    return route.fulfill({ body, contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" } });
  });
  const panel = page.getByRole("region", { name: "Owned execution and lineage" });
  await panel.getByRole("button", { name: "Submit captured execution policy" }).click();
  await expect(panel.getByRole("table").locator("tbody tr")).toHaveCount(1);
  await expect(panel.getByRole("button", { name: "Submit captured execution policy" })).toBeDisabled();
  const downloaded = page.waitForEvent("download");
  await panel.getByRole("button", { name: "Download exact historical receipts" }).click();
  const path = await (await downloaded).path();
  if (path === null) throw new Error("Byte-budget receipt download was not materialized.");
  expect(JSON.parse(await readFile(path, "utf8"))).toEqual([{ sequence: 1, json: body }]);
  expect(calls).toBe(1);
});
