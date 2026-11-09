import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { bindMountArgs, runNamedContainer } from './native-lifecycle.mjs';

test('native probe mounts keep inputs read-only and evidence writable', () => {
  const mounts = [
    ['/native-bundle-probe', '/tmp/probe', false],
    ['/sdk-positive', '/tmp/positive', false],
    ['/input/copilot-unit.json', '/tmp/unit.json', false],
    ['/evidence', '/tmp/evidence', true],
  ];
  assert.deepEqual(bindMountArgs(mounts), [
    '--mount', 'type=bind,source=/tmp/probe,target=/native-bundle-probe,readonly',
    '--mount', 'type=bind,source=/tmp/positive,target=/sdk-positive,readonly',
    '--mount', 'type=bind,source=/tmp/unit.json,target=/input/copilot-unit.json,readonly',
    '--mount', 'type=bind,source=/tmp/evidence,target=/evidence',
  ]);
  assert.deepEqual(bindMountArgs([]), []);
});

const name = 'waza-native-test';
const id = 'a'.repeat(64);
const competitor = 'b'.repeat(64);
const ok = { status: 0, stdout: '', stderr: '' };

function exercise({ create = { ...ok, stdout: id }, start = ok, remove = ok,
  exit = 0, verify = () => 'verified' } = {}) {
  const calls = [];
  const containers = new Map([[competitor, { name }]]);
  const command = args => {
    calls.push(args);
    if (args[0] === 'create') {
      if (create instanceof Error) throw create;
      if (create.status === 0) containers.set(create.stdout, { name });
      return create;
    }
    assert.equal(args.at(-1), id, 'all post-create operations must use captured immutable ID');
    if (args[0] === 'start') {
      if (start instanceof Error) throw start;
      return start;
    }
    if (args[0] === 'inspect') return { ...ok,
      stdout: JSON.stringify([{ Id: id, State: { Running: false, ExitCode: exit } }]) };
    if (args[0] === 'rm') {
      if (remove.status === 0) containers.delete(id);
      return remove;
    }
    assert.fail(`unexpected Docker command ${args}`);
  };
  return { calls, containers, invoke: () => runNamedContainer(command, name,
    ['run', '--name', name, '-i', 'image'], { input: 'request' }, verify) };
}

test('create rejection and timeout report primary cause plus unverified residue without deletion', () => {
  for (const create of [{ ...ok, status: 125, stderr: 'Conflict: name already owned' },
    new Error('docker create timed out')]) {
    const control = exercise({ create });
    assert.throws(control.invoke, error => {
      assert.ok(error instanceof AggregateError);
      assert.match(error.errors[0].message, /Conflict|timed out/);
      assert.match(error.errors[1].message, /ownership unproven.*potential residue/);
      return true;
    });
    assert.deepEqual(control.calls.map(args => args[0]), ['create']);
    assert.ok(control.containers.has(competitor));
  }
});

test('name replacement does not redirect captured ownership', () => {
  const control = exercise({ verify: () => {
    control.containers.get(id).name = 'renamed-original';
    control.containers.set(competitor, { name });
    return 'verified';
  } });
  assert.equal(control.invoke(), 'verified');
  assert.ok(control.containers.has(competitor));
  assert.ok(!control.containers.has(id));
});

test('cidfile and parent replacement cannot influence daemon-captured ownership or delete competing files', () => {
  const work = mkdtempSync(join(tmpdir(), 'waza-ownership-regression-'));
  const parent = join(work, 'ownership');
  const displaced = join(work, 'displaced');
  mkdirSync(parent);
  writeFileSync(join(parent, 'container.id'), id);
  const control = exercise({ verify: () => {
    writeFileSync(join(parent, 'container.id'), competitor);
    renameSync(parent, displaced);
    mkdirSync(parent);
    writeFileSync(join(parent, 'container.id'), competitor);
    writeFileSync(join(parent, 'competing-file'), 'preserve');
    return 'verified';
  } });
  try {
    assert.equal(control.invoke(), 'verified');
    assert.equal(readFileSync(join(parent, 'container.id'), 'utf8'), competitor);
    assert.equal(readFileSync(join(displaced, 'container.id'), 'utf8'), competitor);
    assert.equal(readFileSync(join(parent, 'competing-file'), 'utf8'), 'preserve');
    assert.ok(control.containers.has(competitor));
    assert.ok(!control.containers.has(id));
    assert.ok(control.calls.every(args => !args.includes('--cidfile')));
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});

test('start timeout cleans captured ID and retains simultaneous removal failure', () => {
  const primary = new Error('docker start timed out');
  for (const remove of [ok, { ...ok, status: 1, stderr: 'removal denied' }]) {
    const control = exercise({ start: primary, remove });
    assert.throws(control.invoke, error => {
      if (remove.status === 0) return error === primary;
      assert.ok(error instanceof AggregateError);
      assert.equal(error.errors[0], primary);
      assert.match(error.errors[1].message, /removal denied/);
      assert.ok(error.errors[1].message.includes(id));
      return true;
    });
    assert.deepEqual(control.calls.at(-1), ['rm', '-f', id]);
  }
});

test('observed container status, not attached client status, drives expected CLI failures', () => {
  for (const exit of [0, 1, 2, 4, 5]) {
    const control = exercise({ exit, verify: (result, container) => {
      assert.equal(result.status, exit);
      assert.equal(container.State.Running, false);
      return 'verified';
    } });
    assert.equal(control.invoke(), 'verified');
    assert.deepEqual(control.calls.map(args => args[0]), ['create', 'start', 'inspect', 'rm']);
  }
});

test('invalid creation identity and removal failure cannot report success', () => {
  for (const stdout of ['', '../other', 'bad', `${id}\n${competitor}`]) {
    const control = exercise({ create: { ...ok, stdout } });
    assert.throws(control.invoke, /ownership unproven/);
    assert.deepEqual(control.calls.map(args => args[0]), ['create']);
  }
  const control = exercise({ remove: { ...ok, status: 1, stderr: 'removal denied' } });
  assert.throws(control.invoke, /removal denied/);
});

test('attached client failure is not repaired into a successful container exit', () => {
  const control = exercise({ start: { ...ok, status: 1, stderr: 'attach failed' } });
  assert.throws(control.invoke, /docker start failed.*attach failed/);
  assert.ok(!control.containers.has(id), 'failed attach must still clean owned container');
});
