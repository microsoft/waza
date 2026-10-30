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

test('runner identity fixtures trigger consumer validation and publication without skipping new specs', () => {
  const web = read('.github/workflows/web-ci.yml');
  for (const path of ['internal/runnerunit/**', 'internal/runneridentity/**', 'examples/runner/**']) {
    assert.equal(web.split(`'${path}'`).length - 1, 2, `${path} must trigger push and PR filtering`);
  }
  const site = read('.github/workflows/site-ci.yml');
  const deploy = read('.github/workflows/pages.yml');
  for (const path of ['internal/runnerunit/testdata/**', 'internal/runneridentity/testdata/**']) {
    assert.ok(site.includes(`'${path}'`), `${path} must select the site build`);
    assert.ok(deploy.includes(`'${path}'`), `${path} must select publication`);
  }
  for (const workflow of [web, site, deploy]) {
    assert.match(workflow, /^permissions:\n/m);
    assert.match(workflow, /timeout-minutes:/);
  }
  assert.match(web, /playwright test --project=chromium --retries=0/);
  assert.match(site, /npm run test:e2e -- --project=chromium --retries=0/);
  assert.match(read('web/playwright.config.ts'), /testDir: "\.\/e2e"/);
  assert.match(read('site/playwright.config.ts'), /testDir: '\.\/e2e'/);
  assert.ok(existsSync(resolve(root, 'web/e2e/runner-negotiation.spec.ts')));
  assert.ok(existsSync(resolve(root, 'site/e2e/runner-identities.spec.ts')));
  assert.doesNotMatch(read('site/playwright.config.ts'), /testIgnore:.*runner/);
});

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

test('site validation and deployment pin a supported Astro Node runtime', () => {
  const lock = JSON.parse(read('site/package-lock.json'));
  assert.match(lock.packages['node_modules/astro'].engines.node, />=22\.22\.0/);
  for (const path of ['.github/workflows/site-ci.yml', '.github/workflows/pages.yml']) {
    const versions = [...read(path).matchAll(/node-version: '([^']+)'/g)].map((match) => match[1]);
    assert.ok(versions.length > 0, `${path}: expected an explicit Node version`);
    for (const version of versions) {
      assert.equal(version, '22.22.0', `${path}: keep site CI and deployment on the supported pinned runtime`);
    }
  }
  assert.match(read('site/README.md'), /Use Node\.js 22\.22\.0 or later/);
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
  for (const path of ['docs/CONTROLLED-COMPARISONS.md', 'site/src/content/docs/guides/controlled-comparisons.mdx']) {
    assert.doesNotMatch(read(path), /do not currently produce|producer integration remains deferred/);
    assert.match(read(path), /Run without snapshot/);
    assert.match(read(path), /Run with `--snapshot`/);
  }
});

