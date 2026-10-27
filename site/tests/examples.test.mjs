import assert from 'node:assert/strict';
import { accessSync, constants, existsSync, mkdirSync, readFileSync, readdirSync, realpathSync, statSync, symlinkSync, unlinkSync, writeFileSync } from 'node:fs';
import { isAbsolute, resolve, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import test from 'node:test';
import { allocateExampleFixture, offlineEnvironment, replaceExactlyOnce, validateFixtureAncestry } from './helpers/example-fixtures.mjs';

const root = fileURLToPath(new URL('../../', import.meta.url));
assert.ok(process.env.WAZA_BIN, 'Set WAZA_BIN to a built current-main binary; examples must not be silently skipped');
const binary = resolve(process.env.WAZA_BIN);
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const blocks = (path) => [...read(path).matchAll(/```yaml\n([\s\S]*?)```/g)].map((m) => m[1]);

function cliRunner(env) {
  assert.ok(isAbsolute(env.COPILOT_CLI_PATH) && statSync(env.COPILOT_CLI_PATH).isDirectory());
  return (args, cwd, status = 0) => {
    const result = spawnSync(binary, args, { cwd, env, encoding: 'utf8', timeout: 90_000 });
    assert.equal(result.error, undefined, String(result.error));
    assert.equal(result.status, status, `${args.join(' ')}\n${result.stdout}\n${result.stderr}`);
    return result.stdout;
  };
}

test('copy/paste scaffold, preflight, mock capture and predicate controls', () => {
  const fixture = allocateExampleFixture(root);
  const scratch = fixture.directory;
  const run = cliRunner(fixture.env);
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
    writeFileSync(generated, replaceExactlyOnce(readFileSync(generated, 'utf8'), '\n  executor: copilot-sdk\n', '\n  executor: mock\n'));
    run(['run', 'evals/inventory/eval.yaml', '--snapshot', 'snapshots', '--output', 'results.json'], project, 1);
    const results = JSON.parse(readFileSync(join(project, 'results.json'), 'utf8'));
    const recorded = results.tasks[0].runs[0];
    assert.ok(recorded.evidence, 'opt-in ordinary run must produce result evidence');
    assert.ok(recorded.snapshot_path);
    run(['replay', recorded.snapshot_path, '--json'], project);
    const retained = readFileSync(resolve(project, recorded.snapshot_path));
    for (const name of ['before.json', 'after.json']) writeFileSync(join(project, name), retained);
    const bisect = read('site/src/content/docs/guides/adversarial.mdx')
      .match(/`waza (replay [^`\n]+)`/);
    assert.ok(bisect, 'adversarial guide must publish the complete offline replay comparison');
    const comparison = JSON.parse(run([...bisect[1].split(/\s+/), '--json'], project));
    assert.equal(comparison.baseline, 'before.json', 'positional snapshot is the baseline');
    assert.equal(comparison.replayed, 'after.json', '--bisect snapshot is the comparison');
    const regrade = JSON.parse(run(['grade', 'evals/inventory/eval.yaml', '--results', 'results.json', '--workspace', 'evals/inventory/fixtures', '--output', 'regraded.json'], project));
    assert.equal(regrade.passed, false, 'grade exit zero is not a passing assessment');
    const regraded = JSON.parse(readFileSync(join(project, 'regraded.json'), 'utf8'));
    assert.ok(regraded.tasks[0].runs[0].evidence);

    const quickStart = read('site/src/content/docs/quick-start.mdx');
    assert.match(quickStart, /Create `evals\/docs-smoke\/tasks\/docs-probe\.yaml`/);
    assert.match(quickStart, /Create `evals\/docs-smoke\/eval\.yaml`/);
    assert.match(quickStart, /waza run evals\/docs-smoke\/eval\.yaml --output smoke\.json/);
    assert.doesNotMatch(quickStart, /evals\/inventory\/(?:tasks\/docs-probe\.yaml|smoke\.yaml)/);
    const snippets = blocks('site/src/content/docs/quick-start.mdx');
    const task = snippets.find((block) => block.startsWith('id: docs-probe'));
    const suite = snippets.find((block) => block.includes('name: docs-smoke'));
    assert.ok(task && suite, 'Quick Start must contain complete task and suite YAML');
    const inventoryTasks = () => readdirSync(join(project, 'evals/inventory/tasks')).sort()
      .map((name) => [name, readFileSync(join(project, 'evals/inventory/tasks', name), 'utf8')]);
    const originalTasks = inventoryTasks();
    mkdirSync(join(project, 'evals/docs-smoke/tasks'), { recursive: true });
    writeFileSync(join(project, 'evals/docs-smoke/tasks/docs-probe.yaml'), task);
    const suitePath = join(project, 'evals/docs-smoke/eval.yaml');
    writeFileSync(suitePath, suite);
    run(['preflight', suitePath], project);
    run(['run', suitePath, '--output', 'smoke.json'], project);
    writeFileSync(suitePath, replaceExactlyOnce(suite, '^Mock response for:', '^IMPOSSIBLE_DOCS_SENTINEL$'));
    assert.match(run(['run', suitePath], project, 1), /pattern|regex|FAIL/i);
    writeFileSync(suitePath, replaceExactlyOnce(suite, 'regex_match:', 'pattern:'));
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
    writeFileSync(suitePath, replaceExactlyOnce(withGrader(prompt), 'executor: mock', 'executor: copilot-sdk'));
    run(['preflight', suitePath], project);
    assert.deepEqual(inventoryTasks(), originalTasks, 'smoke controls must not change the inventory task set');
    const gettingStarted = read('site/src/content/docs/getting-started.mdx');
    assert.match(gettingStarted, /if \[ ! -f evals\/inventory\/eval\.yaml \]; then\n  waza new eval inventory --scenario --template repository\nfi/);
    const realRun = gettingStarted.indexOf('waza run evals/inventory/eval.yaml');
    for (const prerequisite of ['**Before running**', 'restore\n`config.executor: copilot-sdk`']) {
      const position = gettingStarted.indexOf(prerequisite);
      assert.ok(position >= 0 && realRun > position, `Real execution must follow ${prerequisite}`);
    }
    const beforeContinuation = readFileSync(generated, 'utf8');
    if (!existsSync(generated)) run(['new', 'eval', 'inventory', '--scenario', '--template', 'repository'], project);
    assert.equal(readFileSync(generated, 'utf8'), beforeContinuation, 'Getting Started must reuse the Quick Start scaffold');
    writeFileSync(generated, replaceExactlyOnce(beforeContinuation, '\n  executor: mock\n', '\n  executor: copilot-sdk\n'));
    run(['preflight', generated], project);
    assert.deepEqual(inventoryTasks(), originalTasks);
    run(['init', 'continuation-only', '--no-skill'], scratch);
    const continuation = join(scratch, 'continuation-only');
    if (!existsSync(join(continuation, 'evals/inventory/eval.yaml'))) {
      run(['new', 'eval', 'inventory', '--scenario', '--template', 'repository'], continuation);
    }
    run(['preflight', 'evals/inventory/eval.yaml'], continuation);
    run(['adversarial', '--engine', 'mock', '--packs', 'prompt-injection,scope-bypass'], project, 2);
    run(['adversarial', '--engine', 'mock', '--packs', 'prompt-injection,scope-bypass', '--on-unsafe-outcome', 'warn'], project);
  } finally {
    fixture.cleanup();
  }
});

test('multi-turn examples preflight both modes and execute scripted offline controls', () => {
  const fixture = allocateExampleFixture(root);
  const run = cliRunner(fixture.env);
  const snippets = blocks('site/src/content/docs/guides/eval-yaml.mdx');
  const suite = snippets.find((block) => block.startsWith('name: multi-turn-demo'));
  const scripted = snippets.find((block) => block.startsWith('id: scripted-preferences'));
  const adaptive = snippets.find((block) => block.startsWith('id: adaptive-preferences'));
  assert.ok(suite && scripted && adaptive, 'publish complete eval and both task files');
  const project = fixture.directory;
  try {
    mkdirSync(join(project, 'tasks'));
    writeFileSync(join(project, 'eval.yaml'), suite);
    writeFileSync(join(project, 'tasks/scripted.yaml'), scripted);
    writeFileSync(join(project, 'tasks/adaptive.yaml'), adaptive);
    run(['preflight', 'eval.yaml'], project);
    const mockSuite = replaceExactlyOnce(suite, 'executor: copilot-sdk', 'executor: mock');
    writeFileSync(join(project, 'eval.yaml'), mockSuite);
    run(['run', 'eval.yaml', '--task', 'Scripted preferences', '--output', 'scripted-smoke.json'], project, 1);
    const result = JSON.parse(readFileSync(join(project, 'scripted-smoke.json'), 'utf8'));
    assert.deepEqual(result.tasks.map((task) => [task.test_id, task.status]), [['scripted-preferences', 'failed']]);
    assert.match(result.tasks[0].runs[0].final_output, /My favorite animal is a cat/);
    writeFileSync(join(project, 'eval.yaml'), replaceExactlyOnce(
      mockSuite, 'regex_match: ["(?i)blue", "(?i)cat"]', 'regex_match: ["^Mock response for:"]',
    ));
    run(['run', 'eval.yaml', '--task', 'Scripted preferences'], project);
    writeFileSync(join(project, 'eval.yaml'), suite);
    writeFileSync(join(project, 'tasks/scripted.yaml'), replaceExactlyOnce(
      scripted, '  follow_up_prompts:', '  responder:\n    instructions: "Answer from the supplied facts."\n    max_followups: 3\n  follow_up_prompts:',
    ));
    run(['preflight', 'eval.yaml'], project, 1);
  } finally {
    fixture.cleanup();
  }
});

test('all published explanation walkthroughs execute only their selected task, including negative controls', () => {
  const fixture = allocateExampleFixture(root);
  const run = cliRunner(fixture.env);
  const canonical = 'site/src/content/docs/getting-started.mdx';
  try {
    for (const [index, path] of [canonical, 'docs/GETTING-STARTED.md', 'site/src/content/docs/index.mdx'].entries()) {
      const name = `walkthrough-${index}`;
      run(['init', name, '--no-skill'], fixture.directory);
      const project = join(fixture.directory, name);
      run(['new', 'skill', 'code-explainer'], project);
      const suite = blocks(path).find((block) => block.startsWith('name: code-explainer-eval'));
      const task = blocks(path).find((block) => block.startsWith('id: basic-usage-001'))
        ?? blocks(canonical).find((block) => block.startsWith('id: basic-usage-001'));
      const python = [...read(path).matchAll(/```python\n([\s\S]*?)```/g)][0]?.[1]
        ?? [...read(canonical).matchAll(/```python\n([\s\S]*?)```/g)][0]?.[1];
      assert.ok(suite && task && python, path);
      assert.match(suite, /executor: mock/);
      assert.match(suite, /tasks:\n  - "tasks\/basic-usage\.yaml"/);
      assert.match(read(path), /1\/1 selected tasks passed/);
      const tasksDirectory = join(project, 'evals/code-explainer/tasks');
      const originalTasks = readdirSync(tasksDirectory).sort();
      assert.ok(originalTasks.length > 1, 'retain unrelated scaffolded tasks to catch accidental wildcard selection');
      writeFileSync(join(tasksDirectory, 'basic-usage.yaml'), task);
      writeFileSync(join(project, 'evals/code-explainer/fixtures/sample.py'), python);
      const destination = join(project, 'evals/code-explainer/eval.yaml');
      writeFileSync(destination, suite);
      run(['preflight', destination], project);
      for (const args of [[], ['code-explainer'], ['code-explainer', '-v'], ['code-explainer', '-o', 'results.json']]) {
        run(['run', ...args], project);
      }
      const result = JSON.parse(readFileSync(join(project, 'results.json'), 'utf8'));
      assert.deepEqual(result.tasks.map((task) => [task.test_id, task.status]), [['basic-usage-001', 'passed']], path);
      assert.deepEqual(readdirSync(tasksDirectory).sort(), originalTasks);
      const predicates = [...suite.matchAll(/regex_match: \[[^\n]+\]/g)];
      assert.equal(predicates.length, 1);
      writeFileSync(destination, replaceExactlyOnce(suite, predicates[0][0], 'regex_match: ["^IMPOSSIBLE_EXPLANATION_SENTINEL$"]'));
      run(['run', 'code-explainer', '-o', 'negative.json'], project, 1);
      const negative = JSON.parse(readFileSync(join(project, 'negative.json'), 'utf8'));
      assert.deepEqual(negative.tasks.map((task) => [task.test_id, task.status]), [['basic-usage-001', 'failed']], path);
    }
  } finally {
    fixture.cleanup();
  }
});

test('adversarial guide executes every offline command and retains actual source binding', () => {
  const fixture = allocateExampleFixture(root);
  const run = cliRunner(fixture.env);
  try {
    const guidePath = 'site/src/content/docs/guides/adversarial.mdx';
    const spec = blocks(guidePath).find((block) => block.startsWith('schemaVersion: "1.2"'));
    assert.ok(spec, 'guide must publish the complete offline spec');
    assert.match(spec, /executor: mock/);
    writeFileSync(join(fixture.directory, 'eval.yaml'), spec);
    let offlineCommands = 0;
    let realCommands = 0;
    for (const [, fence] of read(guidePath).matchAll(/^```[^\n]*\n([\s\S]*?)^```/gm)) {
      const commands = [...fence.matchAll(/^\s*(?:run:\s*)?(waza adversarial [^\n]+)$/gm)].map((match) => match[1]);
      const engines = commands.map((command) => {
        const selections = [...command.matchAll(/--engine (\S+)/g)];
        assert.equal(selections.length, 1, `refuse an implicit or ambiguous engine: ${command}`);
        return selections[0][1];
      });
      assert.ok(engines.every((engine) => engine === 'mock' || engine === 'copilot-sdk'));
      assert.ok(new Set(engines).size <= 1, 'one copyable fence must not mix mock and real execution');
      for (const [index, command] of commands.entries()) {
        if (engines[index] === 'copilot-sdk') {
          realCommands++;
          continue;
        }
        offlineCommands++;
        const args = command.slice('waza '.length).split(/\s+/);
        run(args, fixture.directory, args.includes('warn') ? 0 : 2);
        const outputIndex = args.indexOf('--output');
        if (outputIndex >= 0) {
          const output = JSON.parse(readFileSync(join(fixture.directory, args[outputIndex + 1]), 'utf8'));
          const packsIndex = args.indexOf('--packs');
          assert.equal(output.tasks.length, packsIndex >= 0 ? 4 * args[packsIndex + 1].split(',').length : 8);
          assert.ok(output.tasks.every((task) => task.status === 'failed'), 'mock unsafe observations are not skill-quality passes');
        }
      }
    }
    assert.equal(offlineCommands, 6, 'execute all shell and CI-YAML mock commands, including --spec');
    assert.equal(realCommands, 1, 'extract but never execute the separate SDK example');
    const helper = join(fixture.directory, process.platform === 'win32' ? 'skill-discovery.exe' : 'skill-discovery');
    const build = spawnSync('go', ['build', '-o', helper, './site/tests/helpers/skill-discovery.go'], {
      cwd: root, env: fixture.env, encoding: 'utf8', timeout: 90_000,
    });
    assert.equal(build.error, undefined);
    assert.equal(build.status, 0, build.stderr);
    const missingArguments = spawnSync(helper, [], { env: fixture.env, encoding: 'utf8', timeout: 10_000 });
    assert.equal(missingArguments.error, undefined);
    assert.equal(missingArguments.status, 1);
    assert.match(missingArguments.stderr, /usage: skill-discovery/);
    const discover = (specPath, status = 0) => {
      const result = spawnSync(helper, [specPath], { env: fixture.env, encoding: 'utf8', timeout: 10_000 });
      assert.equal(result.error, undefined);
      assert.equal(result.status, status, result.stderr);
      return result.stdout.trim();
    };
    for (const [index, path] of ['README.md', 'site/src/content/docs/guides/adversarial.mdx'].entries()) {
      const command = read(path).match(/\(\n  cd ([^\s]+) &&\n  waza adversarial --engine copilot-sdk --packs ([^\s]+) --skill ([^\s]+) --model ([^\s]+)\n\)/);
      assert.ok(command, `${path}: real-engine example must bind the skill directory and declared name`);
      const [, directory, pack, name] = command;
      assert.equal(directory, 'skills/my-skill');
      assert.equal(name, 'my-skill');
      assert.equal(pack, 'prompt-injection');
      assert.doesNotMatch(read(path), /--skill \.\/skills\/my-skill/);
      run(['init', `adversarial-${index}`, '--no-skill'], fixture.directory);
      const project = join(fixture.directory, `adversarial-${index}`);
      run(['new', 'skill', name], project);
      const skillDirectory = join(project, directory);
      assert.match(readFileSync(join(skillDirectory, 'SKILL.md'), 'utf8'), /^name: my-skill$/m);
      const artifacts = join(fixture.directory, `adversarial-artifacts-${index}`);
      run(['adversarial', '--engine', 'mock', '--packs', pack, '--skill', name, '--artifacts-dir', artifacts, '--output', 'mock-results.json'], skillDirectory, 2);
      const generatedSpec = join(artifacts, 'eval.yaml');
      assert.match(readFileSync(generatedSpec, 'utf8'), /^skill: my-skill$/m);
      assert.equal(discover(generatedSpec), skillDirectory, 'only command-generated config may supply discovery paths');
      const result = JSON.parse(readFileSync(join(skillDirectory, 'mock-results.json'), 'utf8'));
      assert.equal(result.tasks.length, 4);
      const skillFile = join(skillDirectory, 'SKILL.md');
      const definition = readFileSync(skillFile, 'utf8');
      writeFileSync(skillFile, replaceExactlyOnce(definition, 'name: my-skill', 'name: different-skill'));
      discover(generatedSpec, 1);
      unlinkSync(skillFile);
      discover(generatedSpec, 1);
    }
    discover(join(fixture.directory, 'missing-spec.yaml'), 1);
  } finally {
    fixture.cleanup();
  }
});

test('schema outcome snippet accepts alternatives and rejects wrong state/forbidden file', () => {
  const fixture = allocateExampleFixture(root);
  const directory = fixture.directory;
  const run = cliRunner(fixture.env);
  mkdirSync(join(directory, 'tasks'), { recursive: true });
  mkdirSync(join(directory, 'fixtures'), { recursive: true });
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
    fixture.cleanup();
  }
});

test('inherited executable cannot bypass the directory runtime guard', () => {
  const fixture = allocateExampleFixture(root);
  try {
    const inherited = join(fixture.directory, 'inherited-copilot');
    const marker = join(fixture.directory, 'inherited-runtime-was-used');
    writeFileSync(inherited, `#!/usr/bin/env node\nrequire('node:fs').writeFileSync(${JSON.stringify(marker)}, 'used');\nprocess.exit(97);\n`, { mode: 0o700 });
    accessSync(inherited, constants.X_OK);
    const directory = join(fixture.directory, 'guard-probe');
    mkdirSync(directory);
    const env = offlineEnvironment(directory, { ...process.env, COPILOT_CLI_PATH: inherited });
    assert.notEqual(env.COPILOT_CLI_PATH, inherited);
    assert.ok(statSync(env.COPILOT_CLI_PATH).isDirectory());
    writeFileSync(join(directory, 'eval.yaml'), 'schemaVersion: "2.0"\nname: guard-probe\nscenario: Guard rejection\nconfig:\n  executor: copilot-sdk\n  model: mock\n  trials_per_task: 1\n  timeout_seconds: 1\nmetrics:\n  - name: task_completion\n    weight: 1\n    threshold: 1\ntasks:\n  - task.yaml\n');
    writeFileSync(join(directory, 'task.yaml'), 'id: guard-probe\nname: Guard probe\ninputs:\n  prompt: Never execute an agent.\n');
    const result = spawnSync(binary, ['run', 'eval.yaml'], { cwd: directory, env, encoding: 'utf8', timeout: 10_000 });
    assert.equal(result.error, undefined);
    assert.notEqual(result.status, 0);
    assert.match(`${result.stdout}\n${result.stderr}`, /COPILOT_CLI_PATH.*path is a directory/);
    assert.equal(existsSync(marker), false);
    assert.throws(() => replaceExactlyOnce('executor: future', 'executor: copilot-sdk', 'executor: mock'));
    assert.throws(() => replaceExactlyOnce('executor: copilot-sdk\nexecutor: copilot-sdk', 'executor: copilot-sdk', 'executor: mock'));
  } finally {
    fixture.cleanup();
  }
});

