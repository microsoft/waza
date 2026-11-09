"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync, spawn } = require("node:child_process");
const { createHash } = require("node:crypto");
const { deflateRawSync } = require("node:zlib");
const { LIMITS, goReport, goParser, browserReport: parseBrowserReport, browserInventory, summarizeActions, collectActions, collectObservations, artifactJSON, trends, historyProposal, writeHistoryProposal, main } = require("./ci-evidence.cjs");
const isolatedCache = require("./ci-evidence-cache.cjs");

// Fixture collectors cannot reuse the enclosing workflow's private observation.
process.env.CI_EVIDENCE_COLD_REQUESTED = "false";

const env = { GITHUB_REPOSITORY: "microsoft/waza", GITHUB_WORKFLOW: "Go Build and Test", GITHUB_EVENT_NAME: "push", GITHUB_SHA: "a".repeat(40), GITHUB_RUN_ID: "10", GITHUB_RUN_ATTEMPT: "1", GITHUB_JOB: "native", RUNNER_OS: "Linux", RUNNER_ARCH: "X64", GO_CACHE_HIT: "false", NODE_CACHE_HIT: "false" };
const browserReport = (data, env, discovery, runtime = { go: "go version go1.26.4 linux/amd64" }) => parseBrowserReport(data, env, discovery, runtime);
const parseGo = (events, selection = {}) => {
  const report = Object.assign(goReport(env, { go: "go version go1.26.4 linux/amd64" }), selection), parser = goParser(report);
  if (Array.isArray(selection.selected) && !Object.hasOwn(selection, "discovered")) report.discovered = [...selection.selected];
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
test("production Go capture preserves clean release VCS metadata without hiding unrelated files", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-release-vcs-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const source = path.join(dir, "source");
  fs.mkdirSync(source);
  const scripts = path.join(source, ".github", "scripts");
  fs.mkdirSync(scripts, { recursive: true });
  for (const name of ["ci_go_test_capture.sh", "ci-evidence.cjs", "ci-evidence-cache.cjs", "release_binary_check.sh"]) {
    fs.copyFileSync(path.join(__dirname, name), path.join(scripts, name));
  }
  fs.copyFileSync(path.join(__dirname, "..", "..", ".gitignore"), path.join(source, ".gitignore"));
  for (const [name, content] of [
    ["go.mod", "module example.test/ci-release-vcs\n\ngo 1.26\n"],
    ["main.go", "package main\nfunc main() {}\n"],
    ["main_test.go", 'package main\nimport "testing"\nfunc TestNoProvider(t *testing.T) {}\n'],
  ]) fs.writeFileSync(path.join(source, name), content);
  const execute = (binary, args, environment = {}, expected = 0) => {
    const result = spawnSync(binary, args, { cwd: source, env: { ...process.env, ...environment },
      encoding: "utf8", timeout: 120000, maxBuffer: LIMITS.bytes });
    assert.equal(result.error, undefined);
    assert.equal(result.status, expected, `${binary} ${args.join(" ")}\n${result.stdout}\n${result.stderr}`);
    return result.stdout.trim();
  };
  execute("git", ["init", "-q"]);
  execute("git", ["add", "."]);
  execute("git", ["-c", "commit.gpgsign=false", "-c", "user.name=CI fixture",
    "-c", "user.email=ci-fixture@example.invalid", "commit", "-qm", "fixture"]);
  const sha = execute("git", ["rev-parse", "HEAD"]);
  const environment = { ...env, GITHUB_SHA: sha, CI_EVIDENCE_COLD_REQUESTED: "false" };
  assert.equal(execute("git", ["status", "--porcelain"]), "");
  execute("bash", [path.join(scripts, "ci_go_test_capture.sh"), "all", "coverage.txt"], environment);
  const report = JSON.parse(fs.readFileSync(path.join(source, "coverage.txt.evidence.json"), "utf8"));
  assert.equal(report.complete, true);
  assert.equal(report.counts.top_level.pass, 1);
  for (const name of ["coverage.txt", "coverage.txt.all", "coverage.txt.selected", "coverage.txt.evidence.json"]) {
    assert.ok(fs.statSync(path.join(source, name)).size > 0, name);
  }
  const binary = path.join(dir, process.platform === "win32" ? "waza.exe" : "waza");
  execute("go", ["build", "-o", binary, "."]);
  execute("bash", [path.join(scripts, "release_binary_check.sh"), binary], environment);
  assert.equal(execute("git", ["status", "--porcelain"]), "");
  fs.writeFileSync(path.join(source, "unrelated-user-file.txt"), "not generated evidence\n");
  execute("go", ["build", "-o", binary, "."]);
  execute("bash", [path.join(scripts, "release_binary_check.sh"), binary], environment, 1);
  assert.ok(execute("git", ["status", "--porcelain"]).includes("unrelated-user-file.txt"));
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
test("production capture bounds the actual slow-reader stdout queue", { timeout: 30000 }, async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-backpressure-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\n");
  const script = `
    const write = process.stdout.write.bind(process.stdout);
    let maximum = 0, notified = false;
    process.stdout.write = (...args) => {
      const accepted = write(...args);
      maximum = Math.max(maximum, process.stdout.writableLength);
      if (!accepted && !notified) { notified = true; process.send({ blocked: true }); }
      return accepted;
    };
    require(${JSON.stringify(path.join(__dirname, "ci-evidence.cjs"))}).main(["go", "report.json", "all", "all"])
      .then(() => process.send({ maximum, highWaterMark: process.stdout.writableHighWaterMark }, () => process.disconnect()))
      .catch(error => { process.stderr.write(error.message); process.exitCode = 1; process.disconnect(); });
  `;
  const child = spawn(process.execPath, ["-e", script], { cwd: dir, env: { ...process.env, ...env }, stdio: ["pipe", "pipe", "pipe", "ipc"] });
  t.after(() => { if (child.exitCode === null) child.kill("SIGTERM"); });
  let errors = "", maximum = null, highWaterMark = null, received = 0, timer = null;
  child.stderr.on("data", (data) => { errors = (errors + data).slice(-4096); });
  const blocked = new Promise((resolve) => child.on("message", (message) => {
    if (message.blocked) resolve();
    else { maximum = message.maximum; highWaterMark = message.highWaterMark; }
  }));
  const closed = new Promise((resolve, reject) => { child.once("close", resolve); child.once("error", reject); });
  const write = (line) => new Promise((resolve, reject) => child.stdin.write(line, (error) => error ? reject(error) : resolve()));
  const padding = JSON.stringify({ ...event("output", "TestA"), Output: "x".repeat(65536) }) + "\n";
  const feeding = (async () => {
    await write(JSON.stringify(event("start")) + "\n");
    await write(JSON.stringify(event("run", "TestA")) + "\n");
    for (let index = 0; index < 128; index++) await write(padding);
    await write(JSON.stringify(event("pass", "TestA", 2)) + "\n");
    await write(JSON.stringify(event("pass", null, 3)) + "\n");
    child.stdin.end();
  })();
  await blocked;
  t.after(() => clearTimeout(timer));
  child.stdout.on("data", (chunk) => {
    received += chunk.length;
    child.stdout.pause();
    timer = setTimeout(() => child.stdout.resume(), 1);
  });
  await feeding;
  assert.equal(await closed, 0, errors);
  assert.equal(received, 128 * 65536, "all console bytes reach the real slow reader");
  assert.ok(maximum > 0 && highWaterMark > 0, "observed actual writable buffering and backpressure");
  assert.ok(maximum <= LIMITS.line + highWaterMark, `stdout queued ${maximum} bytes`);
  t.diagnostic(`Observed stdout queue ${maximum} bytes; high-water mark ${highWaterMark}; forwarded ${received} bytes.`);
  const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json")));
  assert.equal(report.targets[0].status, "pass");
});
test("production stdout error and premature close save incomplete capture and fail the collector", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-output-errors-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\n");
  for (const [fault, diagnostic] of [["error", "console-output-failed"], ["close", "console-output-closed"]]) {
    const script = `
      const { Writable } = require("node:stream");
      const stream = new Writable({ write(chunk, encoding, callback) {
        ${fault === "error" ? 'callback(new Error("SECRET"));' : "queueMicrotask(() => stream.destroy());"}
      } });
      Object.defineProperty(process, "stdout", { value: stream });
      require(${JSON.stringify(path.join(__dirname, "ci-evidence.cjs"))}).main(["go", "report.json", "all", "all"])
        .catch(error => { process.stderr.write(error.message); process.exitCode = 1; });
    `;
    const result = spawnSync(process.execPath, ["-e", script], {
      cwd: dir, env: { ...process.env, ...env }, encoding: "utf8", timeout: 10000,
      input: [event("start"), event("run", "TestA"), { ...event("output", "TestA"), Output: "SECRET" }, event("pass", "TestA", 2), event("pass", null, 3)].map(JSON.stringify).join("\n") + "\n",
    });
    assert.equal(result.status, 1, result.stderr);
    assert.ok(result.stderr.includes(diagnostic));
    const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json")));
    assert.equal(report.complete, false);
    assert.ok(report.diagnostics.includes(diagnostic));
    assert.ok(report.diagnostics.includes("execution-finalization-pending"));
    assert.equal(report.targets[0].status, null, "terminal records after output failure are not admitted");
    assert.ok(!JSON.stringify(report).includes("SECRET"));
    const final = spawnSync(process.execPath, [path.join(__dirname, "ci-evidence.cjs"), "finish-go", "report.json", "0", "1"], { cwd: dir, encoding: "utf8" });
    assert.equal(final.status, 0, final.stderr);
    const finalized = JSON.parse(fs.readFileSync(path.join(dir, "report.json")));
    assert.equal(finalized.collector_exit, 1);
    assert.equal(finalized.complete, false);
    assert.ok(finalized.diagnostics.includes(diagnostic));
  }
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
test("actual Go attributes are discarded without hiding malformed or unknown events", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-attributes-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
  fs.writeFileSync(path.join(dir, "example_test.go"), 'package p\nimport "testing"\nfunc TestA(t *testing.T) { t.Attr("SECRET_KEY", "SECRET_VALUE"); t.Attr("empty", ""); t.Run("child", func(t *testing.T) { t.Attr("child", "SECRET_VALUE") }) }\nfunc TestSkipped(t *testing.T) { t.Attr("skip", "SECRET_VALUE"); t.Skip("SECRET") }\n');
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\nexample/p::TestSkipped\n");
  const produced = spawnSync("go", ["test", "-count=1", "-json", "-v", "."], { cwd: dir, encoding: "utf8", timeout: 30000 });
  assert.equal(produced.status, 0, produced.stderr);
  const attributes = produced.stdout.trim().split("\n").map((line) => JSON.parse(line)).filter((event) => event.Action === "attr");
  assert.equal(attributes.length, 4, "actual producer must exercise attributes on parent, child and skipped tests");
  assert.ok(attributes.some((event) => event.Key === "SECRET_KEY" && event.Value === "SECRET_VALUE"));
  assert.ok(attributes.some((event) => event.Key === "empty" && event.Value === undefined), "Go omits an empty attribute value");
  const collector = path.join(__dirname, "ci-evidence.cjs");
  const capture = (input) => {
    const captured = spawnSync(process.execPath, [collector, "go", "report.json", "all", "all"], { cwd: dir, input, encoding: "utf8" });
    assert.equal(captured.status, 0, captured.stderr);
    const finalized = spawnSync(process.execPath, [collector, "finish-go", "report.json", "0", "0"], { cwd: dir, encoding: "utf8" });
    assert.equal(finalized.status, 0, finalized.stderr);
    const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json"), "utf8"));
    assert.ok(!JSON.stringify(report).includes("SECRET"));
    return report;
  };
  const valid = capture(produced.stdout);
  assert.equal(valid.complete, true, JSON.stringify(valid.diagnostics));
  assert.equal(valid.targets.length, 3);
  assert.equal(valid.counts.top_level.pass, 1);
  assert.equal(valid.counts.top_level.skip, 1);
  assert.equal(valid.counts.subtests.pass, 1);
  for (const [extra, diagnostic] of [
    [JSON.stringify({ ...attributes[0], Action: "attribute" }), "unknown-action"],
    [JSON.stringify({ ...attributes[0], Key: 12 }), "invalid-attribute"],
    [JSON.stringify({ ...attributes[0], Value: null }), "invalid-attribute"],
    [JSON.stringify({ ...attributes[0], Test: undefined }), "invalid-attribute"],
    ["{bad}", "malformed-json"],
  ]) {
    const invalid = capture(produced.stdout + extra + "\n");
    assert.equal(invalid.complete, false);
    assert.ok(invalid.diagnostics.includes(diagnostic), JSON.stringify(invalid.diagnostics));
  }
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
test("actual Go initialization failure diagnostics survive nonexecuting discovery", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-discovery-failure-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
  fs.writeFileSync(path.join(dir, "example_test.go"), 'package p\nimport "testing"\nfunc init() { panic("SECRETDiscoveryFailureSentinel") }\nfunc TestA(t *testing.T) { t.Fatal("must not run") }\n');
  const produced = spawnSync("go", ["test", "-race", "-covermode=atomic", "-json", "-list", ".", "."], {
    cwd: dir, env: { ...process.env, GOPROXY: "off", GOSUMDB: "off" }, encoding: "utf8", timeout: 30000,
  });
  assert.equal(produced.status, 1, produced.stderr);
  const events = produced.stdout.trim().split("\n").map((line) => JSON.parse(line));
  assert.ok(events.some((item) => item.Action === "output" && item.Package === "example/p" && item.Output.includes("SECRETDiscoveryFailureSentinel")));
  assert.ok(!events.some((item) => item.Action === "run"), "Go discovery did not execute a test");
  const captured = spawnSync(process.execPath, [path.join(__dirname, "ci-evidence.cjs"), "discover", "all"], { cwd: dir, input: produced.stdout, encoding: "utf8" });
  assert.equal(captured.status, 1, captured.stderr);
  assert.ok(captured.stdout.includes("SECRETDiscoveryFailureSentinel"), "preserve actual package/output diagnostic records");
  assert.equal(fs.existsSync(path.join(dir, "all")), false);
});
for (const kind of ["initialization", "compiler"]) {
  test(`actual multi-package ${kind} failures drain later Go discovery diagnostics`, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-multiple-discovery-failures-"));
    t.after(() => fs.rmSync(dir, { recursive: true }));
    fs.writeFileSync(path.join(dir, "go.mod"), "module example/p\n\ngo 1.26\n");
    const sentinels = ["SECRETFirstDiscoveryFailure", "SECRETSecondDiscoveryFailure"];
    for (const [index, name] of ["first", "second"].entries()) {
      const pkg = path.join(dir, name);
      fs.mkdirSync(pkg);
      fs.writeFileSync(path.join(pkg, "example_test.go"), `package p
import "testing"
${kind === "initialization" ? `func init() { panic("${sentinels[index]}") }` : `var _ = ${sentinels[index]}`}
func TestA(t *testing.T) { t.Fatal("must not run") }
`);
    }
    const produced = spawnSync("go", ["test", "-p=1", "-race", "-covermode=atomic", "-json", "-list", ".", "./first", "./second"], {
      cwd: dir, env: { ...process.env, GOFLAGS: "", GOPROXY: "off", GOSUMDB: "off" }, encoding: "utf8", timeout: 30000,
    });
    assert.equal(produced.status, 1, produced.stderr);
    const events = produced.stdout.trim().split("\n").map((line) => JSON.parse(line));
    const firstFailure = events.findIndex((item) => ["fail", "build-fail"].includes(item.Action));
    assert.ok(firstFailure >= 0, "actual producer recorded package failure");
    assert.ok(events.slice(firstFailure + 1).some((item) => typeof item.Output === "string" && sentinels.some((sentinel) => item.Output.includes(sentinel))), "actual later diagnostics follow the first package failure");
    assert.ok(!events.some((item) => item.Action === "run"), "discovery did not execute tests");
    const collector = path.join(__dirname, "ci-evidence.cjs");
    const captured = spawnSync(process.execPath, [collector, "discover", "all"], { cwd: dir, input: produced.stdout, encoding: "utf8", timeout: 10000 });
    assert.equal(captured.status, 1, captured.stderr);
    assert.match(captured.stderr, /discovery-failed/);
    for (const sentinel of sentinels) assert.ok(captured.stdout.includes(sentinel), `forward actual ${sentinel}`);
    assert.equal(fs.existsSync(path.join(dir, "all")), false, "failed discovery never publishes an inventory");
    fs.writeFileSync(path.join(dir, "all"), "preexisting inventory\n");
    const repeated = spawnSync(process.execPath, [collector, "discover", "all"], { cwd: dir, input: produced.stdout, encoding: "utf8", timeout: 10000 });
    assert.equal(repeated.status, 1, repeated.stderr);
    assert.equal(fs.readFileSync(path.join(dir, "all"), "utf8"), "preexisting inventory\n", "failure does not overwrite competing inventory");
  });
}
test("failed discovery retains hard stream limits and stdout error/close guards", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-discovery-drain-guards-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const collector = path.join(__dirname, "ci-evidence.cjs");
  const failure = JSON.stringify(event("fail")) + "\n";
  for (const [input, diagnostic] of [
    [JSON.stringify(event("run", "TestA")) + "\n", "discovery-executed-tests"],
    ['{"Action":"output"}', "discovery-truncated"],
    [JSON.stringify({ ...event("output"), Output: "x".repeat(LIMITS.line) }) + "\n", "discovery-input-cap"],
    ["{bad}\n", "CI evidence:"],
  ]) {
    const result = spawnSync(process.execPath, [collector, "discover", "all"], { cwd: dir, input: failure + input, encoding: "utf8", timeout: 10000 });
    assert.equal(result.status, 1, result.stderr);
    assert.ok(result.stderr.includes(diagnostic), result.stderr);
    assert.doesNotMatch(result.stderr, /discovery-failed/, "later hard stream failure remains explicit");
    assert.equal(fs.existsSync(path.join(dir, "all")), false);
  }
  for (const [fault, diagnostic] of [["error", "console-output-failed"], ["close", "console-output-closed"]]) {
    const script = `
      const { Writable } = require("node:stream");
      const stream = new Writable({ write(chunk, encoding, callback) {
        ${fault === "error" ? 'callback(new Error("SECRET"));' : "queueMicrotask(() => stream.destroy());"}
      } });
      Object.defineProperty(process, "stdout", { value: stream });
      require(${JSON.stringify(collector)}).main(["discover", "all"])
        .catch(error => { process.stderr.write(error.message); process.exitCode = 1; });
    `;
    const result = spawnSync(process.execPath, ["-e", script], {
      cwd: dir, input: failure + JSON.stringify({ ...event("output"), Output: "later diagnostic\n" }) + "\n",
      encoding: "utf8", timeout: 10000,
    });
    assert.equal(result.status, 1, result.stderr);
    assert.ok(result.stderr.includes(diagnostic), result.stderr);
    assert.equal(fs.existsSync(path.join(dir, "all")), false);
  }
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
test("production Actions totals withhold job integrity failures but preserve unrelated diagnostics and recorded zero", () => {
  const collect = (lists, { artifacts = [], incomplete = false } = {}) => collectActions("10", (endpoint) => {
    if (endpoint.includes("/jobs")) {
      const attempt = Number(/\/attempts\/([0-9]+)\//.exec(endpoint)[1]);
      const page = Number(/[?&]page=([0-9]+)/.exec(endpoint)[1]);
      return { total_count: lists[attempt - 1].length + (incomplete ? 1 : 0),
        jobs: !incomplete || page === 1 ? lists[attempt - 1] : [] };
    }
    if (endpoint.includes("/artifacts")) return { total_count: artifacts.length, artifacts };
    if (endpoint.includes("/attempts/")) return { run_started_at: "2026-11-07T10:01:00Z" };
    return { ...run, run_attempt: lists.length };
  });
  const good = job(1, "01", "02");
  const known = collect([[good]]);
  assert.equal(known.complete, true);
  assert.equal(known.runner_elapsed_minutes_by_os["ubuntu-latest"], 1);
  for (const [lists, diagnostic] of [
    [[[{ ...good, head_sha: "b".repeat(40) }]], "job-source-unverified"],
    [[[{ ...good, run_id: 11 }]], "job-source-unverified"],
    [[[good, good]], "duplicate-job-in-attempt"],
    [[[good, { ...good, id: 1.5 }]], "job-id-unavailable"],
    [[[good, { ...good, id: undefined }]], "job-id-unavailable"],
    [[[good, null]], "job-id-unavailable"],
    [[[good], [job(1, "03", "05", "success", 2)]], "retained-job-conflict"],
    [[[job(1, "01", null)]], "job-metadata-incomplete"],
  ]) {
    const report = collect(lists);
    assert.ok(report.jobs.length > 0, "control must retain an observed job");
    assert.ok(report.diagnostics.includes(diagnostic), diagnostic);
    assert.equal(report.runner_elapsed_minutes_by_os["ubuntu-latest"], null, diagnostic);
    assert.equal(report.attempts.at(-1).complete, false, diagnostic);
    assert.equal(report.attempts.at(-1).observed_job_span_seconds, null, diagnostic);
    assert.equal(report.attempts.at(-1).retained_job_span_seconds, null, diagnostic);
  }
  const pagination = collect([[good]], { incomplete: true });
  assert.ok(pagination.diagnostics.includes("jobs-pagination-incomplete"));
  assert.equal(pagination.jobs.length, 1, "a complete known duration cannot hide the missing inventory");
  assert.equal(pagination.runner_elapsed_minutes_by_os["ubuntu-latest"], null);
  const artifact = { id: 100, name: "unrelated", size_in_bytes: 0,
    workflow_run: { id: 11, head_sha: run.head_sha } };
  const unknownStep = structuredClone(good);
  unknownStep.steps[0].completed_at = null;
  for (const [lists, artifacts, expected, diagnostics] of [
    [[[good]], [artifact], 1, ["artifact-source-unverified"]],
    [[[unknownStep]], [], 1, ["step-duration-unavailable"]],
    [[[job(1, "01", "01")]], [], 0, []],
    [[[job(1, "01", "01")]], [artifact], 0, ["artifact-source-unverified"]],
    [[[job(1, "01", "02", "failure")]], [], 1, []],
  ]) {
    const report = collect(lists, { artifacts });
    for (const code of diagnostics) assert.ok(report.diagnostics.includes(code), code);
    assert.equal(report.runner_elapsed_minutes_by_os["ubuntu-latest"], expected);
    assert.equal(report.attempts[0].complete, true);
    assert.equal(report.attempts[0].observed_job_span_seconds, expected * 60);
    assert.equal(report.attempts[0].retained_job_span_seconds, expected * 60);
  }
});
test("production Actions artifact totals distinguish observed empty inventory from unavailable evidence", () => {
  const good = job(1, "01", "02");
  const artifact = { id: 100, name: "artifact", size_in_bytes: 0,
    workflow_run: { id: run.id, head_sha: run.head_sha } };
  const collect = (mode) => {
    let artifactCalls = 0;
    const report = collectActions("10", (endpoint) => {
      if (endpoint.includes("/artifacts")) {
        artifactCalls++;
        if (mode === "api-failed") throw new Error("private-api-failure");
        if (mode === "api-invalid") return { total_count: 0, artifacts: null };
        if (mode === "incomplete") return { total_count: 1, artifacts: [] };
        if (mode === "zero-byte") return { total_count: 1, artifacts: [artifact] };
        if (mode === "populated") return { total_count: 1, artifacts: [{ ...artifact, size_in_bytes: 42 }] };
        if (mode === "unbound") return { total_count: 1, artifacts: [{ ...artifact, workflow_run: { id: 11, head_sha: run.head_sha } }] };
        return { total_count: 0, artifacts: [] };
      }
      if (endpoint.includes("/jobs")) {
        if (mode === "jobs-failed") throw new Error("private-job-failure");
        return { total_count: 1, jobs: [good] };
      }
      if (endpoint.includes("/attempts/")) return { run_started_at: good.started_at };
      if (mode === "run-failed") throw new Error("private-run-failure");
      return { ...run, run_attempt: 1, ...(mode === "run-unbound" ? { id: 11 } : {}) };
    });
    return { report, artifactCalls };
  };
  for (const [mode, expected, complete, diagnostic] of [
    ["empty", 0, true, null],
    ["zero-byte", 0, true, null],
    ["populated", 42, true, null],
    ["jobs-failed", 0, false, "jobs-api-failed"],
    ["api-failed", null, false, "artifacts-api-failed"],
    ["api-invalid", null, false, "artifacts-api-invalid"],
    ["incomplete", null, false, "artifacts-pagination-incomplete"],
    ["unbound", null, false, "artifact-source-unverified"],
    ["run-failed", null, false, "run-api-failed"],
    ["run-unbound", null, false, "run-source-invalid"],
  ]) {
    const { report, artifactCalls } = collect(mode);
    assert.equal(report.artifact_bytes, expected, mode);
    assert.equal(report.complete, complete, mode);
    if (diagnostic) assert.ok(report.diagnostics.includes(diagnostic), mode);
    else assert.deepEqual(report.diagnostics, [], mode);
    assert.equal(artifactCalls > 0, !mode.startsWith("run-"), mode);
    assert.ok(!JSON.stringify(report).includes("private-"), mode);
  }
});
test("production Actions artifact inventories require unique positive identities and stable pagination totals", async (t) => {
  const artifacts = Array.from({ length: 102 }, (_, index) => ({
    id: index + 1, name: "artifact", size_in_bytes: index + 1,
    workflow_run: { id: run.id, head_sha: run.head_sha },
  }));
  const apiFor = (pages, totals) => (endpoint) => {
    if (endpoint.includes("/artifacts")) {
      const page = Number(/[?&]page=([0-9]+)/.exec(endpoint)[1]) - 1;
      return { total_count: totals[Math.min(page, totals.length - 1)], artifacts: pages[page] || [] };
    }
    if (endpoint.includes("/jobs")) return { total_count: 1, jobs: [job(1, "01", "02")] };
    if (endpoint.includes("/attempts/")) return { run_started_at: "2026-11-07T10:01:00Z" };
    return { ...run, run_attempt: 1 };
  };
  const first = artifacts.slice(0, 100);
  const overlap = apiFor([first, [artifacts[99]]], [101, 101]);
  const cases = [
    ["distinct", [first, [artifacts[100]]], [101, 101], 5151, null],
    ["overlap", [first, [artifacts[99]]], [101, 101], null, "duplicate-artifact-id"],
    ["same-page duplicate", [[artifacts[0], artifacts[0]]], [2], null, "duplicate-artifact-id"],
    ["growing total", [first, artifacts.slice(100)], [101, 102], null, "artifacts-total-count-changed"],
    ["shrinking total", [first, []], [101, 100], null, "artifacts-total-count-changed"],
    ["negative total", [[]], [-1], null, "artifacts-api-invalid"],
    ["oversized page", [artifacts.slice(0, 101)], [101], null, "artifacts-api-invalid"],
    ["null row", [first, [null]], [101, 101], null, "artifact-id-unavailable"],
    ["zero bytes", [[{ ...artifacts[0], size_in_bytes: 0 }]], [1], 0, null],
    ["largest exact byte count", [[{ ...artifacts[0], size_in_bytes: Number.MAX_SAFE_INTEGER }]], [1], Number.MAX_SAFE_INTEGER, null],
    ["unsafe aggregate", [[{ ...artifacts[0], size_in_bytes: Number.MAX_SAFE_INTEGER }, { ...artifacts[1], size_in_bytes: 1 }]], [2], null, "artifact-bytes-overflow"],
  ];
  for (const id of [undefined, null, 0, -1, 1.5, "101", Number.MAX_SAFE_INTEGER + 1]) {
    cases.push([`invalid ID ${id}`, [first, [{ ...artifacts[100], id }]], [101, 101], null, "artifact-id-unavailable"]);
  }
  for (const size of [undefined, null, -1, 0.5, "1", Number.MAX_SAFE_INTEGER + 1]) {
    cases.push([`invalid size ${size}`, [[{ ...artifacts[0], size_in_bytes: size }]], [1], null, "artifact-size-unavailable"]);
  }
  for (const [label, pages, totals, bytes, diagnostic] of cases) {
    const report = collectActions("10", apiFor(pages, totals));
    assert.equal(report.artifact_bytes, bytes, label);
    assert.equal(report.complete, diagnostic === null, label);
    assert.equal(report.attempts[0].complete, true, "independent job evidence remains valid");
    if (diagnostic) assert.ok(report.diagnostics.includes(diagnostic), label);
    else {
      assert.deepEqual(report.diagnostics, []);
      assert.equal(report.artifacts.length, pages.flat().length);
      assert.equal(new Set(report.artifacts.map((artifact) => artifact.id)).size, pages.flat().length);
    }
  }
  const unstableJobs = collectActions("10", (endpoint) => {
    if (!endpoint.includes("/jobs")) return apiFor([[]], [0])(endpoint);
    const firstPage = endpoint.endsWith("page=1");
    return { total_count: firstPage ? 101 : 100,
      jobs: firstPage ? Array.from({ length: 100 }, (_, index) => job(index + 1, "01", "02")) : [] };
  });
  assert.ok(unstableJobs.diagnostics.includes("jobs-total-count-changed"));
  assert.equal(unstableJobs.attempts[0].complete, false);
  assert.equal(unstableJobs.attempts[0].observed_job_span_seconds, null);
  assert.equal(unstableJobs.artifact_bytes, 0, "job pagination does not erase the observed empty artifact inventory");
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "waza-artifact-pages-"));
  t.after(() => fs.rmSync(directory, { recursive: true }));
  const output = path.join(directory, "actions.json");
  const exitCode = process.exitCode;
  try {
    await main(["actions", "10", output], overlap);
    assert.equal(process.exitCode, 1);
  } finally { process.exitCode = exitCode; }
  const saved = JSON.parse(fs.readFileSync(output, "utf8"));
  assert.equal(saved.complete, false);
  assert.equal(saved.artifact_bytes, null);
  assert.equal(saved.artifacts.length, 101, "retain observed rows rather than silently deduplicating");
});
test("production artifact pagination withholds baseline eligibility without dropping readable test observations", () => {
  const report = parseGo(successful, { selected: ["example/p::TestA"], inventory_scope: "qualified" });
  report.identity.source_sha = run.head_sha;
  const fixture = artifactFixture(report, {}, "go-run");
  const bound = fixture.api("repos/microsoft/waza/actions/runs/10/artifacts").artifacts[0];
  const artifacts = [bound, ...Array.from({ length: 101 }, (_, index) => ({
    ...bound, id: 1000 + index, name: "unrelated",
  }))];
  for (const mode of ["distinct", "overlap", "growing", "shrinking", "invalid", "fractional", "overflow"]) {
    let artifactCalls = 0, downloads = 0;
    const observed = collectObservations("10", (endpoint) => {
      if (!endpoint.includes("/artifacts")) return fixture.api(endpoint);
      const page = Number(/[?&]page=([0-9]+)/.exec(endpoint)[1]);
      artifactCalls++;
      if (page === 1) return { total_count: 101, artifacts: artifacts.slice(0, 100) };
      return {
        total_count: mode === "growing" ? 102 : mode === "shrinking" ? 100 : 101,
        artifacts: mode === "overlap" ? [artifacts[99]] : mode === "growing" ? artifacts.slice(100) :
          mode === "shrinking" ? [] : mode === "invalid" ? [{ ...artifacts[100], id: null }] :
            mode === "fractional" || mode === "overflow" ? [{ ...artifacts[100], size_in_bytes: mode === "fractional" ? 0.5 : Number.MAX_SAFE_INTEGER }] : [artifacts[100]],
      };
    }, (id) => {
      downloads++;
      assert.equal(id, fixture.artifactID);
      return fixture.zip;
    });
    assert.equal(artifactCalls, 2, mode);
    assert.equal(downloads, 1, mode);
    assert.equal(observed.reports.length, 1, mode);
    const grouped = trends(observed.reports);
    assert.equal(grouped.cohorts.length, 1, mode);
    assert.equal(grouped.cohorts[0].observations.length, 1, mode);
    assert.equal(grouped.cohorts[0].eligible.length, mode === "distinct" ? 1 : 0, mode);
    assert.equal(historyProposal(observed.reports).targets.length, mode === "distinct" ? 1 : 0, mode);
    assert.equal(observed.metadata.complete, mode === "distinct", mode);
    if (mode !== "distinct") assert.equal(observed.metadata.artifact_bytes, null, mode);
  }
});
function archive(report, name = "coverage-native-0.txt.evidence.json", options = {}) {
  const content = Buffer.isBuffer(report) ? report : Buffer.from(JSON.stringify(report));
  const data = options.deflate ? deflateRawSync(content) : content, filename = Buffer.from(name);
  const expanded = options.expanded ?? content.length, method = options.deflate ? 8 : 0;
  const local = Buffer.alloc(30), central = Buffer.alloc(46), end = Buffer.alloc(22);
  local.writeUInt32LE(0x04034b50, 0); local.writeUInt16LE(method, 8); local.writeUInt32LE(data.length, 18); local.writeUInt32LE(expanded, 22); local.writeUInt16LE(filename.length, 26);
  central.writeUInt32LE(0x02014b50, 0); central.writeUInt16LE(method, 10); central.writeUInt32LE(data.length, 20); central.writeUInt32LE(expanded, 24); central.writeUInt16LE(filename.length, 28);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(1, 8); end.writeUInt16LE(1, 10);
  end.writeUInt32LE(central.length + filename.length, 12); end.writeUInt32LE(local.length + filename.length + data.length, 16);
  return Buffer.concat([local, filename, data, central, filename, end]);
}
function artifactFixture(report, overrides = {}, family = report.kind === "playwright" ? "browser" : "go-native") {
  const filename = family === "browser" ? "ci-evidence-browser.json" : family === "go-run" ? "coverage.txt.evidence.json" : "coverage-native-0.txt.evidence.json";
  const zip = archive(report, filename), id = Number(report.identity.run_id);
  const name = `ci-evidence-${family}-${id}-${report.identity.attempt}` + (family === "go-native" ? "-0" : family === "go-run" ? `-${report.identity.job}` : "");
  const fixtureRun = { ...run, id, run_attempt: 1, run_started_at: "2026-11-07T10:01:00Z", ...overrides };
  const api = (endpoint) => {
    if (endpoint.includes("/jobs")) return { total_count: 1, jobs: [{ ...job(1, "01", "02"), run_id: id }] };
    if (endpoint.includes("/artifacts")) return { total_count: 1, artifacts: [{ id: id * 10, name, size_in_bytes: zip.length, expired: false, digest: `sha256:${createHash("sha256").update(zip).digest("hex")}`, workflow_run: { id, head_sha: fixtureRun.head_sha } }] };
    if (endpoint.includes("/attempts/")) return { run_started_at: fixtureRun.run_started_at };
    return fixtureRun;
  };
  return { id: String(id), artifactID: id * 10, api, zip };
}
function verified(report, overrides = {}, family = report.kind === "playwright" ? "browser" : "go-native") {
  const fixture = artifactFixture(report, overrides, family);
  return collectObservations(fixture.id, fixture.api, () => fixture.zip);
}
test("browser retry inventories require consecutive integer attempts beginning at zero", () => {
  for (const retries of [[1], [-1], [0.5], [0, 2], [0, 0], [1, 0]]) {
    const data = browser();
    data.suites[0].specs[0].tests[0].results = retries.map((retry) => ({ retry, status: "passed", duration: 10 }));
    const report = browserReport(data, env, browserInventory(data));
    assert.equal(report.complete, false, JSON.stringify(retries));
    assert.ok(report.diagnostics.includes("browser-retry-invalid"), JSON.stringify(retries));
  }
  const data = browser();
  data.config.projects[0].retries = 1;
  const target = data.suites[0].specs[0].tests[0];
  target.status = "flaky";
  target.results = [{ retry: 0, status: "failed", duration: 0 }, { retry: 1, status: "passed", duration: 10 }];
  const report = browserReport(data, env, browserInventory(data));
  assert.equal(report.complete, true, JSON.stringify(report.diagnostics));
  assert.equal(report.counts.first_attempt_failures, 1);
  assert.equal(report.policy.retries, null, "configured retries cannot become a retry-disabled sample");
});
test("verified complete Playwright reports require measured rows, inventory and count integrity", () => {
  const produce = () => {
    const data = browser();
    const report = browserReport(data, { ...env, BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "success" }, browserInventory(data));
    report.identity.source_sha = run.head_sha;
    return report;
  };
  const inspect = (report, admitted, eligible) => {
    const snapshot = structuredClone(report);
    const observed = verified(report);
    assert.equal(observed.reports.length, admitted ? 1 : 0);
    assert.deepEqual(report, snapshot, "admission must not repair or reinterpret recorded evidence");
    const grouped = trends(observed.reports);
    assert.equal(grouped.cohorts.reduce((sum, cohort) => sum + cohort.eligible.length, 0), eligible ? 1 : 0);
    assert.deepEqual(historyProposal(observed.reports).targets, [], "browser timings never enter Go placement history");
    if (!admitted) assert.ok(observed.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
  };
  const good = produce();
  assert.equal(good.complete, true);
  assert.equal(good.counts.executed, 1);
  inspect(good, true, true);
  const mutations = [
    (report) => { report.targets = null; },
    (report) => { report.targets = []; },
    (report) => { report.targets[0] = null; },
    (report) => { report.targets.push(structuredClone(report.targets[0])); },
    (report) => { report.targets[0].project = "missing"; },
    (report) => { report.targets[0].status = "unknown"; },
    (report) => { report.targets[0].status = "expected"; report.targets[0].results[0].status = "failed"; },
    (report) => { report.targets[0].results = []; },
    (report) => { report.targets[0].results = null; },
    (report) => { report.targets[0].results[0] = null; },
    (report) => { delete report.targets[0].results[0].seconds; },
    (report) => { report.targets[0].results[0].seconds = -1; },
    (report) => { report.targets[0].results[0].seconds = "0"; },
    (report) => { report.targets[0].results[0].retry = 1; },
    (report) => { report.targets[0].results[0].retry = 0.5; },
    (report) => { report.targets[0].results[0].status = "interrupted"; },
    (report) => { report.targets[0].results.push({ retry: 1, status: "passed", seconds: 0 }); },
    (report) => { report.selected = []; },
    (report) => { report.selected.push(report.selected[0]); },
    (report) => { report.discovered = null; },
    (report) => { report.discovered = []; },
    (report) => { report.discovered.push(report.discovered[0]); },
    (report) => { report.discovered = ["chromium::other"]; },
    (report) => { report.counts = null; },
    (report) => { report.result_counts = null; },
    (report) => { report.runner_seconds = null; },
    (report) => { report.policy.projects = []; },
    (report) => { report.policy.projects[0].retries = 1; },
    (report) => { report.policy.workers = 0; },
  ];
  for (const field of Object.keys(good.counts)) mutations.push((report) => { report.counts[field]++; });
  for (const field of Object.keys(good.result_counts)) mutations.push((report) => { report.result_counts[field]++; });
  for (const mutate of mutations) {
    const report = structuredClone(good);
    mutate(report);
    assert.notDeepEqual(report, good, "mutation must change actual sanitized producer evidence");
    inspect(report, false, false);
  }
  const partial = { ...good, complete: false, diagnostics: ["execution-unavailable"], targets: null };
  inspect(partial, true, false);
  const zero = produce();
  zero.targets[0].results[0].seconds = 0;
  zero.runner_seconds = 0;
  inspect(zero, true, true);
  for (const [status, expected, actual, counts, resultCounts] of [
    ["unexpected", "passed", "failed", { pass: 0, fail: 1, first_attempt_failures: 1 }, { pass: 0, fail: 1 }],
    ["skipped", "skipped", "skipped", { executed: 0, pass: 0, skip: 1 }, { pass: 0, skip: 1 }],
    ["expected", "failed", "failed", { pass: 0, first_attempt_failures: 1 }, { pass: 0, fail: 1 }],
  ]) {
    const report = produce();
    Object.assign(report.targets[0], { status, expected_status: expected });
    report.targets[0].results[0].status = actual;
    Object.assign(report.counts, counts);
    Object.assign(report.result_counts, resultCounts);
    report.execution_outcome = status === "unexpected" ? "failure" : "success";
    inspect(report, true, false);
  }
});
test("verified complete Go reports require selected target and count integrity without repairing incomplete evidence", () => {
  const produce = (events = successful, selection = {
    selected: ["example/p::TestA"], inventory_scope: "qualified",
  }) => {
    const report = parseGo(events, selection);
    report.identity.source_sha = run.head_sha;
    return report;
  };
  const inspect = (report, admitted, eligible) => {
    const snapshot = structuredClone(report);
    const observed = verified(report, {}, "go-run");
    assert.equal(observed.reports.length, admitted ? 1 : 0);
    assert.deepEqual(report, snapshot, "admission must not repair counts or mutate producer evidence");
    const grouped = trends(observed.reports), proposal = historyProposal(observed.reports);
    assert.equal(grouped.cohorts.reduce((sum, cohort) => sum + cohort.eligible.length, 0), eligible ? 1 : 0);
    if (!eligible) assert.deepEqual(proposal.targets, []);
    if (!admitted) assert.ok(observed.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
    return proposal;
  };
  const good = produce();
  assert.equal(good.complete, true);
  assert.equal(good.counts.subtests.skip, 1);
  assert.equal(inspect(good, true, true).targets[0].target, "example/p::TestA");
  const mutations = [
    (report) => { report.targets = []; },
    (report) => { report.targets[0].name = "TestB"; },
    (report) => { report.targets.push(structuredClone(report.targets[0])); },
    (report) => { report.targets[0].executed = null; },
    (report) => { report.targets[0].top_level = false; },
    (report) => { report.selected = []; },
    (report) => { report.selected.push(report.selected[0]); },
    (report) => { report.discovered = null; },
    (report) => { report.discovered = []; },
    (report) => { report.discovered.push(report.discovered[0]); },
    (report) => { delete report.discovered; },
    (report) => { report.discovered = ["example/p::TestOther"]; },
    (report) => { report.packages = []; },
    (report) => {
      report.packages = [];
      report.counts.packages = { observed: 0, executed: 0, pass: 0, fail: 0, skip: 0, unfinished: 0 };
    },
    (report) => {
      for (const target of report.targets) target.package = "example/orphan";
      report.selected = ["example/orphan::TestA"];
    },
    (report) => { report.packages[0].status = "fail"; },
    (report) => { report.counts = null; },
  ];
  for (const scope of ["top_level", "subtests", "packages"]) {
    for (const field of ["observed", "executed", "pass", "fail", "skip", "unfinished"]) {
      mutations.push((report) => { report.counts[scope][field]++; });
    }
  }
  for (const mutate of mutations) {
    const report = structuredClone(good);
    mutate(report);
    assert.notDeepEqual(report, good, "mutation must change actual producer output");
    inspect(report, false, false);
  }
  const subset = structuredClone(good);
  subset.discovered.push("example/p::TestOther");
  inspect(subset, true, true);
  const mismatch = produce([
    event("start"), event("run", "TestB"), event("pass", "TestB", 2), event("pass", null, 3),
  ]);
  assert.ok(mismatch.diagnostics.includes("selection-execution-mismatch"));
  inspect(mismatch, true, false);
  const missingDuration = produce([
    event("start"), event("run", "TestA"), event("pass", "TestA"), event("pass", null, 3),
  ]);
  assert.ok(missingDuration.diagnostics.includes("duration-unavailable"));
  inspect(missingDuration, true, false);
  const missingPackage = produce([event("run", "TestA"), event("pass", "TestA", 1)]);
  assert.ok(missingPackage.diagnostics.includes("execution-unavailable"));
  assert.deepEqual(missingPackage.packages, []);
  assert.deepEqual(missingPackage.counts.packages, { observed: 0, executed: 0, pass: 0, fail: 0, skip: 0, unfinished: 0 });
  inspect(missingPackage, true, false);
  inspect({ ...missingPackage, complete: true, diagnostics: [] }, false, false);
  inspect(produce([event("start"), event("pass", null, 0)], { selected: [] }), true, false);
  inspect(produce([
    event("start"), event("run", "TestA"), event("fail", "TestA", 1), event("fail", null, 1),
  ]), true, false);
  const zero = produce([
    event("start"), event("run", "TestA"), event("pass", "TestA", 0), event("pass", null, 0),
  ]);
  assert.equal(inspect(zero, true, true).targets[0].observations[0].seconds, 0);
  const named = produce([
    event("start"), event("run", "TestA"), event("pass", "TestA", 1), event("pass", null, 1),
    { ...event("start"), Package: "example/other" },
    { ...event("run", "TestA"), Package: "example/other" },
    { ...event("pass", "TestA", 1), Package: "example/other" },
    { ...event("pass", null, 1), Package: "example/other" },
  ], { selected: ["TestA"], inventory_scope: "names" });
  assert.equal(named.complete, true);
  assert.equal(named.counts.top_level.observed, 2);
  assert.equal(inspect(named, true, true).targets.length, 2, "one name may select distinct packages");
});
test("production report processing limits charge valid and invalid bodies at 99, 100 and 101 across shared calls", () => {
  const report = parseGo(successful);
  report.identity.source_sha = run.head_sha;
  report.selected = ["example/p::TestA"];
  report.discovered = [...report.selected];
  const fixture = artifactFixture(report, {}, "go-run");
  const invalid = archive({ ...report, schema: 2 }, "coverage.txt.evidence.json");
  const probe = (count, pattern, budget = { compressed: 0, expanded: 0, reports: 0 }) => {
    const attempted = Math.min(count, LIMITS.reports - budget.reports);
    const zips = Array.from({ length: count }, (_, index) =>
      pattern === "invalid" || (pattern === "mixed" && index % 2 === 0) ||
        (pattern === "invalid-prefix" && index < count - 1) ? invalid : fixture.zip);
    const artifacts = zips.map((zip, index) => ({
      id: 1000 + index, name: "ci-evidence-go-run-10-1-native", size_in_bytes: zip.length,
      expired: false, digest: `sha256:${createHash("sha256").update(zip).digest("hex")}`,
      workflow_run: { id: 10, head_sha: run.head_sha },
    }));
    const api = (endpoint) => {
      if (!endpoint.includes("/artifacts")) return fixture.api(endpoint);
      const page = Number(/[?&]page=([0-9]+)/.exec(endpoint)[1]);
      return { total_count: artifacts.length, artifacts: artifacts.slice((page - 1) * 100, page * 100) };
    };
    const downloads = [];
    const observed = collectObservations("10", api, (id) => {
      downloads.push(id); return zips[id - 1000];
    }, budget);
    assert.equal(downloads.length, attempted);
    assert.equal(observed.reports.length, zips.slice(0, attempted).filter((zip) => zip === fixture.zip).length);
    assert.equal(observed.diagnostics.includes("evidence-artifact-cap"), count > attempted);
    return budget;
  };
  for (const count of [99, 100, 101]) {
    for (const pattern of ["valid", "invalid", "mixed", "invalid-prefix"]) {
      assert.equal(probe(count, pattern).reports, Math.min(count, LIMITS.reports));
    }
  }
  const shared = probe(99, "invalid");
  probe(1, "valid", shared);
  probe(1, "valid", shared);
  assert.equal(shared.reports, 100, "invalid charges remain reserved across source collections");
});
function isolatedObservation(report) {
  return {
    schema: 1, mode: "isolated-empty", platform: "linux", phase: "before-dependencies",
    restore: false, save: false,
    ...Object.fromEntries(["repository", "source_sha", "workflow_sha", "run_id", "attempt", "job"]
      .map((field) => [field, report.identity[field]])),
    image_os: report.identity.image_os, image_version: report.identity.image_version,
    stores: {
      go_modules: { initial_entries: 0, actual_path_match: true },
      go_build: { initial_entries: 0, actual_path_match: true },
      npm: { initial_entries: 0, actual_path_match: true },
    },
  };
}
test("isolated observation admission binds typed cold stores without reinterpreting legacy cache misses", () => {
  const report = parseGo(successful);
  report.identity.source_sha = run.head_sha;
  report.identity.image_os = "ubuntu24";
  report.identity.image_version = "20261107.1";
  report.selected = ["example/p::TestA"];
  report.discovered = report.selected;
  const legacy = verified(report, {}, "go-run");
  assert.equal(legacy.diagnostics.length, 0);
  assert.equal(trends(legacy.reports).cohorts[0].eligible.length, 1);
  assert.equal(Object.hasOwn(trends(legacy.reports).cohorts[0].cohort, "cache_observation"), false);
  report.cache_observation = isolatedObservation(report);
  const observed = verified(report, {}, "go-run");
  assert.equal(observed.diagnostics.length, 0);
  assert.equal(trends(observed.reports).cohorts[0].eligible.length, 1);
  const proposal = historyProposal(observed.reports);
  assert.equal(proposal.targets.length, 1);
  assert.equal(proposal.targets[0].observations.length, 1);
  const descriptor = proposal.cohorts[proposal.targets[0].observations[0].cohort_id];
  assert.equal(descriptor.cache_observation.mode, "isolated-empty");
  assert.equal(Object.hasOwn(descriptor.cache_observation, "run_id"), false);
  assert.equal(Object.hasOwn(descriptor.cache_observation, "source_sha"), false);
  assert.equal(Object.hasOwn(descriptor.cache_observation, "paths"), false);
});
test("invalid isolated evidence cannot hide missing stores, warm caches or source and scope mismatches", () => {
  for (const mutate of [
    (report) => { report.cache_observation.schema = 2; },
    (report) => { report.cache_observation.source_sha = "b".repeat(40); },
    (report) => { report.cache_observation.attempt = "2"; },
    (report) => { delete report.cache_observation.image_os; },
    (report) => { report.cache_observation.image_version = "20261108.1"; },
    (report) => { report.cache_observation.image_os = report.identity.image_os = "/PRIVATE-image"; },
    (report) => { report.cache_observation.restore = true; },
    (report) => { report.cache_observation.save = null; },
    (report) => { report.cache_observation.stores.go_build.initial_entries = 1; },
    (report) => { report.cache_observation.stores.go_modules.actual_path_match = "true"; },
    (report) => { delete report.cache_observation.stores.go_build; },
    (report) => { report.cache.go_dependencies = null; },
    (report) => { report.cache.node_dependencies = true; },
    (report) => { report.cache_observation.stores.npm = "not-applicable"; },
    (report) => { report.cache_observation.stores.npm.path = "SECRET"; },
    (report) => { report.identity.os = "Windows"; },
  ]) {
    const report = parseGo(successful);
    report.identity.source_sha = run.head_sha;
    report.identity.image_os = "ubuntu24";
    report.identity.image_version = "20261107.1";
    report.selected = ["example/p::TestA"];
    report.discovered = report.selected;
    report.cache_observation = isolatedObservation(report);
    mutate(report);
    const observed = verified(report, {}, "go-run");
    assert.equal(observed.reports.length, 0);
    assert.ok(observed.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
    assert.equal(historyProposal(observed.reports).targets.length, 0);
  }
});
test("unavailable isolated capture retains partial observations without inventing false cache values", () => {
  const report = parseGo(successful);
  report.identity.source_sha = run.head_sha;
  report.complete = false;
  report.diagnostics = ["cold-cache-observation-unavailable"];
  report.cache = { test_results: "disabled", go_dependencies: null, node_dependencies: null };
  report.cache_observation = null;
  report.selected = ["example/p::TestA"];
  report.discovered = report.selected;
  const observed = verified(report, {}, "go-run");
  assert.equal(observed.reports.length, 1);
  assert.equal(observed.reports[0].cache.go_dependencies, null);
  assert.equal(observed.reports[0].cache_observation, null);
  assert.equal(trends(observed.reports).cohorts[0].eligible.length, 0);
  assert.equal(historyProposal(observed.reports).targets.length, 0);
});
test("actual runner-image changes split otherwise equal isolated cohorts without including invocation paths", () => {
  const reports = ["20261107.1", "20261108.1"].map((image, index) => {
    const report = parseGo(successful);
    report.identity.source_sha = run.head_sha;
    report.identity.image_os = "ubuntu24";
    report.identity.image_version = image;
    report.identity.run_id = String(10 + index);
    report.selected = ["example/p::TestA"];
    report.discovered = report.selected;
    report.cache_observation = isolatedObservation(report);
    return verified(report, {}, "go-run").reports[0];
  });
  test("native cold receipts require explicit npm non-applicability without claiming a browser dependency miss", () => {
    const report = parseGo(successful);
    report.identity.source_sha = run.head_sha;
    report.identity.image_os = "ubuntu24";
    report.identity.image_version = "20261107.1";
    report.inventory_scope = "names";
    report.selected = ["TestA"];
    report.discovered = report.selected;
    report.cache.node_dependencies = "not-applicable";
    report.cache_observation = isolatedObservation(report);
    report.cache_observation.stores.npm = "not-applicable";
    const native = verified(report);
    assert.equal(native.reports.length, 1);
    assert.equal(trends(native.reports).cohorts[0].eligible.length, 1);
    assert.equal(verified(report, {}, "go-run").reports.length, 0);
    report.cache.node_dependencies = false;
    report.cache_observation.stores.npm = { initial_entries: 0, actual_path_match: true };
    assert.equal(verified(report).reports.length, 0);
    assert.equal(verified(report, {}, "go-run").reports.length, 1);
  });
  assert.equal(trends(reports).cohorts.length, 2);
  const proposal = historyProposal(reports);
  assert.equal(Object.keys(proposal.cohorts).length, 2);
  assert.equal(proposal.targets.length, 1);
  assert.equal(proposal.targets[0].observations.length, 2);
});
test("isolated cache preparation requires explicit supported mode rather than converting disabled caches to misses", () => {
  assert.throws(() => isolatedCache.prepare("go", undefined, {}), /cache-observation-mode-unavailable/);
  const report = goReport({ ...env, GO_CACHE_HIT: "", NODE_CACHE_HIT: "" });
  assert.equal(report.cache.go_dependencies, null);
  assert.equal(report.cache.node_dependencies, null);
  assert.equal(Object.hasOwn(report, "cache_observation"), false);
  const invalidImage = goReport({ ...env, ImageOS: "/PRIVATE-image", ImageVersion: "20261107.1" });
  assert.equal(invalidImage.identity.image_os, null);
  assert.ok(invalidImage.diagnostics.includes("runner-image-unavailable"));
  assert.ok(!JSON.stringify(invalidImage).includes("PRIVATE-image"));
  assert.equal(Object.hasOwn(report.identity, "image_os"), false);
});
test("production cold-capture CLI fails with retained null diagnostics instead of manufacturing an isolated miss", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cold-cli-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const output = path.join(dir, "report.json"), all = path.join(dir, "all");
  fs.writeFileSync(all, "example/p::TestA\n");
  const invocation = spawnSync(process.execPath,
    [path.join(__dirname, "ci-evidence.cjs"), "go", output, all, all], {
      cwd: dir, encoding: "utf8", input: successful.map((event) => JSON.stringify(event)).join("\n") + "\n",
      env: { ...process.env, ...env, CI_EVIDENCE_COLD_REQUESTED: "true",
        CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED: "true",
        RUNNER_TEMP: dir, CI_EVIDENCE_COLD_STATE: path.join(dir, "PRIVATE-unavailable-state") },
    });
  assert.equal(invocation.error, undefined);
  assert.equal(invocation.status, 1);
  const captured = JSON.parse(fs.readFileSync(output, "utf8"));
  assert.equal(captured.complete, false);
  assert.equal(captured.cache.go_dependencies, null);
  assert.equal(captured.cache.node_dependencies, null);
  assert.equal(captured.cache_observation, null);
  assert.ok(captured.diagnostics.includes("cold-cache-observation-unavailable"));
  assert.equal(historyProposal([captured]).targets.length, 0);
  assert.ok(!JSON.stringify(captured).includes("PRIVATE-unavailable-state"));
  assert.ok(!JSON.stringify(captured).includes(dir));
});
test("POSIX production cache file admission rejects real FIFO leaves without blocking or following symlinks",
  { skip: process.platform === "win32" }, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cache-fifo-"));
    t.after(() => fs.rmSync(dir, { recursive: true }));
    const fifo = path.join(dir, "fifo");
    const setup = spawnSync("mkfifo", ["-m", "600", fifo], { encoding: "utf8" });
    assert.equal(setup.error, undefined);
    assert.equal(setup.status, 0, setup.stderr);
    assert.equal(fs.lstatSync(fifo).isFIFO(), true, "fixture must be an actual FIFO, not a missing file");
    const target = path.join(dir, "regular"), link = path.join(dir, "link");
    fs.writeFileSync(target, "regular", { mode: 0o600 });
    fs.symlinkSync(target, link);
    for (const [file, flags, privateState] of [
      [fifo, fs.constants.O_RDONLY, true],
      [fifo, fs.constants.O_WRONLY | fs.constants.O_APPEND, false],
      [link, fs.constants.O_RDONLY, true],
    ]) {
      const script = `
        const cache=require(${JSON.stringify(path.join(__dirname, "ci-evidence-cache.cjs"))});
        try { cache.checkedFile(${JSON.stringify(file)},${flags},${privateState}); }
        catch(error){console.error(error.code||error.message);process.exitCode=1;}
      `;
      const admitted = spawnSync(process.execPath, ["-e", script], { encoding: "utf8", timeout: 5000 });
      assert.equal(admitted.error, undefined, "nonregular admission must reject, not reach the child timeout");
      assert.equal(admitted.status, 1);
      assert.match(admitted.stderr, /cache-observation-file-invalid|ENXIO|ELOOP/);
    }
    const descriptor = isolatedCache.checkedFile(target, fs.constants.O_RDONLY, true);
    assert.equal(isolatedCache.readAndClose(descriptor), "regular", "a valid regular file remains admissible");
    assert.equal(fs.lstatSync(fifo).isFIFO(), true);
    assert.equal(fs.readFileSync(target, "utf8"), "regular");
  });
test("production cache read-and-close preserves both failures and exits nonzero", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cache-read-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const file = path.join(dir, "state");
  fs.writeFileSync(file, '{"schema":1}', { mode: 0o600 });
  for (const fault of ["none", "read", "close", "both"]) {
    const script = `
      const fs=require("node:fs"),cache=require(${JSON.stringify(path.join(__dirname, "ci-evidence-cache.cjs"))});
      const descriptor=fs.openSync(${JSON.stringify(file)},"r"),read=fs.readSync,close=fs.closeSync;
      const readError=new Error("injected-primary-read"),closeError=new Error("injected-close");
      let closed=false;
      fs.readSync=(...args)=>{if(${JSON.stringify(fault)}==="read"||${JSON.stringify(fault)}==="both")throw readError;return read(...args);};
      fs.closeSync=(fd)=>{if(${JSON.stringify(fault)}==="close"||${JSON.stringify(fault)}==="both")throw closeError;close(fd);closed=true;};
      try { console.log(JSON.stringify({raw:cache.readAndClose(descriptor)})); }
      catch(error){console.log(JSON.stringify({aggregate:error instanceof AggregateError,
        primary:error===readError,secondary:error===closeError,
        failures:error.errors?.map(e=>e.message),sameCauses:error.errors?.[0]===readError&&error.errors?.[1]===closeError,
        message:error.message}));process.exitCode=1;}
      finally{fs.readSync=read;fs.closeSync=close;if(!closed)close(descriptor);}
    `;
    const result = spawnSync(process.execPath, ["-e", script], { encoding: "utf8", timeout: 5000 });
    assert.equal(result.error, undefined);
    assert.equal(result.status, fault === "none" ? 0 : 1);
    const observed = JSON.parse(result.stdout);
    if (fault === "none") assert.equal(observed.raw, '{"schema":1}');
    if (fault === "read") assert.equal(observed.primary, true);
    if (fault === "close") assert.equal(observed.secondary, true);
    if (fault === "both") {
      assert.equal(observed.aggregate, true);
      assert.equal(observed.message, "cache-observation-read-and-close-failed");
      assert.equal(observed.sameCauses, true);
      assert.deepEqual(observed.failures, ["injected-primary-read", "injected-close"]);
    }
  }
});
test("Linux production preparation admits only exact plain npm paths and one line terminator",
  { skip: process.platform !== "linux" }, (t) => {
    for (const [output, admitted] of [
      ["lf", true], ["crlf", true], ["unterminated", true],
      ["json", false], ["malformed", false], ["multiline", false], ["wrong-path", false],
      ["leading-space", false], ["trailing-space", false], ["extra-newline", false], ["empty", false],
    ]) {
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cold plain paths "));
      t.after(() => fs.rmSync(dir, { recursive: true }));
      const bin = path.join(dir, "bin");
      fs.mkdirSync(bin);
      fs.writeFileSync(path.join(bin, "go"), `#!${process.execPath}
process.stdout.write(JSON.stringify({GOMODCACHE:process.env.GOMODCACHE,GOCACHE:process.env.GOCACHE})+"\\n");
`, { mode: 0o700 });
      const invocation = path.join(dir, "npm-argv.json");
      fs.writeFileSync(path.join(bin, "npm"), `#!${process.execPath}
const fs=require("node:fs"),args=process.argv.slice(2),value=process.env.npm_config_cache;
fs.writeFileSync(process.env.FIXTURE_NPM_ARGV,JSON.stringify(args));
if(JSON.stringify(args)!==JSON.stringify(["config","get","cache"]))process.exit(64);
const outputs={
lf:value+"\\n",crlf:value+"\\r\\n",unterminated:value,json:JSON.stringify(value)+"\\n",
malformed:"{invalid\\n",multiline:value+"\\n"+value+"\\n", "wrong-path":value+"-other\\n",
"leading-space":" "+value+"\\n","trailing-space":value+" \\n","extra-newline":value+"\\n\\n",empty:"\\n"
};
process.stdout.write(outputs[process.env.FIXTURE_NPM_OUTPUT]);
`, { mode: 0o700 });
      const environment = {
        ...process.env, ...env, RUNNER_TEMP: dir, GITHUB_ENV: path.join(dir, "environment"),
        PATH: bin + path.delimiter + process.env.PATH,
        CI_EVIDENCE_COLD_REQUESTED: "true", CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED: "true",
        ImageOS: "ubuntu24", ImageVersion: "20261107.1",
        FIXTURE_NPM_ARGV: invocation, FIXTURE_NPM_OUTPUT: output,
      };
      fs.writeFileSync(environment.GITHUB_ENV, "previous=preserved\n", { mode: 0o600 });
      const prepare = () => isolatedCache.prepare("go-npm", dir, environment);
      if (admitted) {
        const root = prepare();
        const state = JSON.parse(fs.readFileSync(path.join(root, "state.json"), "utf8"));
        assert.ok(state.paths.npm.includes(" plain paths "), "valid fixture must contain spaces");
        assert.equal(state.observation.stores.npm.initial_entries, 0);
        assert.equal(state.observation.stores.npm.actual_path_match, true);
        assert.ok(fs.readFileSync(environment.GITHUB_ENV, "utf8").includes(`npm_config_cache=${state.paths.npm}\n`));
      } else {
        assert.throws(prepare, /cache-observation-npm-path-mismatch/, output);
        assert.equal(fs.readFileSync(environment.GITHUB_ENV, "utf8"), "previous=preserved\n", output);
        const roots = fs.readdirSync(dir).filter((name) => name.startsWith("waza-ci-cold-"));
        assert.equal(roots.length, 1);
        assert.equal(fs.existsSync(path.join(dir, roots[0], "state.json")), false,
          "invalid npm output cannot publish a partial observation");
      }
      assert.deepEqual(JSON.parse(fs.readFileSync(invocation, "utf8")), ["config", "get", "cache"]);
    }
  });
test("Linux production cache preparation observes actual private tool paths and rejects competing store replacement",
  { skip: process.platform !== "linux" }, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cold-paths-"));
    t.after(() => fs.rmSync(dir, { recursive: true }));
    const identity = {
      repository: env.GITHUB_REPOSITORY, source_sha: spawnSync("git", ["rev-parse", "HEAD"], {
        encoding: "utf8", cwd: path.resolve(__dirname, "../.."),
      }).stdout.trim(),
      workflow_sha: env.GITHUB_SHA, run_id: env.GITHUB_RUN_ID, attempt: env.GITHUB_RUN_ATTEMPT,
      job: env.GITHUB_JOB, os: "Linux",
      image_os: "ubuntu24", image_version: "20261107.1",
    };
    const environment = {
      ...process.env, ...env, RUNNER_TEMP: dir, GITHUB_ENV: path.join(dir, "environment"),
      CI_EVIDENCE_COLD_REQUESTED: "true", CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED: "true",
      ImageOS: "ubuntu24", ImageVersion: "20261107.1",
    };
    fs.writeFileSync(environment.GITHUB_ENV, "", { mode: 0o600 });
    const root = isolatedCache.prepare("go-npm", path.resolve(__dirname, "../..", "web"), environment);
    const assignments = Object.fromEntries(fs.readFileSync(environment.GITHUB_ENV, "utf8").trim().split("\n")
      .map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)]));
    const observedEnv = { ...environment, ...assignments };
    const observed = isolatedCache.observe(identity, observedEnv);
    assert.equal(observed.cache.go_dependencies, false);
    assert.equal(observed.cache.node_dependencies, false);
    assert.equal(observed.observation.stores.go_modules.initial_entries, 0);
    assert.equal(observed.observation.stores.go_build.initial_entries, 0);
    assert.equal(observed.observation.stores.npm.initial_entries, 0);
    assert.equal(isolatedCache.validObservation(observed.observation, identity, observed.cache), true);
    assert.ok(!JSON.stringify(observed).includes(root), "public cache evidence cannot reveal private store paths");
    fs.renameSync(assignments.GOMODCACHE, path.join(root, "original-gomod"));
    const competing = path.join(dir, "competing");
    fs.mkdirSync(competing, { mode: 0o700 });
    fs.writeFileSync(path.join(competing, "sentinel"), "user-owned");
    fs.symlinkSync(competing, assignments.GOMODCACHE, "dir");
    assert.throws(() => isolatedCache.observe(identity, observedEnv), /cache-observation-directory-invalid/);
    assert.equal(fs.readFileSync(path.join(competing, "sentinel"), "utf8"), "user-owned");
    assert.ok(fs.existsSync(path.join(root, "original-gomod")), "failed admission cannot remove original stores");
  });