test('adversarial command fences separate offline smoke from prerequisite-gated real execution', () => {
  const guide = read('site/src/content/docs/guides/adversarial.mdx');
  const fences = [...guide.matchAll(/^```[^\n]*\n([\s\S]*?)^```/gm)];
  assert.ok(fences.length > 0);
  assert.match(fences[0][1], /waza adversarial --engine mock/);
  assert.doesNotMatch(fences[0][1], /copilot-sdk|cd skills\//, 'first copyable block must remain offline only');
  const prerequisites = ['skill-binding fix', '`v0.38.10`',
    'SDK authentication and usage prerequisites', 'standalone CLI login',
    'model access', 'approved runtime/tool permissions', 'Real runs may incur model costs'];
  let realFences = 0;
  for (const fence of fences) {
    const commands = [...fence[1].matchAll(/^\s*(?:run:\s*)?(waza adversarial [^\n]+)$/gm)].map((match) => match[1]);
    for (const command of commands) {
      assert.equal([...command.matchAll(/--engine (\S+)/g)].length, 1, `explicit, unambiguous engine required: ${command}`);
      assert.match(command, /--engine (?:mock|copilot-sdk)(?:\s|$)/);
    }
    const real = /--engine copilot-sdk|executor:\s*copilot-sdk/.test(fence[1]);
    if (!real) continue;
    realFences++;
    assert.doesNotMatch(fence[1], /--engine mock|executor:\s*mock/, 'one fence must not mix offline and real execution');
    for (const prerequisite of prerequisites) {
      const position = guide.indexOf(prerequisite);
      assert.ok(position >= 0 && position < fence.index, `${prerequisite} must precede every real-engine fence`);
    }
    for (const command of commands) assert.match(command, /--skill my-skill/);
  }
  assert.equal(realFences, 1, 'keep real execution in one separate, prerequisite-gated block');
  assert.match(guide, /harness only, not the changed\nskill's safety/);
  assert.match(guide, /expected negative harness control/);
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

test('concepts distinguish repeated trials from discarded retry outputs', () => {
  const concepts = read('site/src/content/docs/concepts.mdx').replaceAll('**', '').replace(/\s+/g, ' ');
  assert.doesNotMatch(concepts, /final success does not erase earlier failures/i, 'ordinary results do not preserve earlier failed attempts');
  assert.match(concepts, /Independently repeated trials produce separate run results \(`trials_per_task`\)/);
  assert.match(concepts, /final attempt's response, grader verdicts and status/);
  assert.match(concepts, /attempt count \(`attempts`\)/);
  assert.match(concepts, /Earlier attempt outputs and verdicts are not retained in that trial's result/);
  assert.match(concepts, /`prior_attempts: not_preserved`/);
  assert.match(concepts, /reported usage from discarded attempts/);
  assert.match(concepts, /An individual snapshot is not a complete retry history/);
});

test('release articles are linked from Releases and use published screenshot assets', () => {
  for (const slug of ['v0-39-0', 'dashboard-lab']) {
    const article = read(`site/src/content/docs/releases/${slug}.mdx`);
    assert.ok(read('site/astro.config.mjs').includes(`slug: 'releases/${slug}'`));
    assert.ok(read('site/src/content/docs/reference/releases.mdx').includes(`](../../releases/${slug}/)`));
    assert.match(article, /import ZoomImage from '\.\.\/\.\.\/\.\.\/components\/ZoomImage\.astro'/);
    const screenshots = [...article.matchAll(/<ZoomImage src="([^"]+)" alt="([^"]+)" \/>/g)];
    assert.equal(screenshots.length, 3, slug);
    for (const [, source] of screenshots) {
      assert.match(source, /^\/images\/.+\.png$/);
      assert.ok(existsSync(resolve(root, `site/public${source}`)), `missing release screenshot: ${source}`);
    }
    assert.doesNotMatch(article, /\]\(\.\.?\/[^)]*CHANGELOG\.md/);
  }
  const article = read('site/src/content/docs/releases/v0-39-0.mdx');
  assert.match(article, /^title: "Waza v0\.39\.0:/m);
  assert.match(article, /Synthetic demo screenshot, not production telemetry/);
  assert.match(article, /Fixture-backed native-profile view/);
});

test('Dashboard Lab article distinguishes saved results, synthetic examples, and release decisions', () => {
  const article = read('site/src/content/docs/releases/dashboard-lab.mdx');
  assert.match(article, /waza serve --lab --results-dir \.\/results --no-browser/);
  assert.match(article, /experimental, opt-in viewer for saved evaluation results/);
  assert.match(article, /Synthetic demo screenshot/);
  assert.match(article, /Fixture-backed saved-result view with cached mock provenance/);
  assert.match(article, /not an active-worker monitor/);
  assert.match(article, /descriptive comparisons, not statistical-significance tests/);
  assert.match(article, /\]\(\.\.\/\.\.\/guides\/dashboard-lab\/\)/);
  assert.match(article, /\]\(\.\.\/v0-39-0\/\)/);
  assert.match(read('site/src/content/docs/releases/v0-39-0.mdx'), /\]\(\.\.\/dashboard-lab\/\)/);
});

test('release overview identifies the published version and retains the former availability anchor', () => {
  const overview = read('site/src/content/docs/reference/releases.mdx');
  assert.match(overview, /## Current published standalone release: v0\.39\.1/);
  assert.match(overview, /id="upcoming-main-only-capabilities"/);
  assert.equal([...overview.matchAll(/releases\/download\/v0\.39\.1\/waza-/g)].length, 6);
  assert.doesNotMatch(overview, /releases\/(?:download|tag)\/v0\.38\.10/);
  assert.match(read('site/src/content/docs/guides/dashboard-lab.mdx'), /Available in v0\.39\.0 and opt-in/);
});

test('extension installation guidance identifies the current registry version', () => {
  const registry = JSON.parse(read('registry.json'));
  const extension = registry.extensions.find(({ id }) => id === 'microsoft.azd.waza');
  assert.ok(extension, 'waza extension must exist in the registry');
  const versions = extension.versions.map(({ version }) => version);
  assert.ok(versions.length > 0, 'waza registry must contain published versions');
  const latest = versions.toSorted((a, b) => a.localeCompare(b, 'en', { numeric: true })).at(-1);
  const guide = read('site/src/content/docs/getting-started.mdx');
  const section = guide.match(/^### 3\. Azure Developer CLI Extension\n([\s\S]*?)(?=^## )/m)?.[1];
  assert.ok(section, 'extension installation section must exist');
  assert.equal(section.match(/The registry\s+lists through `([^`]+)`/)?.[1], latest);
  assert.ok(section.includes(`releases/tag/azd-ext-microsoft-azd-waza_${latest})`));
  assert.match(section, /azd waza --version/);
  assert.match(section, /azd waza <command> --help/);
  assert.doesNotMatch(section, /published `0\.38\.10` is a separate extension artifact/);
});

test('published workflow guides do not require unreleased main for v0.39.0 features', () => {
  const paths = [
    'README.md', 'docs/GETTING-STARTED.md', 'docs/GUIDE.md',
    'site/src/content/docs/index.mdx',
    'site/src/content/docs/getting-started.mdx', 'site/src/content/docs/quick-start.mdx',
    'site/src/content/docs/concepts.mdx', 'site/src/content/docs/about.mdx',
    'site/src/content/docs/reference/cli.mdx',
    ...['eval-yaml', 'preflight', 'snapshot-replay', 'controlled-comparisons', 'ci-cd', 'graders', 'dashboard-lab', 'adversarial']
      .map((name) => `site/src/content/docs/guides/${name}.mdx`),
  ];
  for (const path of paths) {
    const text = read(path);
    assert.match(text, /v0\.39\.0/, path);
    assert.doesNotMatch(text, /(?:upcoming|current source)\/main-only|source-build-only|require upcoming main|build upcoming main/i, path);
    assert.match(text, /reference\/releases|releases\/#upcoming-main-only-capabilities/, path);
  }
});
