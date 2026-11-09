const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawnSync } = require('node:child_process');
const test = require('node:test');
const { captureReceipt, contracts, producerTitle } = require('./pr-completeness.cjs');

const root = path.resolve(__dirname, '../..');
const planWorkflow = fs.readFileSync(path.join(root, '.github/workflows/pr-completeness-plan.yml'), 'utf8');
const admissionWorkflow = fs.readFileSync(path.join(root, '.github/workflows/pr-completeness-admission.yml'), 'utf8');
const planCommand = planWorkflow.match(/^        run: (node .+)$/m)?.[1];
const admissionCommand = admissionWorkflow.match(/^        run: \|\n((?:          .+\n)+)/m)?.[1].replace(/^          /gm, '');
assert.ok(planCommand && admissionCommand, 'execute actual production workflow commands, not handwritten substitutes');

function browser(title = 'selected assertion', file = 'feature.spec.ts') {
  return {
    config: { projects: [{ name: 'chromium', retries: 0 }] },
    stats: { expected: 1, skipped: 0, unexpected: 0, flaky: 0 },
    suites: [{ specs: [{ title, file, tests: [{
      projectName: 'chromium', status: 'expected', results: [{ status: 'passed', retry: 0 }],
    }] }] }],
  };
}

function fixture(t, changed = 'AGENTS.md', retarget = false) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'waza-admission-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  const write = (file, value) => {
    fs.mkdirSync(path.dirname(path.join(directory, file)), { recursive: true });
    fs.writeFileSync(path.join(directory, file), value);
  };
  const json = (file, value) => write(file, JSON.stringify(value));
  const git = (...args) => execFileSync('git', args, { cwd: directory, encoding: 'utf8' }).trim();
  for (const file of ['.github/scripts/pr-completeness.cjs', '.github/scripts/pr-completeness-admission.cjs', 'web/src/lib/strictJson.ts']) {
    write(file, fs.readFileSync(path.join(root, file)));
  }
  write('AGENTS.md', 'unchanged baseline\n');
  git('init', '-q');
  git('config', 'user.name', 'Offline admission control');
  git('config', 'user.email', 'offline-admission@example.invalid');
  git('config', 'commit.gpgsign', 'false');
  git('add', '.');
  git('commit', '-qm', 'baseline production sources');
  git('checkout', '-qb', 'feature/stack-parent');
  write('parent-only', 'not in child scope\n');
  git('add', '.');
  git('commit', '-qm', 'stack parent');
  const base = git('rev-parse', 'HEAD');
  git('checkout', '-qb', 'feature/child');
  write(changed, 'changed production input\n');
  git('add', '.');
  git('commit', '-qm', 'child change');
  const head = git('rev-parse', 'HEAD');
  git('checkout', '-q', 'feature/stack-parent');
  git('merge', '--no-ff', '-qm', 'tested merge', 'feature/child');
  const tested = git('rev-parse', 'HEAD');
  json('event.json', {
    action: retarget ? 'edited' : 'synchronize', number: 900,
    ...(retarget ? { changes: { base: { ref: { from: 'feature/old-parent' }, sha: { from: 'a'.repeat(40) } } } } : {}),
    pull_request: { head: { sha: head }, base: { sha: base, ref: 'feature/stack-parent' } },
  });
  const currentPR = {
    number: 900, state: 'open', head: { sha: head },
    base: { sha: base, ref: 'feature/stack-parent', repo: { full_name: 'microsoft/waza' } },
  };
  json('pr.json', currentPR);
  write('output', '');
  write('summary', '');
  fs.mkdirSync(path.join(directory, 'runner'));
  fs.mkdirSync(path.join(directory, 'bin'));
  write('gh.go', `package main
import ("fmt"; "os"; "strings")
func main() {
  expected := []string{"api", "--hostname", "github.com", "repos/microsoft/waza/pulls/900"}
  if strings.Join(os.Args[1:], "|") != strings.Join(expected, "|") { os.Exit(91) }
  if err := os.WriteFile(os.Getenv("ADMISSION_API_LOG"), []byte(strings.Join(os.Args[1:], "|")), 0600); err != nil { os.Exit(92) }
  if os.Getenv("ADMISSION_API_FAIL") == "1" { fmt.Fprintln(os.Stderr, "Resource not accessible by integration (HTTP 403)"); os.Exit(1) }
  bytes, err := os.ReadFile(os.Getenv("ADMISSION_API_PR"))
  if err != nil { os.Exit(93) }
  os.Stdout.Write(bytes)
}
`);
  execFileSync('go', ['build', '-o', path.join(directory, 'bin', process.platform === 'win32' ? 'gh.exe' : 'gh'), 'gh.go'], {
    cwd: directory, env: { ...process.env, GOFLAGS: '', GOWORK: 'off' },
  });
  const env = {
    ...process.env,
    PATH: `${path.join(directory, 'bin')}${path.delimiter}${process.env.PATH}`,
    GITHUB_REPOSITORY: 'microsoft/waza', GITHUB_EVENT_NAME: 'pull_request',
    GITHUB_WORKFLOW: 'Go Build and Test', GITHUB_RUN_ID: '123', GITHUB_RUN_ATTEMPT: '2',
    GITHUB_SHA: tested, GITHUB_EVENT_PATH: path.join(directory, 'event.json'),
    GITHUB_OUTPUT: path.join(directory, 'output'), GITHUB_STEP_SUMMARY: path.join(directory, 'summary'),
    RUNNER_TEMP: path.join(directory, 'runner'),
    GH_TOKEN: 'offline-placeholder',
    ADMISSION_API_PR: path.join(directory, 'pr.json'), ADMISSION_API_LOG: path.join(directory, 'api.log'),
  };
  const run = (command) => spawnSync('bash', ['--noprofile', '--norc', '-e', '-o', 'pipefail', '-c', command], {
    cwd: directory, env, encoding: 'utf8',
  });
  const planned = run(planCommand);
  assert.equal(planned.status, 0, planned.stderr);
  const plan = JSON.parse(fs.readFileSync(path.join(directory, 'runner/pr-completeness-plan/plan.json')));
  const outputs = JSON.parse(planned.stdout);
  env.WAZA_COMPLETENESS_PLAN = outputs.plan_artifact;
  const admissionEnvironment = { ...env };
  const needs = { 'pr-scope': { result: 'success' } };
  const receipts = [];
  for (const [family, contract] of Object.entries(contracts)) {
    if (!plan.applicable[family]) continue;
    for (const dependency of contract.needs) needs[dependency] = { result: 'success' };
    for (const [unit, commands] of Object.entries(contract.units)) {
      const steps = Object.fromEntries(commands.map((command) => [command, { outcome: 'success', conclusion: 'success' }]));
      steps.checkout.outputs = { commit: tested };
      const reports = family === 'assurance' ? { browser: browser(producerTitle, 'assurance.spec.ts') } :
        family === 'site' ? { browser: browser(), chat: browser(), chat_unavailable: browser() } :
          family === 'dashboard' ? { browser: browser() } : undefined;
      receipts.push(captureReceipt(plan, family, unit, steps, tested, reports, plan.source));
    }
  }
  let invocation = 0;
  const admit = (mutate = () => {}, alterFiles = () => {}) => {
    const input = structuredClone({ plan, needs, receipts, currentPR });
    invocation += 1;
    const prefix = `attempt-${invocation}`;
    fs.mkdirSync(path.join(directory, prefix));
    for (const key of Object.keys(env)) delete env[key];
    Object.assign(env, admissionEnvironment);
    env.RUNNER_TEMP = path.join(directory, prefix);
    mutate(input, env);
    json(`${prefix}/pr-completeness-plan/plan.json`, input.plan);
    for (const receipt of input.receipts) {
      json(`${prefix}/pr-completeness-receipts/${receipt.artifact_name}/receipt.json`, receipt);
    }
    json('pr.json', input.currentPR);
    env.WAZA_COMPLETENESS_NEEDS = JSON.stringify(input.needs);
    alterFiles(input, env);
    const outcome = run(admissionCommand);
    const resultFile = path.join(env.RUNNER_TEMP, 'pr-completeness-result/result.json');
    return { ...outcome, input, result: fs.existsSync(resultFile) ? JSON.parse(fs.readFileSync(resultFile)) : undefined };
  };
  return { directory, plan, outputs, needs, receipts, env, json, run, admit };
}

