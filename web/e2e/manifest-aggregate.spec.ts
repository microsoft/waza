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
  let actual: ManifestAggregate;
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
    for (const trial of [1, 2]) {
      const terminal = execFileSync(binary, ["runner", "run"], {
        cwd: directory, env, input: JSON.stringify({ ...captured, selection: { ...captured.selection, trial } }),
      });
      const result = JSON.parse(terminal.toString());
      expect(result.outcome).toBe("success");
      expect(result.observation.validations).toHaveLength(1);
      const terminalPath = join(directory, `terminal-${trial}.json`);
      const manifestPath = join(directory, `manifest-${trial}.json`);
      writeFileSync(terminalPath, terminal, { flag: "wx", mode: 0o600 });
      writeFileSync(manifestPath, JSON.stringify({
        kind: "waza.artifact-manifest", schema_version: "1.0",
        binding: { identity: result.identity, selection: result.selection,
          execution_mode: result.observation.execution_mode, grader_ids: result.observation.validations.map((v: { identifier: string }) => v.identifier) },
        terminal_artifact: "terminal",
        artifacts: [{ name: "terminal", sha256: createHash("sha256").update(terminal).digest("hex"),
          size_bytes: terminal.length, media_type: "application/json" }],
      }), { flag: "wx", mode: 0o600 });
      const create = JSON.parse(execFileSync(binary, ["artifact", "create", manifestPath, "--store", store], { cwd: directory, env }).toString());
      digests.push(create.manifest_sha256);
      execFileSync(binary, ["artifact", "upload", manifestPath, "terminal", terminalPath, "--store", store], { cwd: directory, env });
      execFileSync(binary, ["artifact", "finalize", create.manifest_sha256, "--store", store], { cwd: directory, env });
    }
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
});
