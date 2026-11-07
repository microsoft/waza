#!/usr/bin/env node
"use strict";

const fs = require("node:fs");
const { spawnSync } = require("node:child_process");
const { performance } = require("node:perf_hooks");
const { StringDecoder } = require("node:string_decoder");
const { createHash } = require("node:crypto");
const { inflateRawSync } = require("node:zlib");
const os = require("node:os");

const LIMITS = Object.freeze({ bytes: 64 * 1024 * 1024, line: 1024 * 1024, targets: 100000, pages: 10, attempts: 10, reports: 100, aggregateBytes: 128 * 1024 * 1024 });
const number = (value) => typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
const text = (value) => typeof value === "string" && value.length <= 1024 && !/[\x00-\x1f]/.test(value) ? value : null;
const seconds = (start, end) => {
  if (typeof start !== "string" || typeof end !== "string") return null;
  const delta = Date.parse(end) - Date.parse(start);
  return Number.isFinite(delta) && delta >= 0 ? delta / 1000 : null;
};
const sorted = (values) => [...values].sort();
const verifiedReports = new WeakMap();
const diagnostic = (report, code) => {
  if (!report.diagnostics.includes(code)) report.diagnostics.push(code);
};
const command = (binary, args) => {
  const result = spawnSync(binary, args, { encoding: "utf8", maxBuffer: LIMITS.bytes, timeout: 60000 });
  if (result.error || result.status !== 0) throw new Error(`command-failed:${binary}`);
  return result.stdout.trim();
};
const readJSON = (file) => {
  const stat = fs.statSync(file);
  if (!stat.isFile() || stat.size > LIMITS.bytes) throw new Error("input-size-or-type");
  return JSON.parse(fs.readFileSync(file, "utf8"));
};
function writeJSON(file, report) {
  const data = JSON.stringify(report, null, 2) + "\n";
  if (Buffer.byteLength(data) > LIMITS.bytes) throw new Error("report-size");
  fs.writeFileSync(file, data);
}

function checkoutSHA() {
  try { return text(command("git", ["rev-parse", "HEAD"])); }
  catch { return null; } // Identity remains explicitly unavailable.
}
function identity(env = process.env, runtime = {}) {
  const checkout = Object.hasOwn(runtime, "source_sha") ? runtime.source_sha : checkoutSHA();
  return {
    repository: text(env.GITHUB_REPOSITORY), source_sha: checkout,
    workflow_sha: text(env.GITHUB_SHA), workflow: text(env.GITHUB_WORKFLOW),
    event: text(env.GITHUB_EVENT_NAME), ref: text(env.GITHUB_REF),
    run_id: text(env.GITHUB_RUN_ID), attempt: text(env.GITHUB_RUN_ATTEMPT),
    job: text(env.GITHUB_JOB), os: text(env.RUNNER_OS) || process.platform,
    arch: text(env.RUNNER_ARCH) || process.arch, node: process.version,
    os_release: os.release(), os_version: os.version(),
    ...runtime,
  };
}
function base(kind, env, runtime) {
  return {
    schema: 1, kind, identity: identity(env, runtime), complete: false,
    observed_at: new Date().toISOString(),
    diagnostics: [], cache: {
      test_results: "disabled",
      go_dependencies: ["true", "false"].includes(env.GO_CACHE_HIT) ? env.GO_CACHE_HIT === "true" : null,
      node_dependencies: env.CI_EVIDENCE_INVENTORY_SCOPE === "names" ? "not-applicable" : ["true", "false"].includes(env.NODE_CACHE_HIT) ? env.NODE_CACHE_HIT === "true" : null,
    },
  };
}
function inventory(file, report) {
  if (!file) { diagnostic(report, "inventory-unavailable"); return null; }
  try {
    const stat = fs.statSync(file);
    if (!stat.isFile() || stat.size > LIMITS.bytes) throw new Error("inventory-size");
    const items = fs.readFileSync(file, "utf8").split(/\r?\n/).filter(Boolean);
    if (items.length > LIMITS.targets || items.some((item) => !text(item)) || new Set(items).size !== items.length) {
      throw new Error("inventory-invalid");
    }
    return sorted(items);
  } catch {
    diagnostic(report, "inventory-invalid-or-missing");
    return null;
  }
}

