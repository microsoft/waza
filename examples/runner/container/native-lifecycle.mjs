import assert from 'node:assert/strict';

export function runNamedContainer(command, name, args, options, verify) {
  let failure;
  try {
    const result = command(args, options);
    assert.notEqual(result.status, 125,
      `docker run rejected container ${name}: ${result.stderr}`);
    const inspected = command(['inspect', name]);
    assert.equal(inspected.status, 0,
      `docker inspect failed after run status ${result.status}: ${result.stderr}\n${inspected.stderr}`);
    return verify(result, JSON.parse(inspected.stdout)[0]);
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    try {
      // Run can fail before creation, or its client can time out after creation.
      const listed = command(['ps', '-a', '--filter', `name=^/${name}$`, '--format', '{{.Names}}']);
      assert.equal(listed.status, 0, `checking container cleanup: ${listed.stderr}`);
      const names = listed.stdout.trim() === '' ? [] : listed.stdout.trim().split('\n');
      assert.ok(names.every(value => value === name), `unexpected cleanup inventory: ${listed.stdout}`);
      if (names.length !== 0) {
        const removed = command(['rm', '-f', name]);
        assert.equal(removed.status, 0, `removing container ${name}: ${removed.stderr}`);
      }
    } catch (cleanupError) {
      if (failure) {
        throw new AggregateError([failure, cleanupError],
          `${failure.message}; container cleanup also failed: ${cleanupError.message}`);
      }
      throw cleanupError;
    }
  }
}
