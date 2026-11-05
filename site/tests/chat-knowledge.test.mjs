import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, symlinkSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { knowledgeFingerprint, knowledgePaths, knowledgeMetadata } from '../src/utils/chat-knowledge.mjs';

function fixture(t) {
  const root = mkdtempSync(join(tmpdir(), 'waza-chat-docs-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  mkdirSync(join(root, 'site/src/content/docs/guides'), { recursive: true });
  mkdirSync(join(root, 'schemas'));
  for (const kind of ['config', 'eval', 'task']) writeFileSync(join(root, `schemas/${kind}.schema.json`), '{}');
  writeFileSync(join(root, 'site/src/content/docs/index.mdx'), '# Waza\n');
  writeFileSync(join(root, 'site/src/content/docs/guides/test.md'), '# Test\n');
  writeFileSync(join(root, 'site/src/content/docs/ignored.json'), '{}');
  return root;
}

test('fingerprint contract uses sorted slash paths, NUL separators and raw bytes', (t) => {
  const root = fixture(t);
  const expected = createHash('sha256');
  for (const path of [
    'schemas/config.schema.json', 'schemas/eval.schema.json', 'schemas/task.schema.json',
    'site/src/content/docs/guides/test.md', 'site/src/content/docs/index.mdx',
  ]) {
    expected.update(path).update('\0').update(path.endsWith('index.mdx') ? '# Waza\n' : path.endsWith('test.md') ? '# Test\n' : '{}').update('\0');
  }
  assert.equal(knowledgePaths(root).length, 5);
  assert.equal(knowledgeFingerprint(root), expected.digest('hex'));
});

test('changes to every schema and docs alter the fingerprint', (t) => {
  const root = fixture(t);
  let fingerprint = knowledgeFingerprint(root);
  for (const path of knowledgePaths(root)) {
    writeFileSync(join(root, path), 'changed\n');
    const next = knowledgeFingerprint(root);
    assert.notEqual(fingerprint, next, path);
    fingerprint = next;
  }
});

test('unrelated files do not affect the content fingerprint', (t) => {
  const root = fixture(t);
  const fingerprint = knowledgeFingerprint(root);
  writeFileSync(join(root, 'README.md'), 'not part of the corpus');
  assert.equal(knowledgeFingerprint(root), fingerprint);
});

test('symlinks and missing schemas fail explicitly', (t) => {
  const root = fixture(t);
  symlinkSync(join(root, 'README.md'), join(root, 'site/src/content/docs/link.mdx'));
  assert.throws(() => knowledgeFingerprint(root), /symlink/);
  rmSync(join(root, 'site/src/content/docs/link.mdx'));
  rmSync(join(root, 'schemas/eval.schema.json'));
  assert.throws(() => knowledgeFingerprint(root), /ENOENT/);
});

test('real metadata identifies the checkout without exposing configuration', () => {
  const result = knowledgeMetadata();
  assert.deepEqual(Object.keys(result), ['version', 'commit', 'fingerprint']);
  assert.equal(result.version, 1);
  assert.match(result.commit, /^[a-f0-9]{40}$/);
  assert.match(result.fingerprint, /^[a-f0-9]{64}$/);
});