function goReport(env = process.env, runtime = {}) {
  const report = base("go", env, runtime);
  report.policy = { retries: 0, count: 1, race: true, covermode: "atomic", timeout: "30m" };
  report.targets = [];
  report.packages = [];
  report.execution_exit = null;
  report.collector_exit = null;
  report.discovery_exit = null;
  report.discovery_collector_exit = null;
  return report;
}
function stdoutWriter(stream = process.stdout) {
  let error = null, rejectPending = null;
  const failed = (cause) => {
    error ??= new Error("console-output-failed", { cause });
    rejectPending?.(error);
  };
  const closed = () => {
    error ??= new Error("console-output-closed");
    rejectPending?.(error);
  };
  stream.on("error", failed);
  stream.on("close", closed);
  return {
    get error() { return error; },
    async write(output) {
      if (stream.destroyed || stream.writableEnded) closed();
      if (error) throw error;
      await new Promise((resolve, reject) => {
        let flushed = false, drained = false;
        const cleanup = () => { stream.removeListener("drain", onDrain); rejectPending = null; };
        const complete = () => { if (flushed && drained) { cleanup(); resolve(); } };
        const onDrain = () => { drained = true; complete(); };
        rejectPending = (cause) => { cleanup(); reject(cause); };
        stream.once("drain", onDrain);
        try {
          if (stream.write(output, (cause) => {
            if (cause) failed(cause);
            else { flushed = true; complete(); }
          })) drained = true;
          complete();
        } catch (cause) { failed(cause); }
      });
    },
    async close() {
      // Writable failures invoke the write callback before emitting error.
      await new Promise((resolve) => setImmediate(resolve));
      stream.removeListener("error", failed);
      stream.removeListener("close", closed);
      return error;
    },
  };
}
function goParser(report, output = () => {}) {
  const tests = new Map(), packages = new Map();
  let bytes = 0, events = 0;
  function accept(line) {
    bytes += Buffer.byteLength(line) + 1;
    const capped = bytes > LIMITS.bytes;
    if (capped) diagnostic(report, "input-byte-cap");
    if (Buffer.byteLength(line) > LIMITS.line) { diagnostic(report, "input-line-cap"); return; }
    let event;
    try { event = JSON.parse(line); } catch { diagnostic(report, "malformed-json"); return; }
    if (!event || typeof event !== "object" || Array.isArray(event)) { diagnostic(report, "invalid-json-record"); return; }
    events++;
    if (["build-output", "build-fail"].includes(event.Action) && text(event.ImportPath)) {
      if (event.Action === "build-output" && typeof event.Output === "string") return output(event.Output);
      if (event.Action === "build-fail") diagnostic(report, "build-failed");
      return;
    }
    const pkg = text(event.Package), name = event.Test === undefined ? null : text(event.Test);
    if (!pkg || (event.Test !== undefined && !name)) { diagnostic(report, "invalid-target"); return; }
    if (event.Action === "attr") {
      if (!name || ["Key", "Value"].some((field) => event[field] !== undefined && typeof event[field] !== "string")) {
        diagnostic(report, "invalid-attribute");
      }
      return;
    }
    if (!["start", "run", "pause", "cont", "pass", "fail", "skip", "output", "build-output", "build-fail"].includes(event.Action)) {
      diagnostic(report, "unknown-action"); return;
    }
    // Console output stays on the runner; never serialize test output or error text.
    if (event.Action === "output" || event.Action === "build-output") {
      if (typeof event.Output === "string") return output(event.Output);
      return;
    }
    if (capped) return;
    const map = name ? tests : packages, key = name ? `${pkg}::${name}` : pkg;
    if (!map.has(key)) {
      if (tests.size + packages.size >= LIMITS.targets) { diagnostic(report, "target-cap"); return; }
      map.set(key, { package: pkg, ...(name ? { name, top_level: !name.includes("/") } : {}), executed: false, status: null, seconds: null });
    }
    const target = map.get(key);
    if (event.Action === "run" || event.Action === "start") target.executed = true;
    if (["pass", "fail", "skip", "build-fail"].includes(event.Action)) {
      if (target.status !== null) diagnostic(report, "duplicate-terminal");
      target.status = event.Action === "build-fail" ? "fail" : event.Action;
      target.seconds = number(event.Elapsed);
      if (target.seconds === null) diagnostic(report, "duration-unavailable");
    }
  }
  function finish() {
    report.targets = [...tests.values()].sort((a, b) => `${a.package}::${a.name}`.localeCompare(`${b.package}::${b.name}`));
    report.packages = [...packages.values()].sort((a, b) => a.package.localeCompare(b.package));
    if (!events || !packages.size) diagnostic(report, "execution-unavailable");
    if ([...tests.values(), ...packages.values()].some((item) => item.status === null)) diagnostic(report, "unterminated-target");
    if (report.selected !== undefined && report.selected !== null) {
      const actual = report.targets.filter((item) => item.top_level).map((item) => report.inventory_scope === "names" ? item.name : `${item.package}::${item.name}`);
      const selectedSet = new Set(report.selected), actualSet = new Set(actual);
      if (actual.some((item) => !selectedSet.has(item)) || report.selected.some((item) => !actualSet.has(item))) diagnostic(report, "selection-execution-mismatch");
    }
    report.counts = {};
    for (const [scope, items] of [["top_level", report.targets.filter((item) => item.top_level)], ["subtests", report.targets.filter((item) => !item.top_level)], ["packages", report.packages]]) {
      report.counts[scope] = {
        observed: items.length, executed: items.filter((item) => item.executed).length,
        pass: items.filter((item) => item.status === "pass").length,
        fail: items.filter((item) => item.status === "fail").length,
        skip: items.filter((item) => item.status === "skip").length,
        unfinished: items.filter((item) => item.status === null).length,
      };
    }
    const top = report.targets.filter((item) => item.top_level);
    report.top_level_seconds = top.length && top.every((item) => item.seconds !== null) ? top.reduce((sum, item) => sum + item.seconds, 0) : null;
    report.complete = report.diagnostics.length === 0;
    return report;
  }
  return { accept, finish };
}
async function discoverGo(output) {
  const targets = new Set();
  let bytes = 0, pending = "";
  let failed = false;
  const decoder = new StringDecoder("utf8");
  const writer = stdoutWriter();
  async function accept(line) {
    const event = JSON.parse(line);
    if (event.Action === "run") throw new Error("discovery-executed-tests");
    if (event.Action === "build-output" && text(event.ImportPath) && typeof event.Output === "string") {
      await writer.write(event.Output);
    }
    if (event.Action === "output" && text(event.Package) && typeof event.Output === "string") {
      const name = event.Output.trim();
      if (/^(Test|Fuzz|Example)\S*$/.test(name) && text(name)) targets.add(`${event.Package}::${name}`);
      else await writer.write(event.Output);
    }
    if (event.Action === "fail" || event.Action === "build-fail") failed = true;
    if (targets.size > LIMITS.targets) throw new Error("discovery-target-cap");
  }
  try {
    for await (const chunk of process.stdin) {
      bytes += chunk.length;
      if (bytes > LIMITS.bytes) throw new Error("discovery-input-cap");
      pending += decoder.write(chunk);
      const lines = pending.split("\n");
      pending = lines.pop();
      for (const line of lines) {
        if (Buffer.byteLength(line) > LIMITS.line) throw new Error("discovery-input-cap");
        if (line.trim()) await accept(line);
      }
      if (Buffer.byteLength(pending) > LIMITS.line) throw new Error("discovery-input-cap");
    }
    if (pending || decoder.end()) throw new Error("discovery-truncated");
    if (failed) throw new Error("discovery-failed");
    if (!targets.size) throw new Error("discovery-empty");
  } finally {
    const error = await writer.close();
    if (error) throw error;
  }
  fs.writeFileSync(output, sorted(targets).join("\n") + "\n");
}
function goVersion() {
  try { return text(command("go", ["version"])); }
  catch { return null; } // Callers retain an explicit unavailable-runtime diagnostic.
}
async function captureGo(output, discovered, selected, env = process.env) {
  const cpu = process.cpuUsage();
  const go = goVersion();
  const report = goReport(env, { go: knownGoVersion(go) ? go : null });
  if (fs.existsSync(output)) {
    try {
      const prior = readJSON(output);
      report.discovery_exit = number(prior.discovery_exit);
      report.discovery_collector_exit = number(prior.discovery_collector_exit);
    } catch {
      diagnostic(report, "prior-discovery-state-invalid");
    }
  }
  if (!go) diagnostic(report, "go-runtime-unavailable");
  else if (!knownGoVersion(go)) diagnostic(report, "go-runtime-invalid");
  report.inventory_scope = env.CI_EVIDENCE_INVENTORY_SCOPE === "names" ? "names" : "qualified";
  report.discovered = inventory(discovered, report);
  report.selected = inventory(selected, report);
  if (report.selected && report.discovered) {
    const discoveredSet = new Set(report.discovered);
    if (report.selected.some((item) => !discoveredSet.has(item))) diagnostic(report, "selection-discovery-mismatch");
  }
  const writer = stdoutWriter();
  const parser = goParser(report, (output) => writer.write(output)), started = performance.now();
  let pending = "", dropping = false;
  const decoder = new StringDecoder("utf8");
  const save = () => {
    parser.finish();
    diagnostic(report, "execution-finalization-pending");
    report.complete = false;
    report.collector_seconds = (performance.now() - started) / 1000;
    const used = process.cpuUsage(cpu);
    report.collector_cpu_seconds = (used.user + used.system) / 1000000;
    writeJSON(output, report);
  };
  writeJSON(output, report);
  const cancelled = () => { diagnostic(report, "collector-cancelled"); save(); process.exit(143); };
  process.once("SIGTERM", cancelled);
  process.once("SIGINT", cancelled);
  let failure = null;
  try {
    for await (const chunk of process.stdin) {
      // Bound even an unterminated output record before decoding it.
      const parts = decoder.write(chunk).split("\n");
      for (let index = 0; index < parts.length; index++) {
        if (!dropping) pending += parts[index];
        if (Buffer.byteLength(pending) > LIMITS.line) { diagnostic(report, "input-line-cap"); pending = ""; dropping = true; }
        if (index < parts.length - 1) {
          if (!dropping && pending) await parser.accept(pending);
          pending = ""; dropping = false;
        }
      }
    }
    pending += decoder.end();
    if (pending || dropping) diagnostic(report, "truncated-json");
  } catch (cause) {
    failure = writer.error || new Error("capture-input-failed", { cause });
    diagnostic(report, failure.message);
  } finally {
    const error = await writer.close();
    if (error) { failure ??= error; diagnostic(report, error.message); }
    try { save(); }
    finally {
      process.removeListener("SIGTERM", cancelled);
      process.removeListener("SIGINT", cancelled);
    }
  }
  if (failure) throw failure;
}

