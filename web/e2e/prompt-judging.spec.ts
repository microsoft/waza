import { test, expect, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { mockAllAPIs } from "./helpers/api-mock";
import recorded from "./fixtures/prompt-judging.json" with { type: "json" };

async function openJudging(page: Page, detail: unknown = recorded) {
  await mockAllAPIs(page);
  await page.route(/\/api\/runs\/run-001$/, (route) => route.fulfill({ json: detail }));
  await page.goto("/#/runs/run-001");
  await page.getByRole("button", { name: "Evidence", exact: true }).click();
  return page.getByTestId("prompt-judging");
}

test("producer-backed judging retains every run, uncertainty and precise zero usage", async ({ page }) => {
  const requests: string[] = [];
  page.on("request", (request) => requests.push(new URL(request.url()).pathname));
  const view = await openJudging(page);
  await expect(view).toContainText("Run 1 · attempt 1 · recorded");
  await expect(view).toContainText("Run 2 · attempt 1 · unavailable");
  await expect(view).toContainText("Run 3 · attempt 1 · unassessed");
  await expect(view).toContainText("a-judge · run · submitted · submission observed · inspection not observable");
  await expect(view).toContainText("b-judge · run · submitted");
  await expect(view).toContainText("Judge AI credits: 0 · 4 input / 1 output tokens");
  await expect(view).toContainText("Judge AI credits: unavailable · 4 input / 1 output tokens");
  await expect(page.getByTestId("evidence-view")).toContainText("Current regrade AI credits: 0");
  await expect(view).toContainText("64 files · 65536 bytes/file · 262144 bytes/bundle");
  await expect(view).toContainText("invalid_evidence");
  await expect(view).not.toContainText("metadata withheld");
  for (const privateValue of ["PRIVATE_", "a.txt", "b.txt", "evidence_context", "rendered_prompt"]) {
    await expect(view).not.toContainText(privateValue);
  }
  expect(requests.filter((path) => /snapshot|a\.txt|b\.txt|upload|grade|replay/.test(path))).toEqual([]);

  const downloadPromise = page.waitForEvent("download");
  await view.getByRole("button", { name: "Export prompt judging metadata" }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe("waza-prompt-judging.json");
  const path = await download.path();
  expect(path).not.toBeNull();
  const data = JSON.parse(await readFile(path!, "utf8"));
  expect(data).toEqual({
    version: "1.0", taskID: recorded.tasks[0].id,
    promptJudgingRuns: recorded.tasks[0].promptJudgingRuns, withheldRunCount: 0,
  });
  expect(data.promptJudgingRuns).toHaveLength(3);
});

for (const corruption of ["unknown-field", "unobserved-submission", "invalid-digest", "observable-inspection", "null-row"] as const) {
  test(`malformed ${corruption} is withheld without hiding valid trials`, async ({ page }) => {
    const detail = structuredClone(recorded);
    const row = detail.tasks[0].promptJudgingRuns[0];
    if (corruption === "unknown-field") Object.assign(row, { renderedPrompt: "PRIVATE_PAYLOAD_CANARY" });
    if (corruption === "unobserved-submission") row.observations[0].submissionObserved = false;
    if (corruption === "invalid-digest") row.bundleSHA256 = "invalid";
    if (corruption === "observable-inspection") row.observations[0].inspection = "all_read";
    if (corruption === "null-row") Object.assign(detail.tasks[0].promptJudgingRuns, { 0: null });
    const errors: string[] = [];
    page.on("pageerror", (error) => errors.push(error.message));
    const view = await openJudging(page, detail);
    await expect(view).toContainText("Prompt judging metadata withheld");
    await expect(view).toContainText("Run 2 · attempt 1 · unavailable");
    await expect(view).not.toContainText("PRIVATE_PAYLOAD_CANARY");
    const pendingDownload = page.waitForEvent("download");
    await view.getByRole("button", { name: "Export prompt judging metadata" }).click();
    const download = await pendingDownload;
    const path = await download.path();
    const exported = JSON.parse(await readFile(path!, "utf8"));
    expect(exported.promptJudgingRuns).toHaveLength(2);
    expect(exported.withheldRunCount).toBe(1);
    expect(errors).toEqual([]);
  });
}

test("nonarray judging metadata is invalid rather than historical absence", async ({ page }) => {
  const detail = structuredClone(recorded);
  Object.assign(detail.tasks[0], { promptJudgingRuns: { content: "PRIVATE_CANARY" } });
  const view = await openJudging(page, detail);
  await expect(view).toContainText("Prompt judging metadata withheld");
  await expect(view).not.toContainText("Prompt judging unassessed");
  await expect(view).not.toContainText("PRIVATE_CANARY");
});
