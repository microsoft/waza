import { expect, test, type Page } from "@playwright/test";
import path from "node:path";
import { fileURLToPath } from "node:url";
import verified from "./fixtures/inventory/verified-report.json" with { type: "json" };
import mismatched from "./fixtures/inventory/mismatched-report.json" with { type: "json" };
import missing from "./fixtures/inventory/missing-report.json" with { type: "json" };
import altered from "./fixtures/inventory/altered-report.json" with { type: "json" };

// Inventories and reports are produced by the verifier and kept in sync by
// TestLabFixturesMatchVerification in internal/inventory.
const fixtures = path.join(path.dirname(fileURLToPath(import.meta.url)), "fixtures", "inventory");

function panel(page: Page) {
  return page.getByRole("region", { name: "Component inventory verification" });
}

function fulfill(page: Page, report: object, exitCode: number, seen?: (body: Record<string, unknown>) => void) {
  return page.route("**/api/v1/inventory/verify", (route) => {
    seen?.(route.request().postDataJSON() as Record<string, unknown>);
    return route.fulfill({ body: JSON.stringify(report), contentType: "application/json", headers: { "X-Waza-Exit-Code": String(exitCode) } });
  });
}

async function verify(page: Page, file?: string, digest = verified.expected.digest) {
  if (file) {
    await panel(page).getByLabel("Component inventory (CycloneDX JSON)").setInputFiles(path.join(fixtures, file));
    await expect(panel(page).getByText(`${file} selected`)).toBeVisible();
  }
  await panel(page).getByLabel("Consumed image manifest digest").fill(digest);
  await panel(page).getByRole("button", { name: "Verify inventory" }).click();
  return panel(page).getByRole("status", { name: "Inventory verification result" });
}

test.describe("Component inventory verification", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("uploads an inventory and shows the verified binding", async ({ page }) => {
    let body: Record<string, unknown> = {};
    await fulfill(page, verified, 0, (value) => { body = value; });
    await panel(page).getByLabel("Image repository (optional)").fill(verified.expected.name ?? "");
    const result = await verify(page, "runner.cdx.json");

    expect(typeof body.inventory).toBe("string");
    expect(body.expect).toEqual(verified.expected);
    await expect(result.getByRole("heading", { name: "Verified" })).toBeVisible();
    await expect(result).toContainText(verified.subject.name);
    await expect(result).toContainText(String(verified.summary.total));
    await expect(result.getByRole("list", { name: "Inventory failures" })).toHaveCount(0);
    await expect(result.getByRole("region", { name: "Inventory checks" }).getByRole("row")).toHaveCount(6);
    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Download report JSON" }).click();
    expect((await download).suggestedFilename()).toBe("waza-inventory-verification.json");
  });

  test("explains a digest mismatch", async ({ page }) => {
    await fulfill(page, mismatched, 1);
    const result = await verify(page, "runner.cdx.json", mismatched.expected.digest);
    await expect(result.getByRole("heading", { name: "Verification failed" })).toBeVisible();
    await expect(result.getByRole("list", { name: "Inventory failures" })).toContainText("subject_digest_mismatch");
  });

  test("reports an altered inventory", async ({ page }) => {
    await fulfill(page, altered, 1);
    const result = await verify(page, "altered.cdx.json");
    await expect(result.getByRole("list", { name: "Inventory failures" })).toContainText("component_ref_mismatch");
  });

  test("reports a missing inventory as unavailable", async ({ page }) => {
    let body: Record<string, unknown> = {};
    await fulfill(page, missing, 1, (value) => { body = value; });
    const result = await verify(page);
    expect(body.inventory).toBeNull();
    await expect(result.getByRole("heading", { name: "Inventory unavailable" })).toBeVisible();
    await expect(result).toContainText("No inventory supplied");
    await expect(result.getByRole("list", { name: "Inventory failures" })).toContainText("inventory_missing");
  });

  test("rejects a response whose transport status contradicts the report", async ({ page }) => {
    await fulfill(page, verified, 1);
    await verify(page, "runner.cdx.json");
    await expect(panel(page).getByRole("alert")).toContainText("contradicts its result");
    await expect(panel(page).getByRole("status", { name: "Inventory verification result" })).toHaveCount(0);
  });
});

test("component inventory verification is unavailable in the demo", async ({ page }) => {
  await page.goto("/#/lab/demo/runner");
  await expect(panel(page)).toContainText("unavailable in the synthetic demo");
  await expect(panel(page).getByRole("button", { name: "Verify inventory" })).toHaveCount(0);
});
