"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");
const { browserReport, browserInventory } = require("./ci-evidence.cjs");

test("installed Playwright producer matches locked versions and emits discovery/pass/fail/skip without browser/provider calls", (t) => {
  const modulePath = process.env.CI_EVIDENCE_PLAYWRIGHT_MODULE_PATH
    ? path.resolve(process.env.CI_EVIDENCE_PLAYWRIGHT_MODULE_PATH)
    : path.resolve(__dirname, "../../web/node_modules/@playwright/test");
  assert.ok(fs.existsSync(path.join(modulePath, "cli.js")), "install locked dashboard dependencies before this contract test");
  const lock = JSON.parse(fs.readFileSync(path.resolve(__dirname, "../../web/package-lock.json"), "utf8"));
  const playwright = require.resolve("playwright/package.json", { paths: [modulePath] });
  const core = require.resolve("playwright-core/package.json", { paths: [path.dirname(playwright)] });
  for (const [name, file] of [["@playwright/test", path.join(modulePath, "package.json")], ["playwright", playwright], ["playwright-core", core]]) {
    const installed = JSON.parse(fs.readFileSync(file, "utf8"));
    assert.equal(installed.version, lock.packages[`node_modules/${name}`].version, `${name}: declared installed version must match lock; this is not content-integrity proof`);
  }
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "waza-ci-browser-"));
  t.after(() => fs.rmSync(dir, { recursive: true }));
  fs.writeFileSync(path.join(dir, "playwright.config.cjs"), `module.exports={testDir:".",workers:1,retries:0,projects:[{name:"chromium"}],reporter:"json"};\n`);
  fs.writeFileSync(path.join(dir, "contract.spec.cjs"), `const {test,expect}=require(${JSON.stringify(modulePath)});\ntest("pass",()=>expect(1).toBe(1));\ntest.skip("skip",()=>{});\ntest("fail",()=>expect("SECRET").toBe("other"));\n`);
  const execute = (file, args) => spawnSync(process.execPath, [path.join(modulePath, "cli.js"), "test", "--config", path.join(dir, "playwright.config.cjs"), "--project=chromium", "--retries=0", "--reporter=json", ...args], {
    cwd: dir, env: { ...process.env, PLAYWRIGHT_JSON_OUTPUT_NAME: path.join(dir, file) }, encoding: "utf8", timeout: 60000,
  });
  const discovered = execute(".ci-evidence-discovery.json", ["--list"]);
  assert.equal(discovered.status, 0, discovered.stderr);
  const actual = execute(".ci-evidence-report.json", []);
  assert.equal(actual.status, 1, actual.stderr);
  const reportInput = path.join(dir, ".ci-evidence-report.json");
  const discoveryInput = path.join(dir, ".ci-evidence-discovery.json");
  const raw = fs.readFileSync(reportInput, "utf8");
  assert.ok(raw.includes("SECRET"), "actual producer must contain the sensitive sentinel before sanitization");
  const data = JSON.parse(raw);
  const inventory = browserInventory(JSON.parse(fs.readFileSync(discoveryInput, "utf8")));
  const outcomes = { ...process.env, BROWSER_DISCOVERY_OUTCOME: discovered.status === 0 ? "success" : "failure", BROWSER_EXECUTION_OUTCOME: actual.status === 0 ? "success" : "failure" };
  const report = browserReport(data, outcomes, inventory);
  assert.equal(report.complete, true, JSON.stringify(report.diagnostics));
  assert.equal(report.counts.selected, 3);
  assert.equal(report.counts.executed, 2);
  assert.equal(report.counts.pass, 1);
  assert.equal(report.counts.fail, 1);
  assert.equal(report.counts.skip, 1);
  assert.ok(!JSON.stringify(report).includes("SECRET"));
  const output = path.join(dir, "sanitized.json");
  const cli = spawnSync(process.execPath, [path.join(__dirname, "ci-evidence.cjs"), "browser", reportInput, output, discoveryInput], { cwd: dir, env: outcomes, encoding: "utf8" });
  assert.equal(cli.status, 0, cli.stderr);
  const saved = fs.readFileSync(output, "utf8");
  assert.equal(JSON.parse(saved).counts.selected, 3);
  assert.equal(JSON.parse(saved).execution_outcome, "failure");
  assert.equal(JSON.parse(saved).complete, true, "fully observed test failure is complete evidence, not an incomplete execution");
  assert.ok(!saved.includes("SECRET"), "production CLI artifact must omit the sensitive sentinel");
});
