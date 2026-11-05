const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const { pathToFileURL } = require('node:url');
const { createHash } = require('node:crypto');

const root = fs.realpathSync(path.resolve(__dirname, '../..'));
const files = ['registry.json', 'history.json', 'registry-history.commit.json'];

test('all delivered casting mutation procedures point to the paired writer', () => {
  for (const file of [
    '.github/agents/squad.agent.md',
    '.github/skills/coordinator-init-mode/SKILL.md',
    '.github/skills/coordinator-source-of-truth/SKILL.md',
    '.squad/templates/squad.agent.md.template',
    '.squad/templates/casting-reference.md',
    '.squad/templates/workflow-wiring-guide.md',
    '.squad/templates/workflow-wiring-appendix-a-code-reviewer.md',
    '.squad/templates/workflow-wiring-appendix-b-documenter.md',
  ]) {
    const content = fs.readFileSync(path.join(root, file), 'utf8');
    assert.ok(content.includes('.squad/casting-transactions.md'), file);
    assert.doesNotMatch(content, /Update `\.squad\/casting\/registry\.json` with the new entry/);
    assert.doesNotMatch(content, /Run `squad upgrade` to regenerate Team Capabilities/);
  }
});

test('casting procedures publish actual locked SDK transactions', {
  skip: !process.env.SQUAD_SDK_READER && 'Set SQUAD_SDK_READER to the verified Squad 1.0.0 reader',
}, async (t) => {
  const { updateCastingPair } = await import(pathToFileURL(path.join(__dirname, 'squad-casting.mjs')).href);
  const reader = path.resolve(process.env.SQUAD_SDK_READER);
  const sdk = await import(pathToFileURL(reader).href);
  const { parseAgentProvenanceRegistry } = await import(
    pathToFileURL(path.join(path.dirname(reader), 'agent-provenance.js')).href);
  const fixture = (empty = false) => {
    const directory = fs.mkdtempSync(path.join(root, '.squad-casting-test-'));
    t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
    if (!empty) for (const file of files) {
      fs.copyFileSync(path.join(root, '.squad/casting', file), path.join(directory, file));
    }
    return directory;
  };
  const accepted = (directory) => {
    const pair = sdk.readCastingRegistryPair(directory, 1);
    const parsed = parseAgentProvenanceRegistry(pair.registry);
    assert.equal(parsed.completeness, 'complete');
    assert.deepEqual(parsed.diagnostics, []);
    return pair;
  };
  const add = (registry, history) => {
    const timestamp = registry.generated_at;
    registry.agents.reuben = {
      display_name: 'Reuben', persistent_name: 'Reuben', role: 'Infrastructure Support',
      universe: "Ocean's Eleven", status: 'active', legacy_named: false,
      created_at: timestamp, updated_at: timestamp,
    };
    history.assignment_cast_snapshots[`member-add-r${registry.revision}`] = {
      universe: "Ocean's Eleven", created_at: timestamp, agents: ['reuben'],
    };
    history.universe_usage_history.push({ universe: "Ocean's Eleven", used_at: timestamp });
  };

  await t.test('raw registry-only status change reproduces manifest rejection', () => {
    const directory = fixture();
    const registry = JSON.parse(fs.readFileSync(path.join(directory, 'registry.json')));
    registry.agents.linus.status = 'inactive';
    fs.writeFileSync(path.join(directory, 'registry.json'), `${JSON.stringify(registry, null, 2)}\n`);
    assert.throws(() => sdk.readCastingRegistryPair(directory, 1),
      /bytes do not match the stable commit manifest/);
  });
  await t.test('add, role change and retirement retain accepted pair/provenance and history', async () => {
    const directory = fixture();
    const original = accepted(directory);
    const added = await updateCastingPair(directory, add, reader);
    assert.equal(added.registry.revision, original.registry.revision + 1);
    assert.notEqual(added.transactionId, original.transactionId);
    assert.equal(accepted(directory).registry.agents.reuben.status, 'active');
    const updated = await updateCastingPair(directory, (registry) => {
      registry.agents.reuben.role = 'Release Support';
      registry.agents.reuben.updated_at = registry.generated_at;
    }, reader);
    assert.equal(accepted(directory).registry.agents.reuben.role, 'Release Support');
    const retired = await updateCastingPair(directory, (registry) => {
      const member = registry.agents.reuben;
      member.status = 'retired';
      member.updated_at = registry.generated_at;
      member.retired_at = registry.generated_at;
    }, reader);
    assert.equal(retired.registry.revision, updated.registry.revision + 1);
    assert.equal(accepted(directory).registry.agents.reuben.status, 'retired');
    for (const [key, snapshot] of Object.entries(original.history.assignment_cast_snapshots)) {
      assert.deepEqual(retired.history.assignment_cast_snapshots[key], snapshot);
    }
    assert.equal(retired.history.universe_usage_history.length,
      original.history.universe_usage_history.length + 1);
    assert.ok(retired.registry.agents.reuben, 'retirement reserves the identity');
  });
  await t.test('initialization uses the same paired writer, not raw JSON creation', async () => {
    const directory = fixture(true);
    await updateCastingPair(directory, add, reader);
    assert.equal(accepted(directory).registry.revision, 1);
  });
  await t.test('documented mutation example and reference schemas are accepted by the real SDK', async () => {
    const directory = fixture();
    const guide = fs.readFileSync(path.join(root, '.squad/casting-transactions.md'), 'utf8')
      .replace(/\r\n/g, '\n');
    const example = guide.match(/```js\n([\s\S]+?)\n```/)[1]
      .replace(/^import .*;\n/m, '').replace("'.squad/casting'", 'directory');
    const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
    await new AsyncFunction('updateCastingPair', 'directory', example)(
      (dir, mutate) => updateCastingPair(dir, mutate, reader), directory);
    assert.equal(accepted(directory).registry.agents.reuben.status, 'active');
    const reference = fs.readFileSync(path.join(root, '.squad/templates/casting-reference.md'), 'utf8')
      .replace(/\r\n/g, '\n');
    const examples = [...reference.matchAll(/```json\n([\s\S]+?)\n```/g)].map((match) => JSON.parse(match[1]));
    const registry = examples.find((value) => value.schema === 'squad-agent-provenance/v1');
    const history = examples.find((value) => value.assignment_cast_snapshots);
    assert.equal(parseAgentProvenanceRegistry(registry).completeness, 'complete');
    sdk.validateCastingRegistryPairForCommit(registry, history, registry.revision);
  });
  const legacyFiles = ['registry.json', 'history.json', 'policy.json'];
  const legacyBytes = Object.fromEntries(legacyFiles.map((file) => [
    file, fs.readFileSync(path.join(root, '.squad/casting/legacy-v0.10.0', file)),
  ]));
  const migrationFixture = (managed = false) => {
    const parent = fixture(true);
    const active = path.join(parent, 'active');
    const archive = path.join(parent, 'archive');
    fs.mkdirSync(active);
    for (const file of managed ? [...files, 'policy.json'] : legacyFiles) {
      fs.writeFileSync(path.join(active, file), managed ?
        fs.readFileSync(path.join(root, '.squad/casting', file)) : legacyBytes[file]);
    }
    return { active, archive };
  };
  const migrationExample = fs.readFileSync(path.join(root, '.squad/casting-transactions.md'), 'utf8')
    .replace(/\r\n/g, '\n').split('## Archive and initialize the known v0.10 state')[1]
    .match(/```js\n([\s\S]+?)\n```/)[1]
    .replace(/^import .*;\n/gm, '')
    .replace(/^const active = .*;\n/m, '')
    .replace(/^const archive = .*;\n/m, '');
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  const runMigration = new AsyncFunction('updateCastingPair', 'active', 'archive',
    'fs', 'assert', 'createHash', migrationExample);

  await t.test('frozen v0.10 files reject before an in-place normalization callback', async () => {
    const { active } = migrationFixture();
    let callbackCalled = false;
    await assert.rejects(updateCastingPair(active, () => { callbackCalled = true; }, reader),
      /registry revision is invalid/);
    assert.equal(callbackCalled, false);
    for (const file of legacyFiles) {
      assert.deepEqual(fs.readFileSync(path.join(active, file)), legacyBytes[file]);
    }
  });
  await t.test('documented frozen archive-and-initialize recipe commits into a genuinely empty target', async () => {
    const { active, archive } = migrationFixture();
    let writerCalled = false;
    await runMigration(async (directory, mutate) => {
      assert.deepEqual(fs.readdirSync(directory), []);
      writerCalled = true;
      return updateCastingPair(directory, mutate, reader);
    }, active, archive, fs, assert, createHash);
    assert.equal(writerCalled, true);
    const pair = accepted(active);
    assert.equal(pair.registry.revision, 1);
    const legacy = JSON.parse(legacyBytes['registry.json']);
    assert.deepEqual(Object.keys(pair.registry.agents).sort(), Object.keys(legacy.agents).sort());
    for (const [id, member] of Object.entries(legacy.agents)) {
      assert.equal(pair.registry.agents[id].display_name, member.persistent_name);
      assert.equal(pair.registry.agents[id].created_at, member.created_at);
      assert.equal(pair.registry.agents[id].legacy_named, true);
    }
    const history = JSON.parse(legacyBytes['history.json']);
    assert.deepEqual(pair.history.assignment_cast_snapshots['2026-02-18'].agents,
      history.assignment_cast_snapshots['2026-02-18'].cast);
    assert.equal(pair.history.universe_usage_history[0].used_at,
      history.universe_usage_history[0].timestamp);
    for (const file of legacyFiles) {
      assert.deepEqual(fs.readFileSync(path.join(archive, 'casting', file)), legacyBytes[file]);
      assert.deepEqual(fs.readFileSync(path.join(root, '.squad/casting/legacy-v0.10.0', file)),
        legacyBytes[file], 'frozen reference must remain unchanged');
    }
    assert.deepEqual(fs.readFileSync(path.join(active, 'policy.json')), legacyBytes['policy.json']);
  });
  for (const missingManifest of [false, true]) {
    await t.test(`corrupt managed state is not legacy (${missingManifest ? 'missing manifest' : 'stale hashes'})`, async () => {
      const { active, archive } = migrationFixture(true);
      if (missingManifest) fs.unlinkSync(path.join(active, files[2]));
      else fs.appendFileSync(path.join(active, files[0]), ' ');
      const before = Object.fromEntries(fs.readdirSync(active).map((file) =>
        [file, fs.readFileSync(path.join(active, file))]));
      let writerCalled = false;
      await assert.rejects(runMigration(() => { writerCalled = true; },
        active, archive, fs, assert, createHash), /Not the supported frozen v0.10/);
      assert.equal(writerCalled, false);
      assert.equal(fs.existsSync(archive), false);
      for (const [file, bytes] of Object.entries(before)) {
        assert.deepEqual(fs.readFileSync(path.join(active, file)), bytes);
      }
      assert.throws(() => sdk.readCastingRegistryPair(active, 1), /consistent casting registry\/history pair/);
    });
  }
  await t.test('archive-and-initialize refuses an existing archive without moving legacy input', async () => {
    const { active, archive } = migrationFixture();
    fs.mkdirSync(archive);
    fs.writeFileSync(path.join(archive, 'sentinel'), 'owned by someone else');
    await assert.rejects(runMigration(() => assert.fail('writer must not run'),
      active, archive, fs, assert, createHash), /EEXIST/);
    assert.equal(fs.readFileSync(path.join(archive, 'sentinel'), 'utf8'), 'owned by someone else');
    for (const file of legacyFiles) {
      assert.deepEqual(fs.readFileSync(path.join(active, file)), legacyBytes[file]);
    }
  });
  await t.test('corrupted committed input fails closed without repairing its manifest', async () => {
    const directory = fixture();
    const manifest = fs.readFileSync(path.join(directory, files[2]));
    fs.appendFileSync(path.join(directory, files[0]), ' ');
    await assert.rejects(updateCastingPair(directory, add, reader), /stable commit manifest/);
    assert.deepEqual(fs.readFileSync(path.join(directory, files[2])), manifest);
  });
  for (const [name, mutation, expected] of [
    ['callback failure', () => { throw new Error('stop mutation'); }, /stop mutation/],
    ['asynchronous callback', () => Promise.resolve(), /must be synchronous/],
    ['invalid member provenance', (registry) => { registry.agents.bad = {}; }, /Invalid casting provenance/],
    ['invalid history', (_, history) => { history.unexpected = true; }, /unexpected fields/],
  ]) {
    await t.test(`${name} leaves original bytes intact and releases the lock`, async () => {
      const directory = fixture();
      const before = files.map((file) => fs.readFileSync(path.join(directory, file)));
      await assert.rejects(updateCastingPair(directory, mutation, reader), expected);
      files.forEach((file, index) => assert.deepEqual(fs.readFileSync(path.join(directory, file)), before[index]));
      await updateCastingPair(directory, add, reader);
      accepted(directory);
    });
  }
  await t.test('missing or wrong-version SDK is rejected before mutation', async () => {
    const directory = fixture();
    await assert.rejects(updateCastingPair(directory, add, ''), /Set SQUAD_SDK_READER/);
    const fakeRoot = fixture(true);
    fs.mkdirSync(path.join(fakeRoot, 'dist/casting'), { recursive: true });
    fs.writeFileSync(path.join(fakeRoot, 'package.json'),
      JSON.stringify({ name: '@bradygaster/squad-sdk', version: '0.10.0' }));
    await assert.rejects(updateCastingPair(directory, add,
      path.join(fakeRoot, 'dist/casting/durable-registry.js')), /require the verified/);
    accepted(directory);
  });
});
