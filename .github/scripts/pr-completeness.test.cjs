const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawnSync } = require('node:child_process');
const test = require('node:test');
const { applicability, captureReceipt, contracts, createPlan, evaluate, planOutputs, prEventMode, prRouting, producerTitle, runtimeInputs, verifyBrowser } = require('./pr-completeness.cjs');

const cli = path.join(__dirname, 'pr-completeness.cjs');
const consumedLeafInputs = [
  ['examples/runner/container/MAINTAINERS.md', 'site', true],
  ['examples/runner/container/published-image-test.mjs', 'site', true],
  ['examples/runner/unit.json', 'examples'],
  ['.gitattributes', 'assurance'],
  ['.gitattributes', 'container'],
  ...[
    'Makefile', 'web/playwright.config.ts', 'web/package-lock.json', 'web/e2e/runner-negotiation.spec.ts', 'cmd/waza/cmd_serve.go',
    '.squad/model-policy.md', 'DEMO-SCRIPT.md', 'LICENSE', 'meet-the-squad.md',
    'internal/interop/testdata/agentv/PROVENANCE.md', 'internal/runnercompat/testdata/eval-1.4.yaml',
    'internal/runnercompat/testdata/task.yaml',
  ].map((file) => [file, 'site']),
  ...[
    'examples/ci/basic-example.yml', 'examples/ci/eval-on-pr.yml', 'examples/ci/README.md',
    'examples/compatibility/README.md', 'examples/evidence-judge/eval.yaml',
    'examples/grader-challenges/authored-output/README.md', 'examples/grader-challenges/calibration/README.md',
    'examples/grader-challenges/cli-target/README.md', 'examples/interop/run.sh',
    'examples/README.md', 'examples/scenarios/README.md',
  ].map((file) => [file, 'site', true]),
];
const siteDirectoryWitnesses = [
  ['examples/README.md', 'examples'],
  ['examples/controlled-comparison/eval.yaml', 'examples/controlled-comparison'],
  ['examples/scenarios/native-questions/eval.yaml', 'examples/scenarios/native-questions'],
];
const source = {
  repository: 'microsoft/waza', event: 'pull_request', action: 'synchronize', pr: 900,
  workflow: 'Go Build and Test',
  head_sha: 'a'.repeat(40), base_sha: 'b'.repeat(40), tested_sha: 'c'.repeat(40),
  base_ref: 'feature/stack-parent', run_id: '123', run_attempt: '1',
  base_changed: false,
};

function retargetChanges(previousRef = 'feature/old-parent', previousSHA = source.base_sha) {
  return { base: { ref: { from: previousRef }, sha: { from: previousSHA } } };
}

function browser(title = 'a real selected assertion', status = 'passed', file = 'feature.spec.ts') {
  return {
    config: { projects: [{ name: 'chromium', retries: 0 }] },
    stats: { expected: status === 'passed' ? 1 : 0, skipped: status === 'skipped' ? 1 : 0, unexpected: !['passed', 'skipped'].includes(status) ? 1 : 0, flaky: 0 },
    suites: [{ specs: [{ title, file, tests: [{ projectName: 'chromium', status: status === 'passed' ? 'expected' : status === 'skipped' ? 'skipped' : 'unexpected', results: [{ status, retry: 0 }] }] }] }],
  };
}

function fixture(paths = ['internal/models/outcome.go', '.squad/routing.md'], observedSource = source) {
  const plan = createPlan(observedSource, paths);
  const needs = { 'pr-scope': { result: 'success' } };
  const receipts = [];
  for (const [family, contract] of Object.entries(contracts)) {
    if (!plan.applicable[family]) continue;
    for (const dependency of contract.needs) needs[dependency] = { result: 'success' };
    for (const [unit, steps] of Object.entries(contract.units)) {
      const receipt = {
        kind: 'waza.pr-completeness-receipt', schema: 1, family, unit,
        source: { ...observedSource }, tested_sha: observedSource.tested_sha,
        steps: Object.fromEntries(steps.map((step) => [step, { outcome: 'success', conclusion: 'success' }])),
      };
      if (receipt.steps.checkout) receipt.steps.checkout.outputs = { commit: observedSource.tested_sha };
      if (family === 'dashboard') receipt.browser_reports = { browser: browser() };
      if (family === 'site') receipt.browser_reports = { browser: browser(), chat: browser(), chat_unavailable: browser() };
      if (family === 'assurance') receipt.browser_reports = { browser: browser(producerTitle, 'passed', 'assurance.spec.ts') };
      receipts.push(receipt);
    }
  }
  const current_pr = {
    number: observedSource.pr, state: 'open', head: { sha: observedSource.head_sha },
    base: { sha: observedSource.base_sha, ref: observedSource.base_ref, repo: { full_name: observedSource.repository } },
  };
  return { plan, needs, receipts, current_pr };
}

function scratch(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'waza-completeness-control-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return directory;
}

