import { expect, test } from "@playwright/test";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import type { ManifestAggregate } from "../src/api/aggregates";

test.describe("Completed manifest aggregation through real publication and API", () => {
  test.describe.configure({ mode: "serial" });
  let directory: string;
  let binary: string;
  let base: string;
  let server: ChildProcess;
  let digests: string[];
  let unicodeDigest: string;
  let actual: ManifestAggregate;
  let protectedSource: { manifest_sha256: string; evidence_sha256: string; execution_id: string };
  let native: ManifestAggregate;
  const missing = "f".repeat(64);
  const env = { ...process.env, COPILOT_CLI_PATH: "/usr/bin/false", ENABLE_COPILOT_TESTS: "false",
    ENABLE_LIVE_COPILOT_TESTS: "false", WAZA_NO_UPDATE_CHECK: "1", NO_COLOR: "1" };

  test.beforeAll(async ({ request }) => {
    test.setTimeout(180_000);
    expect(() => execFileSync("/usr/bin/false")).toThrow();
    directory = mkdtempSync(join(tmpdir(), "waza-aggregate-browser-"));
    binary = join(directory, "waza");
    execFileSync("go", ["build", "-o", binary, "./cmd/waza"], { cwd: resolve(".."), env, timeout: 150_000 });
    const results = join(directory, "results");
    const store = join(results, ".waza-artifacts");
    mkdirSync(store, { recursive: true, mode: 0o700 });
    const captured = JSON.parse(readFileSync(new URL("../../examples/runner/unit.json", import.meta.url), "utf8"));
    digests = [];
    const publishTerminal = (terminal: Buffer, label: string) => {
      const result = JSON.parse(terminal.toString());
      const terminalPath = join(directory, `terminal-${label}.json`);
      const manifestPath = join(directory, `manifest-${label}.json`);
      writeFileSync(terminalPath, terminal, { flag: "wx", mode: 0o600 });
      writeFileSync(manifestPath, JSON.stringify({
        kind: "waza.artifact-manifest", schema_version: "1.0",
        binding: { identity: result.identity, selection: result.selection,
          execution_mode: result.observation.execution_mode, grader_ids: result.observation.validations.map((v: { identifier: string }) => v.identifier) },
        terminal_artifact: "terminal",
        artifacts: [{ name: "terminal", sha256: createHash("sha256").update(terminal).digest("hex"),
          size_bytes: terminal.length, media_type: "application/json" }],
      }), { flag: "wx", mode: 0o600 });
      const create: { manifest_sha256: string } = JSON.parse(execFileSync(binary, ["artifact", "create", manifestPath, "--store", store], { cwd: directory, env }).toString());
      expect(create.manifest_sha256).toMatch(/^[0-9a-f]{64}$/);
      execFileSync(binary, ["artifact", "upload", manifestPath, "terminal", terminalPath, "--store", store], { cwd: directory, env });
      execFileSync(binary, ["artifact", "finalize", create.manifest_sha256, "--store", store], { cwd: directory, env });
      return create.manifest_sha256;
    };
    for (const trial of [1, 2]) {
      const terminal = execFileSync(binary, ["runner", "run"], {
        cwd: directory, env, input: JSON.stringify({ ...captured, selection: { ...captured.selection, trial } }),
      });
      const result = JSON.parse(terminal.toString());
      expect(result.outcome).toBe("success");
      expect(result.observation.validations).toHaveLength(1);
      digests.push(publishTerminal(terminal, String(trial)));
      if (trial === 2) {
        // Authored identifier control; publication verifies bytes/binding, not live grading.
        result.observation.validations = [
          { identifier: "\u{1f600}", passed: true }, { identifier: "\ue000", passed: true },
        ];
        unicodeDigest = publishTerminal(Buffer.from(JSON.stringify(result)), "unicode");
      }
    }
    protectedSource = JSON.parse(execFileSync("go", ["run",
      "./internal/manifestaggregate/testdata/protected-producer", store,
      resolve("../examples/runner/unit.json")], { cwd: resolve(".."), env, timeout: 150_000 }).toString());
    expect(protectedSource.execution_id).toMatch(/^exec-[0-9a-f]{64}$/);
    const listener = createServer();
    await new Promise<void>((resolve) => listener.listen(0, "127.0.0.1", resolve));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("Missing listener port");
    const port = address.port;
    await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
    base = `http://127.0.0.1:${port}`;
    server = spawn(binary, ["serve", "--no-browser", "--lab", "--port", String(port), "--results-dir", results], {
      cwd: directory, env, stdio: "ignore",
    });
    await expect.poll(async () => {
      if (server.exitCode !== null) throw new Error(`Aggregate API server exited ${server.exitCode}`);
      try { return (await request.post(`${base}/api/v1/aggregates`, { data: { manifests: digests } })).status(); }
      catch { return "not-ready"; }
    }, { timeout: 15_000 }).toBe(200);
    const response = await request.post(`${base}/api/v1/aggregates`, { data: { manifests: digests } });
    actual = await response.json();
    expect(actual.members).toHaveLength(2);
    expect(actual.state_counts).toEqual({ success: 2 });
    const cli = execFileSync(binary, ["aggregate", "--store", store, ...digests], { cwd: directory, env }).toString().trim();
    expect(JSON.parse(cli)).toEqual(actual);
    const nativeResponse = await request.post(`${base}/api/v1/aggregates`, { data: {
      manifests: [protectedSource.manifest_sha256],
      evidence: [{ manifest_sha256: protectedSource.manifest_sha256, evidence_sha256: protectedSource.evidence_sha256 }],
    } });
    expect(nativeResponse.status()).toBe(200);
    native = await nativeResponse.json();
    const candidate = native.evidence!.members[0]!.candidates[0]!;
    expect(candidate.categories).toHaveLength(6);
    expect(candidate.source_context).toEqual({
      state: "verified", verification: "content-and-declared-context-only",
      source: { execution_id: protectedSource.execution_id, manifest_digest: protectedSource.manifest_sha256 },
    });
  });

  test.afterAll(async () => {
    if (server && server.exitCode === null) {
      const exited = new Promise((resolve) => server.once("exit", resolve));
      server.kill("SIGTERM");
      await exited;
    }
    if (directory) rmSync(directory, { recursive: true, force: true });
  });

  test("renders exact producer membership, reordering, duplicate collapse, source links and downloaded report", async ({ page }) => {
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Completed manifest aggregation" });
    await panel.getByRole("textbox", { name: "Expected manifest digests" }).fill([...digests].reverse().concat(digests).join("\n"));
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    const result = panel.getByRole("status", { name: "Aggregate composition" });
    await expect(result).toContainText("Membership: complete");
    await expect(result.getByRole("row")).toHaveCount(3);
    await expect(result.locator("dl dd")).toHaveText(["2"]);
    for (const digest of digests) {
      await expect(result.getByRole("link", { name: digest })).toHaveAttribute("href", `/api/v1/manifests/${digest}`);
    }
    await expect(result).toContainText("Usage and billing: unavailable");
    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Download aggregate JSON" }).click();
    const artifact = await download;
    expect(artifact.suggestedFilename()).toBe("manifest-aggregate.json");
    expect(JSON.parse(readFileSync((await artifact.path())!, "utf8"))).toEqual(actual);
  });

  test("renders actual protected source context and native counts without billing attestation", async ({ page, request }) => {
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Completed manifest aggregation" });
    await panel.getByRole("textbox", { name: "Expected manifest digests" }).fill(protectedSource.manifest_sha256);
    await panel.getByRole("textbox", { name: "Optional explicit evidence pairs" }).fill(
      `${protectedSource.manifest_sha256}=${protectedSource.evidence_sha256}`);
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    const result = panel.getByRole("status", { name: "Aggregate composition" });
    await expect(result).toContainText("Original source context: verified");
    await expect(result).toContainText(protectedSource.execution_id);
    await expect(result).toContainText("not creator authority, runtime or billing attestation");
    await expect(result).toContainText("Usage totals are withheld");
    for (const category of native.evidence!.members[0]!.candidates[0]!.categories) {
      const row = result.getByRole("heading", { name: "Native category admission", exact: true })
        .locator("xpath=following-sibling::dl[1]").locator("div")
        .filter({ has: page.locator(`dt:text-is("${category.category}")`) });
      await expect(row).toHaveCount(1);
      await expect(row).toContainText(`observed count: ${category.observed_count ?? "Unavailable"}`);
    }
    const csvResponse = await request.post(`${base}/api/v1/aggregates?format=csv`, { data: {
      manifests: [protectedSource.manifest_sha256],
      evidence: [{ manifest_sha256: protectedSource.manifest_sha256, evidence_sha256: protectedSource.evidence_sha256 }],
    } });
    expect(csvResponse.status()).toBe(200);
    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Download aggregate CSV" }).click();
    expect(readFileSync((await (await download).path())!, "utf8")).toBe(await csvResponse.text());
    const contradictory = structuredClone(native);
    contradictory.evidence!.members[0]!.candidates[0]!.source_context!.state = "missing";
    contradictory.evidence!.members[0]!.candidates[0]!.source_context!.verification = "unverified";
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ json: contradictory }));
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    await expect(result).toHaveCount(0);
    await expect(panel.getByRole("alert")).toContainText("contradictory original source context");
  });

  test("preserves a missing expected source and clears success after an invalid reload", async ({ page }) => {
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Completed manifest aggregation" });
    await panel.getByRole("textbox", { name: "Expected manifest digests" }).fill([...digests, missing].join("\n"));
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    const result = panel.getByRole("status", { name: "Aggregate composition" });
    await expect(result).toContainText("Membership: incomplete");
    await expect(result.getByRole("row")).toHaveCount(4);
    await expect(result.locator("dl dt")).toHaveText(["missing", "success"]);
    await expect(result.locator("dl dd")).toHaveText(["1", "2"]);
    await expect(result.getByRole("row").filter({ hasText: missing })).toContainText("Unavailable");
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({
      json: { ...actual, state_counts: { success: 500 }, provider_url: "https://private.example/secret" },
    }));
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    await expect(result).toHaveCount(0);
    await expect(panel.getByRole("alert")).toContainText("invalid or inconsistent report");
    await expect(panel).not.toContainText("private.example");
  });

  test("rejects contradictory outcomes and duplicate verdicts without retaining success", async ({ page }) => {
    expect(actual.members[0]!.outcome).toBe("success");
    expect(actual.members[0]!.verdicts).toHaveLength(1);
    const contradictory = structuredClone(actual);
    contradictory.members[0]!.state = "evaluation_failure";
    contradictory.state_counts = { success: 1, evaluation_failure: 1 };
    const duplicate = structuredClone(actual);
    duplicate.members[0]!.verdicts!.push({ ...duplicate.members[0]!.verdicts![0]! });
    for (const control of [
      { report: contradictory, message: "contradictory recorded outcome" },
      { report: duplicate, message: "duplicate grader verdicts" },
    ]) {
      await page.goto(`${base}/#/lab/runner`);
      const panel = page.getByRole("region", { name: "Completed manifest aggregation" });
      await panel.getByRole("textbox", { name: "Expected manifest digests" }).fill(digests.join("\n"));
      await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
      await expect(panel.getByRole("status", { name: "Aggregate composition" })).toContainText("Membership: complete");
      await page.route("**/api/v1/aggregates", (route) => route.fulfill({ json: control.report }));
      await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
      await expect(panel.getByRole("status", { name: "Aggregate composition" })).toHaveCount(0);
      await expect(panel.getByRole("alert")).toContainText(control.message);
      await page.unroute("**/api/v1/aggregates");
    }
  });

  test("accepts native UTF-8 grader ordering without treating it as UTF-16 ordering", async ({ page, request }) => {
    const response = await request.post(`${base}/api/v1/aggregates`, { data: { manifests: [unicodeDigest] } });
    expect(response.status()).toBe(200);
    const unicode: ManifestAggregate = await response.json();
    expect(unicode.members).toHaveLength(1);
    expect(unicode.members[0]!.verdicts).toEqual([
      { identifier: "\ue000", passed: true }, { identifier: "\u{1f600}", passed: true },
    ]);
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Completed manifest aggregation" });
    await panel.getByRole("textbox", { name: "Expected manifest digests" }).fill(unicodeDigest);
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    const composition = panel.getByRole("status", { name: "Aggregate composition" });
    await expect(composition).toContainText("Membership: complete");
    await expect(composition.locator("td").nth(3).locator("p")).toHaveText(["\ue000: passed", "\u{1f600}: passed"]);
    unicode.members[0]!.verdicts!.reverse();
    await page.route("**/api/v1/aggregates", (route) => route.fulfill({ json: unicode }));
    await panel.getByRole("button", { name: "Aggregate manifests", exact: true }).click();
    await expect(composition).toHaveCount(0);
    await expect(panel.getByRole("alert")).toContainText("unsorted or duplicate grader verdicts");
  });
});
