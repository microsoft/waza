import assert from 'node:assert/strict';
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs';
import { dirname, resolve, extname } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const withoutCode = (text) => text.replace(/^(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\1\s*$/gm, '');
const slug = (text) => text.toLowerCase().replace(/<\/?[a-z][a-z0-9]*(?:\s[^>]*)?>/g, '').replace(/[^\p{L}\p{N}_ -]/gu, '').replace(/ /g, '-');

function anchors(text) {
  const ids = new Set();
  const counts = new Map();
  for (const [, heading] of withoutCode(text).matchAll(/^#{1,6} (.+?)(?: +#+)?$/gm)) {
    const id = slug(heading);
    const count = counts.get(id) ?? 0;
    ids.add(count ? `${id}-${count}` : id);
    counts.set(id, count + 1);
  }
  for (const [, id] of text.matchAll(/\bid=["']([^"']+)["']/g)) ids.add(id);
  return ids;
}

function markdownFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = resolve(directory, entry.name);
    return entry.isDirectory() ? markdownFiles(path) : /\.(md|mdx)$/.test(path) ? [path] : [];
  });
}

test('public repository Markdown targets and exact heading anchors resolve', () => {
  const files = [
    resolve(root, 'README.md'),
    ...readdirSync(resolve(root, 'docs')).filter((name) => name.endsWith('.md')).map((name) => resolve(root, 'docs', name)),
    ...markdownFiles(resolve(root, 'docs/graders')),
  ];
  const failures = [];
  for (const file of files) {
    for (const [, target] of withoutCode(readFileSync(file, 'utf8')).matchAll(/!?\[[^\]]*\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g)) {
      if (/^(?:[a-z][a-z\d+.-]*:|\/\/|\/)/i.test(target)) continue;
      const [path, fragment] = target.split('#');
      const destination = path ? resolve(dirname(file), decodeURIComponent(path)) : file;
      if (!existsSync(destination)) failures.push(`${file}: missing ${target}`);
      else if (fragment && ['.md', '.mdx'].includes(extname(destination)) &&
        !anchors(readFileSync(destination, 'utf8')).has(decodeURIComponent(fragment))) {
        failures.push(`${file}: missing anchor ${target}`);
      } else if (!statSync(destination).isFile() && fragment) failures.push(`${file}: directory anchor ${target}`);
    }
  }
  assert.deepEqual(failures, []);
});

test('catalogs enumerate exactly the native enum and schema kinds', () => {
  const native = [...read('internal/models/outcome.go').matchAll(/GraderKind\s*=\s*"([^"]+)"/g)].map((m) => m[1]).sort();
  assert.equal(native.length, 13);
  const schema = JSON.parse(read('schemas/eval.schema.json'));
  const findEnum = (node) => {
    if (!node || typeof node !== 'object') return [];
    return [...(Array.isArray(node.enum) && node.enum.includes('tool_constraint') ? [node.enum] : []),
      ...Object.values(node).flatMap(findEnum)];
  };
  assert.ok(findEnum(schema).some((kinds) => JSON.stringify([...kinds].sort()) === JSON.stringify(native)));
  for (const path of ['README.md', 'site/src/content/docs/guides/graders.mdx']) {
    const kinds = path === 'README.md'
      ? [...read(path).split('### Available Grader Types')[1].split('## Documentation')[0].matchAll(/^\| `([a-z_]+)`/gm)].map((m) => m[1])
      : [...read(path).split('## At a glance')[1].split('\n---')[0].matchAll(/^\|.*?\|\s*`([a-z_]+)`\s*\|/gm)].map((m) => m[1]);
    assert.deepEqual([...kinds].sort(), native, path);
  }
});

test('onboarding and active grader docs reject obsolete example contracts', () => {
  const paths = ['README.md', 'docs/GETTING-STARTED.md', 'docs/TUTORIAL.md',
    ...markdownFiles(resolve(root, 'docs/graders')),
    ...markdownFiles(resolve(root, 'site/src/content/docs'))];
  for (const path of paths) {
    const text = read(path);
    assert.doesNotMatch(text, /^\s*-?\s*type:\s*(?:llm|script|llm_comparison|regex|keyword|trigger_tests)\s*$/m, path);
  }
  for (const path of ['site/src/content/docs/index.mdx', 'site/src/content/docs/getting-started.mdx', 'docs/GETTING-STARTED.md']) {
    assert.doesNotMatch(read(path), /^\s+pattern:/m, path);
  }
  const registry = JSON.parse(read('registry.json'));
  for (const version of registry.extensions[0].versions) {
    for (const example of version.examples) assert.doesNotMatch(example.usage, /--interactive/);
  }
});

test('source build and safety boundary examples stay explicit', () => {
  for (const path of ['README.md', 'docs/GUIDE.md', 'site/src/content/docs/quick-start.mdx',
    'site/src/content/docs/getting-started.mdx', 'site/src/content/docs/guides/ci-cd.mdx']) {
    const text = read(path);
    assert.match(text, /git lfs install[\s\S]*git lfs pull[\s\S]*make build/, path);
    assert.doesNotMatch(text, /^go build -o waza/m, path);
  }
  const adversarial = read('site/src/content/docs/guides/adversarial.mdx');
  for (const [, command] of adversarial.matchAll(/^(waza adversarial .+)$/gm)) {
    if (command.includes('--spec')) continue;
    assert.match(command, /--engine (?:mock|copilot-sdk)/);
    if (command.includes('--engine copilot-sdk')) assert.match(command, /--skill /);
  }
  for (const path of ['docs/CONTROLLED-COMPARISONS.md', 'site/src/content/docs/guides/controlled-comparisons.mdx']) {
    assert.doesNotMatch(read(path), /do not currently produce|producer integration remains deferred/);
    assert.match(read(path), /Run without snapshot/);
    assert.match(read(path), /Run with `--snapshot`/);
  }
});