test('actual API/model/schema and container paths select production consumers; unrelated prose does not', () => {
  for (const [file, family, nearMiss = false] of consumedLeafInputs) {
    assert.equal(fs.existsSync(path.resolve(__dirname, '../..', file)), true, file);
    assert.equal(applicability([file])[family], true, file);
    assert.equal(applicability([`${file}-near-miss`])[family], nearMiss, file);
  }
  for (const [file, directory] of siteDirectoryWitnesses) {
    assert.equal(fs.existsSync(path.resolve(__dirname, '../..', file)), true, file);
    assert.equal(fs.statSync(path.resolve(__dirname, '../..', directory)).isDirectory(), true, directory);
    assert.equal(applicability([file]).site, true, file);
    assert.equal(applicability([`${directory}-near-miss/${path.basename(file)}`]).site, directory !== 'examples', directory);
  }
  assert.equal(applicability(['AGENTS.md']).site, false, 'instruction-only exemption remains an explicit coverage exception');
  for (const file of ['internal/webapi/handlers.go', 'internal/webapi/store.go', 'internal/models/outcome.go', 'schemas/eval.schema.json', 'cmd/waza/cmd_run.go', 'docs.go']) {
    assert.equal(fs.existsSync(path.resolve(__dirname, '../..', file)), true, file);
    const selected = applicability([file]);
    assert.equal(selected.dashboard, true, file);
    assert.equal(selected.assurance, true, file);
    assert.equal(selected.examples, true, file);
    assert.equal(selected.container, true, file);
    assert.equal(selected.go, true, file);
  }
  for (const file of ['.github/workflows/web-ci.yml', '.github/scripts/pr-completeness.cjs', 'web/src/lib/strictJson.ts',
    '.github/scripts/go_ci_test.go', '.github/scripts/docker_ci_test.go', '.github/scripts/pr_completeness_test.go',
    '.github/scripts/go_offline_env.sh']) {
    assert.equal(Object.values(applicability([file])).every(Boolean), true, file);
  }
  assert.equal(applicability(['.squad/routing.md']).tools, true);
  for (const file of ['.github/agents/squad.agent.md', '.github/PR_REQUIREMENTS.md', '.gitattributes']) {
    assert.equal(fs.existsSync(path.resolve(__dirname, '../..', file)), true, file);
    assert.equal(applicability([file]).tools, true, file);
  }
  const frozenReader = 'testdata/frozen-assurance-readers/manifest.json';
  assert.equal(fs.existsSync(path.resolve(__dirname, '../..', frozenReader)), true);
  assert.equal(applicability([frozenReader]).assurance, true);
  assert.equal(applicability(['testdata/frozen-assurance-readers-near-miss/manifest.json']).assurance, false);
  assert.equal(applicability(['internal/embedded/path.go']).runtime, true);
  assert.equal(applicability(['.dockerignore']).container, true);
  assert.equal(applicability(['examples/runner/container/Dockerfile']).container, true);
  assert.deepEqual(applicability(['AGENTS.md']), {
    go: true, dashboard: false, site: false, examples: false, assurance: false, container: false, tools: false, runtime: false,
  });
  for (const file of ['docs/CI-CD-GUIDE.md', 'site/src/content/docs/guides/ci-cd.mdx']) {
    assert.equal(applicability([file]).site, true);
    assert.equal(applicability([file]).dashboard, false);
  }
});

test('runtime package and reader/schema helpers select the proof, while adjacent unrelated prefixes do not', () => {
  for (const directory of runtimeInputs) {
    const root = path.resolve(__dirname, '../..', directory);
    const file = fs.readdirSync(root).sort().find((file) => file.endsWith('.go') && !file.endsWith('_test.go'));
    assert.ok(file, `${directory} must have an actual repository Go input`);
    assert.equal(applicability([`${directory}/${file}`]).runtime, true, directory);
    assert.equal(applicability([`${directory}-near-miss/${file}`]).runtime, false, directory);
  }
  assert.equal(Object.isFrozen(runtimeInputs), true);
  assert.equal(applicability(['internal/rootedfile/open_unix.go']).runtime, false, 'not imported by the A69 pinned test binary');
});

test('site compound chat outcome covers all three commands without inventing separate step outcomes', () => {
  const workflow = fs.readFileSync(path.resolve(__dirname, '../..', '.github/workflows/site-ci.yml'), 'utf8');
  assert.match(workflow, /run: cd site && npm run test:private-chat && npm run test:chat -- --project=chromium --retries=0 && npm run test:chat-unavailable -- --project=chromium --retries=0/);
  assert.equal(contracts.site.units.site.includes('chat'), true);
  assert.equal(contracts.site.units.site.includes('private_chat'), false);
  assert.equal(contracts.site.units.site.includes('chat_unavailable'), false);
  const control = fixture(['site/package.json']);
  const site = control.receipts.find((receipt) => receipt.family === 'site');
  assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, true);
  site.steps.chat.outcome = 'failure';
  assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, false);
  site.steps.chat.outcome = 'success';
  site.browser_reports.chat_unavailable = browser('unavailable-service assertion', 'failed');
  assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, false);
});

test('body-only edits have distinct checks/concurrency; real validation retains legacy contexts', () => {
  const real = prRouting('pull_request', { action: 'synchronize' });
  assert.deepEqual([real.lint_name, real.linux_name, real.windows_name, real.completeness_name],
    ['Lint', 'ubuntu-latest', 'windows-latest', 'PR completeness']);
  assert.equal(real.cancel_in_progress, true);
  for (const changes of [{ body: { from: null } }, { body: { from: '' } }, { title: { from: 'old title' } }]) {
    const metadata = prRouting('pull_request', { action: 'edited', changes });
    assert.equal(metadata.validation, false);
    assert.equal(metadata.cancel_in_progress, false);
    assert.notEqual(metadata.concurrency_scope, real.concurrency_scope);
    for (const key of ['lint_name', 'linux_name', 'windows_name', 'completeness_name']) assert.notEqual(metadata[key], real[key]);
  }
  for (const event of ['workflow_dispatch', 'push', 'workflow_call']) {
    const manual = prRouting(event, {});
    assert.notEqual(manual.completeness_name, 'PR completeness');
    assert.equal(manual.validation, false);
    assert.equal(manual.lint_name, 'Lint');
  }
  const baseEdit = prRouting('pull_request', {
    action: 'edited', changes: retargetChanges(),
    pull_request: { base: { ref: 'feature/stack-parent' } },
  });
  assert.deepEqual(baseEdit, real);
  for (const event of [{ action: 'edited' }, { action: 'edited', changes: {} }, { action: 'edited', changes: { base: null } },
    { action: 'edited', changes: { body: {} } }, { action: 'edited', changes: { title: { from: false } } },
    { action: 'edited', changes: { base: { unknown: true } } },
    { action: 'edited', changes: { base: { ref: { from: false } } }, pull_request: { base: { ref: 'main' } } },
    { action: 'edited', changes: { base: { ref: { from: 'main' } } }, pull_request: { base: { ref: 'main' } } }]) {
    assert.throws(() => prEventMode(event));
  }
  assert.throws(() => createPlan({ ...source, action: 'edited' }, []), /Metadata-only/);
  assert.throws(() => createPlan({ ...source, base_changed: null }, []));
});

