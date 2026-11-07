import { readFileSync } from "node:fs";
import { expect, test } from "@playwright/test";
import sources from "./fixtures/lifecycle-source.json" with { type: "json" };

// Exact bytes emitted by the actual protected producer -> CLI/configured HTTP
// regression. These are finalized mock/cancellation sources, not live workers.
test.describe("Lab original lifecycle sources", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  for (const source of sources) {
    test(`renders and exports actual ${source.control} original source without upgrading liveness`, async ({ page }) => {
      const actual = JSON.parse(source.json);
      const panel = page.getByRole("region", { name: "Original lifecycle source inspection" });
      await page.route("**/api/v1/lifecycle/source", (route) => {
        expect(route.request().postData()).toBe(source.query);
        return route.fulfill({ contentType: "application/json", body: source.json });
      });
      await panel.getByRole("textbox", { name: "Original lifecycle source selection (JSON)" }).fill(source.query);
      await panel.getByRole("button", { name: "Inspect original lifecycle source", exact: true }).click();
      const result = panel.getByRole("status", { name: "Original lifecycle source metadata" });
      await expect(result).toContainText("verified-nonsnapshot-content");
      await expect(result).toContainText("original-source-declarations");
      await expect(result).toContainText("caller-declared-unverified");
      await expect(result).toContainText(actual.source.execution_id);
      await expect(result).toContainText(actual.snapshot.terminal.outcome);
      await expect(result.locator("dt").filter({ hasText: /^Original terminal exit$/ }).locator("xpath=following-sibling::dd[1]")).toHaveText(String(actual.snapshot.terminal.exit_code));
      await expect(result.locator("dt").filter({ hasText: /^Native process exit$/ }).locator("xpath=following-sibling::dd[1]")).toHaveText("unknown");
      await expect(result.locator("dt").filter({ hasText: /^Worker liveness \/ reclaim authority$/ }).locator("xpath=following-sibling::dd[1]")).toHaveText("Unavailable / none");
      await expect(result.getByRole("row")).toHaveCount(actual.snapshot.observations.length + 2);
      await expect(result.getByRole("button", { name: /retry|cancel|reclaim/i })).toHaveCount(0);
      const download = page.waitForEvent("download");
      await result.getByRole("button", { name: "Download exact lifecycle source JSON", exact: true }).click();
      const saved = await download;
      expect(saved.suggestedFilename()).toBe("lifecycle-original-source.json");
      expect(readFileSync((await saved.path())!, "utf8")).toBe(source.json);
    });
  }

  test("corrupt, missing and limit-denied reloads clear prior metadata and exports", async ({ page }) => {
    const source = sources[0];
    const panel = page.getByRole("region", { name: "Original lifecycle source inspection" });
    let status = 200;
    await page.route("**/api/v1/lifecycle/source", (route) => route.fulfill({
      status, contentType: "application/json",
      body: status === 200 ? source.json : '{"error":"Current original source unavailable"}',
    }));
    await panel.getByRole("textbox").fill(source.query);
    for (const denial of [409, 404, 503]) {
      status = 200;
      await panel.getByRole("button", { name: "Inspect original lifecycle source", exact: true }).click();
      await expect(panel.getByRole("status")).toBeVisible();
      status = denial;
      await panel.getByRole("button", { name: "Inspect original lifecycle source", exact: true }).click();
      await expect(panel.getByRole("alert")).toContainText(`HTTP ${denial}`);
      await expect(panel.getByRole("status")).toHaveCount(0);
      await expect(panel.getByRole("button", { name: "Download exact lifecycle source JSON" })).toHaveCount(0);
    }
  });

  test("rejects retagged source, invented native exit and authenticated-state near misses", async ({ page }) => {
    const source = sources[0];
    const panel = page.getByRole("region", { name: "Original lifecycle source inspection" });
    for (const mutation of [
      (value: ReturnType<typeof JSON.parse>) => { value.source.manifest_digest = "a".repeat(64); },
      (value: ReturnType<typeof JSON.parse>) => { value.verification = "authenticated-live-worker"; },
      (value: ReturnType<typeof JSON.parse>) => { value.runtime_observations.process_exit = "killed"; },
      (value: ReturnType<typeof JSON.parse>) => { value.snapshot.suspected_orphan = true; },
      (value: ReturnType<typeof JSON.parse>) => { value.snapshot.observations[0].sequence = 2; },
    ]) {
      const invalid = JSON.parse(source.json);
      mutation(invalid);
      expect(invalid).not.toEqual(JSON.parse(source.json));
      await page.route("**/api/v1/lifecycle/source", (route) => route.fulfill({ json: invalid }));
      await panel.getByRole("textbox").fill(source.query);
      await panel.getByRole("button", { name: "Inspect original lifecycle source", exact: true }).click();
      await expect(panel.getByRole("alert")).toContainText("invalid or mismatched");
      await expect(panel.getByRole("status")).toHaveCount(0);
    }
    await page.goto("/#/lab/demo/runner");
    await expect(panel).toContainText("unavailable in the synthetic demo");
    await expect(panel.getByRole("textbox")).toHaveCount(0);
  });
});
