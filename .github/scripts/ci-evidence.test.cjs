"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync, spawn } = require("node:child_process");
const { createHash } = require("node:crypto");
const { LIMITS, goReport, goParser, browserReport, browserInventory, summarizeActions, collectActions, collectObservations, artifactJSON, trends, historyProposal } = require("./ci-evidence.cjs");

const env = { GITHUB_REPOSITORY: "microsoft/waza", GITHUB_WORKFLOW: "Go Build and Test", GITHUB_EVENT_NAME: "push", GITHUB_SHA: "a".repeat(40), GITHUB_RUN_ID: "10", GITHUB_RUN_ATTEMPT: "1", GITHUB_JOB: "native", RUNNER_OS: "Linux", RUNNER_ARCH: "X64", GO_CACHE_HIT: "false", NODE_CACHE_HIT: "false" };
const parseGo = (events) => {
  const report = goReport(env, { go: "go version go1.26.4 linux/amd64" }), parser = goParser(report);
  for (const event of events) parser.accept(typeof event === "string" ? event : JSON.stringify(event));
  const result = parser.finish();
  result.execution_exit = result.counts.packages.fail ? 1 : 0;
  result.collector_exit = 0;
  result.discovery_exit = 0;
  result.discovery_collector_exit = 0;
  return result;
};
const event = (Action, Test, Elapsed) => ({ Action, Package: "example/p", ...(Test ? { Test } : {}), ...(Elapsed !== undefined ? { Elapsed } : {}) });
const successful = [event("start"), event("run", "TestA"), event("run", "TestA/skip"), event("skip", "TestA/skip", 0), event("pass", "TestA", 2), event("pass", null, 3)];

