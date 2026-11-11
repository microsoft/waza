import { expect, test, type Route } from "@playwright/test";
import { execFileSync, spawn, spawnSync, type ChildProcess } from "node:child_process";
import { mkdtemp, mkdir, rm, copyFile, writeFile, chmod } from "node:fs/promises";
import { createHash } from "node:crypto";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { closeSync, openSync, readFileSync } from "node:fs";

const unitExample = JSON.parse(readFileSync(new URL("../../examples/runner/unit.json", import.meta.url), "utf8")) as {
  selection: { shard: string; task: string; trial: number; attempt: number };
  input: { eval: string; task: string };
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
        expect(report.kind).toBe("waza.runner-terminal");
        expect(report.engine_calls).toBe(task === "selected" ? 1 : 0);
        if (task === "selected") {
          expect(report.selection).toEqual(unitExample.selection);
          expect(report.observation.run_number).toBe(2);
          expect(report.observation.attempts).toBe(2);
          expect(report.observation.validations).toEqual([{ identifier: "_output_contains", passed: true }]);
        } else {
          expect(report.outcome).toBe("invalid_input");
          expect(report.selection).toBeUndefined();
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
        expect(action === "run" ? saved.selection : saved.resolved).toEqual(vector.request.selection);
        expect(saved.engine_calls).toBe(action === "run" ? 1 : 0);
      }
    }
  });

  test("embedded Lab inspects actual CLI publication, unknown markers, conflicts and corruption", async ({ page }) => {
    const store = join(directory, "results", ".waza-artifacts");
    await mkdir(store, { mode: 0o700 });
    const env = { ...process.env, COPILOT_CLI_PATH: "/usr/bin/false", WAZA_NO_UPDATE_CHECK: "1" };
    const executed = spawnSync(binary, ["runner", "run"], {
      cwd: directory, input: JSON.stringify(unitExample), env, timeout: 10_000,
    });
    expect(executed.error).toBeUndefined();
    expect(executed.status).toBe(0);
    const frozenTerminal = readFileSync(new URL("../../internal/artifactmanifest/testdata/publication-terminal.json", import.meta.url));
    expect(executed.stdout).toEqual(frozenTerminal);
    const terminalPath = join(directory, "publication-terminal.json");
    const manifestPath = join(directory, "publication-manifest.json");
    await writeFile(terminalPath, executed.stdout, { flag: "wx", mode: 0o600 });
    await copyFile(new URL("../../internal/artifactmanifest/testdata/publication-manifest.json", import.meta.url), manifestPath);
    const artifact = (...args: string[]) => execFileSync(binary, ["artifact", ...args, "--store", store], {
      cwd: directory, env, timeout: 10_000,
    });
    const declaration = artifact("create", manifestPath);
    const created: { manifest_sha256: string; publication: string } = JSON.parse(declaration.toString("utf8"));
    expect(created.publication).toBe("declared");
    expect(created.manifest_sha256).toMatch(/^[0-9a-f]{64}$/);
    expect(artifact("create", manifestPath)).toEqual(declaration);
    const digest = created.manifest_sha256;
    const terminalDigest = createHash("sha256").update(executed.stdout).digest("hex");

    // Load the Go-embedded build, not the preview UI or intercepted receipts.
    const index = await page.request.get(base);
    expect(index.status()).toBe(200);
    const html = await index.text();
    expect(html).toBe(readFileSync(resolve("dist/index.html"), "utf8"));
    const references = [...html.matchAll(/(?:src|href)="(\/assets\/[a-zA-Z0-9._-]+\.(?:js|css))"/g)];
    expect(references.length).toBeGreaterThan(0);
    for (const reference of references) {
      const path = reference[1]!;
      const response = await page.request.get(`${base}${path}`);
      expect(response.status(), path).toBe(200);
      expect(await response.body(), path).toEqual(readFileSync(resolve("dist", path.slice(1))));
    }
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Manifest publication inspection" });
    const receipt = panel.getByRole("status", { name: "Manifest inspection receipt" });
    await panel.getByRole("button", { name: "Find stored manifests" }).click();
    await panel.getByRole("button", { name: digest, exact: true }).click();
    const inspect = async (publication: string, integrity: string, status: number, artifactIntegrity: string) => {
      await panel.getByRole("button", { name: "Inspect manifest", exact: true }).click();
      await expect(receipt.locator("dd").first()).toHaveText(digest);
      await expect(receipt.locator("dd").nth(2)).toHaveText(publication);
      await expect(receipt.locator("dd").nth(3)).toHaveText(integrity);
      await expect(receipt.locator("dd").nth(4)).toHaveText(String(status));
      await expect(receipt.getByRole("row")).toHaveCount(2);
      await expect(receipt.getByRole("row").nth(1).locator("th, td")).toHaveText([
        "terminal.json", artifactIntegrity, terminalDigest, String(executed.stdout.length), "application/json",
      ]);
    };
    await inspect("unfinalized", "failed", 404, "missing");
    await expect(receipt).toContainText("missing_object: terminal.json:");
    const incomplete = spawnSync(binary, ["artifact", "finalize", digest, "--store", store], {
      cwd: directory, env, encoding: "utf8", timeout: 10_000,
    });
    expect(incomplete.error).toBeUndefined();
    expect(incomplete.status).toBe(2);
    expect(JSON.parse(incomplete.stdout)).toMatchObject({ publication: "unfinalized", integrity: "failed" });

    artifact("upload", manifestPath, "terminal.json", terminalPath);
    await inspect("unfinalized", "verified", 200, "verified");
    const finalized = artifact("finalize", digest);
    expect(artifact("finalize", digest)).toEqual(finalized);
    await inspect("finalized", "verified", 200, "verified");
    await expect(receipt).toContainText("No diagnostics reported.");

    const marker = join(store, `final-${digest}`);
    await chmod(marker, 0o600);
    await writeFile(marker, "malformed marker");
    await inspect("unknown", "failed", 409, "verified");
    await expect(receipt).toContainText("corrupt_object:");
    await writeFile(marker, "f".repeat(64));
    await inspect("conflict", "failed", 409, "verified");
    await expect(receipt).toContainText("publication_conflict:");

    await writeFile(marker, digest);
    const blob = join(store, `blob-${terminalDigest}`);
    await chmod(blob, 0o600);
    const corrupt = Buffer.from(executed.stdout);
    corrupt[0] = corrupt[0]! ^ 1;
    await writeFile(blob, corrupt);
    await inspect("finalized", "failed", 409, "corrupt");
    await expect(receipt).toContainText("corrupt_object: terminal.json:");
    await expect(panel).toContainText("Content and declared context only; not execution attestation.");
  });

  test("downgrades and exposes capabilities without execution", async ({ page }) => {
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Compatible (no execution)");
    await expect(result.locator("dd").nth(0)).toHaveText("1.0");
    await expect(result.locator("dd").nth(1)).toHaveText("2.0, 1.0");
    await expect(result.locator("dd").nth(4)).toHaveText("negotiation, exact-unit, inline.mock-text, terminal-result, inline.copilot-text");
    await expect(result.locator("dd").nth(6)).toHaveText("None");
  });

  test("rejects future execution and captures current diagnostics", async ({ page }) => {
    await page.getByRole("textbox", { name: "Runner negotiation request (JSON)" }).fill(JSON.stringify({
      schema_version: "1.0", versions: ["1.0"],
      required_capabilities: ["inline.native-text"], optional_capabilities: [],
    }, null, 2));
    await page.getByRole("button", { name: "Check compatibility" }).click();
    const result = page.getByRole("status", { name: "Negotiation result" });
    await expect(result).toContainText("Incompatible request");
    await expect(result).toContainText('required capability "inline.native-text" is unavailable; available: negotiation, exact-unit, inline.mock-text, terminal-result, inline.copilot-text');
    await expect(result.locator("dd").nth(5)).toHaveText("None");
    await page.setViewportSize({ width: 1440, height: 1100 });
    if (process.env.WAZA_REFRESH_DOCS_MEDIA === "1") {
      const screenshot = "../docs/images/dashboard-lab-negotiation.png";
      await page.screenshot({ path: screenshot, fullPage: true, animations: "disabled" });
      await copyFile(screenshot, "../site/public/images/dashboard-lab-negotiation.png");
    }
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
    if (process.env.WAZA_REFRESH_DOCS_MEDIA === "1") {
      const screenshot = "../docs/images/dashboard-lab-selection.png";
      await page.screenshot({ path: screenshot, fullPage: true, animations: "disabled" });
      await copyFile(screenshot, "../site/public/images/dashboard-lab-selection.png");
    }
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
      expect(["success", "evaluation_failure"]).toContain(completed.outcome);
      expect(completed.kind).toBe("waza.runner-terminal");
      expect(completed.identity).toEqual(vector.identity);
      expect(completed.engine_calls).toBe(1);
    });
  }

  test("Copilot profile validates offline and reports guarded native startup without inference", async ({ page, request }) => {
    // Native execution must reach the actual same-origin server, not the
    // preview proxy used by mock-only controls.
    await page.unroute("**/api/v1/runner/**");
    await page.goto(`${base}/#/lab/runner`);
    const captured = JSON.parse(readFileSync(new URL("../../examples/runner/copilot-unit.json", import.meta.url), "utf8"));
    const admitted = await request.post(`${base}/api/v1/runner/validate`, { data: captured });
    const report = await admitted.json();
    expect(report.status).toBe("ready");
    expect(report.engine_calls).toBe(0);
    expect(report.adjacent_work_selected).toBe(false);
    expect(report.resolved).toEqual(captured.selection);
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    await input.fill(JSON.stringify({ ...captured, allow_paid_execution: false }));
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    const terminal = page.getByRole("status", { name: "Terminal result" });
    await expect(terminal).toContainText("invalid_input");
    await expect(terminal).toContainText("allow_paid_execution: true");
    await input.fill(JSON.stringify(captured));
    await page.getByRole("button", { name: "Validate selection" }).click();
    const selection = page.getByRole("status", { name: "Selection validation result" });
    await expect(selection).toContainText("Valid selection (not executed)");
    await expect(selection).toContainText("shard-a / selected / trial 2 / attempt 2");
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(terminal).toContainText("infrastructure_failure");
    await expect(terminal).toContainText("initialize/start_failed");
    await expect(terminal).toContainText(report.identity.attempt);
    await expect(terminal.getByRole("list", { name: "Observed grader verdicts" })).toHaveCount(0);
  });

  test("native HTTP trust rejection is produced by the real server and preserved by Lab export", async ({ page, request }) => {
    const captured = JSON.parse(readFileSync(new URL("../../examples/runner/copilot-unit.json", import.meta.url), "utf8"));
    const response = await request.post(`${base}/api/v1/runner/run`, {
      headers: { Host: "attacker.example", Origin: "http://attacker.example" }, data: captured,
    });
    expect(response.status()).toBe(403);
    const actual = await response.json();
    expect(actual.outcome).toBe("invalid_input");
    expect(actual.engine_calls).toBe(0);
    expect(actual.identity).toBeUndefined();
    expect(actual.observation).toBeUndefined();
    expect(actual.diagnostics[0].code).toBe("native_http_denied");
    await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(captured));
    await page.getByRole("button", { name: "Validate selection" }).click();
    await expect(page.getByRole("status", { name: "Selection validation result" })).toContainText("Valid selection (not executed)");
    await page.route("**/api/v1/runner/run", (route) => route.fulfill({ response }));
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    const result = page.getByRole("status", { name: "Terminal result" });
    await expect(result).toContainText("native_http_denied");
    await expect(result).toContainText("Unavailable (not admitted)");
    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Export terminal JSON" }).click();
    const artifact = await download;
    expect(JSON.parse(readFileSync((await artifact.path())!, "utf8"))).toEqual(actual);
  });

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

  test("terminal API, Lab and downloaded JSON preserve actual producer outcomes", async ({ page }) => {
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const initial = JSON.parse(await input.inputValue());
    for (const outcome of ["success", "evaluation_failure", "invalid_input", "unsupported_capability"]) {
      const request = structuredClone(initial);
      if (outcome === "evaluation_failure") request.input.task = request.input.task.replace("- hello", "- never-observed");
      if (outcome === "invalid_input") request.selection.task = "neighbor";
      if (outcome === "unsupported_capability") request.versions = ["9.0"];
      await input.fill(JSON.stringify(request));
      const observed = page.waitForResponse((response) =>
        response.url().endsWith("/api/v1/runner/run") && response.request().method() === "POST");
      await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
      const response = await observed;
      const envelope = await response.json();
      expect(envelope.kind).toBe("waza.runner-terminal");
      expect(envelope.outcome).toBe(outcome);
      expect(Number(response.headers()["x-waza-exit-code"])).toBe(envelope.exit_code);
      const result = page.getByRole("status", { name: "Terminal result" });
      await expect(result).toContainText(outcome);
      await expect(result).toContainText(envelope.identity ? "Schema checked; input identity bound" : "Schema checked; no execution identity claimed");
      await expect(result).toContainText("Unavailable in this terminal summary; no inferred zeros");
      if (envelope.observation) {
        await expect(result).toContainText(`mock / ${envelope.observation.status}`);
        await expect(result).toContainText(envelope.identity.attempt);
        if (outcome === "success") expect(envelope.identity).toEqual(identityVectors[0].identity);
        else expect(envelope.identity).not.toEqual(identityVectors[0].identity);
      } else {
        expect(envelope.identity).toBeUndefined();
        await expect(result).toContainText("Unavailable (not admitted)");
      }
      const download = page.waitForEvent("download");
      await result.getByRole("button", { name: "Export terminal JSON" }).click();
      const artifact = await download;
      expect(artifact.suggestedFilename()).toBe("runner-terminal.json");
      const path = await artifact.path();
      expect(path).not.toBeNull();
      expect(JSON.parse(readFileSync(path!, "utf8"))).toEqual(envelope);
      if (outcome === "evaluation_failure" && process.env.WAZA_REFRESH_DOCS_MEDIA === "1") {
        await page.setViewportSize({ width: 1440, height: 1600 });
        const screenshot = "../docs/images/dashboard-lab-terminal.png";
        await page.getByRole("region", { name: "Runner terminal outcome" }).screenshot({ path: screenshot, animations: "disabled" });
        await copyFile(screenshot, "../site/public/images/dashboard-lab-terminal.png");
      }
    }
    await input.fill(JSON.stringify(initial));
    await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
    await page.route("**/api/v1/runner/run", (route) => route.fulfill({ status: 503, body: "unavailable" }));
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(page.getByRole("region", { name: "Runner terminal outcome" }).getByRole("alert")).toBeVisible();
    await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Export terminal JSON" })).toHaveCount(0);
  });

  test("terminal client budget honors captured timeout after validation and remains bounded", async ({ page }) => {
    await page.clock.install();
    await page.addInitScript(() => {
      const budgets: number[] = [];
      Object.defineProperty(window, "runnerTimeoutBudgets", { value: budgets });
      // Native timeout signals do not use the JS clock. Keep real abort behavior
      // while making the production-selected budgets deterministically advanceable.
      AbortSignal.timeout = (milliseconds) => {
        budgets.push(milliseconds);
        const controller = new AbortController();
        setTimeout(() => controller.abort(new DOMException("The operation timed out.", "TimeoutError")), milliseconds);
        return controller.signal;
      };
    });
    await page.reload();
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const initial = JSON.parse(await input.inputValue());
    expect(initial.input.eval).toContain("timeout_seconds: 30");
    for (const timeout of [120_000, 1_000]) {
      const captured = structuredClone(initial);
      captured.timeout_ms = timeout;
      captured.input.eval = captured.input.eval.replace("timeout_seconds: 30", "timeout_seconds: 120");
      const body = JSON.stringify(captured);
      await input.fill(body);
      const validationReady = Promise.withResolvers<void>();
      const validationRelease = Promise.withResolvers<void>();
      const terminalReady = Promise.withResolvers<void>();
      const terminalRelease = Promise.withResolvers<void>();
      let produced: unknown;
      const validateRoute = async (route: Route) => {
        expect(route.request().postData()).toBe(body);
        const response = await route.fetch({ url: `${base}/api/v1/runner/validate` });
        expect((await response.json()).status).toBe("ready");
        validationReady.resolve();
        await validationRelease.promise;
        await route.fulfill({ response });
      };
      const runRoute = async (route: Route) => {
        expect(route.request().postData()).toBe(body);
        const response = await route.fetch({ url: `${base}/api/v1/runner/run` });
        produced = await response.json();
        expect(produced).toMatchObject({ outcome: "success", engine_calls: 1, selection: captured.selection });
        terminalReady.resolve();
        await terminalRelease.promise;
        await route.fulfill({ response });
      };
      await page.route("**/api/v1/runner/validate", validateRoute);
      await page.route("**/api/v1/runner/run", runRoute);
      try {
        await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
        await validationReady.promise;
        await page.clock.runFor(9_000);
        validationRelease.resolve();
        await terminalReady.promise;
        expect(await page.evaluate(() => Reflect.get(window, "runnerTimeoutBudgets").slice(-2)))
          .toEqual([10_000, timeout + 15_000]);
        if (timeout === 120_000) {
          await page.clock.runFor(90_000);
          await expect(page.getByRole("button", { name: "Running selected unit...", exact: true })).toBeVisible();
          await expect(page.getByRole("region", { name: "Runner terminal outcome" }).getByRole("alert")).toHaveCount(0);
          terminalRelease.resolve();
          const result = page.getByRole("status", { name: "Terminal result" });
          await expect(result).toContainText("success");
          await expect(result).toContainText("Schema checked; input identity bound");
          const download = page.waitForEvent("download");
          await result.getByRole("button", { name: "Export terminal JSON" }).click();
          expect(JSON.parse(readFileSync((await (await download).path())!, "utf8"))).toEqual(produced);
        } else {
          await page.clock.runFor(timeout + 15_001);
          await expect(page.getByRole("region", { name: "Runner terminal outcome" }).getByRole("alert")).toContainText("timed out");
          terminalRelease.resolve();
          await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
          await expect(page.getByRole("button", { name: "Export terminal JSON" })).toHaveCount(0);
        }
      } finally {
        validationRelease.resolve();
        terminalRelease.resolve();
        await page.unroute("**/api/v1/runner/validate", validateRoute);
        await page.unroute("**/api/v1/runner/run", runRoute);
      }
    }
  });

  test("terminal client cancellation survives request edits and unmount", async ({ page }) => {
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const original = await input.inputValue();
    for (const action of ["edit", "unmount"]) {
      const ready = Promise.withResolvers<void>();
      const release = Promise.withResolvers<void>();
      const runRoute = async (route: Route) => {
        const response = await route.fetch({ url: `${base}/api/v1/runner/run` });
        expect((await response.json()).outcome).toBe("success");
        ready.resolve();
        await release.promise;
        await route.fulfill({ response });
      };
      await page.route("**/api/v1/runner/run", runRoute);
      try {
        await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
        await ready.promise;
        const aborted = page.waitForEvent("requestfailed", (request) => request.url().endsWith("/api/v1/runner/run"));
        if (action === "edit") {
          await input.fill(original + "\n");
          await expect(page.getByRole("button", { name: "Run selected unit", exact: true })).toBeEnabled();
        } else {
          await page.goto("/#/lab/demo/runner");
        }
        await aborted;
        release.resolve();
        await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Export terminal JSON" })).toHaveCount(0);
      } finally {
        release.resolve();
        await page.unroute("**/api/v1/runner/run", runRoute);
      }
    }
  });

  test("terminal client rejects invalid captured budgets rather than trusting replayed admission", async ({ page, request }) => {
    const admission = await request.post(`${base}/api/v1/runner/validate`, { data: unitExample });
    expect((await admission.json()).status).toBe("ready");
    // Replaying a real receipt for different, invalid input is a negative server control.
    await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ response: admission }));
    let runs = 0;
    await page.route("**/api/v1/runner/run", (route) => { runs++; return route.abort(); });
    const input = page.getByRole("textbox", { name: "Exact work request (JSON)" });
    const initial = JSON.parse(await input.inputValue());
    for (const timeout of [undefined, null, false, "60000", 0, 1.5, 86_400_001]) {
      await input.fill(JSON.stringify({ ...initial, timeout_ms: timeout }));
      await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
      await expect(page.getByRole("region", { name: "Runner terminal outcome" }).getByRole("alert"))
        .toContainText("Admitted runner request has an invalid timeout_ms.");
      await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
      expect(runs).toBe(0);
    }
  });

  test("metric metadata stays identity-bound without aggregate terminal or export claims", async ({ page, request }) => {
    const baseline = await request.post(`${base}/api/v1/runner/run`, { data: unitExample });
    expect(baseline.ok()).toBe(true);
    const original = await baseline.json();
    expect(original.outcome).toBe("success");
    const captured = structuredClone(unitExample);
    captured.input.eval += "\nmetrics: [{name: quality, weight: 1, threshold: 0.99}]\n";
    await page.getByRole("textbox", { name: "Exact work request (JSON)" }).fill(JSON.stringify(captured));
    const region = page.getByRole("region", { name: "Runner terminal outcome" });
    await expect(region).toContainText("Success describes only the selected attempt.");
    await expect(region).toContainText("no aggregate metric values, threshold decisions, or whole-evaluation success");
    const response = page.waitForResponse("**/api/v1/runner/run");
    await region.getByRole("button", { name: "Run selected unit", exact: true }).click();
    const executionResponse = await response;
    expect(executionResponse.request().postDataJSON().input.eval).toBe(captured.input.eval);
    const actual = await executionResponse.json();
    expect(actual.outcome).toBe("success");
    expect(actual.engine_calls).toBe(1);
    expect(actual.identity.canonical_digest).not.toBe(original.identity.canonical_digest);
    expect(actual.metrics).toBeUndefined();
    expect(actual.summary).toBeUndefined();
    expect(actual.observation.metrics).toBeUndefined();
    const result = page.getByRole("status", { name: "Terminal result" });
    await expect(result).toContainText("mock / passed");
    await expect(result).not.toContainText("quality");
    const downloaded = page.waitForEvent("download");
    await result.getByRole("button", { name: "Export terminal JSON" }).click();
    const artifact = await downloaded;
    expect(JSON.parse(readFileSync((await artifact.path())!, "utf8"))).toEqual(actual);
  });

  test("terminal input transport rejection remains visible after successful offline admission", async ({ page, request }) => {
    const response = await request.post(`${base}/api/v1/runner/run`, {
      headers: { "Content-Type": "text/plain" }, data: JSON.stringify(unitExample),
    });
    expect(response.status()).toBe(415);
    const actual = await response.json();
    expect(actual.outcome).toBe("invalid_input");
    expect(actual.identity).toBeUndefined();
    await page.route("**/api/v1/runner/run", (route) => route.fulfill({ response }));
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    const result = page.getByRole("status", { name: "Terminal result" });
    await expect(result).toContainText("invalid_media_type");
    await expect(result).toContainText("Schema checked; no execution identity claimed");
    await expect(result).toContainText("Unavailable (not admitted)");
    const download = page.waitForEvent("download");
    await result.getByRole("button", { name: "Export terminal JSON" }).click();
    const artifact = await download;
    expect(JSON.parse(readFileSync((await artifact.path())!, "utf8"))).toEqual(actual);
  });

  test("terminal guards reject mutations of real producer envelopes", async ({ page, request }) => {
    const response = await request.post(`${base}/api/v1/runner/run`, { data: unitExample });
    const actual = await response.json();
    expect(actual.outcome).toBe("success");
    for (const bad of [
      { ...actual, exit_code: 1 },
      { ...actual, identity: { ...actual.identity, attempt: actual.identity.trial } },
      { ...actual, identity: { ...actual.identity, run: actual.identity.run + "\n" } },
      { ...actual, selection: { ...actual.selection, task: "neighbor" } },
      { ...actual, observation: { ...actual.observation, attempts: 1 } },
      { ...actual, observation: { ...actual.observation, execution_mode: "copilot" } },
      { ...actual, observation: { ...actual.observation, validations: [...actual.observation.validations, ...actual.observation.validations] } },
      { ...actual, observation: null },
    ]) {
      await page.route("**/api/v1/runner/run", (route) => route.fulfill({
        json: bad, headers: { "X-Waza-Exit-Code": String(bad.exit_code) },
      }));
      await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
      await expect(page.getByRole("region", { name: "Runner terminal outcome" }).getByRole("alert")).toBeVisible();
      await expect(page.getByRole("status", { name: "Terminal result" })).toHaveCount(0);
    }
    await page.route("**/api/v1/runner/run", (route) => route.fulfill({
      json: actual, headers: { "X-Waza-Exit-Code": "6" },
    }));
    await page.getByRole("button", { name: "Run selected unit", exact: true }).click();
    await expect(page.getByRole("alert")).toContainText("exit header contradicts its body");
  });
});
