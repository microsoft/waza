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
  let directory: string;
  let mixed: {
    inputs: { manifest_sha256: string; evidence_sha256?: string }[];
    canonical: string;
    forged: string;
    eligibility: {
      name: string;
      inputs: { manifest_sha256: string; evidence_sha256?: string }[];
      canonical: string;
      forged: string;
    }[];
  };
  let delayed: { wire: string; delay: number; started?: () => void; closed?: (completed: boolean) => void } | undefined;

  test.beforeAll(async () => {
    directory = await mkdtemp(join(tmpdir(), "waza-mixed-aggregate-"));
    const output = join(directory, "mixed.json");
    execFileSync("go", ["test", "-count=1", "-run", "^TestMixedIdentityPublicReportFixture$",
      "./internal/manifestaggregate", "-args", "-aggregate-mixed-identity-fixture-output", output], {
      cwd: resolve(".."), timeout: 120_000,
    });
    mixed = JSON.parse(await readFile(output, "utf8"));
    server = await createServer({
      server: { host: "127.0.0.1", port: 0 },
      plugins: [{
        name: "aggregate-delayed-http-controls",
        configureServer(vite) {
          vite.middlewares.use((request, response, next) => {
            const control = delayed;
            if (request.url !== "/api/v1/aggregates" || !control) return next();
            request.resume();
            const timer = setTimeout(() => {
              response.setHeader("Content-Type", "application/json");
              response.end(control.wire);
            }, control.delay);
            response.on("close", () => {
              clearTimeout(timer);
              control.closed?.(response.writableFinished);
            });
            control.started?.();
          });
        },
      }],
    });
    await server.listen();
    const address = server.httpServer?.address();
    if (!address || typeof address === "string") throw new Error("Aggregate harness listener unavailable.");
    base = `http://127.0.0.1:${address.port}`;
  });
  test.afterAll(async () => {
    await server?.close();
    if (directory) await rm(directory, { recursive: true, force: true });
  });

  test("rejects a producer-backed mixed identity subtotal and clears earlier valid admission", async ({ page }) => {
    const original = JSON.parse(mixed.canonical);
    const forged = JSON.parse(mixed.forged);
    expect(original.members).toHaveLength(2);
    expect(original.members.filter((member: { execution_id?: string }) => member.execution_id)).toHaveLength(1);
    expect(forged).toEqual({ ...original, usage: forged.usage });
    expect(original.usage.recorded_historical_usage).toBeNull();
    expect(forged.usage.recorded_historical_usage.ai_credits).toBe(1.25);
    let calls = 0;
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({
      contentType: "application/json", body: calls++ === 0 ? mixed.canonical : mixed.forged,
    }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(mixed.inputs.map((input) =>
      `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n"));
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText("Aggregate admission: complete");
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download aggregate JSON" }).click();
    expect(await readFile((await (await download).path())!)).toEqual(Buffer.from(mixed.canonical));
    await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("inconsistent");
    await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Download aggregate JSON" })).toHaveCount(0);
    expect(calls).toBe(2);
  });

  test("rejects producer-backed declared usage exclusions without excluding optional aggregate credits", async ({ page }) => {
    let response = "";
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ contentType: "application/json", body: response }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    for (const fixture of mixed.eligibility) {
      const original = JSON.parse(fixture.canonical);
      expect(original.members.every((member: { execution_id?: string }) => member.execution_id)).toBe(true);
      const references = fixture.inputs.map((input) =>
        `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n");
      await page.getByRole("textbox").fill(references);
      response = fixture.canonical;
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      const result = page.getByRole("status", { name: "Evaluated manifest aggregate" });
      await expect(result).toContainText("Aggregate admission: complete");
      if (fixture.name === "optional aggregate credits absent") {
        expect(original.usage.recorded_historical_usage.ai_credits).toBeUndefined();
        await expect(result.locator("dd").nth(5)).toHaveText("Unavailable");
      } else if (fixture.name === "optional aggregate attribution absent") {
        expect(original.usage.recorded_historical_usage.model_metrics).toBeUndefined();
        await expect(result.locator("dd").nth(5)).toHaveText("1.25");
      } else if (fixture.name === "recorded zero credits") {
        expect(original.usage.recorded_historical_usage.ai_credits).toBe(0);
        await expect(result.locator("dd").nth(5)).toHaveText("0");
      } else {
        expect(original.usage.recorded_historical_usage).toBeNull();
      }
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download aggregate JSON" }).click();
      expect(await readFile((await (await download).path())!)).toEqual(Buffer.from(fixture.canonical));
      if (fixture.forged) {
        const forged = JSON.parse(fixture.forged);
        expect(forged).toEqual({ ...original, usage: forged.usage });
        response = fixture.forged;
        await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
        await expect(page.getByRole("alert")).toContainText("inconsistent");
        await expect(result).toHaveCount(0);
      }
    }
    expect(mixed.eligibility).toHaveLength(6);
  });

  test("requires a verified single usage observation and honors any cached category", async ({ page }) => {
    const fixture = mixed.eligibility.find((control) => control.name === "optional aggregate credits absent")!;
    let response = fixture.canonical;
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ contentType: "application/json", body: response }));
    await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
    await page.getByRole("textbox").fill(fixture.inputs.map((input) =>
      `${input.manifest_sha256} ${input.evidence_sha256}`).join("\n"));
    for (const change of ["missing category", "cached category", "unverified", "empty count", "withheld declaration"]) {
      const report = JSON.parse(fixture.canonical);
      const member = report.members[0];
      const usage = member.categories.find((category: { category: string }) => category.category === "usage");
      expect(usage.observed_count).toBe(1);
      if (change === "missing category") member.categories = [];
      else if (change === "cached category") member.categories[0].source = "cached";
      else if (change === "unverified") usage.verification = "declared-state-only";
      else if (change === "empty count") usage.observed_count = 0;
      else usage.declared_state = "redacted";
      response = JSON.stringify(report);
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("alert")).toContainText("inconsistent");
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toHaveCount(0);
    }
  });

  test("delayed valid HTTP response outlives the old client deadline and downloads exact bytes", async ({ page }) => {
    test.setTimeout(60_000);
    delayed = { wire: mixed.canonical, delay: 36_000 };
    try {
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      await page.getByRole("textbox").fill(mixed.inputs.map((input) =>
        `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n"));
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" }))
        .toContainText("Aggregate admission: complete", { timeout: 40_000 });
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download aggregate JSON" }).click();
      expect(await readFile((await (await download).path())!)).toEqual(Buffer.from(mixed.canonical));
    } finally {
      delayed = undefined;
    }
  });

  test("caller edits abort actual HTTP and never restore stale aggregate data", async ({ page }) => {
    let started: (() => void) | undefined;
    let closed: ((completed: boolean) => void) | undefined;
    const requestStarted = new Promise<void>((done) => { started = done; });
    const requestClosed = new Promise<boolean>((done) => { closed = done; });
    delayed = { wire: mixed.canonical, delay: 60_000, started: () => started?.(), closed: (complete) => closed?.(complete) };
    try {
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      const references = mixed.inputs.map((input) =>
        `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n");
      await page.getByRole("textbox").fill(references);
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await requestStarted;
      await page.getByRole("textbox").fill(`${references}\n`);
      expect(await requestClosed).toBe(false);
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toHaveCount(0);
      await expect(page.getByRole("alert")).toHaveCount(0);
      delayed.delay = 0;
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText("Aggregate admission: complete");
    } finally {
      delayed = undefined;
    }
  });

  test("finite client timeout clears previous report and closes actual HTTP", async ({ page }) => {
    test.setTimeout(75_000);
    let closed: ((completed: boolean) => void) | undefined;
    const requestClosed = new Promise<boolean>((done) => { closed = done; });
    delayed = { wire: mixed.canonical, delay: 0 };
    try {
      await page.goto(`${base}/e2e/fixtures/aggregate-harness.html`);
      await page.getByRole("textbox").fill(mixed.inputs.map((input) =>
        `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n"));
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toContainText("Aggregate admission: complete");
      delayed = { wire: mixed.canonical, delay: 60_000, closed: (complete) => closed?.(complete) };
      await page.getByRole("button", { name: "Build aggregate", exact: true }).click();
      await expect(page.getByRole("status", { name: "Evaluated manifest aggregate" })).toHaveCount(0);
      await expect(page.getByRole("alert")).toContainText("could not be loaded", { timeout: 55_000 });
      expect(await requestClosed).toBe(false);
      await expect(page.getByRole("button", { name: "Download aggregate JSON" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Build aggregate", exact: true })).toBeEnabled();
    } finally {
      delayed = undefined;
    }
  });

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
