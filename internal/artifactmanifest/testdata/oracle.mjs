// Frozen, independently authored v1 byte oracle. No Go-produced input is used.
// canonical.json has one repository text-file LF, excluded from addressed bytes.
import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';

const empty = 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855';
const manifest = {
  kind: 'waza.artifact-manifest',
  schema_version: '1.0',
  binding: {
    identity: {
      version: 'sha256-waza-c14n-v1',
      canonical_digest: '1'.repeat(64),
      run: 'run-' + '2'.repeat(64),
      shard: 'shard-' + '3'.repeat(64),
      task: 'task-' + '4'.repeat(64),
      trial: 'trial-' + '5'.repeat(64),
      attempt: 'attempt-' + '6'.repeat(64),
    },
    selection: {
      shard: '日本<&>',
      task: 'é😀"\\\n\t\0\u2028\u2029',
      trial: 1,
      attempt: 2,
    },
    execution_mode: 'mock',
    grader_ids: ['a', 'z', 'é'],
  },
  terminal_artifact: 'terminal',
  artifacts: [
    { name: 'A', sha256: empty, size_bytes: 0, media_type: 'text/é' },
    { name: 'terminal', sha256: empty, size_bytes: 0, media_type: 'application/json' },
    { name: 'z', sha256: empty, size_bytes: 0, media_type: 'text/plain' },
  ],
};
// The profile explicitly uses Go compact JSON escaping, not generic JCS.
const bytes = Buffer.from(JSON.stringify(manifest).replace(/[<>&\u2028\u2029]/g,
  c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0')));
const fixture = readFileSync(new URL('./canonical.json', import.meta.url));
assert.equal(fixture.at(-1), 10);
assert.deepEqual(fixture.subarray(0, -1), bytes);
const digest = createHash('sha256').update(bytes).digest('hex');
assert.equal(digest, '5b9c9d9c32dcf710f0662de777e1fc356add8b559673b3b839da66b6a1398298');
console.log(digest);
