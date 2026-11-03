import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export function runNamedContainer(command, name, args, options, verify) {
  const ownership = mkdtempSync(join(tmpdir(), 'waza-container-owner-'));
  const cidfile = join(ownership, 'container.id');
  const ownedID = () => {
    let id;
    try {
      id = readFileSync(cidfile, 'utf8').trim();
    } catch (error) {
      throw new Error(`container ownership unproven for ${name}; potential residue must be inspected manually`, { cause: error });
    }
    assert.match(id, /^[0-9a-f]{64}$/,
      `container ownership unproven for ${name}; invalid cidfile, inspect potential residue manually`);
    return id;
  };
  let failure;
  try {
    const result = command([args[0], '--cidfile', cidfile, ...args.slice(1)], options);
    assert.notEqual(result.status, 125,
      `docker run rejected container ${name}: ${result.stderr}`);
    const id = ownedID();
    const inspected = command(['inspect', id]);
    assert.equal(inspected.status, 0,
      `docker inspect failed after run status ${result.status}: ${result.stderr}\n${inspected.stderr}`);
    const container = JSON.parse(inspected.stdout)[0];
    assert.equal(container.Id, id, 'inspection must match invocation-owned immutable container ID');
    return verify(result, container);
  } catch (error) {
    failure = error;
    throw error;
  } finally {
    try {
      // Never resolve the name again: collision/replacement does not confer ownership.
      const id = ownedID();
      const removed = command(['rm', '-f', id]);
      assert.equal(removed.status, 0, `removing invocation-owned container ${id}: ${removed.stderr}`);
    } catch (cleanupError) {
      if (failure) {
        throw new AggregateError([failure, cleanupError],
          `${failure.message}; container cleanup also failed: ${cleanupError.message}`);
      }
      throw cleanupError;
    } finally {
      rmSync(ownership, { recursive: true, force: true });
    }
  }
}
