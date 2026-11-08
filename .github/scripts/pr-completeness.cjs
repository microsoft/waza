const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const { TextDecoder } = require('node:util');
const { parseStrictJSON } = require('../../web/src/lib/strictJson.ts');

const producerTitle = 'actual offline producer outputs retain version boundaries and operational failures';
const contracts = {
  go: {
    needs: ['lint', 'build-and-test', 'build-and-test-windows', 'native-test-shards', 'linux-gate'],
    units: {
      lint: ['checkout', 'lint_controls', 'workflow_controls', 'lint'],
      linux: ['checkout', 'verify_modules', 'download', 'node', 'guard', 'inventory', 'web_build', 'format', 'vet', 'tests', 'no_launch', 'binary', 'integration'],
      windows: ['checkout', 'verify_modules', 'download', 'node', 'guard', 'vet', 'tests', 'no_launch', 'binary', 'integration'],
      'native-0': ['checkout', 'discovery_controls', 'guard', 'tests', 'coverage'],
      'native-1': ['checkout', 'discovery_controls', 'guard', 'tests', 'coverage'],
      'native-2': ['checkout', 'discovery_controls', 'guard', 'tests', 'coverage'],
      'native-3': ['checkout', 'discovery_controls', 'guard', 'tests', 'coverage'],
      'linux-gate': ['requirements', 'checkout', 'coverage', 'inventory'],
    },
  },
  dashboard: { needs: ['dashboard'], units: { dashboard: ['checkout', 'node', 'install', 'go', 'lint', 'build', 'chromium', 'browser'] } },
  site: { needs: ['documentation'], units: { site: ['checkout', 'node', 'install', 'build', 'metadata', 'chromium', 'browser', 'chat'] } },
  examples: { needs: ['documentation'], units: { examples: ['checkout', 'build', 'examples'] } },
  assurance: {
    needs: ['assurance'],
    units: { assurance: ['checkout', 'build', 'custody', 'guard', 'qualification', 'export', 'preserved_export', 'consent', 'reader', 'preserved_reader', 'browser'] },
  },
  container: {
    needs: ['container'],
    units: { image: ['checkout', 'build', 'version'], reference: ['checkout', 'controls', 'subprocess', 'native'] },
  },
  tools: { needs: ['tools'], units: { linux: ['checkout', 'integrity', 'tests'], windows: ['checkout', 'integrity', 'tests'] } },
  runtime: { needs: ['runtime'], units: { linux: ['checkout', 'tests'], macos: ['checkout', 'tests'] } },
};
const browserCommands = { dashboard: ['browser'], site: ['browser', 'chat', 'chat_unavailable'], assurance: ['browser'] };
const runtimeInputs = Object.freeze([
  'internal/agentevent', 'internal/commandmock', 'internal/config', 'internal/copilotevents',
  'internal/embedded', 'internal/evidence', 'internal/execution', 'internal/faultsequence',
  'internal/graders', 'internal/hooks', 'internal/judgeevidence', 'internal/jsonutil',
  'internal/models', 'internal/pathadmission', 'internal/projectconfig', 'internal/requirements',
  'internal/scaffold', 'internal/schemaloader', 'internal/skill', 'internal/snapshot',
  'internal/statistics', 'internal/testutil/copilotfake', 'internal/tokens', 'internal/utils', 'schemas',
]);

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