test("Linux cache preparation fails explicitly on environment publication without deleting retained stores",
  { skip: process.platform !== "linux" }, (t) => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cold-env-"));
    t.after(() => fs.rmSync(dir, { recursive: true }));
    const environment = {
      ...process.env, ...env, RUNNER_TEMP: dir, GITHUB_ENV: dir,
      CI_EVIDENCE_COLD_REQUESTED: "true", CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED: "true",
      ImageOS: "ubuntu24", ImageVersion: "20261107.1",
    };
    assert.throws(() => isolatedCache.prepare("go", undefined, environment),
      /cache-observation-prepare-failed; retained RUNNER_TEMP\//);
    const roots = fs.readdirSync(dir).filter((name) => name.startsWith("waza-ci-cold-"));
    assert.equal(roots.length, 1);
    assert.ok(fs.statSync(path.join(dir, roots[0], "gomod")).isDirectory());
    assert.ok(fs.statSync(path.join(dir, roots[0], "gobuild")).isDirectory());
    assert.ok(fs.statSync(path.join(dir, roots[0], "state.json")).isFile());
  });
test("Linux preparation retains owned residue and competing entries after mkdir, write, close and exclusive-create faults",
  { skip: process.platform !== "linux" }, (t) => {
    for (const fault of ["mkdir", "write", "close", "exclusive-create"]) {
      const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-cold-fault-"));
      t.after(() => fs.rmSync(dir, { recursive: true }));
      const environment = {
        ...process.env, ...env, RUNNER_TEMP: dir, GITHUB_ENV: path.join(dir, "environment"),
        CI_EVIDENCE_COLD_REQUESTED: "true", CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED: "true",
        ImageOS: "ubuntu24", ImageVersion: "20261107.1",
      };
      fs.writeFileSync(environment.GITHUB_ENV, "previous=preserved\n", { mode: 0o600 });
      const original = { mkdirSync: fs.mkdirSync, openSync: fs.openSync, writeFileSync: fs.writeFileSync, closeSync: fs.closeSync };
      let stateDescriptor, statePath, triggered = false;
      const fail = () => { triggered = true; throw Object.assign(new Error("injected-owned-state-fault"), { code: "EFAULT" }); };
      fs.mkdirSync = (directory, ...args) => {
        if (fault === "mkdir" && directory.endsWith("/gobuild")) fail();
        return original.mkdirSync(directory, ...args);
      };
      fs.openSync = (file, flags, ...args) => {
        if (typeof file === "string" && file.endsWith("/state.json") && flags === "wx") {
          statePath = file;
          if (fault === "exclusive-create") {
            triggered = true;
            original.writeFileSync(file, "competing-owned-state", { mode: 0o600 });
          }
          stateDescriptor = original.openSync(file, flags, ...args);
          return stateDescriptor;
        }
        return original.openSync(file, flags, ...args);
      };
      fs.writeFileSync = (file, ...args) => {
        if (fault === "write" && file === stateDescriptor) fail();
        return original.writeFileSync(file, ...args);
      };
      fs.closeSync = (descriptor) => {
        if (fault === "close" && descriptor === stateDescriptor) fail();
        return original.closeSync(descriptor);
      };
      try {
        assert.throws(() => isolatedCache.prepare("go", undefined, environment), /cache-observation-prepare-failed/);
        assert.equal(triggered, true, "fixture must reach the intended filesystem fault");
      } finally {
        Object.assign(fs, original);
        if (fault === "close" && stateDescriptor !== undefined) original.closeSync(stateDescriptor);
      }
      assert.equal(fs.readFileSync(environment.GITHUB_ENV, "utf8"), "previous=preserved\n");
      const roots = fs.readdirSync(dir).filter((name) => name.startsWith("waza-ci-cold-"));
      assert.equal(roots.length, 1);
      assert.ok(fs.statSync(path.join(dir, roots[0], "gomod")).isDirectory());
      if (fault === "exclusive-create") {
        assert.equal(fs.readFileSync(statePath, "utf8"), "competing-owned-state");
      }
    }
  });
