import { expect, test, type Page } from "@playwright/test";

const digest = "a".repeat(64);
const executionId = `exec-${"b".repeat(64)}`;

const available = {
  kind: "waza.runner-checkpoint-availability", schema_version: "1.0", checkpoint_sha256: digest,
  status: "available", exit_code: 0,
  source: { boundary: "finalized-unit", created_at: "2025-01-02T03:04:05Z", execution_id: executionId },
  compatibility_result: "compatible", compatibility_differences: [], resume_action: "reuse_original_terminal",
  restored_units: 1, remaining_work: 0, diagnostics: [],
};

const incompatible = {
  kind: "waza.runner-checkpoint-availability", schema_version: "1.0", checkpoint_sha256: digest,
  status: "unavailable", exit_code: 6,
  compatibility_result: "incompatible", compatibility_differences: ["runner_revision"],
  restored_units: null, remaining_work: null,
  diagnostics: [{
    code: "checkpoint_incompatible",
    message: "The checkpoint was created by a different build or for different work, policy or consent. Rerun as new work or use the original build; implicit cross-version resume is not supported.",
  }],
};

function panel(page: Page) {
  return page.getByRole("region", { name: "Checkpoint creation and recovery" });
}

async function check(page: Page, body: object, status: number, exit = String((body as { exit_code: number }).exit_code)) {
  const requests: string[] = [];
  await page.route("**/api/v1/checkpoints/**", (route) => {
    requests.push(new URL(route.request().url()).pathname);
    return route.fulfill({ status, json: body, headers: { "X-Waza-Exit-Code": exit } });
  });
  await panel(page).getByRole("textbox", { name: "Checkpoint digest to resume" }).fill(digest);
  await panel(page).getByRole("button", { name: "Check resume availability", exact: true }).click();
  return requests;
}

test.describe("Checkpoint resume availability in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows an available finalized checkpoint without running work", async ({ page }) => {
    const requests = await check(page, available, 200);
    const result = panel(page).getByRole("status", { name: "Checkpoint availability result" });
    await expect(result).toContainText("Resume availability: available");
    await expect(result).toContainText("finalized-unit");
    await expect(result).toContainText("2025-01-02T03:04:05Z");
    await expect(result).toContainText("reuses the original terminal with no new engine calls");
    await expect(result).toContainText("Nothing was executed, reserved or repaired");
    expect(requests).toEqual([`/api/v1/checkpoints/${digest}/availability`]);
    await expect(panel(page).getByRole("button", { name: "Download availability JSON" })).toBeVisible();
  });

  test("explains an incompatible checkpoint with actionable diagnostics", async ({ page }) => {
    await check(page, incompatible, 409);
    const result = panel(page).getByRole("status", { name: "Checkpoint availability result" });
    await expect(result).toContainText("Resume availability: unavailable");
    await expect(result).toContainText("runner_revision");
    const diagnostics = result.getByRole("list", { name: "Checkpoint availability diagnostics" });
    await expect(diagnostics).toContainText("checkpoint_incompatible");
    await expect(diagnostics).toContainText("use the original build");
  });

  test("rejects contradictory transport or boundary state", async ({ page }) => {
    await check(page, { ...available, resume_action: "fresh_retry_child" }, 200);
    await expect(panel(page).getByRole("alert")).toContainText("resume action contradicts its boundary");
    await page.unroute("**/api/v1/checkpoints/**");
    await check(page, available, 409);
    await expect(panel(page).getByRole("alert")).toContainText("transport status contradicts");
    await expect(panel(page).getByRole("status", { name: "Checkpoint availability result" })).toHaveCount(0);
  });

  test("requires a full digest before sending a request", async ({ page }) => {
    let sent = false;
    await page.route("**/api/v1/checkpoints/**", (route) => { sent = true; return route.abort(); });
    await panel(page).getByRole("textbox", { name: "Checkpoint digest to resume" }).fill("abc");
    await panel(page).getByRole("button", { name: "Check resume availability", exact: true }).click();
    await expect(panel(page).getByRole("alert")).toContainText("full lowercase SHA-256 checkpoint digest");
    expect(sent).toBe(false);
  });
});
