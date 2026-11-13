'use strict';

const fs = require('node:fs');

// Dashboard Playwright wall-time budget (#974). It scales with the
// selected test count: the median target is TARGET seconds per executed test,
// and a single run fails only above CEILING_FACTOR times that target, so hosted
// runner noise does not fail an otherwise healthy run.
const BUDGET = Object.freeze({
  schema: 1,
  target_seconds_per_test: 0.42,
  ceiling_factor: 1.25,
  workers: 2,
  retries: 0,
});

function evaluate(report, budget = BUDGET) {
  const stats = report?.stats;
  const counts = ['expected', 'unexpected', 'flaky', 'skipped'].map((key) => stats?.[key]);
  if (!counts.every((value) => Number.isSafeInteger(value) && value >= 0)) {
    throw new Error('Playwright report stats are missing or malformed.');
  }
  const [expected, unexpected, flaky] = counts;
  const seconds = stats.duration / 1000;
  if (!Number.isFinite(seconds) || seconds <= 0) throw new Error('Playwright report duration is missing or malformed.');
  const tests = expected + unexpected + flaky;
  if (tests === 0) throw new Error('Playwright report executed no tests.');
  const target = tests * budget.target_seconds_per_test;
  const ceiling = target * budget.ceiling_factor;
  return {
    tests,
    seconds,
    seconds_per_test: seconds / tests,
    target_seconds: target,
    ceiling_seconds: ceiling,
    status: seconds > ceiling ? 'exceeded' : seconds > target ? 'above-target' : 'within',
  };
}

function summary(result) {
  const fixed = (value) => value.toFixed(1);
  return [
    '### Dashboard Playwright wall-time budget',
    '',
    '| Executed tests | Runner seconds | Seconds per test | Median target | Per-run ceiling | Status |',
    '|---:|---:|---:|---:|---:|---|',
    `| ${result.tests} | ${fixed(result.seconds)} | ${result.seconds_per_test.toFixed(3)} | ${fixed(result.target_seconds)} | ${fixed(result.ceiling_seconds)} | ${result.status} |`,
    '',
  ].join('\n');
}

function main(argv = process.argv.slice(2), env = process.env) {
  if (argv.length !== 1) throw new Error('Usage: ci-evidence-dashboard-budget.cjs PLAYWRIGHT_JSON_REPORT');
  const result = evaluate(JSON.parse(fs.readFileSync(argv[0], 'utf8')));
  const text = summary(result);
  process.stdout.write(text);
  if (env.GITHUB_STEP_SUMMARY) fs.appendFileSync(env.GITHUB_STEP_SUMMARY, text);
  if (result.status === 'exceeded') {
    process.stderr.write(`Dashboard Playwright run took ${result.seconds.toFixed(1)}s, above the ${result.ceiling_seconds.toFixed(1)}s per-run ceiling for ${result.tests} tests.\n`);
    return 1;
  }
  return 0;
}

if (require.main === module) {
  try {
    process.exitCode = main();
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = { BUDGET, evaluate, summary, main };