test("production artifact collection retains malformed expansion charges and rejects exhausted or dishonest declarations", () => {
  const report = parseGo(successful);
  report.selected = ["example/p::TestA"]; report.discovered = report.selected;
  report.identity.source_sha = run.head_sha;
  const fixture = artifactFixture(report), filename = "coverage-native-0.txt.evidence.json";
  const malformed = Buffer.alloc(1024 * 1024, 120);
  const invalid = archive(malformed, filename, { deflate: true });
  const valid = archive(report, filename, { deflate: true });
  const zips = [invalid, invalid, valid];
  const artifacts = zips.map((zip, index) => ({
    id: 100 + index, name: "ci-evidence-go-native-10-1-0", size_in_bytes: zip.length,
    expired: false, digest: `sha256:${createHash("sha256").update(zip).digest("hex")}`,
    workflow_run: { id: 10, head_sha: run.head_sha },
  }));
  const api = (endpoint) => endpoint.includes("/artifacts") ? { total_count: artifacts.length, artifacts } : fixture.api(endpoint);
  const budget = { compressed: 0, expanded: LIMITS.aggregateBytes - malformed.length, reports: 0 };
  const downloads = [];
  const observed = collectObservations("10", api, (id) => { downloads.push(id); return zips[id - 100]; }, budget);
  assert.deepEqual(downloads, [100, 101], "stop before processing later artifacts after exhausted reservation");
  assert.equal(budget.expanded, LIMITS.aggregateBytes, "malformed JSON retains its full admitted charge");
  assert.equal(observed.reports.length, 0);
  assert.ok(observed.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
  assert.ok(observed.diagnostics.includes("evidence-expanded-cap"));

  const secondRun = collectObservations("10", fixture.api, () => fixture.zip, budget);
  assert.ok(secondRun.diagnostics.includes("evidence-expanded-cap"), "shared budget remains exhausted across runs");
  assert.equal(secondRun.reports.length, 0);
  for (const declared of [1, malformed.length + 1]) {
    const zip = archive(malformed, filename, { deflate: true, expanded: declared });
    const charged = { expanded: 0 };
    assert.throws(() => artifactJSON(zip, filename, `sha256:${createHash("sha256").update(zip).digest("hex")}`, charged));
    assert.equal(charged.expanded, declared, "failed inflation/size checks retain reservations");
  }
  const enough = { compressed: 0, expanded: 0, reports: 0 };
  const retained = collectObservations("10", api, (id) => zips[id - 100], enough);
  assert.equal(retained.reports.length, 1, "a later valid artifact remains admissible when budget permits");
  assert.equal(enough.expanded, 2 * malformed.length + Buffer.byteLength(JSON.stringify(report)));
});
test("native Go capture validates probe output while historical unknown runtimes remain retained but ineligible", (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-go-version-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "all"), "example/p::TestA\n");
  const collector = path.join(__dirname, "ci-evidence.cjs");
  for (const version of ["go version go1.26.4 linux/amd64", "go version go1.25.0 linux/amd64", "go version go1.27rc1 linux/amd64", "go version devel go1.27-abcdef linux/amd64", "unknown", "SECRET invalid", ""]) {
    fs.writeFileSync(path.join(dir, "go"), `#!/bin/sh\n[ "$1" = version ] || exit 64\nprintf '%s\\n' '${version}'\n`, { mode: 0o700 });
    for (const args of [["init-go", "report.json"], ["finish-discovery", "report.json", "0", "0"]]) {
      const initialized = spawnSync(process.execPath, [collector, ...args], { cwd: dir, encoding: "utf8" });
      assert.equal(initialized.status, 0, initialized.stderr);
    }
    const captured = spawnSync(process.execPath, [collector, "go", "report.json", "all", "all"], {
      cwd: dir, env: { ...process.env, ...env, PATH: `${dir}${path.delimiter}${process.env.PATH}` },
      input: successful.map(JSON.stringify).join("\n") + "\n", encoding: "utf8", timeout: 10000,
    });
    assert.equal(captured.status, 0, captured.stderr);
    const finalized = spawnSync(process.execPath, [collector, "finish-go", "report.json", "0", "0"], { cwd: dir, encoding: "utf8" });
    assert.equal(finalized.status, 0, finalized.stderr);
    const report = JSON.parse(fs.readFileSync(path.join(dir, "report.json")));
    const valid = version.startsWith("go version ");
    assert.equal(report.identity.go, valid ? version : null);
    assert.equal(report.complete, valid, JSON.stringify(report.diagnostics));
    if (!valid) assert.ok(report.diagnostics.includes(version ? "go-runtime-invalid" : "go-runtime-unavailable"));
    assert.ok(!JSON.stringify(report).includes("SECRET"));
    report.identity.source_sha = run.head_sha;
    const observed = verified(report);
    assert.equal(observed.reports.length, 1, "authenticated unavailable evidence is retained");
    assert.equal(trends(observed.reports).cohorts[0].eligible.length, valid ? 1 : 0);
    assert.equal(historyProposal(observed.reports).targets.length, valid ? 1 : 0);
  }
  for (const version of [null, "", "unknown", "go version unknown linux/amd64", 12]) {
    const legacy = parseGo(successful);
    legacy.selected = ["example/p::TestA"]; legacy.discovered = legacy.selected;
    legacy.identity.source_sha = run.head_sha; legacy.identity.go = version;
    assert.equal(legacy.schema, 1); assert.equal(legacy.complete, true);
    const observed = verified(legacy);
    assert.equal(observed.reports.length, 1);
    assert.deepEqual(observed.reports[0].identity.go, version, "do not reinterpret historical wire values");
    assert.equal(trends(observed.reports).cohorts[0].eligible.length, 0);
    assert.deepEqual(historyProposal(observed.reports).targets, []);
  }
});
test("production history rejects malformed complete target records without losing neighboring observations", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-history-malformed-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const good = parseGo(successful);
  good.selected = ["example/p::TestA"]; good.discovered = good.selected;
  good.identity.source_sha = run.head_sha;
  const neighbor = { ...good, identity: { ...good.identity, run_id: "11" } };
  const oldExit = process.exitCode;
  try {
    const target = good.targets.find((item) => item.top_level);
    const invalidRows = [
      [], { ...target, package: null }, { ...target, package: "" }, { ...target, name: null },
      { ...target, top_level: null }, { ...target, status: "unknown" }, { ...target, seconds: null },
      { ...target, seconds: -1 },
    ];
    for (const targets of [null, undefined, {}, [null], ["invalid"], ...invalidRows.map((row) => [row]), Array(LIMITS.targets + 1).fill(target)]) {
      const bad = { ...good, targets };
      const first = artifactFixture(bad), second = artifactFixture(neighbor);
      const api = (endpoint) => endpoint.includes("/runs/11") ? second.api(endpoint) : first.api(endpoint);
      const download = (id) => id === first.artifactID ? first.zip : second.zip;
      process.exitCode = undefined;
      await main(["history", "10,11", path.join(dir, "history.json")], api, download);
      const proposal = JSON.parse(fs.readFileSync(path.join(dir, "history.json")));
      assert.equal(proposal.targets.length, 1);
      assert.deepEqual(proposal.targets[0].observations.map((item) => item.run_id), ["11"]);
      assert.ok(proposal.diagnostics.includes("evidence-artifact-invalid-or-unverified"));
      assert.equal(process.exitCode, 1, "retain valid output but report the malformed input");
    }
    const partial = { ...good, complete: false, diagnostics: ["execution-unavailable"], targets: null };
    const admitted = verified(partial);
    assert.equal(admitted.reports.length, 1, "retain legacy unavailable schema-1 observations");
    assert.equal(admitted.reports[0].targets, null, "do not fabricate an empty target array");
    assert.equal(trends(admitted.reports).cohorts[0].eligible.length, 0);
    assert.deepEqual(historyProposal(admitted.reports).targets, []);
    const alternative = verified({ ...good, targets: good.targets.map((item) => ({ ...item, future_field: "retained" })) });
    assert.equal(alternative.reports.length, 1, "unknown extra fields do not tighten the schema-1 reader");
    assert.equal(historyProposal(alternative.reports).targets.length, 1);
  } finally { process.exitCode = oldExit; }
});
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
  assert.equal(proposal.schema, 2);
  assert.equal(Object.keys(proposal.cohorts).length, 2);
  assert.deepEqual(observations.map((item) => proposal.cohorts[item.cohort_id].os), ["Linux", "Windows", "Linux"]);
  assert.equal(observations[0].cohort_id, observations[2].cohort_id);
  assert.notEqual(observations[0].cohort_id, observations[1].cohort_id);
  assert.ok(observations.every((item) => item.run_id === "10" && item.attempt === "1" && item.seconds === 2));
});
test("production history output interns authenticated cohorts and scales with targets and cohorts", async (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-history-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  const produce = async (count, versions) => {
    const events = [event("start")];
    for (let index = 0; index < count; index++) {
      const name = `TestRepresentativeInventory${String(index).padStart(5, "0")}`;
      events.push(event("run", name), event("pass", name, 1));
    }
    events.push(event("pass", null, count));
    const report = parseGo(events);
    report.selected = report.targets.map((target) => `${target.package}::${target.name}`);
    report.discovered = [...report.selected];
    report.identity.source_sha = run.head_sha;
    const fixtures = versions.map((go, index) => artifactFixture({
      ...report, identity: { ...report.identity, run_id: String(index + 10), go },
    }, {}, "go-run"));
    const output = path.join(dir, `${count}-${versions.length}.json`);
    await main(["history", fixtures.map((fixture) => fixture.id).join(","), output], (endpoint) => {
      const fixture = fixtures.find((fixture) => endpoint.includes(`/runs/${fixture.id}`));
      assert.ok(fixture, endpoint);
      return fixture.api(endpoint);
    }, (artifactID) => {
      const fixture = fixtures.find((fixture) => fixture.artifactID === artifactID);
      assert.ok(fixture, `download ${artifactID}`);
      return fixture.zip;
    });
    const saved = fs.readFileSync(output), proposal = JSON.parse(saved);
    assert.equal(proposal.schema, 2);
    assert.deepEqual(proposal.diagnostics, []);
    assert.equal(proposal.metadata.length, versions.length);
    assert.ok(proposal.metadata.every((metadata) => metadata.complete));
    assert.equal(proposal.targets.length, count);
    assert.equal(Object.keys(proposal.cohorts).length, new Set(versions).size);
    for (const descriptor of Object.values(proposal.cohorts)) assert.deepEqual(descriptor.selected, report.selected);
    for (const target of proposal.targets) {
      assert.equal(target.observations.length, versions.length);
      for (const observation of target.observations) {
        const descriptor = proposal.cohorts[observation.cohort_id];
        assert.ok(descriptor, "every emitted reference must join to a retained cohort");
        assert.equal(descriptor.go, versions[Number(observation.run_id) - 10]);
        assert.equal(observation.seconds, 1);
        assert.equal(observation.provenance.artifact_id, Number(observation.run_id) * 10);
        assert.match(observation.report_content_sha256, /^[0-9a-f]{64}$/);
        assert.equal(Object.hasOwn(observation, "cohort"), false, "inventory must not be embedded per measurement");
      }
    }
    assert.ok(saved.length < LIMITS.bytes, `${saved.length} bytes must remain below the production writer cap`);
    return { bytes: saved.length, proposal, output };
  };
  const versions = ["go version go1.26.4 linux/amd64", "go version go1.26.4 linux/amd64", "go version go1.26.5 linux/amd64"];
  const smaller = await produce(2000, versions);
  const larger = await produce(4000, versions);
  assert.ok(larger.bytes <= smaller.bytes * 2, "doubling targets must at most double output");
  const first = larger.proposal.targets[0].observations[0];
  const legacyObservation = { ...first, cohort: larger.proposal.cohorts[first.cohort_id] };
  delete legacyObservation.cohort_id;
  assert.ok(Buffer.byteLength(JSON.stringify(legacyObservation)) * 4000 * versions.length > LIMITS.bytes, "the same retained inventory would exceed the old quadratic writer cap");
  const twoCohorts = await produce(1000, versions.filter((_, index) => index !== 1));
  const fourCohorts = await produce(1000, ["go version go1.26.4 linux/amd64", "go version go1.26.5 linux/amd64", "go version go1.26.6 linux/amd64", "go version go1.26.7 linux/amd64"]);
  assert.ok(fourCohorts.bytes <= twoCohorts.bytes * 2, "doubling cohorts must at most double output");
  t.diagnostic(`production bytes: targets 2000/4000 with 3 reports and 2 cohorts=${smaller.bytes}/${larger.bytes}; 1000 targets with 2/4 reports and cohorts=${twoCohorts.bytes}/${fourCohorts.bytes}; cap=${LIMITS.bytes}`);
  const original = fs.readFileSync(larger.output);
  for (const mutation of [
    (proposal) => { proposal.schema = 1; },
    (proposal) => { proposal.kind = "go"; },
    (proposal) => { proposal.cohorts = null; },
    (proposal) => { proposal.cohorts = []; },
    (proposal) => { proposal.targets = null; },
    (proposal) => { proposal.targets[0] = null; },
    (proposal) => { proposal.targets[0].observations = null; },
    (proposal) => { proposal.targets[0].observations[0] = null; },
    (proposal) => { proposal.targets[0].observations[0].cohort_id = "f".repeat(64); },
    (proposal) => { delete proposal.targets[0].observations[0].cohort_id; },
    (proposal) => { proposal.targets[0].observations[0].source_sha = "b".repeat(40); },
    (proposal) => { proposal.cohorts[first.cohort_id].go = "different"; },
    (proposal) => { proposal.cohorts[first.cohort_id] = null; },
  ]) {
    const invalid = structuredClone(larger.proposal);
    mutation(invalid);
    assert.throws(() => writeHistoryProposal(larger.output, invalid), /history-(schema-unsupported|cohort-table-invalid|cohort-reference-invalid|cohort-content-invalid)/);
    assert.deepEqual(fs.readFileSync(larger.output), original, "invalid references/schema must not overwrite valid evidence");
  }
  assert.throws(() => writeHistoryProposal(larger.output, null), /history-schema-unsupported/);
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
test("browser runtime identity stays offline and missing or invalid Go versions cannot become eligible", () => {
  const outcomes = { ...env, BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "success" };
  const report = browserReport(browser(), outcomes, ["chromium::test-id"]);
  report.identity.source_sha = run.head_sha;
  for (const go of ["go version go1.26 linux/amd64", "go version go1.26.4 linux/amd64", "go version go1.27rc1 linux/amd64", "go version devel go1.27-abcd 2026-11-07 linux/amd64", "go version go1.26.4 X:loopvar linux/amd64"]) {
    const observed = verified({ ...report, identity: { ...report.identity, go } }).reports;
    assert.equal(observed.length, 1);
    assert.equal(trends(observed).cohorts[0].eligible.length, 1, go);
    assert.equal(historyProposal(observed).targets.length, 0, "browser timings never enter the Go planner proposal");
  }
  const newer = { ...report, identity: { ...report.identity, run_id: "11", go: "go version go1.26.5 linux/amd64" } };
  const grouped = trends([...verified(report).reports, ...verified(newer).reports]);
  assert.equal(grouped.cohorts.length, 2, "different Go runtimes must not share a browser cohort");
  assert.ok(grouped.cohorts.every((group) => group.eligible.length === 1));
  for (const go of [undefined, null, "", 1, "unknown", "go version go1.26.4", "go version go1.26.bad linux/amd64", "go version go1.26.4 linux/amd64\n"]) {
    const legacy = { ...report, identity: { ...report.identity, go } };
    if (go === undefined) delete legacy.identity.go;
    const observed = verified(legacy);
    assert.equal(observed.reports.length, 1, "schema-1 artifact readers retain authenticated historical observations without repair");
    assert.equal(trends(observed.reports).cohorts[0].eligible.length, 0, String(go));
    const parsed = browserReport(browser(), outcomes, ["chromium::test-id"], { go });
    assert.equal(parsed.identity.go, null, "producer drops malformed runtime content rather than publishing it");
    assert.ok(parsed.diagnostics.includes(go == null ? "go-runtime-unavailable" : "go-runtime-invalid"));
    assert.equal(parsed.complete, false);
  }
  const offline = spawnSync(process.execPath, ["-e", `
    require("node:child_process").spawnSync = () => { throw new Error("unexpected parser subprocess"); };
    const { browserReport } = require(${JSON.stringify(path.join(__dirname, "ci-evidence.cjs"))});
    const report = browserReport(${JSON.stringify(browser())}, ${JSON.stringify(outcomes)}, ["chromium::test-id"], {go:"go version go1.26.4 linux/amd64"});
    if (!report.complete || report.identity.source_sha !== null) throw new Error(JSON.stringify(report.diagnostics));
  `], { encoding: "utf8" });
  assert.equal(offline.status, 0, offline.stderr);
  for (const schema of [0, 2, null]) {
    assert.equal(verified({ ...report, schema }).reports.length, 0, `unsupported artifact schema ${schema} is not silently reinterpreted`);
  }
  for (const schema of [1, 2]) {
    assert.equal(verified({ ...report, schema, kind: "history-proposal" }).reports.length, 0, `history writer schema ${schema} is not a schema-1 test artifact`);
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
test("verified dependency-cache admission retains missing observations without vacuous eligibility", () => {
  const makeReport = (family) => {
    const report = family === "browser" ?
      browserReport(browser(), { ...env, BROWSER_DISCOVERY_OUTCOME: "success", BROWSER_EXECUTION_OUTCOME: "success" },
        ["chromium::test-id"]) : parseGo(successful);
    report.identity.source_sha = run.head_sha;
    if (family !== "browser") {
      report.inventory_scope = family === "go-native" ? "names" : "qualified";
      report.selected = family === "go-native" ? ["TestA"] : ["example/p::TestA"];
      report.discovered = report.selected;
      if (family === "go-native") report.cache.node_dependencies = "not-applicable";
    }
    return report;
  };
  const inspect = (report, family, eligible) => {
    const observed = verified(report, {}, family);
    assert.equal(observed.diagnostics.length, 0);
    assert.equal(observed.reports.length, 1, "legacy cache payload remains readable");
    const grouped = trends(observed.reports);
    assert.equal(grouped.cohorts[0].observations.length, 1);
    assert.equal(grouped.cohorts[0].observations[0].provenance.sample_eligible, true,
      "fixture must establish authenticated source admission independently of cache eligibility");
    assert.equal(grouped.cohorts[0].eligible.length, eligible ? 1 : 0);
    assert.equal(historyProposal(observed.reports).targets.length,
      eligible && family !== "browser" ? 1 : 0);
    for (const field of ["go_dependencies", "node_dependencies"]) {
      assert.equal(Object.hasOwn(observed.reports[0].cache, field), Object.hasOwn(report.cache, field));
      assert.equal(observed.reports[0].cache[field], report.cache[field]);
    }
  };
  for (const family of ["go-run", "go-native", "browser"]) {
    inspect(makeReport(family), family, true);
    for (const field of ["go_dependencies", "node_dependencies"]) {
      for (const value of [undefined, null, "", true, "false", "disabled", "not-applicable", false]) {
        const report = makeReport(family);
        if (value === undefined) delete report.cache[field];
        else report.cache[field] = value;
        inspect(report, family, value === false ||
          (field === "node_dependencies" && value === "not-applicable" && family === "go-native"));
      }
    }
  }
  for (const scope of [undefined, "qualified"]) {
    const report = makeReport("go-native");
    if (scope === undefined) delete report.inventory_scope;
    else report.inventory_scope = scope;
    report.selected = ["example/p::TestA"];
    report.discovered = report.selected;
    inspect(report, "go-native", false);
  }
  const masquerading = makeReport("go-native");
  inspect(masquerading, "go-run", false);
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
