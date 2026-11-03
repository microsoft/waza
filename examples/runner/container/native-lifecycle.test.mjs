import assert from 'node:assert/strict';
import test from 'node:test';
import { runNamedContainer } from './native-lifecycle.mjs';

const name = 'waza-native-test';
const ok = { status: 0, stdout: '', stderr: '' };
const rejected = { status: 125, stdout: '', stderr: 'invalid mount: source does not exist' };

function exercise(run, inventory, removal = ok, verify = () => 'verified') {
  const calls = [];
  const command = args => {
    calls.push(args);
    if (args[0] === 'run') {
      if (run instanceof Error) throw run;
      return run;
    }
    if (args[0] === 'inspect') return { ...ok, stdout: '[{"State":{"Running":false}}]' };
    if (args[0] === 'ps') return inventory;
    if (args[0] === 'rm') return removal;
    assert.fail(`unexpected Docker command ${args}`);
  };
  return { calls, invoke: () => runNamedContainer(command, name, ['run', '--name', name], {}, verify) };
}

test('pre-creation rejection retains original actionable Docker error', () => {
  const control = exercise(rejected, ok);
  assert.throws(control.invoke, /docker run rejected.*invalid mount: source does not exist/);
  assert.deepEqual(control.calls.map(args => args[0]), ['run', 'ps']);
});

test('timeout after creation still removes the invocation container', () => {
  const failure = new Error('docker run timed out');
  const control = exercise(failure, { ...ok, stdout: `${name}\n` });
  assert.throws(control.invoke, error => error === failure);
  assert.deepEqual(control.calls.at(-1), ['rm', '-f', name]);
});

test('primary error and real cleanup failure are both retained', () => {
  for (const inventory of [ok, { ...ok, stdout: `${name}\n` }]) {
    const control = exercise(rejected,
      inventory.stdout === '' ? { ...ok, status: 1, stderr: 'daemon unavailable' } : inventory,
      { ...ok, status: 1, stderr: 'removal denied' });
    assert.throws(control.invoke, error => {
      assert.ok(error instanceof AggregateError);
      assert.equal(error.errors.length, 2);
      assert.match(error.errors[0].message, /invalid mount/);
      assert.match(error.errors[1].message, /daemon unavailable|removal denied/);
      return true;
    });
  }
});

test('successful verification and nonzero expected CLI exits still clean up', () => {
  for (const status of [0, 2]) {
    const control = exercise({ ...ok, status }, { ...ok, stdout: `${name}\n` }, ok,
      (result, container) => {
        assert.equal(result.status, status);
        assert.equal(container.State.Running, false);
        return 'verified';
      });
    assert.equal(control.invoke(), 'verified');
    assert.deepEqual(control.calls.map(args => args[0]), ['run', 'inspect', 'ps', 'rm']);
  }
});

test('cleanup failure cannot turn successful verification into success', () => {
  const control = exercise(ok, { ...ok, stdout: `${name}\n` },
    { ...ok, status: 1, stderr: 'removal denied' });
  assert.throws(control.invoke, /removal denied/);
});
