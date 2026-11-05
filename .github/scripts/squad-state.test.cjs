const assert = require('node:assert/strict');
const { execFileSync } = require('node:child_process');
const { createHash } = require('node:crypto');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');

const root = path.resolve(__dirname, '../..');
const casting = '.squad/casting';
const files = ['registry.json', 'history.json', 'registry-history.commit.json'];
const read = (file) => fs.readFileSync(path.join(root, file), 'utf8');

function assertPair(directory) {
  const manifest = JSON.parse(fs.readFileSync(path.join(directory, files[2]), 'utf8'));
  for (const [file, key] of [['registry.json', 'registry'], ['history.json', 'history']]) {
    const bytes = fs.readFileSync(path.join(directory, file));
    assert.equal(createHash('sha256').update(bytes).digest('hex'), manifest[`${key}_sha256`]);
    const state = JSON.parse(bytes);
    assert.equal(state.transaction_id, manifest.transaction_id);
    assert.equal(state[key === 'registry' ? 'revision' : 'registry_revision'], manifest.registry_revision);
  }
}

test('casting pair matches its committed transaction and byte hashes', () => {
  assertPair(path.join(root, casting));
});

test('all 14 role models agree across config, active charters and roster', () => {
  const config = JSON.parse(read('.squad/config.json'));
  const roster = read('.squad/team.md');
  assert.equal(Object.keys(config.agentModelOverrides).length, 14);
  for (const [member, model] of Object.entries(config.agentModelOverrides)) {
    const charter = `.squad/agents/${member}/charter.md`;
    assert.ok(read(charter).includes(model), `${charter}: missing ${model}`);
    const row = roster.split('\n').find((line) => line.includes(`\`${charter}\``));
    assert.ok(row?.includes(`\`${model}\``), `${member}: roster model mismatch`);
  }
  assert.match(read('.squad/model-policy.md'), /Code review:\*\* use `gpt-5\.6-terra`/);
  assert.match(read('.github/agents/squad.agent.md'), /SQUAD_COORDINATOR_CANARY_a8f3/);
});

test('Tess media ownership preserves role routing and generated capabilities', () => {
  const config = JSON.parse(read('.squad/config.json'));
  const registry = JSON.parse(read(`${casting}/registry.json`));
  assert.equal(registry.agents.tess.role, 'Release Media Producer');
  assert.equal(registry.agents.tess.status, 'active');
  assert.equal(config.agentModelOverrides.tess, 'gpt-6.1-sol');
  assert.equal(config.agentModelOverrides.danny, 'gpt-6-luna');
  assert.equal(config.agentModelOverrides.rusty, 'gpt-5.6-terra');
  assert.equal(config.agentModelOverrides.saul, 'gpt-6-luna');
  assert.equal(config.taskModelOverrides, undefined);
  for (const file of ['.github/agents/squad.agent.md', '.squad/templates/squad.agent.md.template']) {
    const source = read(file);
    const capabilities = source.split('<!-- SQUAD:TEAM-CAPABILITIES:BEGIN -->')[1]
      .split('<!-- SQUAD:TEAM-CAPABILITIES:END -->')[0];
    assert.match(capabilities, /\| Tess \| Release Media Producer \|/);
    assert.match(capabilities, /\| Release media \/ capture \/ video exports \| Tess \|/);
    assert.match(source, /SQUAD_COORDINATOR_CANARY_a8f3/);
  }
  assert.match(read('.github/agents/squad.agent.md'),
    /Code-review assignments[\s\S]*?gpt-5\.6-terra/);
  assert.match(read('.squad/agents/tess/charter.md'),
    /Adding this identity does not clear author\/reviewer lockouts/);
});

test('Git autocrlf checkout preserves the hash-bound casting pair', async (t) => {
  // Keep scratch state inside this checkout, including on native Windows runners.
  const scratch = fs.mkdtempSync(path.join(fs.realpathSync(root), '.squad-checkout-test-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true }));
  const source = path.join(scratch, 'source');
  const checkout = path.join(scratch, 'checkout');
  fs.mkdirSync(path.join(source, casting), { recursive: true });
  fs.mkdirSync(checkout);
  fs.copyFileSync(path.join(root, '.gitattributes'), path.join(source, '.gitattributes'));
  fs.writeFileSync(path.join(source, 'conversion-control.txt'), 'first\nsecond\n');
  for (const file of files) {
    fs.copyFileSync(path.join(root, casting, file), path.join(source, casting, file));
  }
  const git = (...args) => execFileSync('git', args, {
    cwd: source,
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  git('init', '--quiet');
  git('-c', 'core.autocrlf=false', 'add', '.');
  git('-c', 'core.autocrlf=true', 'checkout-index', '--all', '--force',
    `--prefix=${checkout.replaceAll('\\', '/')}/`);
  assert.equal(fs.readFileSync(path.join(checkout, 'conversion-control.txt'), 'utf8'),
    'first\r\nsecond\r\n', 'control must prove Git performed CRLF conversion');
  const checkedCasting = path.join(checkout, casting);
  for (const file of files.slice(0, 2)) {
    assert.deepEqual(fs.readFileSync(path.join(checkedCasting, file)),
      fs.readFileSync(path.join(root, casting, file)));
  }
  assertPair(checkedCasting);

  await t.test('pinned Squad SDK accepts checkout and rejects CRLF corruption', {
    skip: !process.env.SQUAD_SDK_READER && 'Set SQUAD_SDK_READER to the Squad 1.0.0 durable-registry.js',
  }, async () => {
    const reader = path.resolve(process.env.SQUAD_SDK_READER);
    const { readCastingRegistryPair } = await import(pathToFileURL(reader).href);
    const pair = readCastingRegistryPair(checkedCasting, 1);
    const { parseAgentProvenanceRegistry } = await import(
      pathToFileURL(path.join(path.dirname(reader), 'agent-provenance.js')).href);
    const provenance = parseAgentProvenanceRegistry(pair.registry);
    assert.equal(provenance.completeness, 'complete');
    assert.deepEqual(provenance.diagnostics, []);
    assert.equal(pair.transactionId,
      JSON.parse(read(`${casting}/${files[2]}`)).transaction_id);
    for (const file of files.slice(0, 2)) {
      fs.writeFileSync(path.join(checkedCasting, file),
        read(`${casting}/${file}`).replace(/\r?\n/g, '\r\n'));
    }
    assert.throws(() => readCastingRegistryPair(checkedCasting, 1),
      /bytes do not match the stable commit manifest/);

    // Reproduce the original defect through Git itself, not only string replacement.
    fs.writeFileSync(path.join(source, '.gitattributes'),
      read('.gitattributes').split('\n').filter((line) =>
        !/^\.squad\/casting\/(?:registry|history)\.json /.test(line)).join('\n'));
    git('-c', 'core.autocrlf=false', 'add', '.gitattributes');
    git('-c', 'core.autocrlf=true', 'checkout-index', '--all', '--force',
      `--prefix=${checkout.replaceAll('\\', '/')}/`);
    assert.match(fs.readFileSync(path.join(checkedCasting, 'registry.json'), 'utf8'), /\r\n/);
    assert.throws(() => readCastingRegistryPair(checkedCasting, 1),
      /bytes do not match the stable commit manifest/);
  });
});