test('pinned edited-event contract requires old ref and SHA, but same-SHA different-ref retargets remain valid', () => {
  // octokit/webhooks aa80c24: pull_request/edited.schema.json requires both ref.from and sha.from.
  const event = {
    action: 'edited', changes: retargetChanges(),
    pull_request: { base: { ref: source.base_ref, sha: source.base_sha } },
  };
  assert.equal(event.changes.base.sha.from, event.pull_request.base.sha);
  assert.equal(prEventMode(event), 'validation');
  for (const mutate of [
    (event) => { delete event.changes.base.sha; },
    (event) => { delete event.changes.base.sha.from; },
    (event) => { event.changes.base.sha.from = false; },
    (event) => { event.changes.base.sha.from = 'not-a-sha'; },
    (event) => { event.changes.base.extra = true; },
    (event) => { event.changes.base.ref.extra = true; },
    (event) => { event.changes.body = { from: false }; },
    (event) => { event.changes.unknown = { from: 'old' }; },
  ]) {
    const changed = structuredClone(event);
    mutate(changed);
    assert.notDeepEqual(changed, event);
    assert.throws(() => prEventMode(changed));
  }
  const combined = structuredClone(event);
  combined.changes.body = { from: null };
  combined.changes.title = { from: 'old title' };
  assert.equal(prEventMode(combined), 'validation');
});

test('unavailable applicability never becomes an empty/not-applicable plan', () => {
  for (const paths of [undefined, null, '', [''], ['/tmp/x'], ['../x'], ['a/../x'], ['a\\b'], ['a', 'a']]) {
    assert.throws(() => createPlan(source, paths));
  }
  const good = fixture();
  const before = structuredClone(good);
  assert.equal(evaluate(good.plan, good.needs, good.receipts, good.current_pr).passed, true);
  assert.deepEqual(good, before);
  good.plan.applicable.dashboard = false;
  assert.throws(() => evaluate(good.plan, good.needs, good.receipts, good.current_pr), /Applicability changed/);
});

test('large complete manifests stay in artifacts, while routing outputs remain bounded and attempt-scoped', () => {
  const paths = Array.from({ length: 80000 }, (_, index) => `docs/changed-page-${index}.md`);
  const plan = createPlan(source, paths);
  assert.ok(Buffer.byteLength(JSON.stringify(plan)) > 1024 * 1024);
  const outputs = planOutputs(plan);
  assert.ok(Buffer.byteLength(JSON.stringify(outputs)) < 1024);
  assert.equal(outputs.plan_artifact, 'pr-completeness-plan-123-1');
  assert.equal(outputs.site, true);
  assert.equal(outputs.dashboard, false);
  assert.equal(Object.hasOwn(outputs, 'paths'), false);
  assert.deepEqual(plan.paths, paths);
  assert.notEqual(planOutputs(createPlan({ ...source, run_attempt: '2' }, paths)).plan_artifact, outputs.plan_artifact);
  plan.applicable.site = false;
  assert.throws(() => planOutputs(plan), /Applicability changed/);
});

test('every applicable dependency fails closed for failure, cancellation, skip, absence and unknown', () => {
  const baseline = fixture();
  assert.equal(evaluate(baseline.plan, baseline.needs, baseline.receipts, baseline.current_pr).passed, true);
  for (const dependency of Object.keys(baseline.needs)) {
    for (const state of ['failure', 'cancelled', 'skipped', 'pending', undefined, null, false]) {
      const control = structuredClone(baseline);
      if (state === undefined) delete control.needs[dependency];
      else control.needs[dependency] = { result: state };
      const result = evaluate(control.plan, control.needs, control.receipts, control.current_pr);
      assert.equal(result.passed, false, `${dependency}/${state}`);
      assert.ok(result.diagnostics.length > 0);
    }
  }
});

test('nested skips and tolerated failures cannot hide behind successful called jobs', () => {
  const baseline = fixture();
  for (let index = 0; index < baseline.receipts.length; index += 1) {
    for (const step of Object.keys(baseline.receipts[index].steps)) {
      for (const outcome of ['skipped', 'failure', 'cancelled', undefined]) {
        const control = structuredClone(baseline);
        if (outcome === undefined) delete control.receipts[index].steps[step];
        else control.receipts[index].steps[step].outcome = outcome;
        assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, false, `${index}/${step}/${outcome}`);
      }
    }
  }
});

