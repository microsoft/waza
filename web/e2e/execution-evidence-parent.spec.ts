import { expect, test } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { startEvidenceHTTP } from "./helpers/evidence-http";

interface Fixture { digest: string; declaration: string; categories: string; csv: string; status: number }
let directory: string;
let fixtures: Record<string, Fixture>;
test.describe("Actual protected retry category context", () => {
  test.describe.configure({ mode: "serial" });
  test.beforeAll(() => {
    test.setTimeout(180_000);
    directory = mkdtempSync(join(tmpdir(), "waza-parent-category-browser-"));
    const output = join(directory, "fixtures.json");
    execFileSync("go", ["test", "-p=1", "./cmd/waza",
      "-run", "^TestExecutionEvidenceParentContextActualPublicProducerAndDegradation$", "-count=1", "-timeout=2m"], {
      cwd: resolve(".."), timeout: 150_000, env: { ...process.env,
        WAZA_PARENT_CATEGORY_FIXTURES: output, COPILOT_CLI_PATH: "/usr/bin/false",
        ENABLE_COPILOT_TESTS: "false", ENABLE_AZURE_TESTS: "false",
        ENABLE_LIVE_TESTS: "false", ENABLE_LIVE_BILLING_TESTS: "false", NO_COLOR: "1", GOMAXPROCS: "2" },
    });
    fixtures = JSON.parse(readFileSync(output, "utf8"));
    expect(Object.keys(fixtures)).toHaveLength(8);
  });
  test.afterAll(() => { if (directory) rmSync(directory, { recursive: true, force: true }); });

  for (const control of ["verified", "abandoned", "corrupt", "conflicting", "branch", "no-parent", "failed", "canceled"]) {
    test(`${control}: preserves native facts and only qualified one-hop links`, async ({ page, baseURL }) => {
      const fixture = fixtures[control];
      const declaration = JSON.parse(fixture.declaration);
      const document = JSON.stringify(declaration.document);
      if (!baseURL) throw new Error("The configured frontend preview URL is required.");
      const http = await startEvidenceHTTP(baseURL, fixture.digest, fixture.declaration, document, "", {
        receipt: fixture.categories, csv: fixture.csv, status: fixture.status,
      });
      try {
        await page.goto(`${http.base}/#/lab/runner`);
        const panel = page.getByRole("region", { name: "Execution evidence inspection", exact: true });
        await panel.getByRole("textbox", { name: "Evidence SHA-256 digest" }).fill(fixture.digest);
        await panel.getByRole("button", { name: "Inspect evidence", exact: true }).click();
        await panel.getByRole("button", { name: "Admit recorded categories", exact: true }).click();
        const categories = panel.getByRole("region", { name: "Recorded category admission", exact: true });
        await expect(categories.getByRole("row")).toHaveCount(7);
        await expect(panel).toContainText("not execution attestation or assurance acceptance");
        const view = JSON.parse(fixture.categories);
        const parent = view.source_context?.parent_context;
        if (parent) await expect(categories.getByLabel("Immediate parent context")).toContainText(parent.state);
        else await expect(categories.getByLabel("Immediate parent context")).toHaveCount(0);
        const parentLink = categories.getByRole("link", { name: "Verified immediate parent manifest" });
        if (parent?.source && fixture.status === 200) {
          await expect(parentLink).toHaveAttribute("href", `/api/v1/manifests/${parent.source.manifest_digest}`);
        } else await expect(parentLink).toHaveCount(0);
        if (fixture.status !== 200) {
          await expect(panel.getByRole("link")).toHaveCount(0);
          for (const suffix of ["document", "csv"]) {
            const response = await page.request.get(`${http.base}/api/v1/evidence/${fixture.digest}/categories/${suffix}`);
            expect(response.status()).toBe(409);
            expect(response.headers()["content-disposition"]).toBeUndefined();
            expect(await response.text()).toBe(fixture.categories);
          }
        } else {
          for (const [label, bytes] of [["JSON", fixture.categories], ["CSV", fixture.csv]]) {
            const download = page.waitForEvent("download");
            await categories.getByRole("link", { name: `Download qualified category ${label}`, exact: true }).click();
            const path = await (await download).path();
            if (!path) throw new Error("Qualified download did not produce a local file.");
            expect(readFileSync(path, "utf8")).toBe(bytes);
          }
        }
        expect(http.failures).toEqual([]);
      } finally { await http.close(); }
    });
  }
});
