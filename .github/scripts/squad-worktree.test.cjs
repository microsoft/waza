const assert = require('node:assert/strict');
const { execFileSync, spawnSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const root = fs.realpathSync(path.resolve(__dirname, '../..'));

test('documented Squad regressions run in a genuine linked Git worktree', (t) => {
  const scratch = fs.mkdtempSync(path.join(root, '.squad-worktree-test-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const source = path.join(scratch, 'source');
  const linked = path.join(scratch, 'linked');
  fs.mkdirSync(source);
  fs.mkdirSync(path.join(scratch, 'no-hooks'));
  const config = JSON.parse(fs.readFileSync(path.join(root, '.squad/config.json'), 'utf8'));
  const files = [
    '.gitattributes',
    '.github/agents/squad.agent.md',
    '.github/scripts/ralph-triage.test.cjs',
    '.github/scripts/squad-state.test.cjs',
    '.squad/config.json',
    '.squad/team.md',
    '.squad/routing.md',
    '.squad/model-policy.md',
    '.squad/templates/ralph-triage.js',
    '.squad/casting/registry.json',
    '.squad/casting/history.json',
    '.squad/casting/registry-history.commit.json',
    ...Object.keys(config.agentModelOverrides).map((member) =>
      `.squad/agents/${member}/charter.md`),
  ];
  for (const file of files) {
    const destination = path.join(source, file);
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(path.join(root, file), destination);
  }
  const git = (...args) => execFileSync('git', args, {
    cwd: source,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
    timeout: 15000,
  });
  git('init', '--quiet');
  git('-c', 'core.autocrlf=false', 'add', '--force', '.');
  git('-c', 'user.name=Squad regression', '-c', 'user.email=regression@example.invalid',
    '-c', 'commit.gpgsign=false', '-c', `core.hooksPath=${path.join(scratch, 'no-hooks')}`,
    'commit', '--quiet', '-m', 'Linked-worktree regression fixture');
  git('-c', 'core.autocrlf=true', 'worktree', 'add', '--quiet', '--detach', linked, 'HEAD');
  assert.ok(fs.statSync(path.join(linked, '.git')).isFile());
  assert.equal(fs.realpathSync(git('-C', linked, 'rev-parse', '--show-toplevel').trim()),
    fs.realpathSync(linked));
  const env = { ...process.env };
  delete env.NODE_TEST_CONTEXT;
  if (env.SQUAD_SDK_READER) env.SQUAD_SDK_READER = path.resolve(env.SQUAD_SDK_READER);
  const result = spawnSync(process.execPath, [
    '--test',
    '--test-reporter=tap',
    '.github/scripts/ralph-triage.test.cjs',
    '.github/scripts/squad-state.test.cjs',
  ], { cwd: linked, env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 30000 });
  assert.equal(result.status, 0, `${result.error ?? ''}\n${result.stdout}\n${result.stderr}`);
  assert.match(result.stdout, /# tests 31/);
  assert.match(result.stdout, /# fail 0/);
  if (env.SQUAD_SDK_READER) assert.match(result.stdout, /# skipped 0/);
  t.diagnostic('Linked-worktree child suite passed with real Git metadata and autocrlf conversion');
});