test('real plan and admission callers validate a stacked merge and expose legitimate non-applicability', (t) => {
  const control = fixture(t);
  assert.deepEqual(control.plan.paths, ['AGENTS.md']);
  assert.equal(control.plan.source.base_ref, 'feature/stack-parent');
  assert.equal(control.outputs.plan_artifact, 'pr-completeness-plan-123-2');
  assert.equal(fs.existsSync(path.join(control.directory, 'api.log')), false, 'planning must not query the API');
  const output = fs.readFileSync(path.join(control.directory, 'output'), 'utf8');
  assert.match(output, /^go=true$/m);
  assert.match(output, /^dashboard=false$/m);
  const result = control.admit();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.result.passed, true);
  assert.equal(result.input.receipts.length, 8, 'all native Go units must be exercised');
  assert.deepEqual(result.result.scopes, {
    go: 'validated', dashboard: 'not_applicable', site: 'not_applicable', examples: 'not_applicable',
    assurance: 'not_applicable', container: 'not_applicable', tools: 'not_applicable', runtime: 'not_applicable',
  });
  assert.match(fs.readFileSync(path.join(control.directory, 'summary'), 'utf8'), /\| dashboard \| not_applicable \|/);
  assert.equal(fs.readFileSync(path.join(control.directory, 'api.log'), 'utf8'),
    'api|--hostname|github.com|repos/microsoft/waza/pulls/900');
  assert.equal(JSON.stringify(result.result).includes('offline-placeholder'), false);
  assert.equal(fs.readFileSync(path.join(control.directory, 'summary'), 'utf8').includes('offline-placeholder'), false);
  const retarget = fixture(t, 'AGENTS.md', true);
  assert.equal(retarget.plan.source.action, 'edited');
  assert.equal(retarget.plan.source.base_changed, true);
  assert.equal(retarget.admit().status, 0, 'a genuine base-retarget-shaped event must remain alternative-valid');
});

