import { test, expect } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { once } from "node:events";
import { mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { createServer } from "node:net";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { inspectRunnerResult, parseRunnerResults } from "../src/api/runner";
import { runnerResult } from "./fixtures/runner-data";

test("archive 1.1 distinguishes recorded evidence from per-artifact errors", () => {
  const parsed = parseRunnerResults({
    schema_version: "1.1", next_cursor: null, results: [
      { artifact: "bad.runner.json", error: "malformed document" },
      { artifact: "large.runner.json", error: "artifact exceeds 16 MiB" },
      { artifact: "valid.runner.json", result: runnerResult("canceled") },
    ],
  });
  expect(parsed.results.map((entry) => entry.state)).toEqual(["malformed", "malformed", "recorded"]);
  const result = runnerResult();
  result.observation!.final_output = "é".repeat(8 * 1024 * 1024);
  expect(inspectRunnerResult(result)).toEqual({ state: "malformed", reason: "Runner artifact exceeds 16 MiB." });
  result.observation!.final_output = "<".repeat(3 * 1024 * 1024);
  expect(inspectRunnerResult(result)).toEqual({ state: "malformed", reason: "Runner artifact exceeds 16 MiB." });
  expect(() => parseRunnerResults({ schema_version: "1.1", results: [], next_cursor: 4 })).toThrow(/Malformed/);
});

test("real Go archive paginates over 16 MiB without hiding malformed siblings or inferring outcomes", async ({ page, request, browserName }) => {
  test.setTimeout(300_000);
  const root = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
  const workspace = join(root, ".runner-reference", `archive-browser-${process.pid}-${browserName}`);
  const artifacts = join(workspace, "results");
  const scratch = join(workspace, "scratch");
  mkdirSync(artifacts, { recursive: true });
  mkdirSync(scratch);
  const binary = join(workspace, "waza");
  const reservation = createServer();
  reservation.listen(0, "127.0.0.1");
  await once(reservation, "listening");
  const address = reservation.address();
  if (!address || typeof address === "string") throw new Error("No local port");
  const port = address.port;
  await new Promise<void>((done) => reservation.close(() => done()));
  execFileSync("go", ["build", "-o", binary, "./cmd/waza"], {
    cwd: root, env: { ...process.env, TMPDIR: scratch, GOTMPDIR: scratch },
  });
  const produced = JSON.parse(execFileSync(binary, ["runner", "run"], {
    cwd: root, input: readFileSync(join(root, "internal/runnercontract/testdata/request.json")),
    env: { ...process.env, TMPDIR: scratch },
  }).toString());
  expect(inspectRunnerResult(produced).state).toBe("recorded");
  produced.observation.final_output = "x".repeat(9 * 1024 * 1024);
  writeFileSync(join(artifacts, "a.runner.json"), JSON.stringify(produced));
  // Passing observations retained after cancellation are not successful outcomes.
  produced.outcome = "canceled";
  produced.exit_code = 3;
  produced.failure_boundary = "execution";
  produced.diagnostics = [{ code: "canceled", message: "recorded interruption" }];
  writeFileSync(join(artifacts, "z.runner.json"), JSON.stringify(produced));
  produced.observation.final_output = "";
  const overhead = Buffer.byteLength(JSON.stringify(produced));
  produced.observation.final_output = "x".repeat(16 * 1024 * 1024 - overhead - 1);
  writeFileSync(join(artifacts, "y.runner.json"), JSON.stringify(produced));
  writeFileSync(join(artifacts, "b.runner.json"), "{");
  writeFileSync(join(artifacts, "c.runner.json"), " ".repeat(16 * 1024 * 1024 + 1));
  const server = spawn(binary, ["serve", "--no-browser", "--lab", "--port", String(port), "--results-dir", artifacts], {
    cwd: root, stdio: "ignore", env: { ...process.env, TMPDIR: scratch },
  });
  const base = `http://127.0.0.1:${port}`;
  try {
    await expect.poll(async () => {
      try { return (await request.get(`${base}/api/v1/runner/capabilities`)).status(); } catch { return 0; }
    }).toBe(200);
    const first = await request.get(`${base}/api/v1/lab/runner-results`);
    const firstPage = await first.json();
    expect(firstPage.schema_version).toBe("1.1");
    expect(firstPage.next_cursor).toEqual(expect.any(String));
    expect((await first.body()).byteLength).toBeLessThanOrEqual(17 * 1024 * 1024);
    const second = await request.get(`${base}/api/v1/lab/runner-results?cursor=${encodeURIComponent(firstPage.next_cursor)}`);
    const secondPage = await second.json();
    expect(secondPage.next_cursor).toEqual(expect.any(String));
    expect((await second.body()).byteLength).toBeGreaterThan(16 * 1024 * 1024);
    expect((await second.body()).byteLength).toBeLessThanOrEqual(17 * 1024 * 1024);
    const third = await request.get(`${base}/api/v1/lab/runner-results?cursor=${encodeURIComponent(secondPage.next_cursor)}`);
    expect((await third.json()).next_cursor).toBeNull();
    await page.goto(`${base}/#/lab/runner`);
    await expect(page.getByRole("article")).toHaveCount(5);
    await expect(page.getByRole("heading", { name: "Result 1: success" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Result 4: canceled" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Result 5: canceled" })).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(2);
    await expect(page.getByRole("alert").filter({ hasText: "c.runner.json" })).toContainText("16 MiB");
  } finally {
    if (server.exitCode === null && server.signalCode === null) {
      server.kill("SIGTERM");
      await once(server, "exit");
    }
    rmSync(workspace, { recursive: true, force: true });
  }
});

test("non-progressing or oversized pages fail closed without retaining partial success", async ({ page }) => {
  await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/v1/lab/runner-results**", (route) => route.fulfill({ json: {
    schema_version: "1.1", next_cursor: "repeated", results: [{ artifact: "a.runner.json", result: runnerResult() }],
  } }));
  await page.goto("/#/lab/runner");
  await expect(page.getByRole("alert")).toContainText("non-progressing cursor");
  await expect(page.getByRole("article")).toHaveCount(0);
  await page.route("**/api/v1/lab/runner-results**", (route) => route.fulfill({
    contentType: "application/json", body: " ".repeat(17 * 1024 * 1024 + 1),
  }));
  await page.getByRole("button", { name: "Refresh runner results" }).click();
  await expect(page.getByRole("alert")).toContainText("invalid or ambiguous JSON");
  await expect(page.getByRole("article")).toHaveCount(0);
});