test("Go keeps top-level, subtest, package counts and non-overlapping units separate", () => {
  const report = parseGo([...successful.slice(0, -1), event("output", "TestA", undefined), event("pass", null, 3)]);
  assert.equal(report.complete, true);
  assert.equal(report.top_level_seconds, 2);
  assert.deepEqual(report.counts.top_level, { observed: 1, executed: 1, pass: 1, fail: 0, skip: 0, unfinished: 0 });
  assert.equal(report.counts.subtests.skip, 1);
  assert.equal(report.packages[0].seconds, 3);
});
test("Go malformed/truncated/cancelled/missing duration/duplicates cannot be complete", () => {
  for (const [events, diagnostic] of [
    [[...successful, '{"broken":'], "malformed-json"],
    [[...successful, "null"], "invalid-json-record"],
    [[event("start"), event("run", "TestA")], "unterminated-target"],
    [[event("start"), event("skip", "TestA"), event("pass", null, 1)], "duration-unavailable"],
    [[...successful, event("pass", "TestA", 2)], "duplicate-terminal"],
    [[], "execution-unavailable"],
  ]) {
    const report = parseGo(events);
    assert.equal(report.complete, false);
    assert.ok(report.diagnostics.includes(diagnostic));
  }
});
test("Go output and arbitrary event fields are not retained", () => {
  const report = parseGo([...successful, { ...event("output", "TestA"), Output: "SECRET", Token: "SECRET" }]);
  assert.ok(!JSON.stringify(report).includes("SECRET"));
});
test("Go telemetry admission stops at the byte cap but bounded late diagnostics remain on the console", () => {
  const report = goReport(env);
  let forwarded = 0, late = "";
  const parser = goParser(report, (output) => {
    forwarded++;
    if (output.includes("SECRET")) late += output;
  });
  parser.accept(JSON.stringify(event("start")));
  parser.accept(JSON.stringify(event("run", "TestA")));
  const padding = JSON.stringify({ ...event("output", "TestA"), Output: "x".repeat(LIMITS.line - 128) });
  assert.ok(Buffer.byteLength(padding) < LIMITS.line);
  const count = Math.ceil(LIMITS.bytes / Buffer.byteLength(padding));
  for (let index = 0; index < count; index++) parser.accept(padding);
  parser.accept(JSON.stringify({ ...event("output", "TestA"), Output: "SECRET late failure\n" }));
  parser.accept(JSON.stringify({ Action: "build-output", ImportPath: "example/p", Output: "SECRET compiler diagnostic\n" }));
  parser.accept(JSON.stringify(event("fail", "TestA", 4)));
  parser.finish();
  assert.equal(forwarded, count + 2);
  assert.equal(late, "SECRET late failure\nSECRET compiler diagnostic\n");
  assert.equal(report.complete, false);
  assert.ok(report.diagnostics.includes("input-byte-cap"));
  assert.equal(report.targets.length, 1);
  assert.equal(report.targets[0].status, null, "terminal measurements after the cap are not admitted");
  assert.ok(!JSON.stringify(report).includes("SECRET"));
});
test("production streaming capture forwards late failures beyond its report admission cap", { timeout: 30000 }, async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cap-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\n");
  const child = spawn(process.execPath, [path.join(__dirname, "ci-evidence.cjs"), "go", "report.json", "all", "all"], { cwd: dir, env: { ...process.env, ...env } });
  t.after(() => { if (child.exitCode === null) child.kill("SIGTERM"); });
  let tail = "", errors = "";
  child.stdout.on("data", (data) => { tail = (tail + data.toString()).slice(-1024); });
  child.stderr.on("data", (data) => { errors = (errors + data.toString()).slice(-4096); });
  const closed = new Promise((resolve, reject) => { child.once("close", resolve); child.once("error", reject); });
  const write = (line) => new Promise((resolve, reject) => child.stdin.write(line, (error) => error ? reject(error) : resolve()));
  await write(JSON.stringify(event("start")) + "\n");
  await write(JSON.stringify(event("run", "TestA")) + "\n");
  const padding = JSON.stringify({ ...event("output", "TestA"), Output: "x".repeat(LIMITS.line - 128) }) + "\n";
  for (let index = 0; index < Math.ceil(LIMITS.bytes / Buffer.byteLength(padding)); index++) await write(padding);
  await write(JSON.stringify({ ...event("output", "TestA"), Output: "SECRET late failure\n" }) + "\n");
  await write(JSON.stringify(event("fail", "TestA", 4)) + "\n");
  child.stdin.end();
  assert.equal(await closed, 0, errors);
  assert.ok(tail.includes("SECRET late failure"));
  const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json"), "utf8"));
  assert.equal(report.complete, false);
  assert.ok(report.diagnostics.includes("input-byte-cap"));
  assert.equal(report.targets[0].status, null);
  assert.ok(!JSON.stringify(report).includes("SECRET"));
});
test("production Go capture executes a real no-provider test and preserves failure exit with pipefail", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-evidence-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
  fs.writeFileSync(path.join(dir, "example_test.go"), 'package p\nimport "testing"\nfunc TestA(t *testing.T) { t.Run("skip", func(t *testing.T) {t.Skip("SECRET")}) }\nfunc TestFailure(t *testing.T) {t.Fatal("SECRET")}\n');
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\nexample/p::TestFailure\n");
  const collector = path.join(__dirname, "ci-evidence.cjs");
  const result = spawnSync("bash", ["-c", 'set -o pipefail; go test -count=1 -json -v . | node "$1" go report.json all all; status=("${PIPESTATUS[@]}"); node "$1" finish-go report.json "${status[0]}" "${status[1]}"; exit "${status[0]}"', "bash", collector], { cwd: dir, env: { ...process.env, ...env }, encoding: "utf8", timeout: 120000 });
  assert.equal(result.status, 1, result.stderr);
  const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json"), "utf8"));
  assert.equal(report.complete, true, JSON.stringify(report.diagnostics));
  assert.equal(report.counts.top_level.executed, 2);
  assert.equal(report.counts.top_level.fail, 1);
  assert.equal(report.counts.subtests.skip, 1);
  assert.ok(result.stdout.includes("SECRET"), "console output preserved");
  assert.ok(!JSON.stringify(report).includes("SECRET"), "artifact sanitized");
  assert.equal(report.identity.source_sha, null, "isolated fixture has no source checkout");
  const truncated = spawnSync(process.execPath, [collector, "go", "partial.json", "all", "all"], { cwd: dir, env: { ...process.env, ...env }, input: '{"Action":"run"', encoding: "utf8" });
  assert.equal(truncated.status, 0, truncated.stderr);
  const partial = JSON.parse(fs.readFileSync(path.join(dir, "partial.json")));
  assert.equal(partial.complete, false);
  assert.ok(partial.diagnostics.includes("truncated-json"));
});
test("production discovery admits actual Go list output but rejects execution and truncation", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-discovery-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
  fs.writeFileSync(path.join(dir, "example_test.go"), 'package p\nimport "testing"\nfunc TestA(t *testing.T) {t.Fatal("must not run")}\n');
  const collector = path.join(__dirname, "ci-evidence.cjs");
  const result = spawnSync("bash", ["-c", 'set -o pipefail; go test -json -race -covermode=atomic -list . . | node "$1" discover all', "bash", collector], { cwd: dir, encoding: "utf8", timeout: 120000 });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(fs.readFileSync(path.join(dir, "all"), "utf8"), "example/p::TestA\n");
  for (const input of [JSON.stringify(event("run", "TestA")) + "\n", '{"Action":"output"']) {
    const rejected = spawnSync(process.execPath, [collector, "discover", "invalid"], { cwd: dir, input, encoding: "utf8" });
    assert.equal(rejected.status, 1);
    assert.equal(fs.existsSync(path.join(dir, "invalid")), false);
  }
});
test("actual Go ImportPath build events preserve compiler diagnostics without uploading them", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-build-failure-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
  fs.writeFileSync(path.join(dir, "bad.go"), "package p\nvar _ = SECRETBuildFailureSentinel\n");
  const produced = spawnSync("go", ["test", "-count=1", "-json", "."], { cwd: dir, encoding: "utf8", timeout: 30000 });
  assert.equal(produced.status, 1, produced.stderr);
  const events = produced.stdout.trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(events.some((event) => event.Action === "build-output" && event.ImportPath === "example/p" && event.Package === undefined));
  assert.ok(events.some((event) => event.Action === "build-fail" && event.ImportPath === "example/p"));
  const collector = path.join(__dirname, "ci-evidence.cjs");
  const captured = spawnSync(process.execPath, [collector, "go", "report.json", "missing", "missing"], { cwd: dir, input: produced.stdout, encoding: "utf8" });
  assert.equal(captured.status, 0, captured.stderr);
  assert.ok(captured.stdout.includes("SECRETBuildFailureSentinel"));
  const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json"), "utf8"));
  assert.equal(report.complete, false);
  assert.ok(report.diagnostics.includes("build-failed"));
  assert.equal(report.counts.packages.fail, 1);
  assert.ok(!JSON.stringify(report).includes("SECRET"));
  const discovery = spawnSync(process.execPath, [collector, "discover", "all"], { cwd: dir, input: produced.stdout, encoding: "utf8" });
  assert.equal(discovery.status, 1, discovery.stderr);
  assert.ok(discovery.stdout.includes("SECRETBuildFailureSentinel"));
  assert.equal(fs.existsSync(path.join(dir, "all")), false);
});
test("production collector cancellation saves available unfinished evidence", { timeout: 30000 }, async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cancel-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\n");
  const child = spawn(process.execPath, [path.join(__dirname, "ci-evidence.cjs"), "go", "partial.json", "all", "all"], { cwd: dir, env: { ...process.env, ...env } });
  t.after(() => { if (child.exitCode === null) child.kill("SIGTERM"); });
  const closed = new Promise((resolve, reject) => { child.once("close", resolve); child.once("error", reject); });
  const ready = new Promise((resolve) => child.stdout.once("data", resolve));
  child.stdin.write([event("start"), event("run", "TestA"), { ...event("output", "TestA"), Output: "ready" }].map(JSON.stringify).join("\n") + "\n");
  await ready;
  child.kill("SIGTERM");
  assert.equal(await closed, 143);
  const report = JSON.parse(fs.readFileSync(path.join(dir, "partial.json")));
  assert.equal(report.complete, false);
  assert.ok(report.diagnostics.includes("collector-cancelled"));
  assert.equal(report.counts.top_level.unfinished, 1);
});