test('canonical fixture ancestry rejects project markers and ignores inherited TMPDIR', () => {
  const fixture = allocateExampleFixture(root);
  try {
    for (const marker of ['skills', '.waza.yaml', '.git']) {
      const parent = join(fixture.directory, marker.replace(/\W/g, '') || 'marker');
      mkdirSync(join(parent, 'nested'), { recursive: true });
      writeFileSync(join(parent, marker), '');
      assert.throws(() => validateFixtureAncestry(join(parent, 'nested')), /inherits project marker/);
    }
    const alias = join(fixture.directory, 'checkout-alias');
    symlinkSync(root, alias, process.platform === 'win32' ? 'junction' : 'dir');
    assert.throws(() => validateFixtureAncestry(alias), /inherits project marker/);
    assert.throws(() => allocateExampleFixture(root, { ...process.env, WAZA_DOCS_FIXTURE_ROOT: alias }), /inherits project marker/);
    const external = allocateExampleFixture(root, { ...process.env, TMPDIR: root, TMP: root, TEMP: root });
    try {
      validateFixtureAncestry(external.directory);
      assert.equal(realpathSync(external.env.TMPDIR), join(external.directory, 'workspaces'));
      assert.notEqual(external.env.TMPDIR, root);
      assert.equal(external.env.TMP, external.env.TMPDIR);
      assert.equal(external.env.TEMP, external.env.TMPDIR);
    } finally {
      external.cleanup();
    }
  } finally {
    fixture.cleanup();
  }
});