test('missing matrix members, duplicates and mismatched source/attempt/base/actual checkout are rejected', () => {
  const baseline = fixture();
  for (let index = 0; index < baseline.receipts.length; index += 1) {
    const missing = structuredClone(baseline);
    missing.receipts.splice(index, 1);
    assert.equal(evaluate(missing.plan, missing.needs, missing.receipts, missing.current_pr).passed, false);
  }
  for (const key of ['head_sha', 'base_sha', 'tested_sha', 'base_ref', 'run_id', 'run_attempt', 'event', 'workflow']) {
    const control = structuredClone(baseline);
    control.receipts[0].source[key] = key.endsWith('_sha') ? 'd'.repeat(40) : 'different';
    assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, false, key);
  }
  const duplicate = structuredClone(baseline);
  duplicate.receipts.push(duplicate.receipts[0]);
  assert.equal(evaluate(duplicate.plan, duplicate.needs, duplicate.receipts, duplicate.current_pr).passed, false);
  const checkout = structuredClone(baseline);
  checkout.receipts[0].tested_sha = 'd'.repeat(40);
  assert.equal(evaluate(checkout.plan, checkout.needs, checkout.receipts, checkout.current_pr).passed, false);
  const initial = structuredClone(baseline);
  initial.receipts[0].steps.checkout.outputs.commit = 'd'.repeat(40);
  assert.equal(evaluate(initial.plan, initial.needs, initial.receipts, initial.current_pr).passed, false);
});

test('intentional non-applicability is visible, not missing-as-success', () => {
  const control = fixture(['AGENTS.md']);
  const result = evaluate(control.plan, control.needs, control.receipts, control.current_pr);
  assert.equal(result.passed, true);
  assert.equal(result.scopes.go, 'validated');
  assert.equal(result.scopes.dashboard, 'not_applicable');
  assert.equal(control.needs.dashboard, undefined);
  const noSource = structuredClone(control);
  delete noSource.needs['pr-scope'];
  const unavailable = evaluate(noSource.plan, noSource.needs, noSource.receipts, noSource.current_pr);
  assert.equal(unavailable.passed, false);
  assert.equal(Object.values(unavailable.scopes).every((scope) => scope === 'unavailable'), true);
  control.receipts.push({ family: 'dashboard', unit: 'dashboard' });
  assert.equal(evaluate(control.plan, control.needs, control.receipts, control.current_pr).passed, false);
  for (const family of ['constructor', '__proto__', 'unknown']) {
    const unexpected = fixture(['AGENTS.md']);
    unexpected.receipts.push({ family, unit: 'constructor' });
    assert.equal(evaluate(unexpected.plan, unexpected.needs, unexpected.receipts, unexpected.current_pr).passed, false);
  }
});

test('final admission rejects a moved PR head/base, closure, missing API evidence and simultaneous mismatches', () => {
  const baseline = fixture(['AGENTS.md']);
  assert.equal(evaluate(baseline.plan, baseline.needs, baseline.receipts, baseline.current_pr).passed, true);
  for (const mutate of [
    (pr) => { pr.number = 901; },
    (pr) => { pr.state = 'closed'; },
    (pr) => { pr.base.repo.full_name = 'other/repository'; },
    (pr) => { pr.head.sha = 'd'.repeat(40); },
    (pr) => { pr.base.sha = 'e'.repeat(40); },
    (pr) => { pr.base.ref = 'feature/new-parent'; },
    (pr) => { delete pr.base.sha; },
  ]) {
    const current = structuredClone(baseline.current_pr);
    mutate(current);
    assert.notDeepEqual(current, baseline.current_pr);
    const result = evaluate(baseline.plan, baseline.needs, baseline.receipts, current);
    assert.equal(result.passed, false);
    assert.equal(Object.values(result.scopes).every((scope) => scope === 'unavailable'), true);
    assert.equal(result.diagnostics.length, 1);
  }
  for (const missing of [undefined, null, false, {}]) {
    assert.equal(evaluate(baseline.plan, baseline.needs, baseline.receipts, missing).passed, false);
  }
  const multiple = structuredClone(baseline.current_pr);
  multiple.head.sha = 'd'.repeat(40);
  multiple.base.ref = 'feature/new-parent';
  assert.equal(evaluate(baseline.plan, baseline.needs, baseline.receipts, multiple).diagnostics.length, 2);
  baseline.needs.lint.result = 'failure';
  assert.ok(evaluate(baseline.plan, baseline.needs, baseline.receipts, multiple).diagnostics.length > 2);
});

test('receipt capture strips unrelated outputs and browser logs rather than retaining secrets', () => {
  const control = fixture();
  const candidate = control.receipts.find((receipt) => receipt.family === 'dashboard');
  const steps = { ...candidate.steps, other: { outputs: { sensitive: 'must-not-retain' } } };
  steps.checkout.outputs.unrelated = 'must-not-retain';
  const reports = structuredClone(candidate.browser_reports);
  reports.browser.config.metadata = { private: 'must-not-retain' };
  reports.browser.suites[0].specs[0].tests[0].results[0].stdout = [{ text: 'must-not-retain' }];
  const before = structuredClone({ steps, reports });
  const receipt = captureReceipt(control.plan, 'dashboard', 'dashboard', steps, source.tested_sha, reports, source);
  assert.equal(JSON.stringify(receipt).includes('must-not-retain'), false);
  assert.deepEqual({ steps, reports }, before);
  assert.throws(() => captureReceipt(control.plan, 'unknown', 'dashboard', steps, source.tested_sha, reports, source));
  assert.throws(() => captureReceipt(control.plan, 'dashboard', 'dashboard', steps, 'd'.repeat(40), reports, source));
  assert.throws(() => captureReceipt(control.plan, 'dashboard', 'dashboard', steps, source.tested_sha, reports, { ...source, workflow: 'Independent observation' }), /Source mismatch: workflow/);
  assert.throws(() => captureReceipt(control.plan, 'dashboard', 'dashboard', steps, source.tested_sha, reports, { ...source, run_attempt: '2' }), /Source mismatch: run_attempt/);
  delete steps.browser;
  assert.throws(() => captureReceipt(control.plan, 'dashboard', 'dashboard', steps, source.tested_sha, reports, source));

  const windows = control.receipts.find((receipt) => receipt.family === 'go' && receipt.unit === 'windows');
  const windowsSteps = structuredClone(windows.steps);
  assert.equal(captureReceipt(control.plan, 'go', 'windows', windowsSteps, source.tested_sha, undefined, source)
    .steps.shell_controls.outcome, 'success');
  delete windowsSteps.shell_controls;
  assert.throws(() => captureReceipt(control.plan, 'go', 'windows', windowsSteps, source.tested_sha, undefined, source),
    /Missing command shell_controls/);
  windowsSteps.shell_controls = { outcome: 'skipped', conclusion: 'skipped' };
  assert.throws(() => captureReceipt(control.plan, 'go', 'windows', windowsSteps, source.tested_sha, undefined, source),
    /Required step shell_controls did not succeed/);
});

