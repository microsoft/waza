import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

const example = JSON.parse(readFileSync(new URL("../../examples/runner/unit.json", import.meta.url), "utf8"));

function processRequest() {
  const request = structuredClone(example);
  request.input.task = request.input.task.replace("prompt: hello", `prompt: ${"a".repeat(1 << 20)}`);
  assert.notEqual(request.input.task, example.input.task);
  request.input.eval += "graders:\n";
  for (let index = 0; index < 127; index++) {
    const name = `${String(index).padStart(3, "0")}${"<".repeat(253)}`;
    request.input.eval += `  - name: '${name}'\n    type: text\n    config:\n      regex_match: ['(?s)^Mock response for: a+.*$']\n`;
  }
  return { kind: "waza.execution-submission", schema_version: "1.0", request };
}

async function killAt(binary, directory, store, wrapper, boundary, env) {
  const child = spawn(binary, ["runner", "checkpoint", "create", "--store", store], {
    cwd: directory, env, stdio: ["pipe", "pipe", "pipe"],
  });
  const exited = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => resolve({ code, signal }));
  });
  let stderr = "";
  let digest = "";
  let timer;
  const reached = new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(new Error(`CLI did not publish ${boundary}: ${stderr}`)), 60_000);
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
      const match = stderr.match(new RegExp(`^checkpoint ${boundary} ([a-f0-9]{64})\\n`, "m"));
      if (match && !digest) {
        digest = match[1];
        if (!child.kill("SIGKILL")) reject(new Error("Published-boundary CLI could not be killed."));
        else resolve(digest);
      }
    });
    void exited.then(() => {
      if (!digest) reject(new Error(`CLI exited before ${boundary}: ${stderr}`));
    }, reject);
    child.stdin.on("error", reject);
  });
  // The escaped real verdict record exceeds the undrained stdout pipe, keeping
  // the finalized process alive until its actual checkpoint locator is observed.
  child.stdout.pause();
  child.stdin.end(JSON.stringify(wrapper));
  try {
    const reference = await reached;
    assert.deepEqual(await exited, { code: null, signal: "SIGKILL" });
    return reference;
  } finally {
    clearTimeout(timer);
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
    try {
      await exited;
    } finally {
      child.stdin.destroy();
      child.stdout.destroy();
      child.stderr.destroy();
    }
  }
}

function readBoundObject(store, prefix, digest) {
  assert.match(digest, /^[a-f0-9]{64}$/);
  const bytes = readFileSync(join(store, `${prefix}-${digest}`));
  assert.equal(createHash("sha256").update(bytes).digest("hex"), digest);
  return bytes;
}

