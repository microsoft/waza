import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import verified from "./fixtures/provenance/verified-report.json" with { type: "json" };
import tampered from "./fixtures/provenance/tampered-report.json" with { type: "json" };

// Bundle, key and reports are produced by `waza runner provenance` and kept in
// sync by TestLabFixtureReportMatchesVerification.
const fixtures = path.join(path.dirname(fileURLToPath(import.meta.url)), "fixtures", "provenance");

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution provenance verification" });
}

async function select(page: Page) {
  await panel(page).getByLabel("Provenance bundle directory").setInputFiles(path.join(fixtures, "bundle"));
  await panel(page).getByLabel("Trusted public key (PEM)").fill(await readFile(path.join(fixtures, "signing.pub.pem"), "utf8"));
}

function fulfill(page: Page, report: object, exitCode: number, seen?: (body: Record<string, unknown>) => void) {
  return page.route("**/api/v1/provenance/verify", (route) => {
    seen?.(route.request().postDataJSON() as Record<string, unknown>);
    return route.fulfill({ body: JSON.stringify(report), contentType: "application/json", headers: { "X-Waza-Exit-Code": String(exitCode) } });
  });
}

test.describe("Execution provenance verification", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("uploads the bundle and shows a verified statement with links", async ({ page }) => {
    let body: Record<string, unknown> = {};
    await fulfill(page, verified, 0, (value) => { body = value; });
    await select(page);
    await expect(panel(page).getByText("6 bundle files selected")).toBeVisible();
    await panel(page).getByLabel("Expected runner image digest (optional)").fill(verified.predicate.runner.image.digest);
    await panel(page).getByRole("button", { name: "Verify provenance" }).click();

    const files = body.files as Record<string, string>;
    expect(Object.keys(files).sort()).toEqual(verified.members.map((member) => member.path).sort());
    expect(String(body.public_key)).toContain("BEGIN PUBLIC KEY");
    expect(body.expect).toEqual({ image_digest: verified.predicate.runner.image.digest });

    const result = panel(page).getByRole("status", { name: "Provenance verification result" });
    await expect(result.getByRole("heading", { name: "Verified" })).toBeVisible();
    await expect(result).toContainText("local-ed25519");
    await expect(result).toContainText("(trusted)");
    await expect(result).toContainText("mock / mock-model (mock)");
    await expect(result).toContainText("success (exit 0)");
    await expect(result.getByRole("link", { name: "statement.dsse.json" }).first()).toHaveAttribute("href", /^blob:/);
    await expect(result.getByRole("link", { name: "artifacts/objects/terminal.json" }).first()).toHaveAttribute("download", "terminal.json");
    await expect(result.getByRole("list", { name: "Provenance failures" })).toHaveCount(0);
    const members = result.getByRole("region", { name: "Bound members" });
    await expect(members.getByRole("row")).toHaveCount(verified.members.length + 1);
  });

  test("shows precise failure reasons for a tampered artifact", async ({ page }) => {
    await fulfill(page, tampered, 1);
    await select(page);
    await panel(page).getByRole("button", { name: "Verify provenance" }).click();
    const result = panel(page).getByRole("status", { name: "Provenance verification result" });
    await expect(result.getByRole("heading", { name: "Verification failed" })).toBeVisible();
    const failures = result.getByRole("list", { name: "Provenance failures" });
    await expect(failures).toContainText("artifact_digest_mismatch");
    await expect(failures).toContainText("outcome_digest_mismatch");
    await expect(result.getByRole("region", { name: "Bound members" })).toContainText("mismatch");
  });

  test("rejects a response whose transport status contradicts the report", async ({ page }) => {
    await fulfill(page, verified, 1);
    await select(page);
    await panel(page).getByRole("button", { name: "Verify provenance" }).click();
    await expect(panel(page).getByRole("alert")).toContainText("contradicts its result");
    await expect(panel(page).getByRole("status", { name: "Provenance verification result" })).toHaveCount(0);
  });

  test("surfaces malformed upload errors from the server", async ({ page }) => {
    await page.route("**/api/v1/provenance/verify", (route) => route.fulfill({
      status: 400, contentType: "application/json", body: JSON.stringify({ error: "public key: trusted key is not Ed25519", code: 400 }),
    }));
    await select(page);
    await panel(page).getByRole("button", { name: "Verify provenance" }).click();
    await expect(panel(page).getByRole("alert")).toContainText("trusted key is not Ed25519");
  });
});

test("provenance verification is unavailable in the demo", async ({ page }) => {
  await page.goto("/#/lab/demo/runner");
  await expect(panel(page)).toContainText("unavailable in the synthetic demo");
  await expect(panel(page).getByRole("button", { name: "Verify provenance" })).toHaveCount(0);
});
