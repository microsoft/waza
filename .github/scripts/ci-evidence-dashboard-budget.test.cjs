'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const test = require('node:test');
const { BUDGET, evaluate, main } = require('./ci-evidence-dashboard-budget.cjs');

const report = (seconds, stats = {}) => ({
  stats: { expected: 99, unexpected: 0, flaky: 1, skipped: 1, duration: seconds * 1000, ...stats },
});

test('scales the target and per-run ceiling with executed tests, excluding skips', () => {
  const result = evaluate(report(30));
  assert.equal(result.tests, 100);
  assert.equal(result.target_seconds, 100 * BUDGET.target_seconds_per_test);
  assert.equal(result.ceiling_seconds, result.target_seconds * BUDGET.ceiling_factor);
  assert.equal(result.status, 'within');
});

test('reports runs above target but fails only above the ceiling', () => {
  const target = 100 * BUDGET.target_seconds_per_test;
  const ceiling = target * BUDGET.ceiling_factor;
  assert.equal(evaluate(report(target)).status, 'within');
  assert.equal(evaluate(report(target + 0.1)).status, 'above-target');
  assert.equal(evaluate(report(ceiling)).status, 'above-target');
  assert.equal(evaluate(report(ceiling + 0.1)).status, 'exceeded');
});

test('rejects incomplete reports instead of passing them', () => {
  for (const bad of [
    {},
    report(30, { expected: -1 }),
    report(30, { flaky: 0.5 }),
    report(30, { skipped: undefined }),
    report(30, { duration: Number.NaN }),
    report(0),
    report(30, { expected: 0, flaky: 0 }),
  ]) assert.throws(() => evaluate(bad));
});

test('main writes the step summary and exits non-zero only when the ceiling is exceeded', (t) => {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'dashboard-budget-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const summaryFile = path.join(directory, 'summary.md');
  const write = (seconds) => {
    const file = path.join(directory, `${seconds}.json`);
    fs.writeFileSync(file, JSON.stringify(report(seconds)));
    return file;
  };
  const stdout = t.mock.method(process.stdout, 'write', () => true);
  const stderr = t.mock.method(process.stderr, 'write', () => true);
  assert.equal(main([write(30)], { GITHUB_STEP_SUMMARY: summaryFile }), 0);
  assert.equal(main([write(1000)], { GITHUB_STEP_SUMMARY: summaryFile }), 1);
  assert.match(fs.readFileSync(summaryFile, 'utf8'), /\| 100 \| 30\.0 \| 0\.300 \|[^\n]*\| within \|/);
  assert.match(fs.readFileSync(summaryFile, 'utf8'), /\| exceeded \|/);
  assert.equal(stdout.mock.callCount(), 2);
  assert.match(stderr.mock.calls[0].arguments[0], /per-run ceiling for 100 tests/);
  assert.throws(() => main([]));
});