test("compiled checkpoint CLI recovers actual SIGKILL at both stored boundaries and rejects failed restore", {
  timeout: 180_000,
  skip: process.platform === "win32" ? "Unix SIGKILL is not native Windows recovery evidence." : false,
}, async (t) => {
  assert.ok(process.env.WAZA_E2E_CLI, "An actual clean-stamped owning CLI must be supplied; no test revision override.");
  const binary = resolve(process.env.WAZA_E2E_CLI);
  const directory = await mkdtemp(join(tmpdir(), "waza-checkpoint-cli-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const guard = join(directory, "reject-runtime");
  await writeFile(guard, '#!/bin/sh\nprintf invoked > "$0.invoked"\nexit 91\n', { mode: 0o700, flag: "wx" });
  assert.equal(spawnSync(guard, { timeout: 5_000 }).status, 91);
  assert.equal(readFileSync(`${guard}.invoked`, "utf8"), "invoked");
  await rm(`${guard}.invoked`);
  const env = {
    ...process.env, COPILOT_CLI_PATH: guard, WAZA_NO_UPDATE_CHECK: "1",
    ENABLE_COPILOT_TESTS: "false", ENABLE_COPILOT_SDK_TESTS: "false", ENABLE_LIVE_COPILOT_TESTS: "false",
  };
  for (const boundary of ["before-unit", "finalized-unit"]) {
    const store = join(directory, boundary);
    await mkdir(store, { mode: 0o700 });
    const wrapper = processRequest();
    const digest = await killAt(binary, directory, store, wrapper, boundary, env);
    const source = JSON.parse(readBoundObject(store, "blob", digest).toString("utf8"));
    assert.equal(source.kind, "waza.execution-checkpoint");
    assert.equal(source.schema_version, "1.0");
    assert.equal(source.boundary, boundary);
    const originalSelection = { ...wrapper.request.selection };
    wrapper.request.selection.attempt = 3;
    const run = () => spawnSync(binary, ["runner", "checkpoint", "resume", digest, "--store", store], {
      cwd: directory, input: JSON.stringify(wrapper), encoding: "utf8", timeout: 90_000, maxBuffer: 4 << 20, env,
    });
    let terminalPath;
    let originalTerminal;
    if (boundary === "finalized-unit") {
      const manifest = JSON.parse(readBoundObject(store, "manifest", source.manifest_digest).toString("utf8"));
      const terminal = manifest.artifacts.find((artifact) => artifact.name === manifest.terminal_artifact);
      assert.ok(terminal);
      originalTerminal = readBoundObject(store, "blob", terminal.sha256);
      terminalPath = join(store, `blob-${terminal.sha256}`);
      assert.ok(originalTerminal.byteLength > 128 << 10, "The real terminal must exceed undrained pipe buffering.");
    }
    const resumed = run();
    assert.equal(resumed.error, undefined);
    assert.equal(resumed.status, 1, resumed.stderr);
    const result = JSON.parse(resumed.stdout);
    assert.equal(result.exit_code, 1);
    assert.equal(result.source_withheld, undefined);
    assert.equal(result.submission.report, undefined);
    assert.equal(result.remaining_work, 0);
    assert.equal(result.submission.terminal.outcome, "evaluation_failure");
    const verdicts = result.submission.terminal.observation.validations;
    assert.equal(verdicts.length, 128);
    assert.deepEqual(verdicts.filter((value) => !value.passed).map(({ identifier, passed }) => ({ identifier, passed })), [
      { identifier: "_output_contains", passed: false },
    ]);
    if (boundary === "before-unit") {
      assert.equal(result.status, "completed");
      assert.equal(result.restored_units, 0);
      assert.equal(result.submission.engine_calls, 1);
      assert.notEqual(result.submission.reservation.execution_id, source.execution_id);
      assert.deepEqual(result.submission.reservation.parent, { execution_id: source.execution_id, relationship: "retry" });
      assert.equal(result.submission.terminal.selection.attempt, 3);
    } else {
      assert.equal(result.status, "reused");
      assert.equal(result.restored_units, 1);
      assert.equal(result.submission.engine_calls, 0);
      assert.equal(result.submission.reservation.execution_id, source.execution_id);
      assert.deepEqual(result.submission.terminal.selection, originalSelection);
      assert.deepEqual(result.submission.terminal, JSON.parse(originalTerminal.toString("utf8")));
      assert.deepEqual(readFileSync(terminalPath), originalTerminal);
      const marker = join(store, `final-${source.manifest_digest}`);
      assert.equal(readFileSync(marker, "utf8"), source.manifest_digest);
      await rm(marker);
      const failed = run();
      assert.equal(failed.error, undefined);
      const rejected = JSON.parse(failed.stdout);
      assert.equal(rejected.exit_code, 6);
      assert.equal(failed.status, rejected.exit_code);
      assert.equal(rejected.status, "rejected");
      assert.equal(rejected.diagnostics[0].code, "checkpoint_finalized_source_rejected");
      assert.equal(rejected.source_withheld, true);
      assert.equal(rejected.source, undefined);
      assert.equal(rejected.submission, undefined);
      assert.equal(rejected.restored_units, null);
      assert.equal(rejected.remaining_work, null);
      assert.deepEqual(readFileSync(terminalPath), originalTerminal);
    }
    assert.equal(existsSync(`${guard}.invoked`), false, "Owned recovery must never launch the rejecting runtime.");
  }
});
