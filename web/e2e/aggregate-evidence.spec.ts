import { expect, test } from "@playwright/test";
import { createServer, type ViteDevServer } from "vite";
import { execFileSync } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

interface Fixture {
  inputs: { manifest_sha256: string; evidence_sha256?: string }[];
  canonical: string;
}

test.describe("Aggregate evidence states and lineage", () => {
  let server: ViteDevServer;
  let base: string;
  let directory: string;
  let fixture: Fixture;

  test.beforeAll(async () => {
    test.setTimeout(120_000);
    directory = await mkdtemp(join(tmpdir(), "waza-evidence-aggregate-"));
    const output = join(directory, "evidence.json");
    execFileSync("go", ["test", "-count=1", "-run", "^TestEvidenceStatesLabFixture$",
      "./internal/manifestaggregate", "-args", "-aggregate-evidence-fixture-output", output], {
      cwd: resolve(".."), timeout: 120_000,
    });
    fixture = JSON.parse(await readFile(output, "utf8"));
    server = await createServer({ server: { host: "127.0.0.1", port: 0 } });
    await server.listen();
    const address = server.httpServer?.address();
    if (!address || typeof address === "string") throw new Error("Aggregate harness listener unavailable.");
    base = `http://127.0.0.1:${address.port}`;
  });
  test.afterAll(async () => {
    await server?.close();
    if (directory) await rm(directory, { recursive: true, force: true });
  });

  test("explains every state, marks degradation and links retained lineage", async ({ page }) => {
    const report = JSON.parse(fixture.canonical);
    expect(report.state).toBe("conflicting");
    const resumed = report.members.find((member: { lineage?: unknown[] }) => member.lineage);
    expect(resumed.parent.relationship).toBe("retry");
    await page.route("**/api/v1/aggregates", (route) =>
      route.fulfill({ status: 409, contentType: "application/json", body: fixture.canonical }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(fixture.inputs.map((input) =>
      `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n"));
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    const status = page.getByRole("status", { name: "Evaluated manifest aggregate" });
    await expect(status).toContainText("Aggregate admission: conflicting");

    const legend = page.getByLabel("Evidence state legend");
    for (const state of ["present", "unavailable", "incomplete", "redacted", "conflicting"]) {
      await expect(legend.getByText(state, { exact: true })).toBeVisible();
    }
    await expect(legend).toContainText("observed count of 0 means recorded as empty");

    const tables = page.getByRole("region", { name: "Aggregate category states" });
    await expect(tables.getByText("redacted", { exact: true }).first()).toBeVisible();
    await expect(tables.getByText("incomplete", { exact: true }).first()).toBeVisible();
    await expect(tables.getByText("object_size_limit")).toBeVisible();
    await expect(tables.getByText("privacy").first()).toBeVisible();
    await expect(tables.getByText(/declared redacted/).first()).toBeVisible();

    await expect(status).toContainText(`Parent execution (retry)${resumed.parent.execution_id}`);
    const lineage = page.getByRole("list", { name: "Evidence lineage" });
    const link = lineage.getByRole("link", { name: resumed.lineage[0].manifest });
    await expect(lineage).toContainText("resume:");
    await expect(link).toHaveAttribute("href", `/api/v1/manifests/${resumed.lineage[0].manifest}`);
    await expect(page.getByText("conflicting_source")).toBeVisible();
  });
});