test('all-family caller consumes individual native and actual-producer browser receipt controls', (t) => {
  const control = fixture(t, '.github/workflows/changed.yml');
  assert.equal(Object.values(control.plan.applicable).every(Boolean), true);
  const result = control.admit();
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.result.passed, true);
  assert.equal(Object.values(result.result.scopes).every((scope) => scope === 'validated'), true);
  for (const family of Object.keys(contracts)) {
    assert.equal(result.input.receipts.filter((receipt) => receipt.family === family).length,
      Object.keys(contracts[family].units).length, family);
  }
  const producer = result.input.receipts.find((receipt) => receipt.family === 'assurance');
  assert.equal(producer.browser_reports.browser.suites[0].specs[0].title, producerTitle);
});

test('caller rejects missing, failed, cancelled and skipped dependencies and nested receipt failures', (t) => {
  const control = fixture(t, '.github/workflows/changed.yml');
  for (const dependency of Object.keys(control.needs)) {
    for (const status of ['missing', 'failure', 'cancelled', 'skipped']) {
      const result = control.admit((input) => {
        if (status === 'missing') delete input.needs[dependency];
        else input.needs[dependency].result = status;
      });
      assert.equal(result.status, 1, `${dependency}: ${status}`);
      assert.equal(result.result.passed, false);
      assert.ok(result.result.diagnostics.some((diagnostic) =>
        diagnostic.includes(dependency) || (dependency === 'pr-scope' && diagnostic.includes('Applicability/source'))));
    }
  }
  for (const mutate of [
    (input) => { input.receipts = []; },
    (input) => { input.receipts[0].steps.checkout.outcome = 'skipped'; },
    (input) => { input.receipts[0].source.run_attempt = '1'; },
    (input) => { input.receipts[0].tested_sha = 'f'.repeat(40); },
    (input) => { input.receipts[0].steps.checkout.outputs.commit = 'f'.repeat(40); },
    (input) => {
      input.receipts.find((receipt) => receipt.family === 'assurance')
        .browser_reports.browser.suites[0].specs[0].tests[0].results[0].status = 'skipped';
    },
  ]) {
    const result = control.admit(mutate);
    assert.equal(result.status, 1, result.stdout);
    assert.equal(result.result.passed, false);
    assert.ok(result.result.diagnostics.length > 0);
  }
  assert.equal(control.admit().status, 0, 'a previous rejected invocation must not contaminate the next one');
});

