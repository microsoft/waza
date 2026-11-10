import { expect, test } from "@playwright/test";
import { createServer, type ViteDevServer } from "vite";
import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const digest = "a".repeat(64);

function omittedReport(state = "incomplete", memberState = "omitted") {
  return {
    kind: "waza.manifest-aggregate", schema_version: "1.0", state,
    verification: "current-content-and-original-record-context-only",
    members: [{
      manifest_sha256: digest, state: memberState, categories: [],
      diagnostics: [memberState === "conflicting" ? "conflicting_source" : "read_budget_exhausted"],
    }],
    groups: [],
    counts: {
      input_members: 1, unique_verified_manifests: 0, unique_verified_execution_records: 0,
      member_states: { [memberState]: 1 }, member_outcomes: {},
    },
    usage: {
      state: "unavailable", population: "unique-verified-execution-records", executions: 0,
      recorded_historical_usage: null, diagnostics: ["complete_historical_usage_unavailable"],
    },
  };
}

test.describe("Actual aggregate fetch, component and download callers", () => {
  let server: ViteDevServer;
  let base: string;

  test.beforeAll(async () => {
    server = await createServer({ server: { host: "127.0.0.1", port: 0 } });
    await server.listen();
    const address = server.httpServer?.address();
    if (!address || typeof address === "string") throw new Error("Aggregate harness listener unavailable.");
    base = `http://127.0.0.1:${address.port}`;
  });
  test.afterAll(async () => { await server?.close(); });

  test("rejects a matching-count omitted member disguised as complete", async ({ page }) => {
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ json: omittedReport("complete") }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(digest);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("inconsistent");
    await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toHaveCount(0);
  });

  test("rejects conflicting members disguised as merely incomplete", async ({ page }) => {
    const report = omittedReport("incomplete", "conflicting");
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, json: report }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(digest);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("inconsistent");
  });

  test("rejects a usage execution count differing from the unique record population", async ({ page }) => {
    const report = omittedReport();
    report.usage.executions = 1;
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, json: report }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(digest);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("inconsistent");
  });

  test("rejects complete usage attached to incomplete aggregate admission", async ({ page }) => {
    const report = {
      ...omittedReport(),
      members: [{ ...omittedReport().members[0]!, execution_id: `exec-${digest}` }],
      counts: { ...omittedReport().counts, unique_verified_execution_records: 1 },
      usage: {
        ...omittedReport().usage, state: "complete", executions: 1, diagnostics: [],
        recorded_historical_usage: {
          turns: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, premium_requests: 0,
        },
      },
    };
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, json: report }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(digest);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("inconsistent");
  });

  for (const [state, memberState] of [["incomplete", "omitted"], ["conflicting", "conflicting"]]) {
    test(`retains and downloads a valid ${state} evaluated response`, async ({ page }) => {
      const canonical = JSON.stringify(omittedReport(state, memberState));
      await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, contentType: "application/json", body: canonical }));
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      await page.getByRole("textbox").fill(digest);
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText(`Aggregate admission: ${state}`);
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download aggregate JSON" }).click();
      const stream = await (await download).createReadStream();
      const chunks: Buffer[] = [];
      for await (const chunk of stream) chunks.push(Buffer.from(chunk));
      expect(Buffer.concat(chunks).toString("utf8")).toBe(canonical);
    });
  }

  test("preserves conflicting priority when a later member is omitted", async ({ page }) => {
    const report = omittedReport("conflicting", "conflicting");
    const missing = { ...omittedReport().members[0]!, manifest_sha256: "b".repeat(64) };
    report.members.push(missing);
    report.counts.input_members = 2;
    report.counts.member_states.omitted = 1;
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, json: report }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(`${digest}\n${missing.manifest_sha256}`);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText("Aggregate admission: conflicting");
  });

  for (const extra of [0, 1]) {
    test(`enforces the exact 32 MiB aggregate response boundary (${extra} excess bytes)`, async ({ page }) => {
      const report = JSON.stringify(omittedReport());
      const wire = " ".repeat(32 * 1024 * 1024 - report.length + extra) + report;
      expect(Buffer.byteLength(wire)).toBe(32 * 1024 * 1024 + extra);
      await page.route("**/api/v1/aggregates", (route) => route.fulfill({ status: 409, contentType: "application/json", body: wire }));
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      await page.getByRole("textbox").fill(digest);
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      if (extra) await expect(page.getByRole("alert")).toContainText("malformed JSON");
      else await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText("Aggregate admission: incomplete");
    });
  }

  test("uses UTF-8 bytes for the aggregate limit, retaining the unrelated default limit", async ({ page }) => {
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({
      contentType: "application/json", body: `"${"é".repeat(16 * 1024 * 1024)}"`,
    }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(digest);
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("malformed JSON");
    const defaultBoundary = await page.evaluate(async () => {
      const moduleURL = "/src/lib/strictJson.ts";
      const { parseStrictJSON } = await import(moduleURL);
      const accepted = parseStrictJSON(" ".repeat(16 * 1024 * 1024 - 1) + "0") === 0;
      let rejected = false;
      try { parseStrictJSON(" ".repeat(16 * 1024 * 1024) + "0"); }
      catch { rejected = true; }
      return { accepted, rejected };
    });
    expect(defaultBoundary).toEqual({ accepted: true, rejected: true });
  });

  test("consumes and downloads actual canonical public output between 16 and 32 MiB", async ({ page }) => {
    test.setTimeout(240_000);
    const directory = await mkdtemp(join(tmpdir(), "waza-aggregate-caller-"));
    try {
      const output = join(directory, "canonical.json");
      execFileSync("go", ["test", "-count=1", "-run", "^TestLargeCanonicalPublicReportFitsAggregateProfile$",
        "./internal/manifestaggregate", "-args", "-aggregate-browser-fixture-output", output], {
        cwd: resolve(".."), timeout: 180_000, env: { ...process.env, NO_COLOR: "1" },
      });
      const canonical = await readFile(output);
      expect(canonical.length).toBeGreaterThan(16 * 1024 * 1024);
      expect(canonical.length).toBeLessThanOrEqual(32 * 1024 * 1024);
      const report = JSON.parse(canonical.toString("utf8")) as { members: { manifest_sha256: string }[] };
      expect(report.members).toHaveLength(128);
      await page.route("**/api/v1/aggregates", (route) => route.fulfill({ contentType: "application/json", body: canonical }));
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      await page.getByRole("textbox").fill(report.members.map((member) => member.manifest_sha256).join("\n"));
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      const result = page.getByRole("status", { name: "Evaluated manifest aggregate" });
      await expect(result).toContainText("Aggregate admission: complete", { timeout: 30_000 });
      await expect(result.getByRole("link", { name: "Inspect source manifest" })).toHaveCount(128);
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download aggregate JSON" }).click();
      const stream = await (await download).createReadStream();
      const chunks: Buffer[] = [];
      for await (const chunk of stream) chunks.push(Buffer.from(chunk));
      const downloaded = Buffer.concat(chunks);
      expect(downloaded.length).toBe(canonical.length);
      expect(createHash("sha256").update(downloaded).digest("hex")).toBe(createHash("sha256").update(canonical).digest("hex"));
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
});
