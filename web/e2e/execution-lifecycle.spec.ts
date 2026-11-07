import { readFile } from "node:fs/promises";
import { expect, test, type Page } from "@playwright/test";
import fixture from "./fixtures/lifecycle.json";
import terminalFixtures from "./fixtures/lifecycle-terminal.json";

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution lifecycle declarations" });
}

function projection(page: Page) {
  return panel(page).getByRole("status", { name: "Declared lifecycle projection" });
}

async function inspect(page: Page) {
  await panel(page).getByRole("textbox", { name: "Retained lifecycle inspection context (JSON)" }).fill(JSON.stringify(fixture.context));
  await panel(page).getByRole("button", { name: "Inspect lifecycle declarations", exact: true }).click();
}

test.describe("Lab declared execution lifecycle inspection", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows actual API-compatible declarations, unknown verified progress and no executable retry/cancel", async ({ page }) => {
    const requests: string[] = [];
    await page.route("**/api/v1/lifecycle/inspect", (route) => {
      requests.push(route.request().method());
      expect(route.request().postDataJSON()).toEqual(fixture.context);
      return route.fulfill({ json: fixture.projection });
    });
    await inspect(page);
    await expect(projection(page)).toBeVisible();
    await expect(panel(page)).toContainText("Verified worker state, progress and ownership are unavailable.");
    const values = projection(page).locator("dd");
    await expect(values.nth(0)).toHaveText("caller-declared-unverified");
    await expect(values.nth(1)).toHaveText("Unavailable");
    await expect(values.nth(4)).toHaveText("0");
    await expect(values.nth(5)).toHaveText("Unknown");
    await expect(values.nth(15)).toHaveText("60000");
    await expect(values.nth(16)).toHaveText("Suspected; investigate, never reclaim");
    await expect(projection(page).getByRole("row")).toHaveCount(3);
    await expect(projection(page).getByRole("row").nth(2).locator("th, td")).toHaveText([
      "2", "2026-11-07T17:00:00Z", "heartbeat", "Not recorded",
    ]);
    await expect(panel(page).getByRole("button", { name: /^(Retry|Cancel|Reclaim|Restore)/ })).toHaveCount(0);
    expect(requests).toEqual(["POST"]);
    const downloadPromise = page.waitForEvent("download");
    await projection(page).getByRole("button", { name: "Download declared projection JSON" }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("lifecycle-declarations.json");
    const path = await download.path();
    if (path === null) throw new Error("Projection download did not produce a file.");
    expect(JSON.parse(await readFile(path, "utf8"))).toEqual(fixture.projection);
  });

  test("clears obsolete declaration state on failed inspection and when context changes", async ({ page }) => {
    let requests = 0;
    await page.route("**/api/v1/lifecycle/inspect", (route) => {
      requests++;
      return route.fulfill(requests === 1 ? { json: fixture.projection } : {
        status: 503, json: { error: "private provider location must not be echoed" },
      });
    });
    await inspect(page);
    await expect(projection(page)).toBeVisible();
    await panel(page).getByRole("button", { name: "Inspect lifecycle declarations", exact: true }).click();
    await expect(panel(page).getByRole("alert")).toContainText("HTTP 503");
    await expect(projection(page)).toHaveCount(0);
    await expect(panel(page)).not.toContainText("private provider location");
    await panel(page).getByRole("textbox").fill("{}");
    await expect(panel(page).getByRole("alert")).toHaveCount(0);
    await panel(page).getByRole("button", { name: "Inspect lifecycle declarations", exact: true }).click();
    await expect(panel(page).getByRole("alert")).toContainText("valid retained lifecycle inspection context");
    expect(requests).toBe(2);
  });

  test("retains real terminal verdict, failure boundary, digest and absent progress through UI and export", async ({ page }) => {
    for (const control of Object.values(terminalFixtures)) {
      const context = {
        ...fixture.context,
        observation_digests: [...fixture.context.observation_digests, control.observation_digest],
      };
      const expected = {
        ...fixture.projection,
        state: "terminal-recorded",
        terminal: control.terminal,
        completed_units: "completed_units" in control ? control.completed_units : undefined,
        retry: control.retry,
        actions: control.actions,
        suspected_orphan: false,
        observations: [...fixture.projection.observations, {
          sequence: 3, observed_at: fixture.context.as_of, event: "terminal_recorded",
        }],
      };
      await page.route("**/api/v1/lifecycle/inspect", (route) => {
        expect(route.request().postDataJSON()).toEqual(context);
        return route.fulfill({ json: expected });
      });
      await panel(page).getByRole("textbox").fill(JSON.stringify(context));
      await panel(page).getByRole("button", { name: "Inspect lifecycle declarations", exact: true }).click();
      await expect(projection(page)).toBeVisible();
      const fields = projection(page).locator("dl");
      await expect(fields.locator('dt:has-text("Declared terminal outcome") + dd')).toHaveText(control.terminal.outcome);
      await expect(fields.locator('dt:has-text("Declared terminal exit") + dd')).toHaveText(String(control.terminal.exit_code));
      await expect(fields.locator('dt:has-text("Declared execution verdict") + dd')).toHaveText(control.terminal.execution_verdict);
      await expect(fields.locator('dt:has-text("Declared failure boundary") + dd')).toHaveText(control.terminal.failure_boundary);
      await expect(fields.locator('dt:has-text("Original terminal digest") + dd')).toHaveText(control.terminal.digest);
      await expect(fields.locator('dt:has-text("Declared completed units") + dd')).toHaveText(
        "completed_units" in control ? String(control.completed_units) : "Unknown",
      );
      await expect(fields.locator('dt:has-text("Verified worker progress") + dd')).toHaveText("Unavailable");
      await expect(projection(page).getByRole("row")).toHaveCount(4);
      await expect(panel(page).getByRole("button", { name: /^(Retry|Cancel|Reclaim|Restore)/ })).toHaveCount(0);
      const downloadPromise = page.waitForEvent("download");
      await projection(page).getByRole("button", { name: "Download declared projection JSON" }).click();
      const download = await downloadPromise;
      const path = await download.path();
      if (path === null) throw new Error("Terminal projection download did not produce a file.");
      expect(JSON.parse(await readFile(path, "utf8"))).toEqual(JSON.parse(JSON.stringify(expected)));
      await page.unroute("**/api/v1/lifecycle/inspect");
    }
  });

  test("rejects mismatched, null or falsely authenticated API projections", async ({ page }) => {
    for (const change of [
      { reservation_digest: "b".repeat(64) },
      { completed_units: null },
      { completed_units: 1 },
      { deadline_expired: true },
      { evidence: "worker-authenticated" },
      { observations: [] },
      { suspected_orphan: null },
    ]) {
      await page.route("**/api/v1/lifecycle/inspect", (route) =>
        route.fulfill({ json: { ...fixture.projection, ...change } }));
      await inspect(page);
      await expect(panel(page).getByRole("alert")).toContainText("invalid or mismatched declared projection");
      await expect(projection(page)).toHaveCount(0);
      await page.unroute("**/api/v1/lifecycle/inspect");
    }
  });
});