function browser() {
  return { config: { version: "1.58.0", workers: 2, projects: [{ id: "chromium", name: "chromium", retries: 0 }] }, errors: [], stats: { duration: 2500 }, suites: [{ specs: [{ id: "test-id", tests: [{ projectId: "chromium", expectedStatus: "passed", status: "expected", results: [{ retry: 0, status: "passed", duration: 1500, stdout: ["SECRET"], attachments: [{ path: "SECRET" }] }] }] }], suites: [] }] };
}
test("browser discovery, policy, skips, retry failures and missing durations retain actual distinctions", () => {
  const data = browser(), discovery = browserInventory(data);
  const report = browserReport(data, env, discovery);
  assert.equal(report.complete, true);
  assert.equal(report.counts.executed, 1);
  assert.equal(report.runner_seconds, 2.5);
  assert.ok(!JSON.stringify(report).includes("SECRET"));
  const target = data.suites[0].specs[0].tests[0];
  target.status = "flaky";
  target.results = [{ retry: 0, status: "failed", duration: 10 }, { retry: 1, status: "passed", duration: 20 }];
  const retried = browserReport(data, env, discovery);
  assert.equal(retried.counts.first_attempt_failures, 1);
  assert.equal(retried.counts.flaky, 1);
  assert.equal(retried.targets[0].results.length, 2);
  target.status = "skipped"; target.results = [{ retry: 0, status: "skipped", duration: 0 }];
  assert.equal(browserReport(data, env, discovery).counts.skip, 1);
  delete target.results[0].duration;
  assert.ok(browserReport(data, env, discovery).diagnostics.includes("duration-unavailable"));
  target.results[0].status = "interrupted";
  assert.ok(browserReport(data, env, discovery).diagnostics.includes("browser-interrupted"));
  assert.ok(browserReport(data, env, []).diagnostics.includes("selection-execution-mismatch"));
});
test("browser step outcomes distinguish complete test failures from late command failures and unavailable execution", () => {
  const data = browser(), discovery = browserInventory(data);
  const observed = { ...env, BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "success" };
  assert.equal(browserReport(data, observed, discovery).complete, true);
  for (const [key, value, code] of [
    ["BROWSER_DISCOVERY_OUTCOME", "failure", "browser-discovery-not-successful"],
    ["BROWSER_EXECUTION_OUTCOME", "failure", "browser-execution-failed-without-terminal-evidence"],
    ["BROWSER_EXECUTION_OUTCOME", "cancelled", "browser-execution-not-completed"],
    ["BROWSER_EXECUTION_OUTCOME", "skipped", "browser-execution-not-completed"],
    ["BROWSER_EXECUTION_OUTCOME", "SECRET", "browser-outcome-invalid"],
  ]) {
    const report = browserReport(data, { ...observed, [key]: value }, discovery);
    assert.equal(report.complete, false, value);
    assert.ok(report.diagnostics.includes(code), value);
    assert.ok(!JSON.stringify(report).includes("SECRET"));
  }
  const unknown = browserReport(data, env, discovery);
  assert.equal(unknown.discovery_outcome, null);
  assert.equal(unknown.execution_outcome, null);
  const target = data.suites[0].specs[0].tests[0];
  target.status = "unexpected";
  target.results[0].status = "failed";
  const failed = browserReport(data, { ...observed, BROWSER_EXECUTION_OUTCOME: "failure" }, discovery);
  assert.equal(failed.complete, true, "observed terminal test failure is complete failure evidence");
  assert.equal(failed.counts.fail, 1);
});
test("production browser collector retains available discovery when execution is missing and diagnoses malformed discovery independently", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-browser-missing-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const collector = path.join(__dirname, "ci-evidence.cjs");
  fs.writeFileSync(path.join(dir, "discovery.json"), JSON.stringify(browser()));
  const execute = (input, output, outcomes) => spawnSync(process.execPath, [collector, "browser", input, output, "discovery.json"], {
    cwd: dir, env: { ...process.env, ...env, ...outcomes }, encoding: "utf8",
  });
  assert.equal(execute("missing.json", "missing-report.json", { BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "skipped" }).status, 0);
  const missing = JSON.parse(fs.readFileSync(path.join(dir, "missing-report.json")));
  assert.equal(missing.complete, false);
  assert.deepEqual(missing.discovered, ["chromium::test-id"]);
  assert.equal(missing.execution_outcome, "skipped");
  assert.equal(Object.hasOwn(missing, "counts"), false, "unavailable execution must not become zero counts");
  assert.ok(missing.diagnostics.includes("browser-input-invalid-or-missing"));
  fs.writeFileSync(path.join(dir, "discovery.json"), '{"suites":');
  fs.writeFileSync(path.join(dir, "actual.json"), JSON.stringify(browser()));
  assert.equal(execute("actual.json", "invalid-report.json", { BROWSER_DISCOVERY_OUTCOME: "failure", BROWSER_EXECUTION_OUTCOME: "skipped" }).status, 0);
  const invalid = JSON.parse(fs.readFileSync(path.join(dir, "invalid-report.json")));
  assert.equal(invalid.complete, false);
  assert.equal(invalid.discovered, null);
  assert.equal(invalid.targets.length, 1, "retain observed execution records independently of invalid discovery");
  assert.ok(invalid.diagnostics.includes("browser-discovery-invalid-or-missing"));
  assert.ok(invalid.diagnostics.includes("browser-discovery-not-successful"));
});