function browserOutcomes(report, env) {
  for (const [field, key] of [["discovery_outcome", "BROWSER_DISCOVERY_OUTCOME"], ["execution_outcome", "BROWSER_EXECUTION_OUTCOME"]]) {
    const raw = env[key];
    report[field] = ["success", "failure", "cancelled", "skipped"].includes(raw) ? raw : null;
    if (raw !== undefined && raw !== "" && report[field] === null) diagnostic(report, "browser-outcome-invalid");
  }
  if (report.discovery_outcome !== null && report.discovery_outcome !== "success") diagnostic(report, "browser-discovery-not-successful");
  if (["cancelled", "skipped"].includes(report.execution_outcome)) diagnostic(report, "browser-execution-not-completed");
  if (report.execution_outcome === "failure" && report.counts?.fail === 0 && report.counts?.flaky === 0) diagnostic(report, "browser-execution-failed-without-terminal-evidence");
}
function knownGoVersion(version) {
  return text(version) !== null && /^go version (?:go\d+\.\d+(?:\.\d+)?(?:beta\d+|rc\d+)?|devel go\d+\.\d+(?:-[^\r\n]+)?)(?: X:\S+)? [a-z0-9_]+\/[a-z0-9_]+$/.test(version);
}
function browserReport(data, env = process.env, discovery = null, runtime = {}) {
  const report = base("playwright", env, { go: knownGoVersion(runtime.go) ? runtime.go : null, source_sha: text(runtime.source_sha) });
  if (runtime.go == null) diagnostic(report, "go-runtime-unavailable");
  else if (!knownGoVersion(runtime.go)) diagnostic(report, "go-runtime-invalid");
  report.targets = [];
  const config = data?.config;
  report.policy = { workers: number(config?.workers), projects: [], retries: null };
  if (!Array.isArray(config?.projects) || !config.projects.length) diagnostic(report, "browser-policy-unavailable");
  else {
    report.policy.projects = config.projects.map((project) => ({ id: text(project.id), name: text(project.name), retries: number(project.retries) }));
    if (report.policy.projects.some((project) => !project.id || project.retries === null)) diagnostic(report, "browser-policy-unavailable");
    report.policy.retries = report.policy.projects.every((project) => project.retries === 0) ? 0 : null;
  }
  report.identity.playwright = text(config?.version);
  if (!report.identity.playwright) diagnostic(report, "browser-runtime-unavailable");
  const ids = new Set();
  function visit(suites) {
    if (!Array.isArray(suites)) { diagnostic(report, "browser-suites-unavailable"); return; }
    for (const suite of suites) {
      if (suite.suites !== undefined) visit(suite.suites);
      if (!Array.isArray(suite.specs)) { diagnostic(report, "browser-specs-unavailable"); continue; }
      for (const spec of suite.specs) {
        if (!Array.isArray(spec.tests)) { diagnostic(report, "browser-tests-unavailable"); continue; }
        for (const test of spec.tests) {
          const id = text(spec.id), project = text(test.projectId);
          if (!id || !project || ids.size >= LIMITS.targets) { diagnostic(report, "browser-target-invalid-or-cap"); continue; }
          const key = `${project}::${id}`;
          if (ids.has(key)) { diagnostic(report, "browser-duplicate-target"); continue; }
          ids.add(key);
          const target = { id, project, status: text(test.status), expected_status: text(test.expectedStatus), results: [] };
          if (!["expected", "unexpected", "flaky", "skipped"].includes(target.status)) diagnostic(report, "browser-final-status-invalid");
          if (!["passed", "failed", "skipped"].includes(target.expected_status)) diagnostic(report, "browser-expected-status-invalid");
          if (!Array.isArray(test.results) || !test.results.length) diagnostic(report, "browser-results-unavailable");
          else for (const result of test.results) {
            const status = text(result.status), duration = number(result.duration), retry = number(result.retry);
            if (!["passed", "failed", "timedOut", "skipped", "interrupted"].includes(status)) diagnostic(report, "browser-status-invalid");
            if (duration === null) diagnostic(report, "duration-unavailable");
            if (retry === null || target.results.some((item) => item.retry === retry)) diagnostic(report, "browser-retry-invalid");
            if (status === "interrupted") diagnostic(report, "browser-interrupted");
            target.results.push({ status, seconds: duration === null ? null : duration / 1000, retry });
          }
          report.targets.push(target);
        }
      }
    }
  }
  visit(data?.suites);
  if (Array.isArray(data?.errors) && data.errors.length) diagnostic(report, "browser-global-errors");
  if (!ids.size) diagnostic(report, "browser-empty-selection");
  report.selected = sorted(ids);
  report.discovered = discovery;
  if (discovery === null) diagnostic(report, "inventory-unavailable");
  else {
    const discoveredSet = new Set(discovery);
    if (report.selected.some((item) => !discoveredSet.has(item)) || discovery.some((item) => !ids.has(item))) diagnostic(report, "selection-execution-mismatch");
  }
  report.counts = {
    selected: ids.size, executed: report.targets.filter((target) => target.results.some((result) => result.status !== "skipped")).length,
    pass: report.targets.filter((target) => target.status === "expected" && target.expected_status === "passed").length,
    fail: report.targets.filter((target) => target.status === "unexpected").length,
    skip: report.targets.filter((target) => target.status === "skipped").length,
    flaky: report.targets.filter((target) => target.status === "flaky").length,
    first_attempt_failures: report.targets.filter((target) => target.results.some((result) => result.retry === 0 && ["failed", "timedOut"].includes(result.status))).length,
  };
  const results = report.targets.flatMap((target) => target.results);
  report.result_counts = {
    observed: results.length,
    pass: results.filter((result) => result.status === "passed").length,
    fail: results.filter((result) => ["failed", "timedOut"].includes(result.status)).length,
    skip: results.filter((result) => result.status === "skipped").length,
    interrupted: results.filter((result) => result.status === "interrupted").length,
  };
  report.runner_seconds = number(data?.stats?.duration) === null ? null : data.stats.duration / 1000;
  if (report.runner_seconds === null) diagnostic(report, "duration-unavailable");
  browserOutcomes(report, env);
  report.complete = report.diagnostics.length === 0;
  return report;
}
function browserInventory(data) {
  const ids = [];
  function visit(suites) {
    if (!Array.isArray(suites)) throw new Error("browser-discovery-invalid");
    for (const suite of suites) {
      if (suite.suites !== undefined) visit(suite.suites);
      if (!Array.isArray(suite.specs)) throw new Error("browser-discovery-invalid");
      for (const spec of suite.specs) for (const test of spec.tests || []) {
        if (!text(spec.id) || !text(test.projectId) || ids.length >= LIMITS.targets) throw new Error("browser-discovery-invalid");
        ids.push(`${test.projectId}::${spec.id}`);
      }
    }
  }
  visit(data.suites);
  if (!ids.length || new Set(ids).size !== ids.length) throw new Error("browser-discovery-invalid");
  return sorted(ids);
}

