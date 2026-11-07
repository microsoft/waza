import { createHash } from "node:crypto";
import { expect, test, type Page } from "@playwright/test";
import checkpoint from "../../internal/executioncheckpoint/testdata/before-unit.json" with { type: "json" };

const digest = createHash("sha256").update(JSON.stringify(checkpoint)).digest("hex");

// Authored source declarations only. Go writer/API tests exercise the same
// profile through actual immutable storage, without claiming owned recovery.
function receipt(overrides: Record<string, unknown> = {}) {
  return {
    checkpoint_sha256: digest,
    availability: "blob-verified",
    verification: "canonical-declaration-only",
    checkpoint,
    compatibility_result: "unavailable",
    restored_units: null,
    remaining_work: null,
    diagnostics: [{
      code: "source_not_authenticated",
      message: "Blob admission does not authenticate the original attempt or establish input compatibility and restore progress.",
    }],
    ...overrides,
  };
}

function panel(page: Page) {
  return page.getByRole("region", { name: "Checkpoint source inspection" });
}

function result(page: Page) {
  return panel(page).getByRole("status", { name: "Checkpoint source metadata" });
}

async function inspect(page: Page, value = digest) {
  await panel(page).getByRole("textbox", { name: "Checkpoint blob SHA-256 digest" }).fill(value);
  await panel(page).getByRole("button", { name: "Inspect checkpoint", exact: true }).click();
}

test.describe("Lab checkpoint source inspection", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("reads the current declaration with GET without inventing compatibility or progress", async ({ page }) => {
    const methods: string[] = [];
    await page.route(`**/api/v1/checkpoints/${digest}`, (route) => {
      methods.push(route.request().method());
      return route.fulfill({ json: receipt() });
    });
    await inspect(page);
    await expect(result(page).locator("dd")).toHaveText([
      digest, "before-unit", checkpoint.created_at, checkpoint.execution_id,
      checkpoint.compatibility.runner_revision, "Unavailable - source not authenticated",
      "Unavailable", "Unavailable",
    ]);
    await expect(result(page)).toContainText("Blob verified (declaration only)");
    await expect(result(page).getByRole("listitem")).toHaveCount(1);
    await expect(result(page)).toContainText("source_not_authenticated");
    expect(methods).toEqual(["GET"]);
  });

  test("finalized references alone cannot become restored units", async ({ page }) => {
    await page.route("**/api/v1/checkpoints/*", (route) => route.fulfill({ json: receipt({
      checkpoint: { ...checkpoint, boundary: "finalized-unit", completion_digest: "e".repeat(64), manifest_digest: "f".repeat(64) },
    }) }));
    await inspect(page);
    await expect(result(page).locator("dd").nth(1)).toHaveText("finalized-unit");
    await expect(result(page).locator("dd").nth(6)).toHaveText("Unavailable");
    await expect(result(page).locator("dd").nth(7)).toHaveText("Unavailable");
  });

  test("corrupt current content clears prior metadata and preserves actionable diagnostics", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/checkpoints/*", (route) => {
      reads++;
      const value = receipt({
        availability: "unavailable",
        diagnostics: [{ code: "corrupt_object", message: "Preserve the checkpoint source; do not normalize or overwrite it." }],
      });
      Reflect.deleteProperty(value, "checkpoint");
      return route.fulfill(reads === 1 ? { json: receipt() } : { status: 409, json: value });
    });
    await inspect(page);
    await expect(result(page)).toContainText(checkpoint.created_at);
    await panel(page).getByRole("button", { name: "Inspect checkpoint", exact: true }).click();
    await expect(result(page)).toContainText("Checkpoint unavailable");
    await expect(result(page)).not.toContainText(checkpoint.created_at);
    await expect(result(page)).toContainText("corrupt_object: Preserve the checkpoint source");
    expect(reads).toBe(2);
  });

  test("invalid input does not request storage or retain old metadata", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/checkpoints/*", (route) => {
      reads++;
      return route.fulfill({ json: receipt() });
    });
    await inspect(page);
    await expect(result(page)).toBeVisible();
    for (const value of ["", "a".repeat(63), "A".repeat(64), `${digest} `]) {
      await inspect(page, value);
      await expect(panel(page).getByRole("alert")).toContainText("full lowercase SHA-256");
      await expect(result(page)).toHaveCount(0);
    }
    expect(reads).toBe(1);
  });

  for (const invalid of [
    { name: "invented restored zero", value: { restored_units: 0 } },
    { name: "invented compatibility", value: { compatibility_result: "compatible" } },
    { name: "retagged digest", value: { checkpoint_sha256: "0".repeat(64) } },
    { name: "unknown boundary", value: { checkpoint: { ...checkpoint, boundary: "mid-runtime" } } },
    { name: "noncanonical timestamp", value: { checkpoint: { ...checkpoint, created_at: "2026-11-07T16:00:00.000Z" } } },
    { name: "invalid timestamp", value: { checkpoint: { ...checkpoint, created_at: "2026-02-31T16:00:00Z" } } },
  ]) {
    test(`rejects ${invalid.name} instead of presenting source evidence`, async ({ page }) => {
      await page.route("**/api/v1/checkpoints/*", (route) => route.fulfill({ json: receipt(invalid.value) }));
      await inspect(page);
      await expect(panel(page).getByRole("alert")).toContainText("invalid or inconsistent");
      await expect(result(page)).toHaveCount(0);
    });
  }

  test("does not expose private provider errors or substitute a demo", async ({ page }) => {
    await page.route("**/api/v1/checkpoints/*", (route) => route.fulfill({
      status: 503, body: "https://private-provider.example/secret /private/operator/store",
    }));
    await inspect(page);
    await expect(panel(page).getByRole("alert")).toContainText("malformed data");
    await expect(panel(page)).not.toContainText("private-provider");
    await expect(result(page)).toHaveCount(0);
    await page.goto("/#/lab/demo/runner");
    await expect(panel(page)).toContainText("unavailable in the synthetic demo");
    await expect(panel(page).getByRole("textbox")).toHaveCount(0);
  });
});