test('caller rechecks API source and rejects denied, stale head/base and closed PR evidence', (t) => {
  const control = fixture(t);
  for (const mutate of [
    (input) => { input.currentPR.head.sha = 'e'.repeat(40); },
    (input) => { input.currentPR.base.sha = 'e'.repeat(40); },
    (input) => { input.currentPR.base.ref = 'main'; },
    (input) => { input.currentPR.state = 'closed'; },
    (input) => { input.currentPR.number = 901; },
  ]) {
    const result = control.admit(mutate);
    assert.equal(result.status, 1);
    assert.equal(Object.values(result.result.scopes).every((scope) => scope === 'unavailable'), true);
    assert.ok(result.result.diagnostics.some((diagnostic) => diagnostic.startsWith('Current PR')));
  }
  const denied = control.admit((_, env) => { env.ADMISSION_API_FAIL = '1'; });
  assert.equal(denied.status, 1);
  assert.equal(denied.result, undefined, 'API failure must not invent a result');
  assert.match(denied.stderr, /Resource not accessible by integration \(HTTP 403\)/);
  assert.match(denied.stderr, /Current PR API evidence is unavailable/);
  const missingToken = control.admit((_, env) => { delete env.GH_TOKEN; });
  assert.equal(missingToken.status, 1);
  assert.equal(missingToken.result, undefined);
  assert.match(missingToken.stderr, /Missing admission read token/);
});

test('caller refuses filtered path plans, foreign attempts and non-validation events', (t) => {
  const control = fixture(t);
  for (const mutate of [
    (input) => { input.plan.paths = []; },
    (input) => { input.plan.source.base_ref = 'main'; },
    (input) => { input.plan.source.run_attempt = '1'; },
    (input, env) => { env.WAZA_COMPLETENESS_PLAN = 'pr-completeness-plan-123-1'; },
    (_, env) => { env.GITHUB_EVENT_NAME = 'push'; },
    (_, env) => { env.GITHUB_SHA = 'e'.repeat(40); },
  ]) {
    const result = control.admit(mutate);
    assert.equal(result.status, 1);
    assert.equal(result.result, undefined);
  }
  const metadata = JSON.parse(fs.readFileSync(control.env.GITHUB_EVENT_PATH));
  metadata.action = 'edited';
  metadata.changes = { body: { from: 'old body' } };
  control.json('event.json', metadata);
  const result = control.admit();
  assert.equal(result.status, 1);
  assert.equal(result.result, undefined);
});

