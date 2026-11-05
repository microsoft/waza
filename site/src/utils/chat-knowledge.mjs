import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

export function knowledgePaths(root) {
  const collect = (directory) => readdirSync(join(root, directory), { withFileTypes: true })
    .flatMap((entry) => {
      const path = `${directory}/${entry.name}`;
      if (entry.isSymbolicLink()) throw new Error(`Knowledge input cannot be a symlink: ${path}`);
      if (entry.isDirectory()) return collect(path);
      return entry.isFile() && /\.(md|mdx)$/.test(entry.name) ? [path] : [];
    });
  return [
    ...collect('site/src/content/docs'),
    'schemas/config.schema.json',
    'schemas/eval.schema.json',
    'schemas/task.schema.json',
  ].sort();
}

export function knowledgeFingerprint(root) {
  const hash = createHash('sha256');
  for (const path of knowledgePaths(root)) {
    hash.update(path).update('\0').update(readFileSync(join(root, path))).update('\0');
  }
  return hash.digest('hex');
}

export function knowledgeMetadata(root = execFileSync('git', ['rev-parse', '--show-toplevel'], {
  encoding: 'utf8', timeout: 5000, stdio: ['ignore', 'pipe', 'pipe'],
}).trim()) {
  const commit = execFileSync('git', ['rev-parse', 'HEAD'], {
    cwd: root, encoding: 'utf8', timeout: 5000, stdio: ['ignore', 'pipe', 'pipe'],
  }).trim();
  if (!/^[a-f0-9]{40}$/.test(commit)) throw new Error('Invalid docs build commit');
  return { version: 1, commit, fingerprint: knowledgeFingerprint(root) };
}
