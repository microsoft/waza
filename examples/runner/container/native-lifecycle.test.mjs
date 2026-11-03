import assert from 'node:assert/strict';
import { existsSync, rmSync, writeFileSync } from 'node:fs';
import test from 'node:test';
import { runNamedContainer } from './native-lifecycle.mjs';

const name = 'waza-native-test';
const id = 'a'.repeat(64);
const competitor = 'b'.repeat(64);
const ok = { status: 0, stdout: '', stderr: '' };
const rejected = { status: 125, stdout: '', stderr: 'invalid mount: source does not exist' };

function exercise(run, createdID = id, removal = ok, verify = () => 'verified', removeOwnership) {
  const calls = [];
  const containers = new Map();
  const command = args => {
    calls.push(args);
    if (args[0] === 'run') {
      assert.equal(args[1], '--cidfile');
      if (createdID !== null) {
        writeFileSync(args[2], createdID, { flag: 'wx' });
        containers.set(createdID, { name });
      }
      if (run instanceof Error) throw run;
      return run;
    }
    if (args[0] === 'inspect') {
      assert.equal(args[1], id);
      return { ...ok, stdout: JSON.stringify([{ Id: id, State: { Running: false } }]) };
    }
    if (args[0] === 'rm') {
      if (removal.status === 0) containers.delete(args[2]);
      return removal;
    }
    assert.fail(`unexpected Docker command ${args}`);
  };
  return { calls, containers, invoke: () => runNamedContainer(command, name, ['run', '--name', name], {}, verify, removeOwnership) };
}

test('pre-creation rejection retains original error and reports unproven ownership', () => {
  const control = exercise(rejected, null);
  assert.throws(control.invoke, error => {
    assert.ok(error instanceof AggregateError);
    assert.match(error.errors[0].message, /invalid mount: source does not exist/);
    assert.match(error.errors[1].message, /ownership unproven.*potential residue/);
    return true;
  });
  assert.deepEqual(control.calls.map(args => args[0]), ['run']);
});

test('collision never removes a pre-existing container with the requested name', () => {
  const control = exercise({ ...rejected, stderr: `Conflict: name owned by ${competitor}` }, null);
  control.containers.set(competitor, { name });
  assert.throws(control.invoke, /Conflict.*ownership unproven/);
  assert.deepEqual(control.calls.map(args => args[0]), ['run']);
  assert.ok(control.containers.has(competitor));
});

test('name replacement does not redirect cleanup to the competing container', () => {
  const control = exercise(ok, id, ok, () => {
    control.containers.get(id).name = 'renamed-owned-container';
    control.containers.set(competitor, { name });
    return 'verified';
  });
  assert.equal(control.invoke(), 'verified');
  assert.deepEqual(control.calls.at(-1), ['rm', '-f', id]);
  assert.ok(control.calls.every(args => args[0] === 'run' || !args.includes(name)));
  assert.ok(control.calls.every(args => !args.includes(competitor)));
  assert.ok(control.containers.has(competitor), 'replacement must survive cleanup');
  assert.ok(!control.containers.has(id), 'original immutable ID must be cleaned up despite rename');
});

test('timeout after creation still removes the invocation-owned immutable ID', () => {
  const failure = new Error('docker run timed out');
  const control = exercise(failure);
  assert.throws(control.invoke, error => error === failure);
  assert.deepEqual(control.calls.at(-1), ['rm', '-f', id]);
});

test('primary error and real cleanup failure are both retained', () => {
  const control = exercise(new Error('docker run timed out'), id,
    { ...ok, status: 1, stderr: 'removal denied' });
  assert.throws(control.invoke, error => {
    assert.ok(error instanceof AggregateError);
    assert.equal(error.errors.length, 2);
    assert.match(error.errors[0].message, /timed out/);
    assert.match(error.errors[1].message, /removal denied/);
    return true;
  });
});

test('successful verification and nonzero expected CLI exits still clean up', () => {
  for (const status of [0, 2]) {
    const control = exercise({ ...ok, status }, id, ok, (result, container) => {
      assert.equal(result.status, status);
      assert.equal(container.State.Running, false);
      return 'verified';
    });
    assert.equal(control.invoke(), 'verified');
    assert.deepEqual(control.calls.map(args => args[0]), ['run', 'inspect', 'rm']);
  }
});

test('invalid ownership and cleanup failure cannot become success', () => {
  for (const invalid of ['', '../other', 'bad', `${id}\n${competitor}`]) {
    const control = exercise(ok, invalid);
    assert.throws(control.invoke, /ownership unproven/);
    assert.deepEqual(control.calls.map(args => args[0]), ['run']);
  }
  const control = exercise(ok, id, { ...ok, status: 1, stderr: 'removal denied' });
  assert.throws(control.invoke, /removal denied/);
});

test('primary, container and temporary-directory failures are all retained with residue path', () => {
  let residue;
  const control = exercise(new Error('docker run timed out'), id,
    { ...ok, status: 1, stderr: 'removal denied' }, undefined, path => {
      residue = path;
      throw new Error('temporary directory removal denied');
    });
  try {
    assert.throws(control.invoke, error => {
      assert.ok(error instanceof AggregateError);
      assert.equal(error.errors.length, 3);
      assert.match(error.errors[0].message, /timed out/);
      assert.match(error.errors[1].message, /removal denied/);
      assert.match(error.errors[2].message, /residue remains: temporary directory removal denied/);
      assert.ok(error.errors[2].message.includes(residue));
      assert.ok(existsSync(residue), 'fault must leave the reported residue');
      return true;
    });
    assert.deepEqual(control.calls.at(-1), ['rm', '-f', id]);
  } finally {
    if (residue) rmSync(residue, { recursive: true, force: true });
  }
});

test('temporary-directory cleanup failure alone cannot report success', () => {
  let residue;
  const control = exercise(ok, id, ok, undefined, path => {
    residue = path;
    throw new Error('temporary directory removal denied');
  });
  try {
    assert.throws(control.invoke, error => {
      assert.ok(!(error instanceof AggregateError));
      assert.match(error.message, /residue remains: temporary directory removal denied/);
      assert.ok(error.message.includes(residue));
      return true;
    });
    assert.ok(!control.containers.has(id), 'container cleanup still precedes temporary-directory cleanup');
  } finally {
    if (residue) rmSync(residue, { recursive: true, force: true });
  }
});