function record(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function sha(value) {
  return typeof value === 'string' && /^[0-9a-f]{40}$/.test(value);
}

function prEventMode(event) {
  requireValue(record(event), 'Missing event');
  if (['opened', 'synchronize', 'reopened'].includes(event.action)) return 'validation';
  requireValue(event.action === 'edited' && record(event.changes), 'Unsupported or unavailable PR edit');
  const fields = Object.keys(event.changes);
  requireValue(fields.length > 0 && fields.every((field) => ['base', 'body', 'title'].includes(field)), 'Unavailable PR edit classification');
  for (const field of fields.filter((field) => field !== 'base')) {
    const change = event.changes[field];
    requireValue(record(change) && Object.keys(change).length === 1 && Object.hasOwn(change, 'from') &&
      (typeof change.from === 'string' || (field === 'body' && change.from === null)), 'Invalid metadata change');
  }
  if (Object.hasOwn(event.changes, 'base')) {
    const change = event.changes.base;
    const previousRef = change?.ref?.from;
    const currentRef = event.pull_request?.base?.ref;
    requireValue(record(change) && Object.keys(change).length === 2 &&
      record(change.ref) && Object.keys(change.ref).length === 1 &&
      record(change.sha) && Object.keys(change.sha).length === 1 && sha(change.sha.from) &&
      typeof previousRef === 'string' && previousRef.length > 0 && !/[\0\r\n]/.test(previousRef) &&
      typeof currentRef === 'string' && currentRef.length > 0 && !/[\0\r\n]/.test(currentRef) &&
      previousRef !== currentRef, 'Unavailable or invalid base-retarget evidence');
    return 'validation';
  }
  return 'metadata';
}

function prRouting(eventName, event) {
  requireValue(typeof eventName === 'string' && eventName.length > 0, 'Missing workflow event name');
  const mode = eventName === 'pull_request' ? prEventMode(event) : 'non_pr';
  return {
    mode,
    validation: mode === 'validation',
    completeness_name: mode === 'validation' ? 'PR completeness' : `PR ${mode} (not validation)`,
    lint_name: mode === 'metadata' ? 'PR metadata (Lint not selected)' : 'Lint',
    linux_name: mode === 'metadata' ? 'PR metadata (ubuntu-latest not selected)' : 'ubuntu-latest',
    windows_name: mode === 'metadata' ? 'PR metadata (windows-latest not selected)' : 'windows-latest',
    concurrency_scope: mode,
    cancel_in_progress: mode === 'validation',
  };
}

function validateSource(source) {
  requireValue(record(source), 'Missing PR source');
  const keys = ['repository', 'event', 'workflow', 'action', 'head_sha', 'base_sha', 'tested_sha', 'pr', 'run_id', 'run_attempt', 'base_ref', 'base_changed'];
  requireValue(Object.keys(source).length === keys.length && keys.every((key) => Object.hasOwn(source, key)), 'Incomplete or noncanonical PR source');
  requireValue(source.repository === 'microsoft/waza' && source.event === 'pull_request', 'Not ordinary Waza PR evidence');
  requireValue(typeof source.workflow === 'string' && source.workflow.length > 0 && !/[\0\r\n]/.test(source.workflow), 'Missing or invalid caller workflow');
  requireValue(['opened', 'synchronize', 'reopened', 'edited'].includes(source.action), 'Unsupported PR action');
  requireValue(typeof source.base_changed === 'boolean' && source.base_changed === (source.action === 'edited'), 'Metadata-only edit cannot supply validation source');
  for (const key of ['head_sha', 'base_sha', 'tested_sha']) requireValue(sha(source[key]), `Invalid source ${key}`);
  requireValue(Number.isSafeInteger(source.pr) && source.pr > 0, 'Invalid PR number');
  for (const key of ['run_id', 'run_attempt']) requireValue(typeof source[key] === 'string' && /^[1-9][0-9]*$/.test(source[key]), `Invalid source ${key}`);
  requireValue(typeof source.base_ref === 'string' && source.base_ref.length > 0 && !/[\0\r\n]/.test(source.base_ref), 'Invalid base ref');
}

function validatePaths(paths) {
  requireValue(Array.isArray(paths), 'Missing complete changed paths');
  for (const file of paths) {
    requireValue(typeof file === 'string' && file.length > 0 && !file.includes('\0') &&
      !file.includes('\\') && !file.startsWith('/') &&
      file.split('/').every((part) => part.length > 0 && part !== '.' && part !== '..'),
    'Invalid repository-relative changed path');
  }
  requireValue(new Set(paths).size === paths.length, 'Duplicate changed paths');
}

function applicability(paths) {
  validatePaths(paths);
  const matches = (...prefixes) => paths.some((file) => prefixes.some((prefix) => file === prefix || file.startsWith(`${prefix}/`)));
  const all = matches('.github/workflows', '.github/scripts/pr-completeness.cjs', '.github/scripts/pr-completeness.test.cjs',
    '.github/scripts/pr_completeness_test.go', '.github/scripts/go_ci_test.go', '.github/scripts/docker_ci_test.go',
    '.github/scripts/go_offline_env.sh', 'web/src/lib/strictJson.ts');
  const backend = matches('cmd', 'internal', 'schemas') || paths.some((file) => /^[^/]+\.go$/.test(file));
  const modules = matches('go.mod', 'go.sum');
  return {
    go: true,
    dashboard: all || backend || modules || matches('web', 'examples/runner'),
    site: all || matches('site', 'docs', 'README.md', 'CHANGELOG.md', 'version.txt', 'registry.json', 'go.mod', 'schemas', 'internal/embedded',
      'internal/models', 'internal/runneridentity/testdata', 'internal/runnerunit/testdata', 'internal/runnerterminal/testdata'),
    examples: all || backend || modules || matches('site', 'docs', 'examples/interop', 'README.md', 'registry.json', 'Makefile', 'web'),
    assurance: all || backend || modules || matches('web', 'scripts', 'docs/graders', 'examples', 'testdata/frozen-assurance-readers'),
    container: all || backend || modules || paths.some((file) => /^[^/]+\.go$/.test(file)) ||
      matches('Dockerfile', '.dockerignore', 'version.txt', 'web', 'examples/runner', 'docs/graders',
        '.github/scripts/testdata/runner-runtime', '.github/scripts/testdata/native-bundle-probe'),
    tools: all || matches('.squad', '.github/skills', '.github/agents', '.github/PR_REQUIREMENTS.md', '.gitattributes') ||
      paths.some((file) => /^\.github\/scripts\/(?:ralph|squad)[^/]*\.(?:cjs|mjs)$/.test(file)),
    runtime: all || modules || matches('.gitattributes', ...runtimeInputs),
  };
}

function createPlan(source, paths) {
  validateSource(source);
  const applicable = applicability(paths);
  return { kind: 'waza.pr-completeness', schema: 1, source: { ...source }, paths: [...paths], applicable };
}

function sameSource(actual, expected) {
  validateSource(actual);
  for (const key of Object.keys(expected)) requireValue(actual[key] === expected[key], `Source mismatch: ${key}`);
}

function validatePlan(plan) {
  requireValue(record(plan) && plan.kind === 'waza.pr-completeness' && plan.schema === 1, 'Invalid completeness plan');
  validateSource(plan.source);
  const selected = applicability(plan.paths);
  requireValue(record(plan.applicable) && Object.keys(plan.applicable).length === Object.keys(selected).length &&
    Object.entries(selected).every(([key, value]) => plan.applicable[key] === value), 'Applicability changed or incomplete');
  return selected;
}

function planOutputs(plan) {
  const selected = validatePlan(plan);
  return {
    plan_artifact: `pr-completeness-plan-${plan.source.run_id}-${plan.source.run_attempt}`,
    ...selected,
  };
}

function verifyBrowser(report, family) {
  requireValue(record(report) && Array.isArray(report.suites), 'Missing Playwright JSON suites');
  requireValue(record(report.config) && Array.isArray(report.config.projects) &&
    report.config.projects.every(record), 'Missing Playwright project configuration');
  const chromium = report.config.projects.filter((project) => project.name === 'chromium');
  requireValue(chromium.length === 1 && chromium[0].retries === 0, 'Chromium retry configuration is not exactly zero');
  requireValue(record(report.stats), 'Missing Playwright result counts');
  requireValue(report.errors === undefined || (Array.isArray(report.errors) && report.errors.length === 0), 'Playwright global errors or invalid error inventory');
  let passed = 0;
  let skipped = 0;
  let producer = 0;
  const walk = (suites) => {
    for (const suite of suites) {
      requireValue(record(suite), 'Invalid Playwright suite');
      requireValue(suite.specs === undefined || Array.isArray(suite.specs), 'Invalid Playwright spec inventory');
      for (const spec of suite.specs || []) {
        requireValue(record(spec) && Array.isArray(spec.tests) && spec.tests.length > 0, 'Missing Playwright tests');
        for (const test of spec.tests) {
          requireValue(record(test) && test.projectName === 'chromium' && Array.isArray(test.results) && test.results.length === 1, 'Missing Chromium result or retried browser case');
          const result = test.results[0];
          requireValue(record(result) && result.retry === 0, 'Invalid Chromium result or unavailable zero-retry evidence');
          const actualProducer = spec.file === 'assurance.spec.ts' && spec.title === producerTitle;
          if (test.status === 'skipped' && result.status === 'skipped' && actualProducer && family === 'dashboard') {
            skipped += 1;
          } else {
            requireValue(test.status === 'expected' && result.status === 'passed', 'Browser case failed, skipped, interrupted, or flaky');
            passed += 1;
            if (actualProducer) producer += 1;
          }
        }
      }
      if (suite.suites !== undefined) {
        requireValue(Array.isArray(suite.suites), 'Invalid nested Playwright suites');
        walk(suite.suites);
      }
    }
  };
  walk(report.suites);
  requireValue(passed > 0 && skipped <= 1, 'Empty browser selection or duplicate intentional skip');
  requireValue(report.stats.expected === passed && report.stats.skipped === skipped &&
    report.stats.unexpected === 0 && report.stats.flaky === 0, 'Playwright counts disagree with selected case results');
  if (family === 'assurance') requireValue(producer === 1, 'Actual-producer assurance browser case did not pass exactly once');
  return { passed, skipped };
}

function validateReceipt(receipt, plan, family, unit) {
  requireValue(receipt.kind === 'waza.pr-completeness-receipt' && receipt.schema === 1, 'Invalid execution receipt');
  sameSource(receipt.source, plan.source);
  requireValue(receipt.tested_sha === plan.source.tested_sha, 'Actual checkout SHA mismatch');
  requireValue(record(receipt.steps), 'Missing command outcomes');
  requireValue(receipt.steps.checkout?.outputs?.commit === plan.source.tested_sha, 'Initial checkout SHA mismatch or unavailable');
  for (const step of contracts[family].units[unit]) {
    requireValue(receipt.steps[step]?.outcome === 'success' && receipt.steps[step]?.conclusion === 'success', `Required step ${step} did not succeed`);
  }
  if (browserCommands[family]) {
    requireValue(record(receipt.browser_reports), 'Missing browser command evidence');
    for (const command of browserCommands[family]) verifyBrowser(receipt.browser_reports[command], family);
  }
}

function captureReceipt(plan, family, unit, steps, testedSHA, reports, observedSource) {
  const selected = validatePlan(plan);
  sameSource(observedSource, plan.source);
  requireValue(typeof family === 'string' && Object.hasOwn(contracts, family) &&
    typeof unit === 'string' && Object.hasOwn(contracts[family].units, unit) &&
    selected[family] === true, 'Unknown or inapplicable receipt unit');
  requireValue(record(steps), 'Missing actual command outcomes');
  const outcomes = {};
  for (const key of contracts[family].units[unit]) {
    requireValue(record(steps[key]), `Missing command ${key}`);
    outcomes[key] = { outcome: steps[key].outcome, conclusion: steps[key].conclusion };
    if (key === 'checkout') outcomes[key].outputs = { commit: steps[key].outputs?.commit };
  }
  const receipt = {
    kind: 'waza.pr-completeness-receipt', schema: 1, family, unit,
    source: { ...observedSource }, tested_sha: testedSHA, steps: outcomes,
  };
  if (browserCommands[family]) {
    const normalize = (suites) => suites.map((suite) => ({
      specs: (suite.specs || []).map((spec) => ({
        file: spec.file, title: spec.title,
        tests: spec.tests.map((test) => ({
          projectName: test.projectName, status: test.status,
          results: test.results.map((result) => ({ status: result.status, retry: result.retry })),
        })),
      })),
      suites: normalize(suite.suites || []),
    }));
    receipt.browser_reports = {};
    for (const command of browserCommands[family]) {
      const counts = verifyBrowser(reports?.[command], family);
      receipt.browser_reports[command] = {
        config: {
          projects: reports[command].config.projects.filter((project) => project.name === 'chromium')
            .map((project) => ({ name: project.name, retries: project.retries })),
        },
        suites: normalize(reports[command].suites),
        stats: { expected: counts.passed, skipped: counts.skipped, unexpected: 0, flaky: 0 },
      };
    }
  }
  validateReceipt(receipt, plan, family, unit);
  return receipt;
}

function currentPRDiagnostics(currentPR, source) {
  if (!record(currentPR)) return ['Current PR API evidence is unavailable'];
  const errors = [];
  for (const [actual, expected, label] of [
    [currentPR.number, source.pr, 'number'],
    [currentPR.state, 'open', 'state'],
    [currentPR.base?.repo?.full_name, source.repository, 'repository'],
    [currentPR.head?.sha, source.head_sha, 'head SHA'],
    [currentPR.base?.sha, source.base_sha, 'base SHA'],
    [currentPR.base?.ref, source.base_ref, 'base ref'],
  ]) {
    if (actual !== expected) errors.push(`Current PR ${label} differs from the tested event`);
  }
  return errors;
}

function evaluate(plan, needs, receipts, currentPR) {
  const selected = validatePlan(plan);
  requireValue(record(needs) && Array.isArray(receipts), 'Missing same-run dependency or receipt inventory');
  const diagnostics = currentPRDiagnostics(currentPR, plan.source);
  const scopes = {};
  const sourceAvailable = needs['pr-scope']?.result === 'success' && diagnostics.length === 0;
  if (needs['pr-scope']?.result !== 'success') diagnostics.push('Applicability/source job did not succeed');
  for (const [family, contract] of Object.entries(contracts)) {
    if (!selected[family]) {
      scopes[family] = sourceAvailable ? 'not_applicable' : 'unavailable';
      continue;
    }
    const errors = [];
    for (const dependency of contract.needs) {
      if (needs[dependency]?.result !== 'success') errors.push(`Dependency ${dependency}: ${needs[dependency]?.result || 'missing'}`);
    }
    for (const unit of Object.keys(contract.units)) {
      const found = receipts.filter((receipt) => record(receipt) && receipt.family === family && receipt.unit === unit);
      if (found.length !== 1) {
        errors.push(`${unit}: expected exactly one execution receipt, found ${found.length}`);
        continue;
      }
      try {
        validateReceipt(found[0], plan, family, unit);
      } catch (error) {
        errors.push(`${unit}: ${error.message}`);
      }
    }
    scopes[family] = errors.length === 0 && sourceAvailable ? 'validated' : 'unavailable';
    diagnostics.push(...errors.map((error) => `${family}: ${error}`));
  }
  for (const receipt of receipts) {
    if (!record(receipt) || typeof receipt.family !== 'string' || !Object.hasOwn(contracts, receipt.family) ||
      typeof receipt.unit !== 'string' || !Object.hasOwn(contracts[receipt.family].units, receipt.unit) || !selected[receipt.family]) {
      diagnostics.push('Unexpected or inapplicable execution receipt');
    }
  }
  return { kind: 'waza.pr-completeness-result', schema: 1, source: plan.source, passed: diagnostics.length === 0, scopes, diagnostics };
}

function eventSource(event, env) {
  requireValue(record(event) && record(event.pull_request), 'Missing PR event payload');
  const mode = prEventMode(event);
  requireValue(mode === 'validation', 'Metadata-only edit is not PR validation');
  const pr = event.pull_request;
  const source = {
    repository: env.GITHUB_REPOSITORY, event: env.GITHUB_EVENT_NAME, workflow: env.GITHUB_WORKFLOW, action: event.action,
    pr: event.number, head_sha: pr.head?.sha, base_sha: pr.base?.sha, base_ref: pr.base?.ref,
    tested_sha: env.GITHUB_SHA, run_id: env.GITHUB_RUN_ID, run_attempt: env.GITHUB_RUN_ATTEMPT,
    base_changed: event.action === 'edited',
  };
  validateSource(source);
  return source;
}

function discoverPlan(event, env, cwd) {
  const source = eventSource(event, env);
  const git = (...args) => execFileSync('git', args, { cwd, maxBuffer: 128 * 1024 * 1024 });
  requireValue(git('rev-parse', 'HEAD').toString().trim() === source.tested_sha, 'Checkout does not match triggering merge SHA');
  const parents = git('show', '-s', '--format=%P', source.tested_sha).toString().trim().split(' ');
  requireValue(parents.length === 2 && parents[0] === source.base_sha && parents[1] === source.head_sha, 'Merge parents do not match event base and head; fetch depth must include both parents');
  const bytes = git('diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', source.base_sha, source.tested_sha, '--');
  const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  requireValue(text === '' || text.endsWith('\0'), 'Incomplete changed-path inventory');
  return createPlan(source, text === '' ? [] : text.slice(0, -1).split('\0'));
}

function invocationPlan(plan, event, env, cwd) {
  validatePlan(plan);
  const observed = discoverPlan(event, env, cwd);
  sameSource(observed.source, plan.source);
  const actualPaths = [...observed.paths].sort();
  const expectedPaths = [...plan.paths].sort();
  requireValue(actualPaths.length === expectedPaths.length &&
    actualPaths.every((file, index) => file === expectedPaths[index]), 'Changed-path inventory does not match actual tested merge');
  return observed;
}

function main(args) {
  const readJSON = (file) => parseStrictJSON(new TextDecoder('utf-8', { fatal: true }).decode(fs.readFileSync(file)));
  if (args[0] === 'route' && args.length === 1) {
    return prRouting(process.env.GITHUB_EVENT_NAME, readJSON(process.env.GITHUB_EVENT_PATH));
  }
  if (args[0] === 'plan' && args.length === 1) {
    return discoverPlan(readJSON(process.env.GITHUB_EVENT_PATH), process.env, process.cwd());
  }
  if (args[0] === 'select' && args.length === 3) {
    requireValue(Object.hasOwn(contracts, args[2]), 'Unknown validation family');
    const observed = invocationPlan(readJSON(args[1]), readJSON(process.env.GITHUB_EVENT_PATH), process.env, process.cwd());
    return { family: args[2], applicable: observed.applicable[args[2]], source: observed.source };
  }
  if (args[0] === 'outputs' && args.length === 2) {
    const observed = invocationPlan(readJSON(args[1]), readJSON(process.env.GITHUB_EVENT_PATH), process.env, process.cwd());
    return planOutputs(observed);
  }
  if (args[0] === 'receipt' && [5, 6].includes(args.length)) {
    const plan = readJSON(args[1]);
    const observed = invocationPlan(plan, readJSON(process.env.GITHUB_EVENT_PATH), process.env, process.cwd());
    return captureReceipt(plan, args[2], args[3], readJSON(args[4]), observed.source.tested_sha, args[5] === undefined ? undefined : readJSON(args[5]), observed.source);
  }
  requireValue(args[0] === 'evaluate' && args.length === 2, 'Usage: pr-completeness.cjs route | plan | outputs PLAN.json | select PLAN.json FAMILY | receipt PLAN.json FAMILY UNIT STEPS.json [BROWSER.json] | evaluate INPUT.json');
  const input = readJSON(args[1]);
  return evaluate(input.plan, input.needs, input.receipts, input.current_pr);
}

if (require.main === module) {
  try {
    const result = main(process.argv.slice(2));
    process.stdout.write(`${JSON.stringify(result)}\n`);
    if (result.passed === false) process.exitCode = 1;
  } catch (error) {
    process.stderr.write(`PR completeness: ${error.message}\n`);
    process.exitCode = 1;
  }
}

module.exports = { applicability, captureReceipt, contracts, createPlan, discoverPlan, evaluate, eventSource, planOutputs, prEventMode, prRouting, producerTitle, runtimeInputs, verifyBrowser };
