import assert from 'node:assert/strict';

export function runNamedContainer(command, name, args, options, verify) {
  let id;
  let verified;
  const failures = [];
  try {
    // Capture daemon-issued ownership directly; no mutable cidfile or name lookup.
    const created = command(['create', ...args.slice(1)]);
    assert.equal(created.status, 0, `docker create rejected container ${name}: ${created.stderr}`);
    const received = created.stdout.trim();
    assert.match(received, /^[0-9a-f]{64}$/, `invalid Docker creation identity for ${name}`);
    id = received;
    const result = command(['start', '--attach', '--interactive', id], options);
    const inspected = command(['inspect', id]);
    assert.equal(inspected.status, 0,
      `docker inspect failed after start status ${result.status}: ${result.stderr}\n${inspected.stderr}`);
    const container = JSON.parse(inspected.stdout)[0];
    assert.equal(container.Id, id, 'inspection must match invocation-owned immutable container ID');
    assert.equal(container.State.Running, false, 'attached container must have stopped');
    assert.notEqual(container.State.ExitCode, undefined, 'container exit status must be observed');
    assert.ok(result.status === 0 || result.status === container.State.ExitCode,
      `docker start failed with status ${result.status}: ${result.stderr}`);
    verified = verify({ ...result, status: container.State.ExitCode }, container);
  } catch (error) {
    failures.push(error);
  }
  if (id) {
    try {
      const removed = command(['rm', '-f', id]);
      assert.equal(removed.status, 0, `removing invocation-owned container ${id}: ${removed.stderr}`);
    } catch (error) {
      failures.push(error);
    }
  } else {
    failures.push(new Error(`container ownership unproven for ${name}; potential residue must be inspected manually`));
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, failures.map(error => error.message).join('; '));
  return verified;
}
