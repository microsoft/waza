import assert from 'node:assert/strict';
import { readFileSync, readdirSync, existsSync, statSync } from 'node:fs';
import { dirname, resolve, extname } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));
const read = (path) => readFileSync(resolve(root, path), 'utf8');
const catalogInputs = {
  native: 'internal/models/outcome.go',
  schema: 'schemas/eval.schema.json',
  catalogs: ['README.md', 'site/src/content/docs/guides/graders.mdx'],
};
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
  const native = [...read(catalogInputs.native).matchAll(/GraderKind\s*=\s*"([^"]+)"/g)].map((m) => m[1]).sort();
  assert.equal(native.length, 13);
  const schema = JSON.parse(read(catalogInputs.schema));
  const findEnum = (node) => {
    if (!node || typeof node !== 'object') return [];
    return [...(Array.isArray(node.enum) && node.enum.includes('tool_constraint') ? [node.enum] : []),
      ...Object.values(node).flatMap(findEnum)];
  };
  assert.ok(findEnum(schema).some((kinds) => JSON.stringify([...kinds].sort()) === JSON.stringify(native)));
  for (const path of catalogInputs.catalogs) {
    const kinds = path === 'README.md'
      ? [...read(path).split('### Available Grader Types')[1].split('## Documentation')[0].matchAll(/^\| `([a-z_]+)`/gm)].map((m) => m[1])
      : [...read(path).split('## At a glance')[1].split('\n---')[0].matchAll(/^\|.*?\|\s*`([a-z_]+)`\s*\|/gm)].map((m) => m[1]);
    assert.deepEqual([...kinds].sort(), native, path);
  }
});

test('every catalog input enables PR site checks and the main push workflow', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  const push = workflow.match(/^  push:\n([\s\S]*?)(?=^  pull_request:)/m)?.[1];
  const pullRequest = workflow.match(/^  pull_request:\n([\s\S]*?)(?=^permissions:)/m)?.[1];
  const site = workflow.match(/^            site:\n([\s\S]*?)(?=^            examples:)/m)?.[1];
  assert.ok(push && pullRequest && site, 'expected workflow event/filter blocks must exist');
  assert.match(push, /branches: \[main\]/);
  assert.match(pullRequest, /branches: \[main\]/);
  assert.doesNotMatch(pullRequest, /^\s+paths(?:-ignore)?:/m, 'required PR status must not be path-gated');
  const paths = (block) => [...block.matchAll(/^\s+- '([^'\n]+)'$/gm)].map((match) => match[1]);
  const matches = (pattern, path) => pattern.endsWith('/**')
    ? path.startsWith(pattern.slice(0, -2)) : pattern === path;
  for (const input of Object.values(catalogInputs).flat()) {
    assert.ok(paths(site).some((pattern) => matches(pattern, input)), `${input}: PR site filter excludes catalog input`);
    assert.ok(paths(push).some((pattern) => matches(pattern, input)), `${input}: main push gate excludes catalog input`);
  }
  const build = workflow.match(/^      - name: Build site\n([\s\S]*?)(?=^      - name:)/m)?.[1];
  assert.ok(build, 'site build step must exist');
  assert.match(build, /if: steps\.filter\.outputs\.site == 'true' \|\| github\.event_name == 'push'/);
  assert.match(build, /run: cd site && npm run build/);
  const scripts = JSON.parse(read('site/package.json')).scripts;
  assert.equal(scripts.prebuild, 'npm run test:docs');
  assert.equal(scripts['test:docs'], 'node --test tests/documentation.test.mjs');
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

test('executable documentation controls have a maintained real-asset CI entrypoint', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  assert.match(workflow, /name: Offline documentation examples/);
  assert.match(workflow, /lfs: true/);
  assert.match(workflow, /run: make build/);
  assert.match(workflow, /WAZA_BIN="\$GITHUB_WORKSPACE\/waza" npm --prefix site run test:examples/);
  assert.match(workflow, /COPILOT_CLI_PATH: \$\{\{ github.workspace \}\}/);
});

test('adversarial replay example identifies the baseline before its comparison', () => {
  const examples = [...read('site/src/content/docs/guides/adversarial.mdx')
    .matchAll(/`(waza replay [^`\n]+)`/g)].map((match) => match[1]);
  assert.deepEqual(examples, ['waza replay before.json --bisect after.json']);
});