function summarizeActions(run, attempts, artifacts, diagnostics = []) {
  const report = { schema: 1, kind: "actions", observed_at: new Date().toISOString(), test_content: "not-loaded; metadata-only", repository: "microsoft/waza", run_id: run.id ?? null, run_attempt: number(run.run_attempt), source_sha: text(run.head_sha), event: text(run.event), workflow: text(run.name), workflow_id: run.workflow_id ?? null, final_conclusion: text(run.conclusion), diagnostics: [...diagnostics], attempts: [], jobs: [], artifacts: [] };
  const retained = new Map();
  for (const attempt of attempts) {
    const unique = new Map();
    for (const job of attempt.jobs) {
        if (!Number.isSafeInteger(job.id)) { diagnostic(report, "job-id-unavailable"); continue; }
        if (job.run_id !== run.id || job.head_sha !== run.head_sha) diagnostic(report, "job-source-unverified");
        if (unique.has(job.id)) { diagnostic(report, "duplicate-job-in-attempt"); continue; }
      unique.set(job.id, job);
      if (retained.has(job.id)) {
        const original = retained.get(job.id);
        if (original.started_at !== text(job.started_at) || original.completed_at !== text(job.completed_at) || original.conclusion !== text(job.conclusion)) diagnostic(report, "retained-job-conflict");
        if (!original.seen_in_attempts.includes(attempt.number)) original.seen_in_attempts.push(attempt.number);
      }
      else retained.set(job.id, {
        id: job.id, name: text(job.name), run_attempt: number(job.run_attempt),
        seen_in_attempts: [attempt.number], os: Array.isArray(job.labels) ? job.labels.find((label) => /^(ubuntu|windows|macos)-/.test(label)) || null : null,
        status: text(job.status), conclusion: text(job.conclusion),
        started_at: text(job.started_at), completed_at: text(job.completed_at),
        elapsed_seconds: seconds(job.started_at, job.completed_at),
        steps: Array.isArray(job.steps) ? job.steps.map((step) => ({ number: number(step.number), name: text(step.name), conclusion: text(step.conclusion), seconds: seconds(step.started_at, step.completed_at) })) : null,
      });
    }
    const jobs = [...unique.values()];
    const durations = jobs.map((job) => seconds(job.started_at, job.completed_at));
    const starts = jobs.map((job) => Date.parse(job.started_at));
    const ends = jobs.map((job) => Date.parse(job.completed_at));
    const queueDelay = seconds(attempt.created_at, attempt.started_at);
    const timestampsComplete = attempt.number !== 1 || queueDelay !== null;
    if (!timestampsComplete) diagnostic(report, "first-attempt-timestamps-unavailable");
    const complete = jobs.length > 0 && durations.every((duration) => duration !== null) && jobs.every((job) => job.status === "completed") && attempt.complete && timestampsComplete;
    const fresh = jobs.filter((job) => job.run_attempt === attempt.number);
    const freshComplete = complete && fresh.length > 0 && jobs.every((job) => Number.isSafeInteger(job.run_attempt));
    report.attempts.push({
      number: attempt.number, complete, job_ids: [...unique.keys()].sort((a, b) => a - b),
      original_failures: jobs.filter((job) => job.conclusion === "failure").map((job) => job.id),
      cancellations: jobs.filter((job) => job.conclusion === "cancelled").map((job) => job.id),
      observed_job_span_seconds: freshComplete ? (Math.max(...fresh.map((job) => Date.parse(job.completed_at))) - Math.min(...fresh.map((job) => Date.parse(job.started_at)))) / 1000 : null,
      retained_job_span_seconds: complete ? (Math.max(...ends) - Math.min(...starts)) / 1000 : null,
      reused_job_ids: jobs.filter((job) => job.run_attempt !== attempt.number).map((job) => job.id),
      queue_start_delay_seconds: queueDelay,
    });
  }
  report.jobs = [...retained.values()].sort((a, b) => a.id - b.id);
  report.runner_elapsed_minutes_by_os = {};
  for (const os of new Set(report.jobs.map((job) => job.os))) {
    const jobs = report.jobs.filter((job) => job.os === os);
    const inventoryComplete = report.attempts.every((attempt) => attempt.complete) && !diagnostics.some((code) => code === "attempt-cap" || code.startsWith("jobs-"));
    report.runner_elapsed_minutes_by_os[os || "unknown"] = inventoryComplete && jobs.every((job) => job.elapsed_seconds !== null) ? jobs.reduce((sum, job) => sum + job.elapsed_seconds / 60, 0) : null;
  }
  const stepDurations = report.jobs.flatMap((job) => job.steps || []).map((step) => step.seconds);
  report.longest_observed_step_seconds = stepDurations.length ? Math.max(...stepDurations.filter((duration) => duration !== null)) : null;
  if (!Number.isFinite(report.longest_observed_step_seconds)) report.longest_observed_step_seconds = null;
  report.artifact_bytes = artifacts.length && !diagnostics.some((code) => code.startsWith("artifacts-")) && artifacts.every((artifact) => artifact.workflow_run?.id === run.id && artifact.workflow_run?.head_sha === run.head_sha && number(artifact.size_in_bytes) !== null) ? artifacts.reduce((sum, artifact) => sum + artifact.size_in_bytes, 0) : null;
  for (const artifact of artifacts) {
    const bound = artifact.workflow_run?.id === run.id && artifact.workflow_run?.head_sha === run.head_sha;
    report.artifacts.push({ id: artifact.id ?? null, name: text(artifact.name), source_bound: bound, expired: typeof artifact.expired === "boolean" ? artifact.expired : null, bytes: bound ? number(artifact.size_in_bytes) : null, digest: text(artifact.digest) });
    if (!bound) diagnostic(report, "artifact-source-unverified");
  }
  if (!report.attempts.length || report.attempts.some((attempt) => !attempt.complete)) diagnostic(report, "attempts-incomplete");
  if (!report.source_sha || !report.event || !report.workflow || run.status !== "completed") diagnostic(report, "run-metadata-incomplete");
  if (report.jobs.some((job) => job.elapsed_seconds === null || job.steps === null)) diagnostic(report, "job-metadata-incomplete");
  if (report.jobs.some((job) => job.steps?.some((step) => step.conclusion !== "skipped" && step.seconds === null))) diagnostic(report, "step-duration-unavailable");
  if (report.jobs.some((job) => job.run_attempt === null)) diagnostic(report, "job-attempt-unavailable");
  report.complete = report.diagnostics.length === 0;
  return report;
}
function collectActions(runID, api = (endpoint) => JSON.parse(command("gh", ["api", endpoint]))) {
  if (!/^[1-9][0-9]*$/.test(String(runID))) throw new Error("invalid-run-id");
  const prefix = `repos/microsoft/waza/actions/runs/${runID}`;
  const diagnostics = [], attempts = [], artifacts = [];
  let run;
  try { run = api(prefix); } catch { return summarizeActions({ id: Number(runID) }, [], [], ["run-api-failed"]); }
  if (!run || run.repository?.full_name !== "microsoft/waza" || String(run.id) !== String(runID)) return summarizeActions({ id: Number(runID) }, [], [], ["run-source-invalid"]);
  function pages(endpoint, field) {
    const items = [];
    let complete = false;
    for (let page = 1; page <= LIMITS.pages; page++) {
      let result;
      try { result = api(`${endpoint}?per_page=100&page=${page}`); } catch { diagnostics.push(`${field}-api-failed`); break; }
      if (!result || !Array.isArray(result[field]) || !Number.isSafeInteger(result.total_count)) { diagnostics.push(`${field}-api-invalid`); break; }
      items.push(...result[field]);
      if (items.length >= result.total_count) { complete = items.length === result.total_count; break; }
    }
    if (!complete) diagnostics.push(`${field}-pagination-incomplete`);
    return { items, complete };
  }
  if (!Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1) diagnostics.push("attempt-count-unavailable");
  else {
    for (let attempt = 1; attempt <= Math.min(run.run_attempt, LIMITS.attempts); attempt++) {
      let metadata = {};
      try { metadata = api(`${prefix}/attempts/${attempt}`); } catch { diagnostics.push("attempt-api-failed"); }
      const jobs = pages(`${prefix}/attempts/${attempt}/jobs`, "jobs");
      attempts.push({ number: attempt, jobs: jobs.items, complete: jobs.complete, created_at: null, started_at: metadata.run_started_at });
      // GitHub does not expose retry-request time; original creation is not retry queue time.
      if (attempt === 1) attempts.at(-1).created_at = run.created_at;
    }
    if (run.run_attempt > LIMITS.attempts) diagnostics.push("attempt-cap");
  }
  artifacts.push(...pages(`${prefix}/artifacts`, "artifacts").items);
  return summarizeActions(run, attempts, artifacts, diagnostics);
}

