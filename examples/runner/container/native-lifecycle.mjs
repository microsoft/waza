import assert from 'node:assert/strict';
import { mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export function runNamedContainer(command, name, args, options, verify,
  removeOwnership = path => rmSync(path, { recursive: true, force: true })) {
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
  const failures = [];
  let verified;
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
    verified = verify(result, container);
  } catch (error) {
    failures.push(error);
  }
  try {
    // Never resolve the name again: collision/replacement does not confer ownership.
    const id = ownedID();
    const removed = command(['rm', '-f', id]);
    assert.equal(removed.status, 0, `removing invocation-owned container ${id}: ${removed.stderr}`);
  } catch (error) {
    failures.push(error);
  }
  try {
    removeOwnership(ownership);
  } catch (error) {
    failures.push(new Error(`removing private cidfile directory ${ownership}; residue remains: ${error.message}`, { cause: error }));
  }
  if (failures.length === 1) throw failures[0];
  if (failures.length > 1) throw new AggregateError(failures, failures.map(error => error.message).join('; '));
  return verified;
}