test('downloaded artifact layout and ambiguous JSON cannot bypass admission', (t) => {
  const control = fixture(t);
  for (const kind of ['wrong-name', 'extra-file', 'non-receipt', 'extra-plan-file', 'null-needs']) {
    const result = control.admit((input, env) => {
      if (kind === 'wrong-name') input.receipts[0].artifact_name = 'unrelated-artifact';
      if (kind === 'extra-file') control.json(
        `${path.relative(control.directory, env.RUNNER_TEMP)}/pr-completeness-receipts/extra/steps.json`, {});
      if (kind === 'non-receipt') input.receipts[0] = { ...input.receipts[0], kind: 'steps' };
      if (kind === 'extra-plan-file') {
        control.json(`${path.relative(control.directory, env.RUNNER_TEMP)}/pr-completeness-plan/extra.json`, {});
      }
      if (kind === 'null-needs') input.needs = null;
    });
    assert.equal(result.status, 1, kind);
    if (result.result) assert.equal(result.result.passed, false, kind);
  }
  for (const kind of ['duplicate-plan-key', 'duplicate-receipt-key', 'duplicate-needs-key', 'invalid-utf8', 'artifact-name-disagreement', 'wrong-leaf']) {
    const result = control.admit(() => {}, (input, env) => {
      const planFile = path.join(env.RUNNER_TEMP, 'pr-completeness-plan/plan.json');
      const receiptFile = path.join(env.RUNNER_TEMP, 'pr-completeness-receipts', input.receipts[0].artifact_name, 'receipt.json');
      if (kind === 'duplicate-plan-key') {
        fs.writeFileSync(planFile, fs.readFileSync(planFile, 'utf8').replace('"schema":1', '"schema":1,"schema":1'));
      }
      if (kind === 'duplicate-receipt-key') {
        fs.writeFileSync(receiptFile, fs.readFileSync(receiptFile, 'utf8').replace('"schema":1', '"schema":1,"schema":1'));
      }
      if (kind === 'duplicate-needs-key') {
        env.WAZA_COMPLETENESS_NEEDS = '{"pr-scope":{"result":"failure"},"pr-scope":{"result":"success"}}';
      }
      if (kind === 'invalid-utf8') fs.writeFileSync(receiptFile, Buffer.from([0xff]));
      if (kind === 'artifact-name-disagreement') {
        fs.renameSync(path.dirname(receiptFile), `${path.dirname(receiptFile)}-different-name`);
      }
      if (kind === 'wrong-leaf') fs.renameSync(receiptFile, path.join(path.dirname(receiptFile), 'steps.json'));
    });
    assert.equal(result.status, 1, kind);
    assert.equal(result.result, undefined, kind);
  }
  const preservedPlan = fs.readFileSync(path.join(control.env.RUNNER_TEMP, 'pr-completeness-plan/plan.json'));
  const repeatedPlan = control.run(planCommand);
  assert.equal(repeatedPlan.status, 1, 'existing plan directory cannot be overwritten');
  assert.deepEqual(fs.readFileSync(path.join(control.env.RUNNER_TEMP, 'pr-completeness-plan/plan.json')), preservedPlan);
  const occupied = control.admit(() => {}, (_, env) => {
    fs.mkdirSync(path.join(env.RUNNER_TEMP, 'pr-completeness-result'));
    fs.writeFileSync(path.join(env.RUNNER_TEMP, 'pr-completeness-result/owned-elsewhere'), 'preserve');
  });
  assert.equal(occupied.status, 1);
  assert.equal(occupied.result, undefined);
  assert.equal(fs.readFileSync(path.join(control.env.RUNNER_TEMP, 'pr-completeness-result/owned-elsewhere'), 'utf8'), 'preserve');
  if (process.platform !== 'win32') {
    const linked = control.admit(() => {}, (input, env) => {
      const file = path.join(env.RUNNER_TEMP, 'pr-completeness-receipts', input.receipts[0].artifact_name, 'receipt.json');
      const target = path.join(control.directory, 'linked-receipt.json');
      fs.renameSync(file, target);
      fs.symlinkSync(target, file);
    });
    assert.equal(linked.status, 1);
    assert.equal(linked.result, undefined);
    assert.match(linked.stderr, /Expected regular JSON file/);
  }
});