const run = { id: 10, repository: { full_name: "microsoft/waza" }, head_sha: "a".repeat(40), event: "push", name: env.GITHUB_WORKFLOW, workflow_id: 20, status: "completed", conclusion: "success", run_attempt: 2, created_at: "2026-11-07T10:00:00Z" };
const job = (id, start, end, conclusion = "success", attempt = 1) => ({ id, run_id: 10, head_sha: run.head_sha, run_attempt: attempt, name: `job-${id}`, labels: ["ubuntu-latest"], status: "completed", conclusion, started_at: `2026-11-07T10:${start}:00Z`, completed_at: end === null ? null : `2026-11-07T10:${end}:00Z`, steps: [{ number: 1, name: "Tests", conclusion, started_at: `2026-11-07T10:${start}:00Z`, completed_at: end === null ? null : `2026-11-07T10:${end}:00Z` }] });
test("Actions preserves original failures and deduplicates retained jobs across retry attempts", () => {
  const first = job(1, "01", "03", "failure"), retained = job(2, "01", "04"), retry = job(3, "05", "07", "success", 2);
  const report = summarizeActions(run, [{ number: 1, complete: true, created_at: run.created_at, started_at: first.started_at, jobs: [first, retained] }, { number: 2, complete: true, jobs: [retained, retry] }], []);
  assert.equal(report.complete, true);
  assert.deepEqual(report.attempts[0].original_failures, [1]);
  assert.equal(report.attempts[0].observed_job_span_seconds, 180);
  assert.equal(report.attempts[1].observed_job_span_seconds, 120);
  assert.equal(report.attempts[1].retained_job_span_seconds, 360);
  assert.equal(report.runner_elapsed_minutes_by_os["ubuntu-latest"], 7);
  assert.deepEqual(report.jobs.find((item) => item.id === 2).seen_in_attempts, [1, 2]);
  assert.equal(report.attempts[0].queue_start_delay_seconds, 60);
  assert.equal(report.attempts[1].queue_start_delay_seconds, null);
});
test("duplicate job IDs and missing step durations invalidate metadata instead of hiding first failures", () => {
  const original = job(1, "01", "03", "failure");
  const duplicate = job(1, "01", "03");
  const report = summarizeActions(run, [{ number: 1, complete: true, jobs: [original, duplicate] }], []);
  assert.equal(report.complete, false);
  assert.ok(report.diagnostics.includes("duplicate-job-in-attempt"));
  assert.deepEqual(report.attempts[0].original_failures, [1]);
  const missing = job(2, "01", "03");
  missing.steps[0].completed_at = null;
  assert.ok(summarizeActions(run, [{ number: 1, complete: true, jobs: [missing] }], []).diagnostics.includes("step-duration-unavailable"));
});
test("Actions cancellation, missing timestamps and artifact provenance never manufacture totals", () => {
  const report = summarizeActions(run, [{ number: 1, complete: false, jobs: [job(1, "01", null, "cancelled")] }], [{ id: 1, name: "artifact", size_in_bytes: 42, expired: true, workflow_run: { id: 99, head_sha: run.head_sha } }]);
  assert.equal(report.complete, false);
  assert.equal(report.runner_elapsed_minutes_by_os["ubuntu-latest"], null);
  assert.equal(report.attempts[0].observed_job_span_seconds, null);
  assert.deepEqual(report.attempts[0].cancellations, [1]);
  assert.equal(report.artifacts[0].bytes, null);
  assert.equal(report.artifacts[0].expired, true);
});
test("Actions production collector uses attempt endpoints, bounded pagination and visible API failures", () => {
  const calls = [];
  const api = (endpoint) => {
    calls.push(endpoint);
    if (!endpoint.includes("/attempts/") && !endpoint.includes("?")) return run;
    if (endpoint.endsWith("/attempts/1")) return { run_started_at: "2026-11-07T10:01:00Z" };
    if (endpoint.endsWith("/attempts/2")) return { run_started_at: "2026-11-07T10:05:00Z" };
    if (endpoint.includes("/jobs")) {
      const attempt = endpoint.includes("/attempts/1/") ? 1 : 2;
      return { total_count: 1, jobs: [job(attempt, "01", "02", attempt === 1 ? "failure" : "success", attempt)] };
    }
    return { total_count: 1, artifacts: [{ id: 1, name: "ci-evidence", size_in_bytes: 42, expired: false, workflow_run: { id: 10, head_sha: run.head_sha } }] };
  };
  const report = collectActions("10", api);
  assert.equal(report.complete, true);
  assert.ok(calls.some((endpoint) => endpoint.includes("/attempts/1/jobs?per_page=100&page=1")));
  assert.equal(report.artifacts[0].bytes, 42);
  assert.equal(collectActions("10", () => { throw new Error("SECRET"); }).complete, false);
  const capped = collectActions("10", (endpoint) => endpoint.includes("/jobs") ? { total_count: 1001, jobs: [] } : api(endpoint));
  assert.ok(capped.diagnostics.includes("jobs-pagination-incomplete"));
  assert.ok(!JSON.stringify(capped).includes("SECRET"));
});
function archive(report, name = "coverage-native-0.txt.evidence.json") {
  const data = Buffer.from(JSON.stringify(report)), filename = Buffer.from(name);
  const local = Buffer.alloc(30), central = Buffer.alloc(46), end = Buffer.alloc(22);
  local.writeUInt32LE(0x04034b50, 0); local.writeUInt32LE(data.length, 18); local.writeUInt32LE(data.length, 22); local.writeUInt16LE(filename.length, 26);
  central.writeUInt32LE(0x02014b50, 0); central.writeUInt32LE(data.length, 20); central.writeUInt32LE(data.length, 24); central.writeUInt16LE(filename.length, 28);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(1, 8); end.writeUInt16LE(1, 10);
  end.writeUInt32LE(central.length + filename.length, 12); end.writeUInt32LE(local.length + filename.length + data.length, 16);
  return Buffer.concat([local, filename, data, central, filename, end]);
}
function verified(report, overrides = {}, family = report.kind === "playwright" ? "browser" : "go-native") {
  const filename = family === "browser" ? "ci-evidence-browser.json" : family === "go-run" ? "coverage.txt.evidence.json" : "coverage-native-0.txt.evidence.json";
  const zip = archive(report, filename), id = Number(report.identity.run_id);
  const name = `ci-evidence-${family}-${id}-${report.identity.attempt}` + (family === "go-native" ? "-0" : family === "go-run" ? `-${report.identity.job}` : "");
  const fixtureRun = { ...run, id, run_attempt: 1, run_started_at: "2026-11-07T10:01:00Z", ...overrides };
  const api = (endpoint) => {
    if (endpoint.includes("/jobs")) return { total_count: 1, jobs: [{ ...job(1, "01", "02"), run_id: id }] };
    if (endpoint.includes("/artifacts")) return { total_count: 1, artifacts: [{ id: 100, name, size_in_bytes: zip.length, expired: false, digest: `sha256:${createHash("sha256").update(zip).digest("hex")}`, workflow_run: { id, head_sha: fixtureRun.head_sha } }] };
    if (endpoint.includes("/attempts/")) return { run_started_at: fixtureRun.run_started_at };
    return fixtureRun;
  };
  return collectObservations(String(id), api, () => zip);
}
test("first-attempt timestamp omissions or invalid pairs withhold complete metadata and baseline admission", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  for (const overrides of [
    { created_at: null }, { created_at: "invalid" },
    { run_started_at: null }, { run_started_at: "invalid" },
    { created_at: "2026-11-07T10:02:00Z" },
  ]) {
    const observed = verified(report, overrides);
    assert.equal(observed.reports.length, 1, "preserve authenticated observed test content");
    assert.ok(observed.metadata.jobs.length > 0);
    assert.equal(observed.metadata.complete, false);
    assert.equal(observed.metadata.attempts[0].queue_start_delay_seconds, null);
    assert.ok(observed.diagnostics.includes("first-attempt-timestamps-unavailable"));
    assert.equal(trends(observed.reports).cohorts[0].eligible.length, 0);
  }
  const zero = verified(report, { created_at: "2026-11-07T10:01:00Z" });
  assert.equal(zero.metadata.complete, true);
  assert.equal(zero.metadata.attempts[0].queue_start_delay_seconds, 0);
  assert.equal(trends(zero.reports).cohorts[0].eligible.length, 1);
});
test("authenticated event and workflow mismatches reject otherwise source-matching artifacts", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  assert.equal(verified(report).reports.length, 1);
  for (const identity of [{ event: "workflow_dispatch" }, { workflow: "Different workflow" }]) {
    const observed = verified({ ...report, identity: { ...report.identity, ...identity } });
    assert.equal(observed.reports.length, 0);
    assert.ok(observed.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
  }
});
test("history preserves same-run platform and job observations while deduplicating repeated copies", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  const windows = { ...report, identity: { ...report.identity, job: "windows", os: "Windows", go: "go version go1.26.4 windows/amd64" } };
  const otherJob = { ...report, identity: { ...report.identity, job: "another-linux-job" } };
  const linux = verified(report, {}, "go-run").reports[0];
  const win = verified(windows, {}, "go-run").reports[0];
  const other = verified(otherJob, {}, "go-run").reports[0];
  const proposal = historyProposal([linux, win, other, linux, win]);
  assert.equal(proposal.targets.length, 1);
  const observations = proposal.targets[0].observations;
  assert.equal(observations.length, 3);
  assert.deepEqual(observations.map((item) => item.job), ["native", "windows", "another-linux-job"]);
  assert.deepEqual(observations.map((item) => item.cohort.os), ["Linux", "Windows", "Linux"]);
  assert.ok(observations.every((item) => item.run_id === "10" && item.attempt === "1" && item.seconds === 2));
});
test("ordinary job artifacts and authenticated incomplete observations retain source binding without invented selection or eligibility", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  report.identity.job = "build-and-test";
  const ordinary = verified(report, {}, "go-run");
  assert.equal(ordinary.reports.length, 1);
  assert.equal(trends(ordinary.reports).cohorts[0].eligible.length, 1);
  for (const absent of [false, true]) {
    const partial = { ...report, complete: false, selected: null, policy: null, diagnostics: ["execution-not-observed"] };
    if (absent) { delete partial.selected; delete partial.policy; }
    const retained = verified(partial, { conclusion: "failure" }, "go-run");
    assert.equal(retained.reports.length, 1, "retain authenticated diagnostic artifact after failed admission/discovery");
    assert.equal(Object.hasOwn(retained.reports[0], "selected"), !absent);
    assert.equal(retained.reports[0].selected, absent ? undefined : null);
    assert.equal(trends(retained.reports).cohorts[0].eligible.length, 0);
    assert.equal(historyProposal(retained.reports).targets.length, 0);
  }
  for (const malformed of [{ selected: "not-an-inventory" }, { policy: [] }, { cache: [] }]) {
    assert.equal(verified({ ...report, complete: false, diagnostics: ["missing"], ...malformed }, {}, "go-run").reports.length, 0);
  }
});
test("verified browser baselines require successful recorded discovery and execution outcomes", () => {
  const report = browserReport(browser(), { ...env, BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "success" }, ["chromium::test-id"]);
  report.identity.source_sha = run.head_sha;
  const admitted = verified(report);
  assert.equal(admitted.reports.length, 1);
  assert.equal(trends(admitted.reports).cohorts[0].eligible.length, 1, "successful observed outcomes are an alternative-valid control");
  for (const field of ["discovery_outcome", "execution_outcome"]) {
    for (const value of [null, "failure", "cancelled", "skipped"]) {
      const withheld = verified({ ...report, [field]: value });
      assert.equal(withheld.reports.length, 1, "retain authenticated observations while withholding eligibility");
      assert.equal(trends(withheld.reports).cohorts[0].eligible.length, 0, `${field}=${value}`);
    }
  }
});
test("trend eligibility requires verified distinct comparable first-run samples; authored JSON cannot be promoted", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"];
  report.discovered = report.selected;
  report.identity.source_sha = "a".repeat(40);
  const authored = ["10", "11", "12"].map((id) => ({ ...report, identity: { ...report.identity, run_id: id } }));
  assert.equal(trends(authored).cohorts[0].eligible.length, 0);
  assert.equal(historyProposal(authored).targets.length, 0);
  const reports = authored.map((report) => verified(report).reports[0]);
  const group = trends([...reports, reports[0]]).cohorts[0];
  assert.equal(group.eligible.length, 3);
  assert.equal(group.minimum_samples_available, true);
  assert.equal(group.attainment, "not-assessed");
  const retry = { ...reports[0], identity: { ...reports[0].identity, attempt: "2" } };
  assert.equal(trends([retry]).cohorts[0].eligible.length, 0);
  const missing = { ...report, cache: { ...report.cache, go_dependencies: null } };
  assert.equal(trends([missing]).cohorts[0].eligible.length, 0);
  assert.equal(trends([reports[0], { ...reports[1], selected: ["another"] }]).cohorts.length, 2);
  const proposal = historyProposal(reports);
  assert.equal(proposal.targets.length, 1);
  assert.equal(proposal.targets[0].observations.length, 3);
  assert.equal(proposal.targets[0].observations[0].seconds, 2);
  assert.equal(proposal.targets[0].observations[0].provenance.artifact_id, 100);
  assert.match(proposal.targets[0].observations[0].report_content_sha256, /^[0-9a-f]{64}$/);
});
test("artifact content, filename, source and expiry must verify independently", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  const zip = archive(report);
  const digest = `sha256:${createHash("sha256").update(zip).digest("hex")}`;
  assert.equal(artifactJSON(zip, "coverage-native-0.txt.evidence.json", digest).report.kind, "go");
  assert.throws(() => artifactJSON(zip, "other.json", digest), /artifact-zip-entry/);
  assert.throws(() => artifactJSON(zip, "coverage-native-0.txt.evidence.json", null), /artifact-digest-unverified/);
  assert.throws(() => artifactJSON(zip.subarray(0, 10), "coverage-native-0.txt.evidence.json", digest), /artifact-digest-unverified/);
  const wrongSource = { ...report, identity: { ...report.identity, workflow_sha: "other" } };
  assert.equal(verified(wrongSource).reports.length, 0);
  assert.ok(verified(wrongSource).diagnostics.includes("evidence-artifact-invalid-or-unverified"));
  const counterfeitCheckout = { ...report, identity: { ...report.identity, source_sha: "b".repeat(40) } };
  assert.equal(verified(counterfeitCheckout).reports.length, 0);
  assert.ok(verified(counterfeitCheckout).diagnostics.includes("evidence-artifact-invalid-or-unverified"));
  const mergeObservation = { ...report, identity: { ...report.identity, event: "pull_request", source_sha: "b".repeat(40), workflow_sha: "b".repeat(40) } };
  const pullRequest = verified(mergeObservation, { event: "pull_request" });
  assert.equal(pullRequest.reports.length, 1, "retain authenticated artifact contents without inventing checkout binding");
  assert.ok(pullRequest.diagnostics.includes("pull-request-merge-checkout-binding-unavailable"));
  assert.equal(trends(pullRequest.reports).cohorts[0].observations[0].provenance.checkout_binding, "unavailable-pr-merge");
  assert.equal(trends(pullRequest.reports).cohorts[0].eligible.length, 0);
  assert.equal(historyProposal(pullRequest.reports).targets.length, 0);
});
test("latest API retry state and unavailable exact runtimes cannot become first-pass timing samples", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  const masquerading = verified(report, { run_attempt: 2 }).reports[0];
  assert.equal(trends([masquerading]).cohorts[0].eligible.length, 0);
  assert.equal(historyProposal([masquerading]).targets.length, 0);
  assert.equal(verified({ ...report, identity: { ...report.identity, attempt: "2" } }).reports.length, 0);
  const failed = verified(report, { conclusion: "failure" }).reports[0];
  assert.equal(trends([failed]).cohorts[0].eligible.length, 0);
  for (const field of ["go", "node", "os_release", "os_version", "job"]) {
    const missing = verified({ ...report, identity: { ...report.identity, [field]: null } }).reports[0];
    assert.equal(trends([missing]).cohorts[0].eligible.length, 0, field);
    assert.equal(historyProposal([missing]).targets.length, 0, field);
  }
});