function cohort(report) {
  return JSON.stringify({
    kind: report.kind, workflow: report.identity.workflow, event: report.identity.event,
    source_sha: report.identity.source_sha,
    os: report.identity.os, arch: report.identity.arch, go: report.identity.go ?? null,
    os_release: report.identity.os_release, os_version: report.identity.os_version,
    node: report.identity.node, playwright: report.identity.playwright ?? null,
    policy: report.policy, selected: report.selected, cache: report.cache,
    source_ref: report.identity.ref,
  });
}
function artifactJSON(archive, expectedName, digest, budget = { expanded: 0 }) {
  if (!Buffer.isBuffer(archive) || archive.length > LIMITS.bytes) throw new Error("artifact-byte-cap");
  const actualDigest = `sha256:${createHash("sha256").update(archive).digest("hex")}`;
  if (digest !== actualDigest) throw new Error("artifact-digest-unverified");
  let end = -1;
  for (let index = archive.length - 22; index >= Math.max(0, archive.length - 65557); index--) {
    if (archive.readUInt32LE(index) === 0x06054b50 && index + 22 + archive.readUInt16LE(index + 20) === archive.length) { end = index; break; }
  }
  if (end < 0 || archive.readUInt16LE(end + 4) || archive.readUInt16LE(end + 6) ||
      archive.readUInt16LE(end + 8) !== 1 || archive.readUInt16LE(end + 10) !== 1) throw new Error("artifact-zip-layout");
  const central = archive.readUInt32LE(end + 16);
  if (central + 46 > end || archive.readUInt32LE(central) !== 0x02014b50) throw new Error("artifact-zip-layout");
  const flags = archive.readUInt16LE(central + 8), method = archive.readUInt16LE(central + 10);
  const compressed = archive.readUInt32LE(central + 20), expanded = archive.readUInt32LE(central + 24);
  const nameLength = archive.readUInt16LE(central + 28), offset = archive.readUInt32LE(central + 42);
  if (central + 46 + nameLength > end || (flags & 1) || ![0, 8].includes(method) ||
      compressed > LIMITS.bytes || expanded > LIMITS.bytes ||
      archive.subarray(central + 46, central + 46 + nameLength).toString("utf8") !== expectedName ||
      offset + 30 > central || archive.readUInt32LE(offset) !== 0x04034b50) throw new Error("artifact-zip-entry");
  const localNameLength = archive.readUInt16LE(offset + 26);
  const start = offset + 30 + localNameLength + archive.readUInt16LE(offset + 28);
  if (archive.subarray(offset + 30, offset + 30 + localNameLength).toString("utf8") !== expectedName ||
      start + compressed > central) throw new Error("artifact-zip-entry");
  const compressedBytes = archive.subarray(start, start + compressed);
  if (budget.expanded + expanded > LIMITS.aggregateBytes) throw new Error("evidence-expanded-cap");
  budget.expanded += expanded;
  if (!expanded) throw new Error("artifact-zip-size");
  const content = method === 8 ? inflateRawSync(compressedBytes, { maxOutputLength: expanded }) : compressedBytes;
  if (content.length !== expanded) throw new Error("artifact-zip-size");
  return { report: JSON.parse(content.toString("utf8")), content_sha256: createHash("sha256").update(content).digest("hex"), content_bytes: content.length };
}
function collectObservations(runID, api, download = (id) => {
  const result = spawnSync("gh", ["api", `repos/microsoft/waza/actions/artifacts/${id}/zip`], { maxBuffer: LIMITS.bytes, timeout: 60000 });
  if (result.error || result.status !== 0) throw new Error("artifact-download-failed");
  return result.stdout;
}, budget = { compressed: 0, expanded: 0, reports: 0 }) {
  const metadata = collectActions(runID, api), reports = [], diagnostics = [...metadata.diagnostics];
  for (const artifact of metadata.artifacts) {
    const match = /^ci-evidence-(go-native|go-run|browser)-([1-9][0-9]*)-([1-9][0-9]*)(?:-([A-Za-z0-9_-]+))?$/.exec(artifact.name || "");
    if (!match) continue;
    if (!artifact.source_bound || match[2] !== String(runID) || !Number.isSafeInteger(artifact.id) || artifact.expired !== false) {
      diagnostics.push("evidence-artifact-unavailable"); continue;
    }
    budget.compressed += artifact.bytes ?? LIMITS.aggregateBytes;
    if (budget.compressed > LIMITS.aggregateBytes || budget.reports >= LIMITS.reports) { diagnostics.push("evidence-artifact-cap"); break; }
    try {
      if ((match[1] === "go-native" && !/^[0-3]$/.test(match[4] || "")) ||
          (match[1] === "browser" && match[4] !== undefined) ||
          (match[1] === "go-run" && !/^[A-Za-z_][A-Za-z0-9_-]*$/.test(match[4] || ""))) throw new Error("artifact-name-binding");
      const expectedName = match[1] === "browser" ? "ci-evidence-browser.json" : match[1] === "go-run" ? "coverage.txt.evidence.json" : `coverage-native-${match[4]}.txt.evidence.json`;
      const { report, content_sha256 } = artifactJSON(download(artifact.id), expectedName, artifact.digest, budget);
      const partial = report.complete === false && Array.isArray(report.diagnostics) && report.diagnostics.length > 0 &&
        report.diagnostics.every((code) => text(code) !== null);
      const selectedValid = Array.isArray(report.selected) || (partial && report.selected == null);
      const policyValid = (report.policy && typeof report.policy === "object" && !Array.isArray(report.policy)) || (partial && report.policy == null);
      const targetsValid = report.kind !== "go" || report.complete !== true ||
        (Array.isArray(report.targets) && report.targets.length <= LIMITS.targets && report.targets.every((target) =>
          target && typeof target === "object" && !Array.isArray(target) &&
          text(target.package) && text(target.name) && typeof target.top_level === "boolean" &&
          ["pass", "fail", "skip"].includes(target.status) && number(target.seconds) !== null));
      if (report.schema !== 1 || report.kind !== (match[1] === "browser" ? "playwright" : "go") ||
          report.identity?.repository !== "microsoft/waza" || report.identity?.run_id !== String(runID) ||
          report.identity?.event !== metadata.event || report.identity?.workflow !== metadata.workflow ||
          report.identity?.attempt !== match[3] ||
          Number(match[3]) > metadata.run_attempt || metadata.run_attempt === null ||
          !selectedValid || !policyValid || !targetsValid || !report.cache || typeof report.cache !== "object" || Array.isArray(report.cache) ||
          (match[1] === "go-run" && report.identity.job !== match[4])) throw new Error("artifact-report-binding");
      const checkoutVerified = /^[0-9a-f]{40}$/.test(metadata.source_sha || "") && report.identity.source_sha === metadata.source_sha && report.identity.workflow_sha === metadata.source_sha;
      if (!checkoutVerified && metadata.event !== "pull_request") throw new Error("artifact-checkout-binding");
      if (!checkoutVerified) diagnostics.push("pull-request-merge-checkout-binding-unavailable");
      // Artifact names are not an API-authenticated attempt receipt. Only a
      // successful single-attempt run can supply a baseline timing sample.
      const sampleEligible = checkoutVerified && metadata.complete && metadata.run_attempt === 1 && match[3] === "1" && metadata.final_conclusion === "success" &&
        metadata.jobs.every((job) => ["success", "skipped"].includes(job.conclusion));
      verifiedReports.set(report, { run_id: String(runID), artifact_id: artifact.id, content_sha256, metadata_complete: metadata.complete, checkout_binding: checkoutVerified ? "api-head" : "unavailable-pr-merge", sample_eligible: sampleEligible, attempt_binding: metadata.run_attempt === 1 ? "single-api-attempt" : "name-and-report-only" });
      reports.push(report);
      budget.reports++;
    } catch (error) {
      if (error.message === "evidence-expanded-cap") { diagnostics.push("evidence-expanded-cap"); break; }
      diagnostics.push("evidence-artifact-invalid-or-unverified");
    }
  }
  if (!reports.length) diagnostics.push("test-evidence-unavailable");
  return { metadata, reports, diagnostics };
}
function trends(reports) {
  const groups = new Map(), seen = new Set();
  for (const report of reports) {
    const key = cohort(report);
    if (!groups.has(key)) groups.set(key, { cohort: JSON.parse(key), observations: [], eligible: [], attainment: "not-assessed" });
    const group = groups.get(key);
    const binding = verifiedReports.get(report);
    const source = { run_id: report.identity.run_id, attempt: report.identity.attempt, source_sha: report.identity.source_sha, job: report.identity.job, provenance: binding || "unverified" };
    const id = JSON.stringify([key, source.run_id, source.attempt, source.job]);
    if (seen.has(id)) continue;
    seen.add(id); group.observations.push(source);
    if (eligibleSample(report)) {
      if (!group.eligible.some((item) => item.run_id === source.run_id)) group.eligible.push(source);
    }
  }
  return { schema: 1, kind: "trends", policy: "non-blocking; no target attainment assessed", cohorts: [...groups.values()].map((group) => ({ ...group, minimum_samples_available: group.eligible.length >= 3 })) };
}
function knownIdentity(report) {
  const required = ["repository", "source_sha", "workflow", "event", "os", "arch", "os_release", "os_version", "node", "run_id", "attempt", "job"];
  required.push(report.kind === "go" ? "go" : "playwright");
  return required.every((field) => text(report.identity[field]) !== null && report.identity[field].length > 0) &&
    /^[0-9a-f]{40}$/.test(report.identity.source_sha) &&
    knownGoVersion(report.identity.go);
}
function eligibleSample(report) {
  if (!verifiedReports.get(report)?.sample_eligible || !knownIdentity(report) ||
      report.complete !== true || report.identity.attempt !== "1" || report.policy?.retries !== 0 ||
      report.cache.test_results !== "disabled") return false;
  const dependencies = Object.values(report.cache).filter((value) => value !== "disabled" && value !== "not-applicable");
  if (!dependencies.length || !dependencies.every((value) => value === false)) return false;
  const counts = report.kind === "go" ? report.counts?.top_level : report.counts;
  if (number(counts?.executed) === null || counts.executed <= 0 || counts.fail !== 0) return false;
  return report.kind === "go" ? report.discovery_exit === 0 && report.discovery_collector_exit === 0 && report.execution_exit === 0 && report.collector_exit === 0 && report.policy.count === 1 && report.counts?.packages?.fail === 0 && counts.unfinished === 0 : report.discovery_outcome === "success" && report.execution_outcome === "success" && counts.flaky === 0 && counts.first_attempt_failures === 0;
}
function historyProposal(reports) {
  const samples = new Map(), cohorts = {};
  for (const report of reports) {
    const binding = verifiedReports.get(report);
    if (report.kind !== "go" || !eligibleSample(report)) continue;
    const cohortKey = cohort(report), cohortID = createHash("sha256").update(cohortKey).digest("hex");
    for (const target of report.targets) {
      if (!target.top_level || target.status !== "pass" || number(target.seconds) === null) continue;
      const key = `${target.package}::${target.name}`;
      if (!samples.has(key)) samples.set(key, []);
      const observations = samples.get(key);
      if (!Object.hasOwn(cohorts, cohortID)) cohorts[cohortID] = JSON.parse(cohortKey);
      const source = { run_id: report.identity.run_id, attempt: report.identity.attempt, job: report.identity.job, source_sha: report.identity.source_sha, report_content_sha256: binding.content_sha256, seconds: target.seconds, cohort_id: cohortID, provenance: binding };
      if (!observations.some((item) => item.run_id === source.run_id && item.attempt === source.attempt && item.job === source.job && item.cohort_id === cohortID)) observations.push(source);
    }
  }
  return { schema: 2, kind: "history-proposal", application: "review-only; canonical TSV is never rewritten", cohorts, targets: [...samples].sort(([a], [b]) => a.localeCompare(b)).map(([target, observations]) => ({ target, observations })) };
}
function writeHistoryProposal(output, proposal) {
  if (!proposal || proposal.schema !== 2 || proposal.kind !== "history-proposal") throw new Error("history-schema-unsupported");
  if (!proposal.cohorts || typeof proposal.cohorts !== "object" || Array.isArray(proposal.cohorts) || !Array.isArray(proposal.targets)) throw new Error("history-cohort-table-invalid");
  for (const [id, descriptor] of Object.entries(proposal.cohorts)) {
    if (!descriptor || typeof descriptor !== "object" || Array.isArray(descriptor) || descriptor.kind !== "go" ||
        text(descriptor.source_sha) === null || !/^[0-9a-f]{40}$/.test(descriptor.source_sha) ||
        createHash("sha256").update(JSON.stringify(descriptor)).digest("hex") !== id) throw new Error("history-cohort-content-invalid");
  }
  for (const target of proposal.targets) {
    if (!target || !Array.isArray(target.observations)) throw new Error("history-cohort-reference-invalid");
    for (const observation of target.observations) {
      if (!observation || !Object.hasOwn(proposal.cohorts, observation.cohort_id) || proposal.cohorts[observation.cohort_id]?.source_sha !== observation.source_sha) throw new Error("history-cohort-reference-invalid");
    }
  }
  writeJSON(output, proposal);
}
async function main(args, api, download) {
  const [mode, input, output, ...extra] = args;
  if (mode === "finish-discovery" && input && /^[0-9]+$/.test(output || "") && /^[0-9]+$/.test(extra[0] || "")) {
    const report = readJSON(input);
    report.discovery_exit = Number(output);
    report.discovery_collector_exit = Number(extra[0]);
    if (report.discovery_exit !== 0 || report.discovery_collector_exit !== 0) diagnostic(report, "discovery-command-failed");
    writeJSON(input, report);
    return;
  }
  if (mode === "finish-go" && input && /^[0-9]+$/.test(output || "") && /^[0-9]+$/.test(extra[0] || "")) {
    const report = readJSON(input);
    report.execution_exit = Number(output);
    report.collector_exit = Number(extra[0]);
    report.diagnostics = report.diagnostics.filter((code) => code !== "execution-finalization-pending");
    if (report.collector_exit !== 0) diagnostic(report, "collector-command-failed");
    if (report.execution_exit !== 0 && report.counts?.packages?.fail === 0) diagnostic(report, "execution-failed-without-terminal-evidence");
    report.complete = report.diagnostics.length === 0;
    writeJSON(input, report);
    return;
  }
  if (mode === "init-go" && input && !output) {
    const report = goReport(process.env, { go: null });
    diagnostic(report, "execution-not-observed");
    writeJSON(input, report);
    return;
  }
  if (mode === "discover" && input && !output) return discoverGo(input);
  if (mode === "go" && input && output) return captureGo(input, output, extra[0]);
  if (mode === "browser" && input && output) {
    let report, discovery = null, discoveryInvalid = false;
    const started = performance.now();
    const runtime = { go: goVersion(), source_sha: checkoutSHA() };
    if (extra[0]) {
      try { discovery = browserInventory(readJSON(extra[0])); }
      catch { discoveryInvalid = true; }
    }
    try { report = browserReport(readJSON(input), process.env, discovery, runtime); }
    catch {
      report = base("playwright", process.env, { ...runtime, go: knownGoVersion(runtime.go) ? runtime.go : null });
      if (!runtime.go) diagnostic(report, "go-runtime-unavailable");
      else if (!knownGoVersion(runtime.go)) diagnostic(report, "go-runtime-invalid");
      report.discovered = discovery;
      browserOutcomes(report, process.env);
      diagnostic(report, "browser-input-invalid-or-missing");
    }
    if (discoveryInvalid) diagnostic(report, "browser-discovery-invalid-or-missing");
    if (report.diagnostics.length) report.complete = false;
    report.collector_seconds = (performance.now() - started) / 1000;
    writeJSON(output, report);
    return;
  }
  if (mode === "actions" && input && output) {
    const report = collectActions(input, api);
    writeJSON(output, report);
    if (!report.complete) process.exitCode = 1;
    return;
  }
  if (["trends", "history"].includes(mode) && input && output) {
    const ids = input.split(",");
    if (ids.length > LIMITS.attempts || new Set(ids).size !== ids.length) throw new Error("run-count-cap-or-duplicate");
    const budget = { compressed: 0, expanded: 0, reports: 0 };
    const collections = ids.map((id) => collectObservations(id, api, download, budget));
    const reports = collections.flatMap((collection) => collection.reports);
    const report = mode === "trends" ? trends(reports) : historyProposal(reports);
    report.diagnostics = collections.flatMap((collection) => collection.diagnostics);
    report.metadata = collections.map((collection) => collection.metadata);
    if (mode === "history") writeHistoryProposal(output, report);
    else writeJSON(output, report);
    if (report.diagnostics.length) process.exitCode = 1;
    return;
  }
  throw new Error("usage: ci-evidence.cjs init-go OUT | discover OUT | go OUT DISCOVERED SELECTED | browser IN OUT DISCOVERY | actions RUN OUT | trends|history RUN[,RUN...] OUT");
}
module.exports = { LIMITS, goReport, goParser, browserReport, browserInventory, summarizeActions, collectActions, collectObservations, artifactJSON, trends, historyProposal, writeHistoryProposal, cohort, main };
if (require.main === module) main(process.argv.slice(2)).catch((error) => { console.error(`CI evidence: ${error.message}`); process.exitCode = 1; });
