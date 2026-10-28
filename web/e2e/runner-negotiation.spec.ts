import { expect, test } from "@playwright/test";
import { execFileSync, spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, mkdir, rm, copyFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test.describe("Runner negotiation through the real Go API", () => {
  test.describe.configure({ mode: "serial" });
  let directory: string;
  let server: ChildProcess;
  let base: string;
  let serverOutput = "";

  test.beforeAll(async ({ request }) => {
    test.setTimeout(180_000);
    directory = await mkdtemp(join(tmpdir(), "waza-runner-browser-"));
    const binary = join(directory, "waza");
    const env = {
      ...process.env,
      COPILOT_CLI_PATH: "/usr/bin/false",
      ENABLE_COPILOT_TESTS: "false",
      ENABLE_COPILOT_SDK_TESTS: "false",
      ENABLE_LIVE_COPILOT_TESTS: "false",
      WAZA_NO_UPDATE_CHECK: "1",
    };
    const guard = spawn("/usr/bin/false");
    expect(await new Promise((resolve) => guard.on("exit", resolve))).toBe(1);
    execFileSync("go", ["build", "-o", binary, "./cmd/waza"], {
      cwd: resolve(".."), env, timeout: 150_000,
    });
    const listener = createServer();
    await new Promise<void>((resolve) => listener.listen(0, "127.0.0.1", resolve));
    const address = listener.address();
    if (!address || typeof address === "string") throw new Error("Missing test listener port");
    const port = address.port;
    await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
    await mkdir(join(directory, "results"));
    server = spawn(binary, ["serve", "--no-browser", "--lab", "--port", String(port), "--results-dir", join(directory, "results")], {
      cwd: directory, env, stdio: ["ignore", "pipe", "pipe"],
    });
    server.stdout?.on("data", (chunk: Buffer) => { serverOutput += chunk.toString(); });
    server.stderr?.on("data", (chunk: Buffer) => { serverOutput += chunk.toString(); });
    base = `http://127.0.0.1:${port}`;
    await expect.poll(async () => {
      try {
        return (await request.get(`${base}/api/v1/runner/capabilities`)).status();
      } catch (error) {
        if (server.exitCode !== null) throw new Error(`Go server exited ${server.exitCode}: ${serverOutput}`, { cause: error });
        return error instanceof Error ? error.message : "Server request failed";
      }
    }, { timeout: 15_000, message: `Go runner API must become available at ${base}` }).toBe(200);
  });

  test.afterAll(async () => {
    if (server && server.exitCode === null) {
      const exited = new Promise((resolve) => server.once("exit", resolve));
      server.kill("SIGTERM");
      await exited;
    }
    if (directory) await rm(directory, { recursive: true, force: true });
  });

  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.route("**/api/v1/runner/**", async (route) => {
      const url = new URL(route.request().url());
      const response = await route.fetch({ url: `${base}${url.pathname}` });
      await route.fulfill({ response });
    });
    await page.goto("/#/lab/runner");
  });

  test("downgrades and exposes capabilities without execution", async ({ page }) => {
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Compatible (no execution)");
    await expect(result.locator("dd").nth(0)).toHaveText("1.0");
    await expect(result.locator("dd").nth(1)).toHaveText("2.0, 1.0");
    await expect(result.locator("dd").nth(4)).toHaveText("negotiation");
    await expect(result.locator("dd").nth(6)).toHaveText("exact-unit");
  });

  test("rejects future execution and captures current diagnostics", async ({ page }) => {
    await page.getByRole("textbox", { name: "Runner negotiation request (JSON)" }).fill(JSON.stringify({
      schema_version: "1.0", versions: ["1.0"],
      required_capabilities: ["exact-unit"], optional_capabilities: [],
    }, null, 2));
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Incompatible request");
    await expect(result).toContainText('required capability "exact-unit" is unavailable; available: negotiation');
    await expect(result.locator("dd").nth(5)).toHaveText("None");
    await page.setViewportSize({ width: 1440, height: 1100 });
    const screenshot = "../docs/images/dashboard-lab-negotiation.png";
    await page.screenshot({ path: screenshot, fullPage: true, animations: "disabled" });
    await copyFile(screenshot, "../site/public/images/dashboard-lab-negotiation.png");
  });

  test("clears previous success on edit, rejection and failed reload", async ({ page }) => {
    const check = page.getByRole("button", { name: "Check compatibility" });
    const result = page.getByRole("status", { name: "Negotiation result" });
    await check.click();
    await expect(result).toContainText("Compatible (no execution)");
    await page.getByRole("textbox").fill('{"schema_version":"1.0","versions":["9.0"],"required_capabilities":[],"optional_capabilities":[]}');
    await expect(result).toHaveCount(0);
    await check.click();
    await expect(result).toContainText("no supported runner version; offer 1.0");
    await page.route("**/api/v1/runner/negotiate", (route) => route.fulfill({ status: 503 }));
    await check.click();
    await expect(page.getByRole("alert")).toContainText("HTTP 503");
    await expect(result).toHaveCount(0);
  });

  test("rejects malformed or contradictory API responses", async ({ page }) => {
    for (const body of [
      '{"kind":"waza.runner-negotiation","kind":"waza.runner-negotiation"}',
      JSON.stringify({
        kind: "waza.runner-negotiation", schema_version: "1.0", status: "compatible",
        negotiation: {
          requested_versions: ["1.0"], required_capabilities: [], optional_capabilities: [],
          available_capabilities: ["negotiation"], selected_capabilities: [], unavailable_optional_capabilities: [],
        }, diagnostics: [],
      }),
    ]) {
      await page.route("**/api/v1/runner/negotiate", (route) => route.fulfill({ contentType: "application/json", body }));
      await page.getByRole("button", { name: "Check compatibility" }).click();
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(page.getByRole("status", { name: "Negotiation result" })).toHaveCount(0);
    }
  });

  test("demo makes no runner requests and invents no negotiation", async ({ page }) => {
    const requests: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/runner/")) requests.push(request.url());
    });
    await page.goto("/#/lab/demo/runner");
    await expect(page.getByText("Runner compatibility is unavailable in the synthetic demo.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Check compatibility" })).toHaveCount(0);
    expect(requests).toEqual([]);
  });
});