test('browser discovery rejects empty, retried, failed, near-miss and unexpected skip evidence', () => {
  const producer = browser(producerTitle, 'passed', 'assurance.spec.ts');
  assert.equal(fs.readFileSync(path.resolve(__dirname, '../../web/e2e/assurance.spec.ts'), 'utf8').includes(`test("${producerTitle}",`), true);
  assert.deepEqual(verifyBrowser(producer, 'assurance'), { passed: 1, skipped: 0 });
  assert.throws(() => verifyBrowser({ ...browser(), suites: [], stats: { expected: 0, skipped: 0, unexpected: 0, flaky: 0 } }, 'dashboard'), /Empty browser selection/);
  assert.throws(() => verifyBrowser(browser(`${producerTitle} extra`, 'passed', 'assurance.spec.ts'), 'assurance'));
  assert.throws(() => verifyBrowser(browser(producerTitle, 'passed', 'other/assurance.spec.ts'), 'assurance'));
  for (const status of ['failed', 'skipped', 'interrupted', 'timedOut']) assert.throws(() => verifyBrowser(browser('ordinary', status), 'dashboard'));
  const retried = browser();
  retried.suites[0].specs[0].tests[0].results.push({ status: 'passed' });
  assert.throws(() => verifyBrowser(retried, 'dashboard'));
  const ordinary = browser();
  ordinary.suites[0].specs.push(...browser(producerTitle, 'skipped', 'assurance.spec.ts').suites[0].specs);
  ordinary.stats.skipped = 1;
  assert.deepEqual(verifyBrowser(ordinary, 'dashboard'), { passed: 1, skipped: 1 });
  assert.throws(() => verifyBrowser(ordinary, 'assurance'));
  ordinary.suites[0].specs.push(ordinary.suites[0].specs[1]);
  assert.throws(() => verifyBrowser(ordinary, 'dashboard'));
  for (const mutate of [
    (report) => { report.stats.expected = 2; },
    (report) => { report.stats.flaky = 1; },
    (report) => { report.errors = [{ message: 'after-all failure' }]; },
    (report) => { report.suites.push({ specs: null }); },
    (report) => { delete report.stats; },
    (report) => { report.suites[0].specs[0].tests[0].results[0].retry = 1; },
    (report) => { delete report.suites[0].specs[0].tests[0].results[0].retry; },
    (report) => { delete report.config; },
    (report) => { report.config.projects[0].retries = 1; },
    (report) => { report.config.projects[0].retries = false; },
    (report) => { report.config.projects.push({ name: 'chromium', retries: 0 }); },
  ]) {
    const malformed = browser();
    mutate(malformed);
    assert.throws(() => verifyBrowser(malformed, 'dashboard'));
  }
});

test('production evaluate CLI rejects ambiguous JSON before source admission', (t) => {
  const directory = scratch(t);
  const input = path.join(directory, 'input.json');
  const control = fixture(['AGENTS.md']);
  const invoke = () => spawnSync(process.execPath, [cli, 'evaluate', input], { encoding: 'utf8' });
  for (const bytes of [
    Buffer.from(JSON.stringify(control).replace('"schema":1', '"schema":1,"schema":1')),
    Buffer.from(JSON.stringify(control).replace('"pr":900', '"pr":900.000000000000000001')),
    Buffer.concat([Buffer.from('{"plan":"'), Buffer.from([0xff]), Buffer.from('"}')]),
  ]) {
    fs.writeFileSync(input, bytes);
    const result = invoke();
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /PR completeness:/);
  }
});

