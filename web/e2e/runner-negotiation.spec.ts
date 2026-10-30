import { expect, test } from "@playwright/test";
import { execFileSync, spawn, spawnSync, type ChildProcess } from "node:child_process";
import { mkdtemp, mkdir, rm, copyFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { closeSync, openSync, readFileSync } from "node:fs";

const unitExample = JSON.parse(readFileSync(new URL("../../examples/runner/unit.json", import.meta.url), "utf8")) as {
  selection: { shard: string; task: string; trial: number; attempt: number };
};

const identityVectors = JSON.parse(readFileSync(new URL("../../internal/runnerunit/testdata/identities.json", import.meta.url), "utf8")) as {
  name: string;
  request: { selection: { task: string }; input: { task: string } };
  identity: Record<string, string>;
}[];

test.describe("Runner negotiation through the real Go API", () => {
  test.describe.configure({ mode: "serial" });
  let directory: string;
  let binary: string;
  let server: ChildProcess;
  let base: string;
  let serverOutput = "";

  test.beforeAll(async ({ request }) => {
    test.setTimeout(180_000);
    directory = await mkdtemp(join(tmpdir(), "waza-runner-browser-"));
    binary = join(directory, "waza");
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

  test("built CLI executes one explicit attempt on every dispatch and rejects neighbors", () => {
      for (const task of ["selected", "neighbor", "selected"]) {
        const request = { ...unitExample, selection: { ...unitExample.selection, task } };
        const process = spawnSync(binary, ["runner", "run"], {
          cwd: directory, input: JSON.stringify(request), encoding: "utf8",
          env: { ...globalThis.process.env, COPILOT_CLI_PATH: "/usr/bin/false", WAZA_NO_UPDATE_CHECK: "1" },
          timeout: 10_000,
        });
        expect(process.error).toBeUndefined();
        expect(process.status).toBe(task === "selected" ? 0 : 6);
        const report = JSON.parse(process.stdout);
        expect(report.kind).toBe("waza.runner-unit");
        expect(report.engine_calls).toBe(task === "selected" ? 1 : 0);
        expect(report.adjacent_work_selected).toBe(false);
        if (task === "selected") {
          expect(report.resolved).toEqual(unitExample.selection);
          expect(report.observation.run_number).toBe(2);
          expect(report.observation.attempts).toBe(2);
          expect(report.observation.final_output).toContain("hello");
        } else {
          expect(report.status).toBe("rejected");
          expect(report.resolved).toBeUndefined();
          expect(report.observation).toBeUndefined();
        }
      }
  });

  test("built CLI saves every frozen identity through actual stdout redirection", () => {
    expect(identityVectors).toHaveLength(11);
    for (const vector of identityVectors) {
      for (const action of ["validate", "run"]) {
        const path = join(directory, `${vector.name}-${action}.json`);
        const file = openSync(path, "wx", 0o600);
        let result;
        try {
          result = spawnSync(binary, ["runner", action], {
            cwd: directory, input: JSON.stringify(vector.request), encoding: "utf8",
            env: { ...process.env, COPILOT_CLI_PATH: "/usr/bin/false", WAZA_NO_UPDATE_CHECK: "1" },
            stdio: ["pipe", file, "pipe"], timeout: 10_000,
          });
        } finally {
          closeSync(file);
        }
        expect(result.error).toBeUndefined();
        expect([0, 1]).toContain(result.status);
        const saved = JSON.parse(readFileSync(path, "utf8"));
        expect(saved.identity, `${vector.name}/${action}`).toEqual(vector.identity);
        expect(saved.resolved).toEqual(vector.request.selection);
        expect(saved.engine_calls).toBe(action === "run" ? 1 : 0);
      }
    }
  });

  test("downgrades and exposes capabilities without execution", async ({ page }) => {
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Compatible (no execution)");
    await expect(result.locator("dd").nth(0)).toHaveText("1.0");
    await expect(result.locator("dd").nth(1)).toHaveText("2.0, 1.0");
    await expect(result.locator("dd").nth(4)).toHaveText("negotiation, exact-unit, inline.mock-text");
    await expect(result.locator("dd").nth(6)).toHaveText("terminal-result");
  });

  test("rejects future execution and captures current diagnostics", async ({ page }) => {
    await page.getByRole("textbox", { name: "Runner negotiation request (JSON)" }).fill(JSON.stringify({
      schema_version: "1.0", versions: ["1.0"],
      required_capabilities: ["terminal-result"], optional_capabilities: [],
    }, null, 2));
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Incompatible request");
    await expect(result).toContainText('required capability "terminal-result" is unavailable; available: negotiation, exact-unit, inline.mock-text');
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
    await page.getByRole("textbox", { name: "Runner negotiation request (JSON)" }).fill('{"schema_version":"1.0","versions":["9.0"],"required_capabilities":[],"optional_capabilities":[]}');
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

  test("validates the captured tuple, rejects conflicts, clears stale state", async ({ page }) => {
    const requests: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/runner/")) requests.push(new URL(request.url()).pathname);
    });
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const check = page.getByRole("button", { name: "Validate selection" });
    const result = page.getByRole("status", { name: "Selection validation result" });
    await check.click();
    await expect(result).toContainText("Valid selection (not executed)");
    await expect(result.locator("dd").nth(0)).toHaveText("shard-a / selected / trial 2 / attempt 2");
    await expect(result.locator("dd").nth(1)).toHaveText("shard-a / selected / trial 2 / attempt 2");
    await expect(result.locator("dd").nth(2)).toHaveText("valid");
    await expect(result.locator("dd").nth(3)).toHaveText("No");
    await expect(result.locator("dd").nth(4)).toHaveText("0");
    const lineage = result.getByRole("region", { name: "Logical identity lineage" });
    for (const value of Object.values(identityVectors[0].identity)) {
      await expect(lineage).toContainText(value);
    }
    await page.setViewportSize({ width: 1440, height: 1600 });
    const screenshot = "../docs/images/dashboard-lab-selection.png";
    await page.screenshot({ path: screenshot, fullPage: true, animations: "disabled" });
    await copyFile(screenshot, "../site/public/images/dashboard-lab-selection.png");
    const value = JSON.parse(await input.inputValue()) as { selection: { task: string } };
    value.selection.task = "neighbor";
    await input.fill(JSON.stringify(value));
    await expect(result).toHaveCount(0);
    await check.click();
    await expect(result).toContainText("Selection rejected");
    await expect(result).toContainText("selection conflicts with declared input shard/task");
    await expect(result.locator("dd").nth(1)).toHaveText("Not resolved");
    await expect(result).toContainText("Identity unavailable");
    await expect(result.getByRole("button", { name: "Export validation JSON" })).toHaveCount(0);
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ status: 503 }));
    await check.click();
    await expect(page.getByRole("alert")).toContainText("HTTP 503");
    await expect(result).toHaveCount(0);
    expect(requests).not.toContain("/api/v1/runner/run");
    expect(requests.filter((path) => path === "/api/v1/runner/validate")).toHaveLength(3);
  });

  for (const vector of identityVectors) {
    test(`real producer identity survives API, Lab and downloaded JSON: ${vector.name}`, async ({ page, request }) => {
      expect(identityVectors).toHaveLength(11);
      const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
      const check = page.getByRole("button", { name: "Validate selection" });
      await input.fill(JSON.stringify(vector.request));
      const observed = page.waitForResponse((response) =>
        response.url().endsWith("/api/v1/runner/validate") && response.request().method() === "POST");
      await check.click();
      const report = await (await observed).json();
      expect(report.status, vector.name).toBe("ready");
      expect(report.identity, vector.name).toEqual(vector.identity);
      expect(report.engine_calls).toBe(0);
      expect(report.observation).toBeUndefined();
      const lineage = page.getByRole("region", { name: "Logical identity lineage" });
      for (const value of Object.values(vector.identity)) {
        await expect(lineage).toContainText(value);
      }
      await expect(lineage).toContainText("not execution");
      const download = page.waitForEvent("download");
      await lineage.getByRole("button", { name: "Export validation JSON" }).click();
      const artifact = await download;
      expect(artifact.suggestedFilename()).toBe("runner-validation.json");
      const path = await artifact.path();
      expect(path).not.toBeNull();
      expect(JSON.parse(readFileSync(path!, "utf8"))).toEqual(report);
      const execution = await request.post(`${base}/api/v1/runner/run`, {
        headers: { "Content-Type": "application/json" }, data: vector.request,
      });
      expect(execution.ok()).toBe(true);
      const completed = await execution.json();
      expect(completed.status).toBe("completed");
      expect(completed.identity).toEqual(vector.identity);
      expect(completed.engine_calls).toBe(1);
    });
  }

  test("identity guard preserves absence and rejects malformed or contradictory real reports", async ({ page, request }) => {
    const response = await request.post(`${base}/api/v1/runner/validate`, {
      headers: { "Content-Type": "application/json" }, data: identityVectors[0].request,
    });
    expect(response.ok()).toBe(true);
    const ready = await response.json();
    expect(ready.identity).toEqual(identityVectors[0].identity);
    const { identity, ...historical } = ready;
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: historical }));
    await page.getByRole("button", { name: "Validate selection" }).click();
    await expect(page.getByRole("region", { name: "Logical identity lineage" })).toContainText("Identity unavailable");
    await expect(page.getByRole("button", { name: "Export validation JSON" })).toHaveCount(0);
    const partial = { ...identity };
    delete partial.attempt;
    for (const bad of [
      { ...ready, identity: null },
      { ...ready, identity: partial },
      { ...ready, identity: { ...identity, version: "sha256-json-v1" } },
      { ...ready, identity: { ...identity, attempt: identity.trial } },
      { ...ready, identity: { ...identity, canonical_digest: identity.canonical_digest.slice(1) } },
      { ...ready, identity: { ...identity, canonical_digest: identity.canonical_digest.toUpperCase() } },
      { ...ready, identity: { ...identity, canonical_digest: identity.canonical_digest + "\n" } },
      { ...ready, identity: { ...identity, attempt: identity.attempt + "\n" } },
      { ...ready, identity: { ...identity, verified: true } },
      { ...ready, status: "rejected", validation: "invalid", resolved: undefined },
    ]) {
      await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: bad }));
      await page.getByRole("button", { name: "Validate selection" }).click();
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(page.getByRole("region", { name: "Logical identity lineage" })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Export validation JSON" })).toHaveCount(0);
    }
  });

  test("selection rejects contradictory and malformed backend reports", async ({ page, request }) => {
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const response = await request.post(`${base}/api/v1/runner/validate`, {
      headers: { "Content-Type": "application/json" }, data: await input.inputValue(),
    });
    expect(response.ok()).toBe(true);
    const ready = await response.json() as { resolved: { task: string }; engine_calls: number; status: string };
    expect(ready.status).toBe("ready");
    for (const body of [
      JSON.stringify({ ...ready, resolved: { ...ready.resolved, task: "neighbor" } }),
      JSON.stringify({ ...ready, engine_calls: 1 }),
      JSON.stringify({ ...ready, adjacent_work_selected: true }),
      '{"kind":"waza.runner-unit","kind":"waza.runner-unit"}',
    ]) {
      await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ contentType: "application/json", body }));
      await page.getByRole("button", { name: "Validate selection" }).click();
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(page.getByRole("status", { name: "Selection validation result" })).toHaveCount(0);
    }
  });
});
