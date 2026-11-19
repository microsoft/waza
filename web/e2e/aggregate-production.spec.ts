import { expect, test } from "@playwright/test";
import { execFileSync, spawn, spawnSync, type ChildProcess } from "node:child_process";
import { mkdtemp, readFile, rename, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test.describe("Configured production aggregate CLI, server and embedded Lab", () => {
  test.describe.configure({ mode: "serial" });
  let directory: string;
  let fixture: string;
  let binary: string;
  let base: string;
  let server: ChildProcess;
  let canonical: Buffer;
  let inputs: { manifest_sha256: string; evidence_sha256: string }[];
  let output = "";
  const env = {
    ...process.env, COPILOT_CLI_PATH: "/usr/bin/false", WAZA_NO_UPDATE_CHECK: "1",
    ENABLE_COPILOT_TESTS: "false", ENABLE_COPILOT_SDK_TESTS: "false", ENABLE_LIVE_COPILOT_TESTS: "false",
  };

  test.beforeAll(async ({ request }) => {
    test.setTimeout(180_000);
    directory = await mkdtemp(join(tmpdir(), "waza-aggregate-production-"));
    fixture = join(directory, "fixture");
    binary = join(directory, "waza");
    execFileSync("go", ["test", "-count=1", "-run", "^TestAggregateProductionConfiguredSourceAndBrowserFixture$",
      "./internal/webserver", "-args", `-aggregate-production-fixture-output=${fixture}`], {
      cwd: resolve(".."), env, timeout: 120_000,
    });
    execFileSync("go", ["build", "-o", binary, "./cmd/waza"], { cwd: resolve(".."), env, timeout: 120_000 });
    inputs = JSON.parse(await readFile(join(fixture, "inputs.json"), "utf8"));
    canonical = await readFile(join(fixture, "canonical.json"));
    expect(inputs).toHaveLength(1);
    const saved = join(directory, "saved-cli.json");
    const cli = spawnSync(binary, ["aggregate", "--store", join(fixture, "results", ".waza-artifacts"),
      "--inputs", join(fixture, "inputs.json"), "-o", saved], {
      cwd: directory, env: { ...env, WAZA_NO_UPDATE_CHECK: "" }, timeout: 15_000,
    });
    expect(cli.error).toBeUndefined();
    expect(cli.status, cli.stderr.toString()).toBe(0);
    expect(await readFile(saved)).toEqual(canonical);
    expect(cli.stdout).toEqual(Buffer.concat([canonical, Buffer.from("\n")]));
    const listener = createServer();
    await new Promise<void>((done) => listener.listen(0, "127.0.0.1", done));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("Missing aggregate test listener");
    await new Promise<void>((done, reject) => listener.close((error) => error ? reject(error) : done()));
    base = `http://127.0.0.1:${address.port}`;
    server = spawn(binary, ["serve", "--no-browser", "--lab", "--port", String(address.port),
      "--results-dir", join(fixture, "results")], { cwd: directory, env, stdio: ["ignore", "pipe", "pipe"] });
    server.stdout?.on("data", (chunk: Buffer) => { output += chunk.toString(); });
    server.stderr?.on("data", (chunk: Buffer) => { output += chunk.toString(); });
    await expect.poll(async () => {
      if (server.exitCode !== null) throw new Error(`Aggregate server exited: ${output}`);
      try { return (await request.post(`${base}/api/v1/aggregates`, { data: inputs })).status(); }
      catch { return 0; }
    }, { timeout: 15_000 }).toBe(200);
  });

  test.afterAll(async () => {
    if (server && server.exitCode === null) {
      const exited = new Promise((done) => server.once("exit", done));
      server.kill("SIGTERM");
      await exited;
    }
    if (directory) await rm(directory, { recursive: true, force: true });
  });

  test("saved CLI bytes reach real Lab, source inspection and exact download", async ({ page }) => {
    const index = await page.request.get(base);
    expect(await index.text()).toBe(await readFile(resolve("dist/index.html"), "utf8"));
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Standalone manifest aggregation" });
    await panel.getByRole("textbox").fill(`${inputs[0]!.manifest_sha256} ${inputs[0]!.evidence_sha256}`);
    const response = page.waitForResponse(`${base}/api/v1/aggregates`);
    await panel.getByRole("button", { name: "Build aggregate", exact: true }).click();
    expect(await (await response).body()).toEqual(canonical);
    const receipt = panel.getByRole("status", { name: "Evaluated manifest aggregate" });
    await expect(receipt.getByRole("heading", { name: "Aggregate admission: complete", exact: true })).toBeVisible();
    await expect(receipt.locator("dd").nth(2)).toHaveText("1");
    await expect(receipt.locator("dd").nth(5)).toHaveText("1.25");
    const link = receipt.getByRole("link", { name: "Inspect source manifest" });
    const inspected = await page.request.get(`${base}${await link.getAttribute("href")}`);
    expect(inspected.status()).toBe(200);
    expect(await inspected.json()).toMatchObject({ manifest_sha256: inputs[0]!.manifest_sha256, integrity: "verified" });
    const download = page.waitForEvent("download");
    await receipt.getByRole("button", { name: "Download aggregate JSON" }).click();
    const downloaded = await download;
    const path = await downloaded.path();
    expect(path).not.toBeNull();
    expect(await readFile(path!)).toEqual(canonical);
    if (process.env.WAZA_REFRESH_DOCS_MEDIA === "1") {
      for (const path of ["../docs/images/manifest-aggregate.png", "../site/public/images/manifest-aggregate.png"]) {
        await panel.screenshot({ path, animations: "disabled" });
      }
    }
  });

  test("actual missing neighbor retains evaluated 409 and downloadable membership", async ({ page }) => {
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Standalone manifest aggregation" });
    const missing = "a".repeat(64);
    await panel.getByRole("textbox").fill(`${inputs[0]!.manifest_sha256} ${inputs[0]!.evidence_sha256}\n${missing}`);
    const response = page.waitForResponse(`${base}/api/v1/aggregates`);
    await panel.getByRole("button", { name: "Build aggregate", exact: true }).click();
    const evaluated = await response;
    expect(evaluated.status()).toBe(409);
    const wire = await evaluated.body();
    expect(JSON.parse(wire.toString())).toMatchObject({
      state: "incomplete", counts: { input_members: 2, unique_verified_execution_records: 1 },
      usage: { state: "unavailable", recorded_historical_usage: null },
    });
    const receipt = panel.getByRole("status", { name: "Evaluated manifest aggregate" });
    await expect(receipt.getByRole("heading", { name: "Aggregate admission: incomplete", exact: true })).toBeVisible();
    await expect(receipt.getByRole("heading", { name: `omitted: ${missing}`, exact: true })).toBeVisible();
    const download = page.waitForEvent("download");
    await receipt.getByRole("button", { name: "Download aggregate JSON" }).click();
    const path = await (await download).path();
    expect(await readFile(path!)).toEqual(wire);
  });

  test("real store failure and transport loss clear previous success", async ({ page }) => {
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Standalone manifest aggregation" });
    await panel.getByRole("textbox").fill(`${inputs[0]!.manifest_sha256} ${inputs[0]!.evidence_sha256}`);
    const button = panel.getByRole("button", { name: "Build aggregate", exact: true });
    const receipt = panel.getByRole("status", { name: "Evaluated manifest aggregate" });
    await button.click();
    await expect(receipt).toBeVisible();
    const store = join(fixture, "results", ".waza-artifacts");
    const unavailable = join(fixture, "results", ".temporarily-unavailable");
    await rename(store, unavailable);
    try {
      const response = page.waitForResponse(`${base}/api/v1/aggregates`);
      await button.click();
      expect((await response).status()).toBe(503);
      await expect(panel.getByRole("alert")).toBeVisible();
      await expect(receipt).toHaveCount(0);
    } finally { await rename(unavailable, store); }
    await button.click();
    await expect(receipt).toBeVisible();
    await page.route("**/api/v1/aggregates", (route) => route.abort("connectionfailed"));
    await button.click();
    await expect(panel.getByRole("alert")).toBeVisible();
    await expect(receipt).toHaveCount(0);
    await expect(panel.getByRole("button", { name: "Download aggregate JSON" })).toHaveCount(0);
  });

  test("synthetic demo never offers aggregation or requests its API", async ({ page }) => {
    const aggregateRequests: string[] = [];
    page.on("request", (request) => { if (request.url().endsWith("/api/v1/aggregates")) aggregateRequests.push(request.url()); });
    await page.goto(`${base}/#/lab/demo/runner`);
    const panel = page.getByRole("region", { name: "Standalone manifest aggregation" });
    await expect(panel).toContainText("Aggregation is unavailable in the synthetic demo.");
    await expect(panel.getByRole("textbox")).toHaveCount(0);
    await expect(panel.getByRole("button", { name: "Build aggregate" })).toHaveCount(0);
    expect(aggregateRequests).toEqual([]);
  });
});