test('production Git discovery covers renamed/deleted inputs and stacked bases; missing source fails closed', (t) => {
  const directory = scratch(t);
  const git = (...args) => execFileSync('git', args, { cwd: directory, encoding: 'utf8' }).trim();
  git('init', '-q');
  git('config', 'user.name', 'Offline Control');
  git('config', 'user.email', 'offline-control@example.invalid');
  git('config', 'commit.gpgsign', 'false');
  fs.mkdirSync(path.join(directory, 'internal/webapi'), { recursive: true });
  fs.writeFileSync(path.join(directory, 'internal/webapi/handlers.go'), 'old API input\n');
  fs.mkdirSync(path.join(directory, 'internal/schemaloader'), { recursive: true });
  const helperPath = 'internal/schemaloader/runtime-input.go';
  fs.writeFileSync(path.join(directory, helperPath), 'baseline helper input\n');
  fs.writeFileSync(path.join(directory, 'baseline'), 'base\n');
  git('add', '.');
  git('commit', '-qm', 'control base');
  const initialMain = git('rev-parse', 'HEAD');
  const branch = 'feature/stack-parent';
  git('checkout', '-qb', branch);
  fs.writeFileSync(path.join(directory, 'parent-only'), 'must not enter child changed-path scope\n');
  git('add', '.');
  git('commit', '-qm', 'control stacked parent');
  const base = git('rev-parse', 'HEAD');
  assert.notEqual(base, initialMain);
  git('checkout', '-qb', 'feature/stack-child');
  fs.renameSync(path.join(directory, 'internal/webapi/handlers.go'), path.join(directory, 'renamed-input'));
  fs.writeFileSync(path.join(directory, helperPath), 'changed helper input\n');
  const literalPath = process.platform === 'win32' ? 'portable-input' : 'a\nnewline-input';
  fs.writeFileSync(path.join(directory, literalPath), 'name control\n');
  git('add', '.');
  git('commit', '-qm', 'control head');
  const head = git('rev-parse', 'HEAD');
  git('checkout', '-q', branch);
  git('merge', '--no-ff', '-qm', 'control tested merge', head);
  const tested = git('rev-parse', 'HEAD');
  const eventPath = path.join(directory, 'event.json');
  const event = { action: 'synchronize', number: 900, pull_request: { head: { sha: head }, base: { sha: base, ref: 'feature/stack-parent' } } };
  fs.writeFileSync(eventPath, JSON.stringify(event));
  const env = {
    ...process.env, GITHUB_EVENT_PATH: eventPath, GITHUB_REPOSITORY: 'microsoft/waza',
    GITHUB_EVENT_NAME: 'pull_request', GITHUB_WORKFLOW: 'Go Build and Test', GITHUB_SHA: tested, GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '1',
  };
  const invoke = () => spawnSync(process.execPath, [cli, 'plan'], { cwd: directory, env, encoding: 'utf8' });
  const good = invoke();
  assert.equal(good.status, 0, good.stderr);
  const plan = JSON.parse(good.stdout);
  assert.deepEqual(plan.paths, [literalPath, helperPath, 'internal/webapi/handlers.go', 'renamed-input'].sort());
  assert.equal(plan.applicable.dashboard, true);
  assert.equal(plan.applicable.runtime, true);
  assert.equal(createPlan(plan.source, plan.paths.filter((file) => file !== helperPath)).applicable.runtime, false,
    'the changed transitive helper must actually cause runtime selection');
  assert.equal(plan.source.base_ref, 'feature/stack-parent');
  assert.equal(plan.paths.includes('parent-only'), false);
  const planPath = path.join(directory, 'plan.json');
  const stepsPath = path.join(directory, 'steps.json');
  const evaluationPath = path.join(directory, 'evaluation.json');
  const evaluation = fixture(plan.paths, plan.source);
  fs.writeFileSync(evaluationPath, JSON.stringify(evaluation));
  const evaluateCLI = (extra = {}) => spawnSync(process.execPath, [cli, 'evaluate', evaluationPath], {
    cwd: directory, env: { ...env, ...extra }, encoding: 'utf8',
  });
  const evaluated = evaluateCLI();
  assert.equal(evaluated.status, 0, evaluated.stderr);
  assert.equal(JSON.parse(evaluated.stdout).passed, true);
  const reorderedEvaluation = structuredClone(evaluation);
  reorderedEvaluation.plan.paths.reverse();
  fs.writeFileSync(evaluationPath, JSON.stringify(reorderedEvaluation));
  const reorderedResult = evaluateCLI();
  assert.equal(reorderedResult.status, 0, reorderedResult.stderr);
  assert.equal(JSON.parse(reorderedResult.stdout).passed, true);
  fs.writeFileSync(evaluationPath, JSON.stringify(evaluation));
  for (const extra of [
    { GITHUB_EVENT_NAME: 'push', GITHUB_RUN_ID: '999', GITHUB_RUN_ATTEMPT: '2', GITHUB_SHA: head },
    { GITHUB_EVENT_NAME: 'push' }, { GITHUB_WORKFLOW: 'Unrelated workflow' },
    { GITHUB_RUN_ID: '999' }, { GITHUB_RUN_ATTEMPT: '2' }, { GITHUB_SHA: head },
    { GITHUB_REPOSITORY: 'unrelated/repository' },
    { GITHUB_EVENT_PATH: path.join(directory, 'missing-event.json') },
  ]) {
    const replayed = evaluateCLI(extra);
    assert.equal(replayed.status, 1);
    assert.equal(replayed.stdout, '');
    assert.match(replayed.stderr, /PR completeness:/);
  }
  const omittedEvaluation = fixture(plan.paths.filter((file) => file !== helperPath), plan.source);
  fs.writeFileSync(evaluationPath, JSON.stringify(omittedEvaluation));
  const omittedEvaluationInput = evaluateCLI();
  assert.equal(omittedEvaluationInput.status, 1);
  assert.equal(omittedEvaluationInput.stdout, '');
  assert.match(omittedEvaluationInput.stderr, /Changed-path inventory does not match actual tested merge/);
  for (const mutate of [
    (input) => { delete input.current_pr; },
    (input) => { input.needs.lint.result = 'skipped'; },
  ]) {
    const unavailableInput = structuredClone(evaluation);
    mutate(unavailableInput);
    fs.writeFileSync(evaluationPath, JSON.stringify(unavailableInput));
    const unavailable = evaluateCLI();
    assert.equal(unavailable.status, 1, unavailable.stderr);
    const result = JSON.parse(unavailable.stdout);
    assert.equal(result.passed, false);
    if (!unavailableInput.current_pr) {
      assert.equal(Object.values(result.scopes).every((scope) => scope === 'unavailable'), true);
    }
  }
  fs.writeFileSync(evaluationPath, JSON.stringify(evaluation));
  fs.writeFileSync(planPath, JSON.stringify(plan));
  const outputs = spawnSync(process.execPath, [cli, 'outputs', planPath], { cwd: directory, env, encoding: 'utf8' });
  assert.equal(outputs.status, 0, outputs.stderr);
  assert.deepEqual(JSON.parse(outputs.stdout), planOutputs(plan));
  const steps = Object.fromEntries(contracts.go.units.lint.map((step) => [step, { outcome: 'success', conclusion: 'success' }]));
  steps.checkout.outputs = { commit: tested };
  fs.writeFileSync(stepsPath, JSON.stringify(steps));
  const capture = (extra = {}) => spawnSync(process.execPath, [cli, 'receipt', planPath, 'go', 'lint', stepsPath], { cwd: directory, env: { ...env, ...extra }, encoding: 'utf8' });
  const captured = capture();
  assert.equal(captured.status, 0, captured.stderr);
  assert.equal(JSON.parse(captured.stdout).tested_sha, tested);
  const select = (family) => spawnSync(process.execPath, [cli, 'select', planPath, family], { cwd: directory, env, encoding: 'utf8' });
  assert.deepEqual(JSON.parse(select('dashboard').stdout), { family: 'dashboard', applicable: true, source: plan.source });
  assert.deepEqual(JSON.parse(select('runtime').stdout), { family: 'runtime', applicable: true, source: plan.source });
  fs.writeFileSync(planPath, JSON.stringify(createPlan(plan.source, plan.paths.filter((file) => file !== helperPath))));
  const omittedHelper = select('runtime');
  assert.equal(omittedHelper.status, 1);
  assert.equal(omittedHelper.stdout, '');
  assert.match(omittedHelper.stderr, /Changed-path inventory does not match actual tested merge/);
  fs.writeFileSync(planPath, JSON.stringify(plan));
  const notApplicable = select('tools');
  assert.equal(notApplicable.status, 0, notApplicable.stderr);
  assert.equal(JSON.parse(notApplicable.stdout).applicable, false);
  const unknown = select('constructor');
  assert.equal(unknown.status, 1);
  assert.equal(unknown.stdout, '');
  const incomplete = createPlan(plan.source, ['renamed-input']);
  assert.equal(incomplete.applicable.dashboard, false, 'omitting the old API path must alter the proposed applicability');
  fs.writeFileSync(planPath, JSON.stringify(incomplete));
  const incompleteOutputs = spawnSync(process.execPath, [cli, 'outputs', planPath], { cwd: directory, env, encoding: 'utf8' });
  for (const rejected of [select('dashboard'), capture(), incompleteOutputs]) {
    assert.equal(rejected.status, 1);
    assert.equal(rejected.stdout, '');
    assert.match(rejected.stderr, /Changed-path inventory does not match actual tested merge/);
  }
  fs.writeFileSync(planPath, JSON.stringify({ ...plan, paths: [...plan.paths].reverse() }));
  assert.equal(select('dashboard').status, 0, 'alternative path ordering must retain complete scope');
  fs.writeFileSync(planPath, JSON.stringify(plan));
  for (const extra of [
    { GITHUB_WORKFLOW: 'Independent observation' }, { GITHUB_WORKFLOW: '' },
    { GITHUB_RUN_ATTEMPT: '2' }, { GITHUB_RUN_ID: '124' },
    { GITHUB_EVENT_NAME: 'workflow_dispatch' }, { GITHUB_SHA: head },
  ]) {
    const wrongSource = capture(extra);
    assert.equal(wrongSource.status, 1);
    assert.equal(wrongSource.stdout, '');
    assert.match(wrongSource.stderr, /(?:Source mismatch:|caller workflow|Not ordinary Waza PR evidence|Checkout does not match triggering merge SHA)/);
  }
  const wrongEvent = structuredClone(event);
  wrongEvent.pull_request.base.sha = initialMain;
  fs.writeFileSync(eventPath, JSON.stringify(wrongEvent));
  const wrongParents = capture();
  assert.equal(wrongParents.status, 1);
  assert.equal(wrongParents.stdout, '');
  assert.match(wrongParents.stderr, /Merge parents do not match event base and head/);
  assert.equal(evaluateCLI().status, 1, 'evaluation must reject mismatched event merge parents');
  wrongEvent.pull_request.base.sha = base;
  wrongEvent.pull_request.base.ref = 'feature/different-parent';
  fs.writeFileSync(eventPath, JSON.stringify(wrongEvent));
  const wrongRef = capture();
  assert.equal(wrongRef.status, 1);
  assert.equal(wrongRef.stdout, '');
  assert.match(wrongRef.stderr, /Source mismatch: base_ref/);
  assert.equal(evaluateCLI().status, 1, 'evaluation must reject a stale base ref');
  fs.writeFileSync(eventPath, JSON.stringify(event));
  assert.equal(capture().status, 0, 'source-negative controls must retain successful steps');
  steps.lint.outcome = 'skipped';
  fs.writeFileSync(stepsPath, JSON.stringify(steps));
  const rejected = capture();
  assert.equal(rejected.status, 1);
  assert.equal(rejected.stdout, '');
  env.GITHUB_SHA = head;
  const mismatch = invoke();
  assert.equal(mismatch.status, 1);
  assert.equal(mismatch.stdout, '');
  env.GITHUB_SHA = tested;
  event.pull_request.base.sha = 'f'.repeat(40);
  fs.writeFileSync(eventPath, JSON.stringify(event));
  assert.equal(invoke().status, 1);
  event.pull_request.base.sha = initialMain;
  fs.writeFileSync(eventPath, JSON.stringify(event));
  assert.equal(invoke().status, 1);
  event.pull_request.base.sha = base;
  event.action = 'edited';
  event.changes = { body: { from: null } };
  fs.writeFileSync(eventPath, JSON.stringify(event));
  const routed = spawnSync(process.execPath, [cli, 'route'], { cwd: directory, env, encoding: 'utf8' });
  assert.equal(routed.status, 0, routed.stderr);
  const route = JSON.parse(routed.stdout);
  assert.equal(route.validation, false);
  assert.notEqual(route.completeness_name, 'PR completeness');
  assert.equal(route.cancel_in_progress, false);
  const metadata = invoke();
  assert.equal(metadata.status, 1);
  assert.match(metadata.stderr, /Metadata-only edit is not PR validation/);
  assert.equal(evaluateCLI().status, 1, 'metadata edits cannot replay ordinary evaluation evidence');
  event.changes = retargetChanges();
  fs.writeFileSync(eventPath, JSON.stringify(event));
  const retarget = invoke();
  assert.equal(retarget.status, 0, retarget.stderr);
  const retargetPlan = JSON.parse(retarget.stdout);
  assert.equal(retargetPlan.source.base_changed, true);
  assert.equal(retargetPlan.source.base_sha, base);
  assert.equal(retargetPlan.source.tested_sha, tested);
  assert.deepEqual(retargetPlan.paths, plan.paths);
  assert.equal(evaluateCLI().status, 1, 'retarget requires freshly bound evaluation input');
  fs.writeFileSync(evaluationPath, JSON.stringify(fixture(retargetPlan.paths, retargetPlan.source)));
  const retargetEvaluation = evaluateCLI();
  assert.equal(retargetEvaluation.status, 0, retargetEvaluation.stderr);
  assert.equal(JSON.parse(retargetEvaluation.stdout).passed, true);
  let leafControl = 0;
  const leafCases = consumedLeafInputs.flatMap(([file, family, nearMiss = false]) => [
    [file, family, true], [`${file}-near-miss`, family, nearMiss],
  ]);
  leafCases.push(...siteDirectoryWitnesses.map(([file, directory]) => [file, 'site', true, directory]));
  leafCases.push(['examples-near-miss/README.md', 'site', false]);
  for (const [changedFile, family, applicable, directoryWitness] of leafCases) {
    const operations = directoryWitness || changedFile === 'Makefile' || changedFile === 'web/e2e/runner-negotiation.spec.ts'
      ? ['add', 'delete', 'rename'] : ['add'];
    for (const operation of operations) {
      const suffix = String(++leafControl);
      let leafBase = initialMain;
      if (operation !== 'add') {
        git('checkout', '-qb', `consumer-setup-${suffix}`, initialMain);
        fs.mkdirSync(path.dirname(path.join(directory, changedFile)), { recursive: true });
        fs.writeFileSync(path.join(directory, changedFile), '# original consumer input\n');
        git('add', '-f', '--', changedFile);
        git('commit', '-qm', 'prepare one consumer input');
        leafBase = git('rev-parse', 'HEAD');
        if (directoryWitness) {
          assert.deepEqual(git('ls-tree', '-r', '--name-only', leafBase, '--', directoryWitness).split('\n'), [changedFile],
            'directory control must contain exactly its final tracked leaf');
          assert.equal(fs.statSync(path.join(directory, directoryWitness)).isDirectory(), true);
        }
      }
      git('checkout', '-qb', `consumer-head-${suffix}`, leafBase);
      const renamedFile = `renamed-consumer-${suffix}`;
      if (operation === 'add') {
        fs.mkdirSync(path.dirname(path.join(directory, changedFile)), { recursive: true });
        fs.writeFileSync(path.join(directory, changedFile), '# changed consumer input\n');
        git('add', '-f', '--', changedFile);
      } else if (operation === 'delete') {
        git('rm', '--', changedFile);
      } else {
        git('mv', '--', changedFile, renamedFile);
      }
      git('commit', '-qm', 'change one consumer input');
      const leafHead = git('rev-parse', 'HEAD');
      const leafBaseRef = `consumer-base-${suffix}`;
      git('checkout', '-qb', leafBaseRef, leafBase);
      git('merge', '--no-ff', '-qm', 'test one consumer input', leafHead);
      env.GITHUB_SHA = git('rev-parse', 'HEAD');
      if (directoryWitness && operation !== 'add') {
        const checkout = path.join(directory, `consumer-checkout-${suffix}`);
        git('clone', '--shared', '--quiet', '--no-checkout', directory, checkout);
        git('-C', checkout, 'checkout', '--detach', '-q', env.GITHUB_SHA);
        assert.equal(git('-C', checkout, 'ls-tree', '-r', '--name-only', 'HEAD', '--', directoryWitness), '');
        assert.equal(fs.existsSync(path.join(checkout, directoryWitness)), false,
          'removing the final tracked leaf must remove the consumed directory from a fresh checkout');
      }
      fs.writeFileSync(eventPath, JSON.stringify({
        action: 'synchronize', number: source.pr,
        pull_request: { head: { sha: leafHead }, base: { sha: leafBase, ref: leafBaseRef } },
      }));
      const discovered = invoke();
      assert.equal(discovered.status, 0, discovered.stderr);
      const leafPlan = JSON.parse(discovered.stdout);
      assert.deepEqual(leafPlan.paths, operation === 'rename' ? [changedFile, renamedFile].sort() : [changedFile]);
      assert.equal(leafPlan.applicable[family], applicable, changedFile);
      fs.writeFileSync(planPath, JSON.stringify(leafPlan));
      const selected = select(family);
      assert.equal(selected.status, 0, selected.stderr);
      assert.equal(JSON.parse(selected.stdout).applicable, applicable, changedFile);
      fs.writeFileSync(planPath, JSON.stringify(createPlan(leafPlan.source, [])));
      const omittedInput = select(family);
      assert.equal(omittedInput.status, 1);
      assert.equal(omittedInput.stdout, '');
      assert.match(omittedInput.stderr, /Changed-path inventory does not match actual tested merge/);
    }
  }
});
