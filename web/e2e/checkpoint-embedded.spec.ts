import { expect, test } from "@playwright/test";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { lstat, mkdir, mkdtemp, readdir, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

test("matched owning CLI serves every current embedded asset and actual checkpoint Lab recovery", async ({ page, request }) => {
  test.skip(process.env.WAZA_CHECKPOINT_DELIVERY !== "final", "Preview/API evidence is not packaged embedded delivery.");
  test.setTimeout(90_000);
  expect(process.env.WAZA_E2E_CLI).toBeTruthy();
  const binary = resolve(process.env.WAZA_E2E_CLI!);
  const metadata = execFileSync("go", ["version", "-m", binary], { encoding: "utf8", timeout: 10_000 });
  const revision = execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8", timeout: 10_000 }).trim();
  expect(metadata.split("\n")).toContain(`\tbuild\tvcs.revision=${revision}`);
  expect(metadata.split("\n")).toContain("\tbuild\tvcs.modified=false");
  const directory = await mkdtemp(join(tmpdir(), "waza-checkpoint-embedded-"));
  const guard = join(directory, "reject-runtime");
  let server: ReturnType<typeof spawn> | undefined;
  let closed: Promise<void> | undefined;
  let safeToRemove = true;
  let output = "";
  try {
    await mkdir(join(directory, "results", ".waza-artifacts"), { recursive: true, mode: 0o700 });
    await writeFile(guard, '#!/bin/sh\nprintf invoked > "$0.invoked"\nexit 91\n', { flag: "wx", mode: 0o700 });
    expect(spawnSync(guard, { timeout: 5_000 }).status).toBe(91);
    expect(readFileSync(`${guard}.invoked`, "utf8")).toBe("invoked");
    await rm(`${guard}.invoked`);
    const env: NodeJS.ProcessEnv = {
      ...process.env, HOME: directory, USERPROFILE: directory, COPILOT_HOME: join(directory, "copilot"),
      COPILOT_CLI_PATH: guard, WAZA_NO_UPDATE_CHECK: "1", NO_COLOR: "1",
      ENABLE_COPILOT_TESTS: "false", ENABLE_COPILOT_SDK_TESTS: "false", ENABLE_LIVE_COPILOT_TESTS: "false",
    };
    for (const name of ["GH_TOKEN", "GITHUB_TOKEN", "COPILOT_GITHUB_TOKEN", "COPILOT_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"]) {
      delete env[name];
    }
    const listener = createServer();
    await new Promise<void>((resolve, reject) => {
      listener.once("error", reject);
      listener.listen(0, "127.0.0.1", resolve);
    });
    let port: number;
    try {
      const address = listener.address();
      if (!address || typeof address === "string") throw new Error("Missing embedded fixture port.");
      port = address.port;
    } finally {
      await new Promise<void>((resolve, reject) => listener.close((error) => error ? reject(error) : resolve()));
    }
    server = spawn(binary, ["serve", "--no-browser", "--lab", "--port", String(port), "--results-dir", join(directory, "results")], {
      cwd: directory, env, stdio: ["ignore", "pipe", "pipe"],
    });
    let startFailure: Error | undefined;
    server.once("error", (error) => { startFailure = error; });
    safeToRemove = false;
    closed = new Promise((resolve) => server!.once("close", () => {
      safeToRemove = true;
      resolve();
    }));
    for (const stream of [server.stdout, server.stderr]) {
      stream?.on("data", (chunk: Buffer) => {
        if (Buffer.byteLength(output) + chunk.byteLength > 1 << 20) {
          startFailure = new Error("Embedded CLI output exceeded its bounded fixture budget.");
          server!.kill("SIGTERM");
        } else {
          output += chunk.toString();
        }
      });
    }
    const base = `http://127.0.0.1:${port}`;
    await expect.poll(async () => {
      if (startFailure) throw startFailure;
      if (server!.exitCode !== null || server!.signalCode !== null) throw new Error(`Embedded CLI exited: ${output}`);
      try {
        return (await request.get(`${base}/api/health`, { timeout: 1_000 })).status();
      } catch (error) {
        return error instanceof Error ? error.message : String(error);
      }
    }, { timeout: 15_000 }).toBe(200);
    const dist = new URL("../dist/", import.meta.url);
    const index = await request.get(base);
    expect(index.status()).toBe(200);
    expect(await index.body()).toEqual(readFileSync(new URL("index.html", dist)));
    const assets = (await readdir(new URL("assets/", dist), { recursive: true })).sort();
    let verified = 0;
    for (const asset of assets) {
      const physical = new URL(`assets/${asset}`, dist);
      if ((await lstat(physical)).isDirectory()) continue;
      expect((await lstat(physical)).isFile()).toBe(true);
      const response = await request.get(`${base}/assets/${asset}`);
      expect(response.status(), asset).toBe(200);
      expect(await response.body(), asset).toEqual(readFileSync(physical));
      verified++;
    }
    expect(verified).toBeGreaterThan(1);
    expect(assets.some((asset) => asset.endsWith(".js"))).toBe(true);
    expect(assets.some((asset) => asset.endsWith(".css"))).toBe(true);
    await page.goto(`${base}/#/lab/runner`);
    const panel = page.getByRole("region", { name: "Checkpoint creation and recovery" });
    await expect(panel).toBeVisible();
    const wrapper = {
      kind: "waza.execution-submission", schema_version: "1.0",
      request: JSON.parse(readFileSync(new URL("../../examples/runner/unit.json", import.meta.url), "utf8")),
    };
    await panel.getByRole("textbox", { name: "Captured checkpoint submission (JSON)" }).fill(JSON.stringify(wrapper));
    const createdResponse = page.waitForResponse((response) =>
      response.url() === `${base}/api/v1/checkpoints` && response.request().method() === "POST");
    await panel.getByRole("button", { name: "Create checkpoints and run unit", exact: true }).click();
    const createdHTTP = await createdResponse;
    expect(createdHTTP.status()).toBe(200);
    const created = await createdHTTP.json();
    expect(created.status).toBe("completed");
    expect(created.exit_code).toBe(0);
    expect(created.submission.terminal.outcome).toBe("success");
    expect(created.submission.engine_calls).toBe(1);
    expect(created.finalized_checkpoint_sha256).toMatch(/^[a-f0-9]{64}$/);
    await expect(panel.getByRole("status", { name: "Published checkpoint current metadata" })).toContainText("finalized-unit");
    const downloadEvent = page.waitForEvent("download");
    await panel.getByRole("button", { name: "Download checkpoint result JSON", exact: true }).click();
    const download = await downloadEvent;
    expect(await download.failure()).toBeNull();
    expect(download.suggestedFilename()).toBe("waza-checkpoint-result.json");
    const path = await download.path();
    expect(path).not.toBeNull();
    expect(readFileSync(path!)).toEqual(await createdHTTP.body());
    await panel.getByRole("textbox", { name: "Checkpoint digest to resume" }).fill(created.finalized_checkpoint_sha256);
    const resumedResponse = page.waitForResponse((response) =>
      response.url() === `${base}/api/v1/checkpoints/${created.finalized_checkpoint_sha256}/resume` && response.request().method() === "POST");
    await panel.getByRole("button", { name: "Resume checkpoint", exact: true }).click();
    const resumedHTTP = await resumedResponse;
    expect(resumedHTTP.status()).toBe(200);
    const resumed = await resumedHTTP.json();
    expect(resumed.status).toBe("reused");
    expect(resumed.exit_code).toBe(0);
    expect(resumed.restored_units).toBe(1);
    expect(resumed.remaining_work).toBe(0);
    expect(resumed.submission.engine_calls).toBe(0);
    expect(resumed.submission.terminal).toEqual(created.submission.terminal);
    expect(resumed.submission.reservation.execution_id).toBe(created.submission.reservation.execution_id);
    await expect(panel.getByRole("status", { name: "Checkpoint operation result" })).toContainText("reused");
  } finally {
    try {
      if (server && closed) {
        if (server.exitCode === null && server.signalCode === null) server.kill("SIGTERM");
        const graceful = await Promise.race([closed.then(() => true), delay(5_000).then(() => false)]);
        if (!graceful) {
          server.kill("SIGKILL");
          const forced = await Promise.race([closed.then(() => true), delay(5_000).then(() => false)]);
          expect(forced, `Embedded CLI streams did not close after force: ${output}`).toBe(true);
          throw new Error("Embedded CLI exceeded its graceful shutdown budget.");
        }
      }
      expect(await lstat(`${guard}.invoked`).then(() => true, (error: NodeJS.ErrnoException) => {
        if (error.code !== "ENOENT") throw error;
        return false;
      }), "Checkpoint controls must not launch the rejecting runtime.").toBe(false);
    } finally {
      if (!safeToRemove) throw new Error(`Embedded CLI cleanup did not join; retained fixture: ${directory}`);
      await rm(directory, { recursive: true, force: true });
    }
  }
});
