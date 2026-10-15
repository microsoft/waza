import assert from 'node:assert/strict';
import { mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
assert.ok(process.env.WAZA_BIN, 'Set WAZA_BIN to a built current-main binary; examples must not be silently skipped');
const binary = resolve(process.env.WAZA_BIN);
const scratch = resolve(root, '.docs-validation', `examples-${process.pid}`);
mkdirSync(join(scratch, 'tmp'), { recursive: true });
const env = { ...process.env, CI: '1', NO_COLOR: '1', WAZA_NO_UPDATE_CHECK: '1',
  ENABLE_COPILOT_TESTS: 'false', ENABLE_COPILOT_SDK_TESTS: 'false',
  ENABLE_LIVE_COPILOT_TESTS: 'false', TMPDIR: join(scratch, 'tmp') };
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const blocks = (path) => [...read(path).matchAll(/```yaml\n([\s\S]*?)```/g)].map((m) => m[1]);

function run(args, cwd, status = 0) {
  const result = spawnSync(binary, args, { cwd, env, encoding: 'utf8', timeout: 90_000 });
  assert.equal(result.error, undefined, String(result.error));
  assert.equal(result.status, status, `${args.join(' ')}\n${result.stdout}\n${result.stderr}`);
  return result.stdout;
}

test('copy/paste scaffold, preflight, mock capture and predicate controls', () => {
  try {
    run(['init', 'workflow-lab', '--no-skill'], scratch);
    const project = join(scratch, 'workflow-lab');
    run(['new', 'skill', 'my-skill'], project);
    assert.match(readFileSync(join(project, 'skills/my-skill/SKILL.md'), 'utf8'), /name: my-skill/);
    run(['preflight', 'evals/my-skill/eval.yaml'], project);
    for (const template of ['repository', 'cli', 'mcp']) {
      const name = template === 'repository' ? 'inventory' : `inventory-${template}`;
      run(['new', 'eval', name, '--scenario', '--template', template], project);
      run(['preflight', `evals/${name}/eval.yaml`], project);
    }
    const generated = join(project, 'evals/inventory/eval.yaml');
    writeFileSync(generated, readFileSync(generated, 'utf8').replace('executor: copilot-sdk', 'executor: mock'));
    run(['run', 'evals/inventory/eval.yaml', '--snapshot', 'snapshots', '--output', 'results.json'], project, 1);
    const results = JSON.parse(readFileSync(join(project, 'results.json'), 'utf8'));
    const recorded = results.tasks[0].runs[0];
    assert.ok(recorded.evidence, 'opt-in ordinary run must produce result evidence');
    assert.ok(recorded.snapshot_path);
    run(['replay', recorded.snapshot_path, '--json'], project);
    const regrade = JSON.parse(run(['grade', 'evals/inventory/eval.yaml', '--results', 'results.json', '--workspace', 'evals/inventory/fixtures', '--output', 'regraded.json'], project));
    assert.equal(regrade.passed, false, 'grade exit zero is not a passing assessment');
    const regraded = JSON.parse(readFileSync(join(project, 'regraded.json'), 'utf8'));
    assert.ok(regraded.tasks[0].runs[0].evidence);

    const snippets = blocks('site/src/content/docs/quick-start.mdx');
    const task = snippets.find((block) => block.startsWith('id: docs-probe'));
    const suite = snippets.find((block) => block.includes('name: docs-smoke'));
    assert.ok(task && suite, 'Quick Start must contain complete task and suite YAML');
    writeFileSync(join(project, 'evals/inventory/tasks/docs-probe.yaml'), task);
    const suitePath = join(project, 'evals/inventory/smoke.yaml');
    writeFileSync(suitePath, suite);
    run(['preflight', suitePath], project);
    run(['run', suitePath, '--output', 'smoke.json'], project);
    writeFileSync(suitePath, suite.replace('^Mock response for:', '^IMPOSSIBLE_DOCS_SENTINEL$'));
    assert.match(run(['run', suitePath], project, 1), /pattern|regex|FAIL/i);
    writeFileSync(suitePath, suite.replace('regex_match:', 'pattern:'));
    run(['preflight', suitePath, '--strict'], project, 1);
    const tutorial = blocks('docs/TUTORIAL.md');
    run(['new', 'skill', 'my-awesome-skill'], project);
    const tutorialSuite = tutorial.find((block) => block.startsWith('name: tutorial-eval'));
    const tutorialTask = tutorial.find((block) => block.startsWith('id: explain-function'));
    assert.ok(tutorialSuite && tutorialTask);
    writeFileSync(join(project, 'evals/my-awesome-skill/eval.yaml'), tutorialSuite);
    writeFileSync(join(project, 'evals/my-awesome-skill/tasks/basic-usage.yaml'), tutorialTask);
    run(['preflight', 'evals/my-awesome-skill/eval.yaml'], project);
    run(['run', 'evals/my-awesome-skill/eval.yaml'], project);
    writeFileSync(join(project, 'evals/my-awesome-skill/eval.yaml'), tutorialSuite.replace('(?i)(function|parameter)', '^IMPOSSIBLE_TUTORIAL_SENTINEL$'));
    run(['run', 'evals/my-awesome-skill/eval.yaml'], project, 1);
    const program = tutorial.find((block) => block.startsWith('- type: program'));
    const prompt = tutorial.find((block) => block.startsWith('- type: prompt'));
    assert.ok(program && prompt);
    const withGrader = (grader) => suite.replace(
      /graders:\n[\s\S]*?\ntasks:/,
      `graders:\n${grader.trimEnd().split('\n').map((line) => `  ${line}`).join('\n')}\ntasks:`,
    );
    writeFileSync(suitePath, withGrader(program));
    run(['preflight', suitePath], project);
    run(['run', suitePath], project);
    writeFileSync(suitePath, withGrader(program).replace('output.trim() ? 0 : 1', '1'));
    run(['run', suitePath], project, 1);
    writeFileSync(suitePath, withGrader(prompt).replace('executor: mock', 'executor: copilot-sdk'));
    run(['preflight', suitePath], project);
    run(['new', 'skill', 'code-explainer'], project);
    for (const path of ['site/src/content/docs/index.mdx', 'site/src/content/docs/getting-started.mdx', 'docs/GETTING-STARTED.md']) {
      const example = blocks(path).find((block) => block.startsWith('name: code-explainer-eval'));
      assert.ok(example, path);
      const destination = join(project, 'evals/code-explainer/docs-example.yaml');
      writeFileSync(destination, example);
      run(['preflight', destination], project);
    }
    run(['adversarial', '--engine', 'mock', '--packs', 'prompt-injection,scope-bypass'], project, 2);
    run(['adversarial', '--engine', 'mock', '--packs', 'prompt-injection,scope-bypass', '--on-unsafe-outcome', 'warn'], project);
  } finally {
    rmSync(scratch, { recursive: true, force: true });
  }
});

test('schema outcome snippet accepts alternatives and rejects wrong state/forbidden file', () => {
  const directory = resolve(root, '.docs-validation', `outcomes-${process.pid}`);
  mkdirSync(join(directory, 'tasks'), { recursive: true });
  mkdirSync(join(directory, 'fixtures'), { recursive: true });
  mkdirSync(join(scratch, 'tmp'), { recursive: true });
  try {
    const task = blocks('site/src/content/docs/reference/schema.mdx').find((block) => block.startsWith('id: local-record'));
    assert.ok(task);
    writeFileSync(join(directory, 'tasks/record.yaml'), task);
    writeFileSync(join(directory, 'eval.yaml'), 'schemaVersion: "2.0"\nname: outcome-controls\nscenario: Local record\nconfig:\n  executor: mock\n  trials_per_task: 1\n  timeout_seconds: 30\n  model: mock\nmetrics:\n  - name: task_completion\n    weight: 1\n    threshold: 1\ntasks:\n  - tasks/record.yaml\n');
    for (const state of ['complete', 'done', 'wrong-state']) {
      writeFileSync(join(directory, 'fixtures/record.txt'), `${state}\n`);
      run(['preflight', 'eval.yaml'], directory);
      run(['run', 'eval.yaml'], directory, state === 'wrong-state' ? 1 : 0);
    }
    writeFileSync(join(directory, 'fixtures/record.txt'), 'complete\n');
    writeFileSync(join(directory, 'fixtures/forbidden.txt'), 'forbidden\n');
    writeFileSync(join(directory, 'tasks/record.yaml'), task.replace('    - path: record.txt', '    - path: record.txt\n    - path: forbidden.txt'));
    run(['preflight', 'eval.yaml'], directory);
    run(['run', 'eval.yaml'], directory, 1);
  } finally {
    rmSync(directory, { recursive: true, force: true });
    rmSync(scratch, { recursive: true, force: true });
  }
});
