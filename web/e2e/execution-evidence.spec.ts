import fs from "node:fs";
import { expect, test, type Page } from "@playwright/test";

// This exact authored/mock receipt is emitted through the real CLI, immutable
// Local store and HTTP API in TestArtifactEvidenceCLIToPersistedAPIAndDownloads.
const fixture = fs.readFileSync(new URL("./fixtures/execution-evidence.json", import.meta.url), "utf8");
const csvFixture = fs.readFileSync(new URL("./fixtures/execution-evidence.csv", import.meta.url), "utf8");
const documentFixture = fixture.slice(fixture.indexOf('"document":') + '"document":'.length, fixture.indexOf(',"sources":'));
const digest = "b0f8e5199f751414edebc1d32bfe28e60cb0d378f6adfacd3260ac5c00d108dd";
const origin = "943df82988b4f73e9a50469ff5319a15c6c2c089082f2686b29523cae26ec3d9";
function panel(page: Page) {
  return page.getByRole("region", { name: "Execution evidence inspection", exact: true });
}
async function inspect(page: Page) {
  await panel(page).getByRole("textbox", { name: "Evidence SHA-256 digest" }).fill(digest);
  await panel(page).getByRole("button", { name: "Inspect evidence", exact: true }).click();
}

test.describe("Lab execution evidence states", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows six declared categories, explicit zero, precise counts and privacy states", async ({ page }) => {
    const methods: string[] = [];
    await page.route(`**/api/v1/evidence/${digest}`, (route) => {
      methods.push(route.request().method());
      return route.fulfill({ contentType: "application/json", body: fixture });
    });
    await inspect(page);
    const rows = panel(page).getByRole("row");
    await expect(rows).toHaveCount(7);
    await expect(rows.nth(1).locator("th, td")).toHaveText([
      "conversation", "present (observed empty declaration)", "0", "authored", "declared (declaration)", "None",
    ]);
    await expect(rows.nth(2)).toContainText("redacted");
    await expect(rows.nth(3).getByRole("cell").nth(1)).toHaveText("9007199254740993");
    await expect(rows.nth(3)).toContainText("total unknown");
    await expect(rows.nth(4)).toContainText("conflicting");
    await expect(rows.nth(5)).toContainText("Unavailable");
    await expect(rows.nth(6)).toContainText("incomplete");
    await expect(panel(page).getByRole("link", { name: origin, exact: true })).toHaveAttribute("href", `/api/v1/manifests/${origin}`);
    await expect(panel(page)).toContainText("verified bytes are not execution attestation or assurance acceptance");
    await expect(panel(page)).not.toContainText("ai_credits");
    expect(methods).toEqual(["GET"]);
  });

  test("clears stale evidence and download links after current source verification fails", async ({ page }) => {
    let reads = 0;
    await page.route(`**/api/v1/evidence/${digest}`, (route) => {
      reads++;
      return reads === 1 ? route.fulfill({ contentType: "application/json", body: fixture }) : route.fulfill({
        status: 409, json: {
          evidence_sha256: digest, publication: "derived", integrity: "failed",
          verification: "content-and-declared-context-only", sources: [],
          diagnostics: [{ code: "corrupt_source", message: "Current evidence or source bytes do not match their immutable declaration." }],
        },
      });
    });
    await inspect(page);
    await expect(panel(page).getByRole("row")).toHaveCount(7);
    await panel(page).getByRole("button", { name: "Inspect evidence", exact: true }).click();
    await expect(panel(page)).toContainText("Category evidence is withheld");
    await expect(panel(page).getByRole("row")).toHaveCount(0);
    await expect(panel(page).getByRole("link", { name: "Download evidence JSON" })).toHaveCount(0);
  });

  test("rejects ambiguous JSON and impossible integer counts without displaying source text", async ({ page }) => {
    for (const body of [
      fixture.replace('"observed_count":9007199254740993', '"observed_count":9223372036854775808'),
      fixture.replace('"observed_count":0', '"observed_count":0,"observed_count":1'),
      "https://private-provider.example/secret /private/operator/store",
    ]) {
      await page.route(`**/api/v1/evidence/${digest}`, (route) => route.fulfill({ contentType: "application/json", body }));
      await inspect(page);
      await expect(panel(page).getByRole("alert")).toContainText("malformed, inconsistent, or unverified");
      await expect(panel(page).getByRole("row")).toHaveCount(0);
      await expect(panel(page)).not.toContainText("private-provider");
      await page.unroute(`**/api/v1/evidence/${digest}`);
    }
  });

  test("typing a new digest aborts and clears the prior receipt", async ({ page }) => {
    await page.route(`**/api/v1/evidence/${digest}`, (route) => route.fulfill({ contentType: "application/json", body: fixture }));
    await inspect(page);
    await expect(panel(page).getByRole("row")).toHaveCount(7);
    await panel(page).getByRole("textbox", { name: "Evidence SHA-256 digest" }).fill("c".repeat(64));
    await expect(panel(page).getByRole("row")).toHaveCount(0);
    await expect(panel(page).getByRole("link", { name: "Download evidence CSV" })).toHaveCount(0);
  });

  test("downloads exactly the producer-proven JSON and CSV without numeric rounding", async ({ page }) => {
    await page.route(`**/api/v1/evidence/${digest}`, (route) => route.fulfill({ contentType: "application/json", body: fixture }));
    await page.route(`**/api/v1/evidence/${digest}/document`, (route) => route.fulfill({
      contentType: "application/json", headers: { "Content-Disposition": `attachment; filename="execution-evidence-${digest}.json"` }, body: documentFixture,
    }));
    await page.route(`**/api/v1/evidence/${digest}/csv`, (route) => route.fulfill({
      contentType: "text/csv", headers: { "Content-Disposition": `attachment; filename="execution-evidence-${digest}.csv"` }, body: csvFixture,
    }));
    await inspect(page);
    for (const [name, content] of [["Download evidence JSON", documentFixture], ["Download evidence CSV", csvFixture]]) {
      const [download] = await Promise.all([
        page.waitForEvent("download"), panel(page).getByRole("link", { name, exact: true }).click(),
      ]);
      const path = await download.path();
      if (path === null) throw new Error("Downloaded evidence file is unavailable.");
      expect(fs.readFileSync(path, "utf8")).toBe(content);
      expect(content).toContain("9007199254740993");
    }
  });

  test("synthetic demo does not reconstruct recorded evidence", async ({ page }) => {
    let requests = 0;
    await page.route("**/api/v1/evidence/**", (route) => {
      requests++;
      return route.abort();
    });
    await page.goto("/#/lab/demo/runner");
    await expect(panel(page)).toContainText("unavailable in the synthetic demo");
    await expect(panel(page).getByRole("textbox")).toHaveCount(0);
    await expect(panel(page).getByRole("row")).toHaveCount(0);
    expect(requests).toBe(0);
  });
});
