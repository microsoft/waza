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
const embeddedTargets = ['darwin_amd64', 'darwin_arm64', 'linux_amd64', 'linux_arm64', 'windows_amd64', 'windows_arm64'];
const embeddedFiles = embeddedTargets.flatMap((target) => [
  `internal/embedded/zcopilot_${target}.go`,
  `internal/embedded/zcopilot_inprocess_${target}.go`,
]);
const embeddedAssets = embeddedFiles.flatMap((path) =>
  [...read(path).matchAll(/^\/\/go:embed (\S+)$/gm)]
    .map((match) => `internal/embedded/${match[1]}`));
const releaseInputs = [
  'version.txt', 'registry.json', 'go.mod', 'CHANGELOG.md',
  'internal/models/schema_version.go', ...embeddedFiles, ...embeddedAssets,
];
const withoutCode = (text) => text.replace(/^(`{3,}|~{3,})[^\n]*\n[\s\S]*?^\1\s*$/gm, '');
const slug = (text) => text.toLowerCase().replace(/<\/?[a-z][a-z0-9]*(?:\s[^>]*)?>/g, '').replace(/[^\p{L}\p{N}_ -]/gu, '').replace(/ /g, '-');

function assertExecutionBrowserDiscovery(config, target) {
  assert.ok(['web', 'site'].includes(target), 'expected web or site discovery profile');
  // Keep literal contents opaque so comments and example strings cannot supply configuration fields.
  const tokens = [...config.matchAll(/(['"`])(?:\\[\s\S]|(?!\1)[^\\])*\1|\/\/[^\r\n]*|\/\*[\s\S]*?\*\/|\.\.\.|[A-Za-z_$][\w$]*|[^\s]/g)]
    .map((match) => match[0]).filter((token) => !token.startsWith('//') && !token.startsWith('/*'));
  const opening = ['export', 'default', 'defineConfig', '(', '{'];
  const start = tokens.findIndex((_, index) => opening.every((token, offset) => tokens[index + offset] === token));
  assert.ok(start >= 0, 'browser discovery requires a literal exported defineConfig object');
  const fields = [];
  let depth = 0;
  let end;
  for (let index = start + opening.length - 1; index < tokens.length; index++) {
    const token = tokens[index];
    if (token === '{') depth++;
    if (token === '}' && --depth === 0) {
      end = index;
      break;
    }
    assert.notEqual(token, '...', 'dynamic configuration spreads can narrow browser discovery');
    assert.ok(!(token === ']' && tokens[index + 1] === ':'), 'computed configuration keys can narrow browser discovery');
    const name = /^['"]/.test(token) ? token.slice(1, -1) : token;
    if (['testDir', 'testIgnore', 'testMatch'].includes(name) && tokens[index + 1] === ':') {
      fields.push({ name, index, depth });
    }
  }
  assert.ok(end !== undefined, 'browser configuration object must close');
  assert.deepEqual(tokens.slice(end + 1), [')', ';'], 'exported browser configuration must not have subsequent mutations');
  assert.equal(fields.filter(({ name }) => name === 'testMatch').length, 0, 'testMatch must not narrow browser discovery');
  const expectedIgnore = target === 'web'
    ? ['string:**/checkpoint-cli-process.test.mjs']
    : ['[', 'string:chat.spec.ts', ',', 'string:chat-unavailable.spec.ts', ']'];
  for (const [name, expected] of [
    ['testDir', ['string:./e2e']],
    ['testIgnore', expectedIgnore],
  ]) {
    const matches = fields.filter((field) => field.name === name);
    assert.equal(matches.length, 1, `${target}: exactly one ${name} declaration is required`);
    const field = matches[0];
    assert.equal(field.depth, 1, `${target}: ${name} must be declared on the exported configuration`);
    const value = tokens.slice(field.index + 2, field.index + 2 + expected.length)
      .map((token) => /^['"]/.test(token) ? `string:${token.slice(1, -1)}` : token);
    assert.deepEqual(value, expected, `${target}: ${name} must retain the exact literal discovery contract`);
    assert.ok([',', '}'].includes(tokens[field.index + 2 + expected.length]),
      `${target}: ${name} must not have a dynamic or additional value`);
  }
}

function assertExecutionLifecycleWorkflowDiscovery(workflow) {
  const push = workflow.match(/^  push:\n([\s\S]*?)(?=^  pull_request:)/m)?.[1];
  const filter = workflow.match(/^            web:\n([\s\S]*?)(?=^      - name:)/m)?.[1];
  assert.ok(push && filter, 'actual dashboard push and PR filter blocks must exist');
  const apiSelector = 'internal/webapi/lifecycle*.go';
  for (const path of [apiSelector, 'internal/executionlifecycle/**',
    'schemas/execution-lifecycle*.json', 'schemas/execution_lifecycle.go']) {
    assert.equal(workflow.split(`'${path}'`).length - 1, 2, `${path} must select both push and PR paths-filter`);
    for (const [name, block] of [['main push gate', push], ['PR web filter', filter]]) {
      const patterns = [...block.matchAll(/^\s+- '([^'\r\n]+)'$/gm)].map((match) => match[1]);
      assert.equal(patterns.filter((pattern) => pattern === path).length, 1,
        `${name}: missing exact lifecycle selector ${path}`);
    }
  }
  for (const path of ['internal/webapi/lifecycle.go', 'internal/webapi/lifecycle_source.go']) {
    assert.ok(existsSync(resolve(root, path)), `${path}: actual lifecycle API source must exist`);
    const [prefix, suffix] = apiSelector.split('*');
    assert.ok(path.startsWith(prefix) && path.endsWith(suffix) &&
      !path.slice(prefix.length, -suffix.length).includes('/'), `${apiSelector} must admit ${path}`);
  }
}

test('checkpoint source schema registration and migration notes retain contract-only limits', () => {
  const schema = JSON.parse(read('schemas/execution-checkpoint-1.0.schema.json'));
  assert.equal(schema.$id, 'https://microsoft.github.io/waza/schemas/execution-checkpoint-1.0.schema.json');
  assert.equal(schema.properties.kind.const, 'waza.execution-checkpoint');
  assert.equal(schema.properties.schema_version.const, '1.0');
  assert.deepEqual(schema.properties.boundary.enum, ['before-unit', 'finalized-unit']);
  assert.equal(schema.additionalProperties, false);
  assert.deepEqual(schema.oneOf[1].required, ['completion_digest', 'manifest_digest']);
  const route = read('site/src/pages/schemas/[schema].json.ts');
  assert.match(route, /import checkpoint from '\.\.\/\.\.\/\.\.\/\.\.\/schemas\/execution-checkpoint-1\.0\.schema\.json'/);
  assert.match(route, /schema: 'execution-checkpoint-1\.0\.schema' \}, props: \{ schema: checkpoint \}/);
  const resultSchema = JSON.parse(read('schemas/runner-checkpoint-result-1.0.schema.json'));
  assert.equal(resultSchema.properties.kind.const, 'waza.runner-checkpoint-result');
  assert.equal(resultSchema.additionalProperties, false);
  assert.equal(resultSchema.properties.submission.$ref, 'execution-submission-result-1.0.schema.json');
  assert.ok(resultSchema.required.includes('restored_units'));
  assert.ok(resultSchema.required.includes('remaining_work'));
  assert.match(route, /schema: 'runner-checkpoint-result-1\.0\.schema' \}, props: \{ schema: checkpointResult \}/);
  const notes = read('site/src/content/docs/reference/schema-changes.md');
  assert.match(notes, /Checkpoint declaration 1\.0 \(unreleased contract layer\)/);
  assert.match(notes, /unreleased source provides protected checkpoint\s+creation and resume commands for the exact-unit profile/);
  assert.match(notes, /ownership, compatibility and restore\s+progress explicitly unavailable/);
  assert.match(notes, /\(unreleased contract layer\)/);
  assert.match(notes, /\.\.\/\.\.\/schemas\/execution-checkpoint-1\.0\.schema\.json/);
  assert.match(notes, /Only destination attempt ordinal may differ; original source\s+identity\/selection must not be retagged/);
  assert.ok(existsSync(resolve(root, 'site/e2e/execution-checkpoint.spec.ts')));
  assert.match(read('site/e2e/execution-checkpoint.spec.ts'), /The unreleased source provides protected checkpoint creation and resume commands for the exact-unit profile\./);
  assertExecutionBrowserDiscovery(read('site/playwright.config.ts'), 'site');
  const lab = read('site/src/content/docs/guides/dashboard-lab.mdx');
  assert.match(lab, /GET \/api\/v1\/checkpoints\/\{digest\}/);
  assert.match(lab, /restored units and remaining work stay \*\*Unavailable\*\*/);
  assert.ok(existsSync(resolve(root, 'web/e2e/checkpoint-inspection.spec.ts')));
  assertExecutionBrowserDiscovery(read('web/playwright.config.ts'), 'web');
  assert.equal(resultSchema.properties.source_withheld.const, true);
  assert.match(notes, /output-only optional `source_withheld: true`/);
  assert.match(read('site/astro.config.mjs'), /slug: 'guides\/runner-checkpoints'/);
  const guide = read('site/src/content/docs/guides/runner-checkpoints.mdx');
  assert.match(guide, /checkpoint create --store/);
  assert.match(guide, /checkpoint resume BEFORE_SHA256/);
  assert.match(guide, /checkpoint resume FINALIZED_SHA256/);
  assert.match(guide, /before-unit|fresh retry child/);
  assert.match(guide, /zero new engine calls/);
  assert.match(guide, /never\s+accepted as operation input or authority/);
  const dashboardWorkflow = read('.github/workflows/web-ci.yml');
  for (const path of ['internal/executioncheckpoint/**', 'internal/executionattempt/**',
    'internal/executionledger/**', 'internal/artifactmanifest/**', 'internal/webapi/checkpoints*.go',
    'internal/webapi/manifests.go', 'schemas/execution-*.json', 'schemas/artifact-manifest-*.json',
    'schemas/execution_checkpoint.go']) {
    assert.equal(dashboardWorkflow.split(`'${path}'`).length - 1, 2, `${path} must select push and PR dashboard checks`);
  }
  for (const path of ['site/**', 'schemas/**']) {
    assert.ok(read('.github/workflows/site-ci.yml').includes(`'${path}'`), `${path} must select site checks`);
    assert.ok(read('.github/workflows/pages.yml').includes(`'${path}'`), `${path} must select publication`);
  }
});

test('lifecycle source schemas, declared inspection guidance and browser discovery stay connected', () => {
  const route = read('site/src/pages/schemas/[schema].json.ts');
  for (const [file, kind] of [
    ['execution-lifecycle-observation-1.0', 'waza.execution-lifecycle-observation'],
    ['execution-lifecycle-inspection-1.0', 'waza.execution-lifecycle-inspection'],
    ['execution-lifecycle-1.0', 'waza.execution-lifecycle'],
  ]) {
    const schema = JSON.parse(read(`schemas/${file}.schema.json`));
    assert.equal(schema.$id, `https://microsoft.github.io/waza/schemas/${file}.schema.json`);
    assert.equal(schema.properties.kind.const, kind);
    assert.equal(schema.properties.schema_version.const, '1.0');
    assert.equal(schema.additionalProperties, false);
    assert.ok(route.includes(`schemas/${file}.schema.json`));
    assert.ok(route.includes(`schema: '${file}.schema'`));
  }
  const guide = read('site/src/content/docs/guides/execution-lifecycle.mdx');
  assert.match(guide, /waza lifecycle inspect inspection\.json --store \.waza-artifacts/);
  assert.match(guide, /verified worker progress\s+unavailable/i);
  assert.match(guide, /Advisory next steps are\s+text, not retry\/cancel\/reclaim controls/);
  assert.match(guide, /```mermaid/);
  assert.match(read('site/astro.config.mjs'), /slug: 'guides\/execution-lifecycle'/);
  assert.match(read('site/src/content/docs/reference/cli.mdx'), /## waza lifecycle/);
  assert.match(read('site/src/content/docs/reference/schema-changes.md'), /Lifecycle declarations and inspection 1\.0/);
  assert.ok(existsSync(resolve(root, 'site/e2e/execution-lifecycle.spec.ts')));
  assertExecutionBrowserDiscovery(read('site/playwright.config.ts'), 'site');
  assertExecutionBrowserDiscovery(read('web/playwright.config.ts'), 'web');
  const workflow = read('.github/workflows/web-ci.yml');
  assertExecutionLifecycleWorkflowDiscovery(workflow);
  assert.match(workflow, /^permissions:\n  contents: read/m);
  assert.match(workflow, /timeout-minutes: 30/);
  assert.match(workflow, /delivery_phase:[\s\S]*options: \[preview, final\][\s\S]*default: preview/);
  assert.match(workflow, /npx --no-install playwright test --project=chromium --retries=0/);
});

test('execution browser discovery admits actual configurations and literal equivalents', async (t) => {
  const web = read('web/playwright.config.ts');
  const site = read('site/playwright.config.ts');
  for (const [name, config, target] of [
    ['actual web Node-harness exclusion', web, 'web'],
    ['actual site legacy chat exclusions', site, 'site'],
    ['web single-quoted literal and CRLF', web.replace(
      '"**/checkpoint-cli-process.test.mjs"', "'**/checkpoint-cli-process.test.mjs'").replaceAll('\n', '\r\n'), 'web'],
    ['site double-quoted literals and comments', site.replace(
      "['chat.spec.ts', 'chat-unavailable.spec.ts']", '["chat.spec.ts", "chat-unavailable.spec.ts"]')
      .replace('export default', '// testIgnore and testMatch examples are not declarations.\nexport default'), 'site'],
  ]) {
    await t.test(name, () => assertExecutionBrowserDiscovery(config, target));
  }
});

test('execution browser discovery rejects mutations of the actual protected configurations', async (t) => {
  for (const target of ['web', 'site']) {
    const config = read(`${target}/playwright.config.ts`);
    const declaration = config.match(/^  testIgnore:.*$/m)?.[0];
    const directory = config.match(/^  testDir:.*$/m)?.[0];
    assert.ok(declaration && directory, `${target}: actual discovery declarations must exist`);
    for (const [name, mutated, diagnostic] of [
      ['missing exclusion', config.replace(declaration, ''), /exactly one testIgnore/],
      ['duplicate exclusion', config.replace(declaration, `${declaration}\n${declaration}`), /exactly one testIgnore/],
      ['checkpoint browser exclusion', config.replace(declaration, '  testIgnore: "**/checkpoint*.spec.ts",'), /exact literal discovery/],
      ['lifecycle browser exclusion', config.replace(declaration, '  testIgnore: "**/execution-lifecycle.spec.ts",'), /exact literal discovery/],
      ['broad browser exclusion', config.replace(declaration, '  testIgnore: "**/*.spec.ts",'), /exact literal discovery/],
      ['broader Node exclusion', config.replace(declaration, '  testIgnore: "**/*.test.mjs",'), /exact literal discovery/],
      ['additional exclusion', config.replace(declaration,
        '  testIgnore: ["**/checkpoint-cli-process.test.mjs", "**/checkpoint-inspection.spec.ts"],'), /exact literal discovery/],
      ['dynamic exclusion', config.replace(declaration, '  testIgnore: process.env.PLAYWRIGHT_IGNORE,'), /exact literal discovery/],
      ['computed exclusion key', config.replace('testIgnore:', '["testIgnore"]:'), /computed configuration keys/],
      ['additional computed key', config.replace(declaration,
        `${declaration}\n  ["test" + "Ignore"]: "**/*.spec.ts",`), /computed configuration keys/],
      ['dynamic configuration spread', config.replace(declaration, `${declaration}\n  ...discovery,`), /dynamic configuration spreads/],
      ['testMatch narrowing', config.replace(declaration, `${declaration}\n  testMatch: "**/checkpoint-inspection.spec.ts",`), /testMatch must not narrow/],
      ['project exclusion', config.replace('name: ', 'testIgnore: "**/checkpoint*.spec.ts", name: '), /exactly one testIgnore/],
      ['commented-out exclusion', config.replace(declaration, `  // ${declaration.trim()}`), /exactly one testIgnore/],
      ['example-string exclusion', config.replace(declaration, '').replace('export default',
        `const example = \`\n${declaration}\n\`;\nexport default`), /exactly one testIgnore/],
      ['dynamic exclusion suffix', config.replace(declaration, declaration.replace(/,$/, ' + extraExclusions,')), /dynamic or additional value/],
      ['missing test directory', config.replace(directory, ''), /exactly one testDir/],
      ['dynamic test directory', config.replace(directory, '  testDir: process.env.PLAYWRIGHT_TEST_DIR,'), /exact literal discovery/],
    ]) {
      assert.notEqual(mutated, config, `${target}/${name}: mutation must change the actual configuration`);
      await t.test(`${target}/${name}`, () =>
        assert.throws(() => assertExecutionBrowserDiscovery(mutated, target), diagnostic));
    }
  }
});

test('lifecycle workflow discovery rejects missing selectors despite matching comment counts', async (t) => {
  const workflow = read('.github/workflows/web-ci.yml');
  const selector = 'internal/webapi/lifecycle*.go';
  const pushLine = `      - '${selector}'\n`;
  const filterLine = `              - '${selector}'\n`;
  for (const [name, lines, diagnostic] of [
    ['main push selector', [pushLine], /main push gate: missing exact lifecycle selector/],
    ['PR web selector', [filterLine], /PR web filter: missing exact lifecycle selector/],
    ['both production selectors', [pushLine, filterLine], /main push gate: missing exact lifecycle selector/],
  ]) {
    let mutated = workflow;
    for (const line of lines) {
      assert.equal(workflow.split('\n').filter((entry) => `${entry}\n` === line).length, 1,
        `${name}: actual selector must exist exactly once in the targeted gate`);
      mutated = mutated.replace(line, '') + `\n# non-selector '${selector}'\n`;
    }
    assert.notEqual(mutated, workflow, `${name}: mutation must remove the actual gate selector`);
    assert.equal(mutated.split(`'${selector}'`).length - 1, 2, 'comments must preserve the misleading global count');
    await t.test(name, () => assert.throws(() => assertExecutionLifecycleWorkflowDiscovery(mutated), diagnostic));
  }
});

test('local container conformance is selected by its production build inputs', () => {
  const workflow = read('.github/workflows/docker-ci.yml');
  for (const path of ['examples/runner/unit.json', 'examples/runner/copilot-unit.json', 'examples/runner/container/**',
    '.github/scripts/testdata/runner-runtime/**', '.github/scripts/testdata/native-bundle-probe/**',
    'cmd/**', 'internal/**', 'web/**', 'schemas/**',
    'go.mod', 'go.sum', 'version.txt', 'docs.go', 'docs/graders/**', '.dockerignore',
    '.github/workflows/docker-ci.yml']) {
    assert.ok(workflow.includes(`'${path}'`), `${path} must trigger container verification`);
  }
  assert.match(workflow, /paths: &docker-paths/);
  assert.match(workflow, /paths: \*docker-paths/);
  assert.match(workflow, /^permissions:\n  contents: read/m);
  const job = workflow.split('\n  runner-reference:\n')[1];
  assert.ok(job, 'real container job exists');
  assert.match(job, /timeout-minutes: 30/);
  assert.match(job, /lfs: true/);
  assert.match(job, /run: node --test examples\/runner\/container\/test\.mjs/);
  assert.match(job, /run: node --test examples\/runner\/container\/native-test\.mjs/);
  assert.doesNotMatch(job, /continue-on-error|if:/);
  assert.ok(existsSync(resolve(root, 'site/e2e/runner-container.spec.ts')));
  assert.doesNotMatch(read('site/playwright.config.ts'), /testIgnore:.*runner/);
});

test('manual runner publication and public acceptance remain independent fail-closed gates', () => {
  const workflow = read('.github/workflows/publish-runner-reference.yml');
  assert.match(workflow, /^on:\n  workflow_dispatch:/m);
  assert.doesNotMatch(workflow, /^  (push|pull_request|pull_request_target):/m);
  assert.match(workflow, /packages: write/);
  assert.match(workflow, /linux\/amd64/);
  assert.match(workflow, /org\.opencontainers\.image\.revision/);
  const maintainers = read('examples/runner/container/MAINTAINERS.md');
  assert.match(maintainers, /manual-only GHCR publisher/);
  assert.match(maintainers, /Publication and\s+public-image acceptance are separate gates/);
  assert.match(maintainers, /anonymous-pull\/provenance\/production-caller acceptance passed on Linux AMD64/);
  assert.match(maintainers, /review was waived for this one publication, not performed/);
  assert.match(maintainers, /permissions are declarations,\s+not proof/);
  assert.match(maintainers, /A successful push\/attestation is not public-image acceptance/);
  assert.match(maintainers, /failed, cancelled or incomplete publisher is not accepted/);
  assert.match(maintainers, /signed\s+GitHub provenance\s+attestation binds the published image digest/);
  assert.doesNotMatch(maintainers, /spboyer-native-container-integration/);
  assert.match(maintainers, /empty Docker configuration/);
  assert.match(maintainers, /manual-only `\.github\/workflows\/verify-runner-reference\.yml` workflow/);

  const verifier = read('.github/workflows/verify-runner-reference.yml');
  assert.match(verifier, /^on:\n  workflow_dispatch:/m);
  assert.doesNotMatch(verifier, /^  (push|pull_request|pull_request_target):/m);
  assert.match(verifier, /attestations: read/);
  assert.doesNotMatch(verifier, /packages: (read|write)/);
  assert.match(verifier, /node --test examples\/runner\/container\/published-image-test\.mjs/);
  assert.match(verifier, /gh attestation verify/);
  assert.doesNotMatch(verifier, /docker login/);
  const publishedTest = read('examples/runner/container/published-image-test.mjs');
  assert.match(publishedTest, /RepoDigests\.includes\(imageReference\)/);
  assert.match(publishedTest, /org\.opencontainers\.image\.revision/);
  assert.match(publishedTest, /runNamedContainer\(command, mockName/);
  assert.match(publishedTest, /runNamedContainer\(command, nativeName/);
});

test('runner identity fixtures trigger consumer validation and publication without skipping new specs', () => {
  const web = read('.github/workflows/web-ci.yml');
  for (const path of ['internal/runnerunit/**', 'internal/runneridentity/**', 'internal/runnerterminal/**', 'examples/runner/**']) {
    assert.equal(web.split(`'${path}'`).length - 1, 2, `${path} must trigger push and PR filtering`);
  }
  const site = read('.github/workflows/site-ci.yml');
  const deploy = read('.github/workflows/pages.yml');
  for (const path of [
    '.github/workflows/publish-runner-reference.yml',
    '.github/workflows/verify-runner-reference.yml',
  ]) {
    assert.equal(site.split(`'${path}'`).length - 1, 2,
      `${path} must select both push and PR site documentation checks`);
  }
  for (const path of ['internal/runnerunit/testdata/**', 'internal/runneridentity/testdata/**', 'internal/runnerterminal/testdata/**']) {
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
  assert.ok(existsSync(resolve(root, 'site/e2e/runner-compatibility.spec.ts')));
  assert.ok(existsSync(resolve(root, 'site/e2e/runner-identities.spec.ts')));
  assert.ok(existsSync(resolve(root, 'site/e2e/runner-terminal.spec.ts')));
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

function assertSiteDiscovery(workflow, inputs) {
  const push = workflow.match(/^  push:\n([\s\S]*?)(?=^  pull_request:)/m)?.[1];
  const pullRequest = workflow.match(/^  pull_request:\n([\s\S]*?)(?=^  [a-z_]+:|^permissions:)/m)?.[1];
  const site = workflow.match(/^            site:\n([\s\S]*?)(?=^            examples:)/m)?.[1];
  assert.ok(push && pullRequest !== undefined && site, 'expected workflow event/filter blocks must exist');
  assert.match(push, /branches: \[main\]/);
  assert.doesNotMatch(pullRequest, /^\s+branches(?:-ignore)?:/m, 'required PR status must cover every stacked base');
  assert.doesNotMatch(pullRequest, /^\s+paths(?:-ignore)?:/m, 'required PR status must not be path-gated');
  assert.match(workflow, /^  workflow_dispatch:\s*$/m, 'manual validation trigger must exist');
  const paths = (block) => [...block.matchAll(/^\s+- '([^'\n]+)'$/gm)].map((match) => match[1]);
  const matches = (pattern, path) => pattern.endsWith('/**')
    ? path.startsWith(pattern.slice(0, -2)) : pattern === path;
  for (const input of inputs) {
    assert.ok(paths(site).some((pattern) => matches(pattern, input)), `${input}: PR site filter excludes documentation input`);
    assert.ok(paths(push).some((pattern) => matches(pattern, input)), `${input}: main push gate excludes documentation input`);
  }
  assert.ok(!paths(site).some((pattern) => matches(pattern, 'internal/cache/cache.go')), 'unrelated Go code must not select the PR site build');
  const build = workflow.match(/^      - name: Build site\n([\s\S]*?)(?=^      - name:)/m)?.[1];
  assert.ok(build, 'site build step must exist');
  assert.match(build, /^        if: needs\.changes\.outputs\.site == 'true' \|\| github\.event_name == 'push' \|\| github\.event_name == 'workflow_dispatch'$/m,
    'manual site validation must not skip the build');
  assert.match(build, /run: cd site && npm run build/);
  const scripts = JSON.parse(read('site/package.json')).scripts;
  assert.equal(scripts.prebuild, 'npm run test:docs');
  assert.equal(scripts['test:docs'], 'node --test tests/documentation.test.mjs');
}

test('every catalog, release and Docker workflow input enables PR site checks and the main push workflow', () => {
  assertSiteDiscovery(read('.github/workflows/site-ci.yml'),
    [...Object.values(catalogInputs).flat(), ...releaseInputs, '.github/workflows/docker-ci.yml']);
});

test('Docker guard discovery rejects removal from either production site selector', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  const input = '.github/workflows/docker-ci.yml';
  for (const [selector, line, diagnostic] of [
    ['push', `      - '${input}'\n`, /main push gate excludes documentation input/],
    ['PR site filter', `              - '${input}'\n`, /PR site filter excludes documentation input/],
  ]) {
    const mutated = workflow.replace(line, '');
    assert.notEqual(mutated, workflow, `${selector} mutation must remove the actual selector`);
    assert.throws(() => assertSiteDiscovery(mutated, [input]), diagnostic, selector);
  }
});

test('site discovery rejects main-only stacked PRs and skipped manual builds', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  const condition = "needs.changes.outputs.site == 'true' || github.event_name == 'push'";
  for (const [name, mutated, diagnostic] of [
    ['stacked base', workflow.replace('  pull_request:\n', '  pull_request:\n    branches: [main]\n'), /every stacked base/],
    ['manual trigger', workflow.replace('  workflow_dispatch:\n', ''), /manual validation trigger/],
    ['manual build', workflow.replaceAll(`${condition} || github.event_name == 'workflow_dispatch'`, condition), /manual site validation/],
  ]) {
    assert.notEqual(mutated, workflow, `${name} mutation must change the production workflow`);
    assert.throws(() => assertSiteDiscovery(mutated, []), diagnostic, name);
  }
});

function assertInteropExampleDiscovery(workflow) {
  const push = workflow.match(/^  push:\n([\s\S]*?)(?=^  pull_request:)/m)?.[1];
  const examples = workflow.match(/^            examples:\n([\s\S]*?)(?=^      - name:)/m)?.[1];
  for (const [name, block] of [['main push gate', push], ['PR examples filter', examples]]) {
    assert.ok(block, `${name} must exist`);
    const patterns = [...block.matchAll(/^\s+- '([^'\n]+)'$/gm)].map((match) => match[1]);
    assert.ok(patterns.includes('examples/interop/**'), `${name} must select script-only interop changes`);
  }
  assert.match(workflow, /examples: \$\{\{ steps\.filter\.outputs\.examples \}\}/);
  assert.match(workflow, /if: needs\.changes\.outputs\.examples == 'true' \|\| github\.event_name == 'push'/);
}

function assertSiteParallelChecks(workflow) {
  const jobs = Object.fromEntries([...workflow.matchAll(/^  (changes|build|examples):\n([\s\S]*?)(?=^  [a-z][a-z-]*:\n|$(?![\s\S]))/gm)]
    .map((match) => [match[1], match[2]]));
  assert.ok(jobs.changes, 'short change detection prerequisite must exist');
  assert.match(jobs.changes, /timeout-minutes: 5/);
  assert.doesNotMatch(jobs.changes, /^\s+needs:/m, 'change detection must run independently');
  assert.match(jobs.changes, /site: \$\{\{ steps\.filter\.outputs\.site \}\}/, 'site output must bind to actual filter');
  assert.match(jobs.changes, /examples: \$\{\{ steps\.filter\.outputs\.examples \}\}/);
  assert.match(jobs.changes, /uses: dorny\/paths-filter@/);
  assert.doesNotMatch(jobs.changes, /npm|playwright/, 'change detection must not wait for browser or example work');
  for (const [id, name] of [['build', 'Build site'], ['examples', 'Offline documentation examples']]) {
    assert.ok(jobs[id], `${id} job must exist`);
    assert.match(jobs[id], /^\s+needs: \[changes\]$/m, `${id} must depend only on successful change detection`);
    assert.match(jobs[id], new RegExp(`^    name: ${name}$`, 'm'), 'required check names must stay stable');
    assert.doesNotMatch(jobs[id], /^    if:.*always\(\)/m, 'workflow cancellation must not enable validation');
  }
  assert.match(jobs.build, /^    if: \$\{\{ !cancelled\(\) \}\}$/m, 'required Build site check must run after failed detection');
  assert.match(jobs.build, /CHANGES_RESULT: \$\{\{ needs\.changes\.result \}\}/);
  assert.match(jobs.build, /run: test "\$CHANGES_RESULT" = success/, 'required site check must reject failed or skipped change detection');
  assert.match(jobs.build, /if: needs\.changes\.outputs\.site == 'true' \|\| github\.event_name == 'push'/);
  assert.match(jobs.examples, /if: needs\.changes\.outputs\.examples == 'true' \|\| github\.event_name == 'push'/,
    'examples condition must consume short prerequisite output');
}

test('site browser checks and offline examples wait only for short change detection', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  assertSiteParallelChecks(workflow);
  for (const [old, replacement, diagnostic] of [
    ['    needs: [changes]\n    if: needs.changes.outputs.examples',
      '    needs: [build]\n    if: needs.changes.outputs.examples', /examples must depend only/],
    ['    needs: [changes]\n    if: ${{ !cancelled() }}',
      '    needs: [changes, examples]\n    if: ${{ !cancelled() }}', /build must depend only/],
    ['    timeout-minutes: 5', '    timeout-minutes: 15', /timeout-minutes: 5/],
    ['site: ${{ steps.filter.outputs.site }}', "site: 'true'", /site output must bind/],
    ["if: needs.changes.outputs.examples == 'true' || github.event_name == 'push'",
      "if: needs.build.outputs.examples == 'true' || github.event_name == 'push'", /examples condition must consume/],
    ['run: test "$CHANGES_RESULT" = success', 'run: echo ignored', /required site check must reject/],
  ]) {
    const mutated = workflow.replace(old, replacement);
    assert.notEqual(mutated, workflow, 'mutation must change actual workflow scheduling or output');
    assert.throws(() => assertSiteParallelChecks(mutated), diagnostic);
  }
});

test('interop script-only changes select executable controls and selector removal fails closed', () => {
  const workflow = read('.github/workflows/site-ci.yml');
  assertInteropExampleDiscovery(workflow);
  for (const [line, diagnostic] of [
    ["      - 'examples/interop/**'\n", /main push gate must select script-only interop changes/],
    ["              - 'examples/interop/**'\n", /PR examples filter must select script-only interop changes/],
  ]) {
    const mutated = workflow.replace(line, '');
    assert.notEqual(mutated, workflow, 'mutation must remove the actual selector');
    assert.throws(() => assertInteropExampleDiscovery(mutated), diagnostic);
  }
});

test('site validation and deployment pin a supported Astro Node runtime', () => {
  const lock = JSON.parse(read('site/package-lock.json'));
  assert.match(lock.packages['node_modules/astro'].engines.node, />=22\.23\.4/);
  for (const path of ['.github/workflows/site-ci.yml', '.github/workflows/pages.yml']) {
    const versions = [...read(path).matchAll(/node-version: '([^']+)'/g)].map((match) => match[1]);
    assert.ok(versions.length > 0, `${path}: expected an explicit Node version`);
    for (const version of versions) {
      assert.equal(version, '24', `${path}: keep site CI and deployment on the supported Node major`);
    }
  }
  assert.match(read('site/README.md'), /Node\.js 22\.23\.4\+ \(22\.x\)/);
  assert.match(read('site/README.md'), /Pages workflows use Node\.js 24/);
});

test('source development prerequisites agree with the locked frontend engines', () => {
  const site = JSON.parse(read('site/package-lock.json')).packages;
  const web = JSON.parse(read('web/package-lock.json')).packages;
  assert.equal(site['node_modules/astro'].engines.node, '>=22.23.4');
  assert.equal(site['node_modules/astro'].engines.npm, '>=9.6.5');
  assert.equal(web['node_modules/eslint'].engines.node, '^20.19.0 || ^22.13.0 || >=24');
  assert.equal(web['node_modules/vite'].engines.node, '^20.19.0 || >=22.12.0');
  for (const path of [
    'README.md', 'docs/GUIDE.md', 'site/src/content/docs/getting-started.mdx',
    'site/src/content/docs/quick-start.mdx', 'site/src/content/docs/about.mdx',
    'site/src/content/docs/reference/releases.mdx', 'site/src/content/docs/guides/ci-cd.mdx',
    'site/src/content/docs/guides/docs-assistant.mdx',
  ]) {
    const text = read(path);
    assert.match(text, /Node(?:\.js)? 22\.23\.4\+/, `${path}: document the shared Node 22 minimum`);
    assert.match(text, /npm 9\.6\.5\+/, `${path}: document the site npm minimum`);
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

test('release articles are linked from Releases and retain their existing screenshot assets', () => {
  for (const slug of ['v0-40-1', 'v0-40-0', 'v0-39-0', 'dashboard-lab']) {
    const article = read(`site/src/content/docs/releases/${slug}.mdx`);
    assert.ok(read('site/astro.config.mjs').includes(`slug: 'releases/${slug}'`));
    assert.ok(read('site/src/content/docs/reference/releases.mdx').includes(`](../../releases/${slug}/)`));
    const screenshots = [...article.matchAll(/<ZoomImage src="([^"]+)" alt="([^"]+)" \/>/g)];
    assert.equal(screenshots.length, slug.startsWith('v0-40-') ? 0 : 3, slug);
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

test('v0.40.1 article owns hotfix detail while the homepage leads with the product', () => {
  const homepage = read('site/src/content/docs/index.mdx');
  const intro = homepage.split('## What can waza do?')[0];
  assert.doesNotMatch(intro, /<Aside\b/);
  assert.equal((intro.match(/\]\(\/waza\/releases\/v0-40-1\/\)/g) ?? []).length, 1);
  assert.match(intro, /\*\*New in v0\.40\.1:\*\*/);
  assert.match(homepage, /A mock pass proves harness wiring, not agent quality/);
  assert.match(homepage, /authentication, permissions and possible-cost prerequisites/);
  assert.match(read('README.md'), /https:\/\/microsoft\.github\.io\/waza\/releases\/v0-40-1\//);
  assert.match(read('README.md'), /https:\/\/microsoft\.github\.io\/waza\/releases\/v0-40-0\//);

  const hotfix = read('site/src/content/docs/releases/v0-40-1.mdx');
  const hotfixText = hotfix.replace(/\s+/g, ' ');
  assert.match(hotfix, /^title: "Waza v0\.40\.1: make release workflows match their contracts"$/m);
  assert.match(hotfix, /\*\*Released November 6, 2026\*\*/);
  for (const heading of [
    'Count and group the work that will actually run',
    'Make automation observe the documented process result',
    'Keep saved runs and artifact paths distinct',
    'Avoid canceling healthy execution and large graders',
    'Upgrade without changing the experiment',
  ]) assert.ok(hotfix.includes(`## ${heading}`), heading);
  for (const phrase of [
    'reports zero tasks and Partial coverage',
    '`waza tokens check --strict` now exits **1**',
    '`config.group_by: tags`',
    'top-level eval `inputs`',
    'distinct, monotonic nanosecond-resolution run IDs',
    '`waza adversarial --artifacts-dir`',
    'disarms the first-event watchdog without canceling the active request',
    'reads its context directly from file descriptor 0',
  ]) assert.match(hotfixText, new RegExp(phrase.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  assert.match(hotfixText, /v0\.40\.0\.\.\.v0\.40\.1 comparison/);

  const article = read('site/src/content/docs/releases/v0-40-0.mdx');
  assert.match(article, /^title: "Waza v0\.40\.0: run exactly the work you declared"$/m);
  assert.match(article, /\*\*Released November 4, 2026\*\*/);
  for (const heading of [
    'Execute one attempt, not an implicit suite',
    'Know which input a result belongs to',
    'Handle failed checks differently from failed infrastructure',
    'Use the same boundary locally or in a container',
    'Design more focused tests and inspect better evidence',
    "Upgrade with the published release's limits in view",
  ]) assert.ok(article.includes(`## ${heading}`), heading);
  assert.match(article, /sha256-waza-c14n-v1/);
  assert.match(article, /input-derived identities, not execution attestations/);
  assert.match(article, /one explicitly selected, pinned AgentV\s+case-sensitive `contains` assertion/);
  assert.match(article, /whole-task\/suite interoperability and full evidence\/provenance round trips\s+remain unsupported/);
  assert.match(article, /#761\]\(https:\/\/github\.com\/microsoft\/waza\/issues\/761\)/);
  assert.match(article, /#789\]\(https:\/\/github\.com\/microsoft\/waza\/issues\/789\)/);
  assert.match(article, /#882\]\(https:\/\/github\.com\/microsoft\/waza\/pull\/882\) is \*\*main-only and unreleased\*\*/);
  assert.match(article, /cancellation-cleanup follow-up/);
  assert.match(article, /Those main changes have not been republished as v0\.40\.0/);
  assert.match(article, /make build VERSION=0\.40\.0/);
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
  assert.match(overview, /## Current published standalone release: v0\.40\.1/);
  assert.match(overview, /id="upcoming-main-only-capabilities"/);
  const downloads = [...overview.matchAll(/releases\/download\/v0\.40\.1\/(waza-[^)\s]+)/g)].map((match) => match[1]);
  assert.deepEqual(downloads.toSorted(), [
    'waza-darwin-amd64', 'waza-darwin-arm64', 'waza-linux-amd64',
    'waza-linux-arm64', 'waza-windows-amd64.exe', 'waza-windows-arm64.exe',
  ]);
  assert.match(overview, /releases\/download\/v0\.40\.1\/checksums\.txt/);
  assert.match(overview, /publication does not\s+by itself establish execution on every platform/);
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
  assert.match(section, /releases\/tag\/azd-ext-microsoft-azd-waza_0\.40\.1/);
  assert.match(section, /registry and version metadata are synchronized\s+to `0\.40\.1`/);
  assert.match(section, /existing source\s+or installation may need updating before it selects v0\.40\.1/);
  const release = read('site/src/content/docs/reference/releases.mdx');
  assert.match(release, /commit `dc26e098d29243972b4e00ff420ca857a9b6da4f`/);
  assert.match(release, /exactly six platform URLs, entrypoints and SHA-256 values matching the\s+published archives/);
  assert.match(release, /does not establish execution on all six platforms or prove that an existing\s+installation has updated to v0\.40\.1/);
  assert.match(release, /repository registry lists through `0\.40\.1`/);
  assert.doesNotMatch(section + release, /not yet merged to main|merge to main\s+remains pending|registry still lists through `0\.39\.1`/);
  for (const text of [read('README.md'), section, release]) {
    assert.match(text, /source (?:URL above|below) follows `main`, not this branch/);
    assert.match(text, /release-sync PR #908/);
    assert.match(text, /still serves `0\.40\.0`; after the merge it includes `0\.40\.1`/);
    assert.match(text, /installation before the merge cannot select `0\.40\.1` from that source/);
  }
});

test('release entry points agree on published versions and the current runner result writer', () => {
  const release = read('CHANGELOG.md').match(/^## \[(\d+\.\d+\.\d+)\] - (\d{4}-\d{2}-\d{2})$/m);
  assert.ok(release, 'a dated release entry, not post-tag synchronization metadata, identifies the published version');
  const published = release[1];
  assert.match(read('version.txt').trim(), /^\d+\.\d+\.\d+$/);
  const readme = read('README.md').replace(/\s+/g, ' ');
  assert.equal(readme.match(/published standalone CLI is `v([^`]+)`/)?.[1], published);
  assert.equal(
    read('site/src/content/docs/reference/releases.mdx')
      .match(/^## Current published standalone release: v(\S+)$/m)?.[1],
    published,
  );

  const registry = JSON.parse(read('registry.json'));
  const extension = registry.extensions.find(({ id }) => id === 'microsoft.azd.waza');
  assert.ok(extension);
  const latest = extension.versions.map(({ version }) => version)
    .toSorted((a, b) => a.localeCompare(b, 'en', { numeric: true })).at(-1);
  assert.equal(readme.match(/registry currently lists through `([^`]+)`/)?.[1], latest);

  const writer = read('internal/models/schema_version.go')
    .match(/CurrentResultSchemaVersion = "([^"]+)"/)?.[1];
  assert.match(writer ?? '', /^\d+\.\d+$/);
  const escapedWriter = writer.replace('.', '\\.');
  const compatibilityRow = read('site/src/content/docs/guides/runner-compatibility.mdx')
    .match(/^\| Local results \| ([^|]+)\|/m)?.[1];
  assert.match(compatibilityRow ?? '', new RegExp(`\\bschema ${escapedWriter}\\b`));
  // Released v0.40.0 entries keep the writer that release shipped, which can
  // trail the current source writer.
  const releaseWriter = read('site/src/content/docs/reference/releases.mdx')
    .match(/v0\.40\.0 result writers emit \*\*(\d+\.\d+)\*\*/)?.[1];
  assert.match(releaseWriter ?? '', /^\d+\.\d+$/);
  const escapedReleaseWriter = releaseWriter.replace('.', '\\.');
  const preview = read('docs/PRD.md').split('The v0.40.0 runner')[1]?.split('### The Problem')[0];
  assert.match(preview ?? '', new RegExp(`result schema ${escapedReleaseWriter}\\b`));
  const changelogEntry = read('CHANGELOG.md').split('\n')
    .find((line) => line.startsWith('- **Bounded local compatibility'));
  assert.match(changelogEntry ?? '', new RegExp(`writer schema ${escapedReleaseWriter}\\b`));

  const sdk = read('go.mod').match(/github\.com\/github\/copilot-sdk\/go v(\S+)/)?.[1];
  const bundledCLI = read('internal/embedded/zcopilot_darwin_arm64.go').match(/Version:\s+"([^"]+)"/)?.[1];
  assert.ok(sdk && bundledCLI);
  for (const path of embeddedFiles) {
    const target = path.match(/(darwin|linux|windows)_(amd64|arm64)\.go$/)?.[0].slice(0, -3);
    assert.ok(target, `${path}: expected an embedded CLI target`);
    const bundled = read(path);
    const executable = `zcopilot_${bundledCLI}_${target}${target.startsWith('windows_') ? '.exe' : ''}`;
    assert.equal(bundled.match(/Version:\s+"([^"]+)"/)?.[1], bundledCLI, `${path}: embedded CLI version mismatch`);
    assert.ok(bundled.includes(`//go:embed ${executable}.zst`), `${path}: embedded CLI bundle mismatch`);
    assert.ok(bundled.includes(`//go:embed ${executable}.license`), `${path}: embedded CLI license mismatch`);
    const runtimeAssets = [...bundled.matchAll(/^\/\/go:embed (zcopilotruntime(?:wrapper|assets)?_\S+)$/gm)]
      .map((match) => match[1]);
    assert.equal(runtimeAssets.length, target.startsWith('linux_') ? 6 : 3, `${path}: missing runtime companions`);
    for (const asset of [`${executable}.zst`, `${executable}.license`, ...runtimeAssets]) {
      assert.ok(asset.includes(`_${bundledCLI}_`), `${path}: ${asset} version mismatch`);
      const location = resolve(root, 'internal/embedded', asset);
      assert.ok(existsSync(location) && statSync(location).isFile() && statSync(location).size > 0, `${path}: missing ${asset}`);
    }
  }
  assert.ok(read('site/src/content/docs/reference/cli.mdx').includes(
    `github.com/github/copilot-sdk/go\` v${sdk} with bundled CLI ${bundledCLI}`,
  ));
  const runtimeEntry = read('CHANGELOG.md').split('\n')
    .find((line) => line.startsWith('- **Copilot runtime dependencies**'));
  assert.ok(runtimeEntry?.includes(`Go SDK to \`${sdk}\` and bundled CLI to \`${bundledCLI}\``));
  assert.ok(read('site/src/content/docs/guides/custom-agents.mdx').includes(
    `The bundled Copilot CLI ${bundledCLI} uses the \`workflow\` permission kind.`,
  ));
});

test('released feature guides retain scope and separate publication gates', () => {
  const article = read('site/src/content/docs/releases/v0-40-0.mdx');
  assert.match(article, /\*\*mock\/text\*\* and explicitly opted-in \*\*Copilot\/text\*\*/);
  assert.match(article, /tools and skills\s+disabled/);
  assert.match(article, /General native file\/tool\/skill workflows remain unsupported/);
  assert.match(article, /default\s+scratch target is mock-only/);
  assert.match(article, /Linux AMD64/);
  assert.match(article, /not live-provider reliability/);
  assert.match(article, /published-image record\]\(\.\.\/\.\.\/guides\/runner-container\/#published-image-and-acceptance-evidence\)/);

  const overview = read('site/src/content/docs/reference/releases.mdx')
    .split('## v0.40.0 highlights and boundaries')[1]?.split('<span id=')[0];
  assert.match(overview ?? '', /guides describe standalone v0\.40\.0/);
  assert.match(overview ?? '', /mock\/text and explicitly opted-in\s+Copilot\/text profiles, with tools and skills disabled/);
  assert.match(overview ?? '', /General native file\/tool\/skill workflows and full external task\/suite interoperability remain incomplete/);
  assert.match(overview ?? '', /manual-only GHCR publisher and separate verifier have independent gates/);
  assert.match(overview ?? '', /\[selected AgentV assertion adapter\]\(\.\.\/\.\.\/guides\/external-interoperability\/\)/);
  assert.match(overview ?? '', /`waza interop import\/export`/);
  assert.match(overview ?? '', /#789\]\(https:\/\/github\.com\/microsoft\/waza\/issues\/789\)\s+remains open/);
  const released = read('CHANGELOG.md').split('## [0.40.0] - 2026-11-04')[1]?.split('## [0.39.1]')[0];
  assert.match(released ?? '', /Selected AgentV assertion interoperability/);
  assert.match(released ?? '', /`waza interop import\/export`/);
  assert.match(released ?? '', /not whole-task\/suite support/);
  assert.match(released ?? '', /#789 remains open/);
  assert.match(overview ?? '', /\[local container foundation\]\(\.\.\/\.\.\/guides\/runner-container\/\)/);
  assert.match(overview ?? '', /published native reference passed digest-pinned\s+acceptance on Linux AMD64/);
  assert.match(overview ?? '', /Publisher attempt 1 and the verifier succeeded: anonymous pull, source\/platform,/);
  assert.match(overview ?? '', /signed provenance\/finalization and mock\/native synthetic-provider production\s+controls passed/);
  assert.match(overview ?? '', /publisher run alone\s+does not establish public-image acceptance/);
  assert.match(overview ?? '', /azd extension artifact and registry sync are\s+independent gates/);
  assert.match(overview ?? '', /v0\.40\.0 result writers emit \*\*1\.8\*\*; v0\.39\.1 writes \*\*1\.7\*\*/);
  assert.match(overview ?? '', /legacy eval\/result inputs still default to \*\*1\.4\*\*/);
  assert.match(overview ?? '', /Runner request and terminal\s+schemas remain independently versioned \*\*1\.0\*\*/);
  assert.match(overview ?? '', /task YAML has no independent\s+version field/);

  for (const [path, heading] of [
    ['site/src/content/docs/guides/eval-yaml.mdx', '### Task-level MCP mocks'],
    ['site/src/content/docs/guides/graders.mdx', '#### Background shell lifecycle'],
    ['site/src/content/docs/guides/graders.mdx', '## Program'],
    ['site/src/content/docs/guides/dependency-recovery.mdx', '### Multi-turn MCP sequences'],
    ['site/src/content/docs/guides/dashboard-lab.mdx', '### Read a multi-turn conversation'],
  ]) {
    const section = read(path).split(heading)[1]?.split(/\n#{2,4} /)[0];
    assert.match(section ?? '', /\*\*Added in v0\.40\.0:\*\*/, `${path}: ${heading}`);
    assert.match(section ?? '', /v0\.39\.1/, `${path}: ${heading}`);
  }
});

test('release scope does not claim the legacy multi-trial timeout is fixed', () => {
  for (const path of [
    'README.md', 'CHANGELOG.md', 'docs/GUIDE.md', 'docs/PRD.md', 'docs/RUNNER_CONTRACT.md',
    'site/src/content/docs/releases/v0-40-0.mdx', 'site/src/content/docs/reference/releases.mdx',
    'site/src/content/docs/guides/runner-contract.mdx',
  ]) {
    const text = read(path).replace(/\s+/g, ' ');
    assert.match(text, /(?:do not fix|do not resolve) the (?:broader |separate )?legacy multi-trial timeout issue/, path);
    assert.match(text, /\[#865\]\(https:\/\/github\.com\/microsoft\/waza\/issues\/865\)/, path);
    assert.match(text, /\[#870\]\(https:\/\/github\.com\/microsoft\/waza\/pull\/870\)/, path);
    assert.match(text, /post-v0\.40\.0 source behavior, not included in the published v0\.40\.0 artifacts/, path);
    assert.doesNotMatch(text, /remains open\/backlogged/, path);
  }
  const changelog = read('CHANGELOG.md');
  const unreleased = changelog.split('## [Unreleased]')[1]?.split('## [0.40.1]')[0];
  const hotfix = changelog.split('## [0.40.1]')[1]?.split('## [0.40.0]')[0];
  assert.match(unreleased ?? '', /Trial timeout recovery and result persistence/);
  assert.match(unreleased ?? '', /pull\/870/);
  assert.doesNotMatch(unreleased ?? '', /\*\*JavaScript `code` grader input\*\*/);
  assert.match(hotfix ?? '', /\*\*JavaScript `code` grader input\*\*/);
  assert.match(hotfix ?? '', /pull\/882/);
  assert.match(changelog, /^\[Unreleased\]: https:\/\/github\.com\/microsoft\/waza\/compare\/v0\.40\.1\.\.\.HEAD$/m);
  const published = changelog.split('## [0.40.0]')[1]?.split('## [0.39.1]')[0];
  assert.doesNotMatch(published ?? '', /\*\*Trial timeout recovery and result persistence\*\*/);
  assert.doesNotMatch(published ?? '', /\*\*JavaScript `code` grader input\*\*/);
  assert.match(read('README.md'), /\*\*Post-v0\.40\.0 source behavior:\*\* The following cleanup notes describe/);
  assert.match(unreleased ?? '', /\*\*Cancellation force-stop join\*\*/);
  assert.doesNotMatch(published ?? '', /\*\*Cancellation force-stop join\*\*/);
});

test('released runner guides no longer require an unpublished source preview', () => {
  for (const name of ['runner-contract', 'runner-identities', 'runner-terminal', 'runner-compatibility']) {
    const path = `site/src/content/docs/guides/${name}.mdx`;
    assert.match(read(path), /Available in standalone v0\.40\.0/, path);
    assert.doesNotMatch(read(path), /Source-build preview|publication pending|This unreleased layer|belong to the next release layer|^## Initial contract notes \(unreleased\)/m, path);
  }
  assert.doesNotMatch(read('docs/RUNNER_CONTRACT.md'),
    /Native integration and pinned published-image acceptance remain outstanding/);
  assert.doesNotMatch(read('docs/RUNNER_IDENTITIES.md'), /The next #760 layer/);
  assert.match(read('docs/RUNNER_COMPATIBILITY_MIGRATION.md'), /^\| Category \| v0\.40\.0 behavior \|$/m);
  assert.match(read('site/src/content/docs/guides/external-interoperability.mdx'), /Standalone \*\*v0\.40\.0\*\* converts/);
  assert.match(read('site/src/content/docs/guides/runner-container.mdx'),
    /Published native reference from v0\.40\.0 source; accepted on Linux AMD64/);
  assert.match(read('CHANGELOG.md'), /^## \[0\.40\.0\] - 2026-11-04$/m);
  assert.doesNotMatch(read('CHANGELOG.md').split('## [0.40.0]')[1]?.split('## [0.39.1]')[0] ?? '',
    /v0\.40\.0 preview|publication pending|This is not a release announcement|No release blog or publication is implied/);
});

test('release preparation preserves incoming preview anchors', () => {
  for (const [path, ids] of [
    ['README.md', ['waza-interop-source-build-preview', 'waza-runner-source-build-preview']],
    ['docs/GUIDE.md', ['waza-runner-source-build-preview']],
    ['site/src/content/docs/reference/releases.mdx', ['source-build-v0400-preview', 'v0400-release-candidate-publication-pending', 'upcoming-main-only-capabilities']],
    ['site/src/content/docs/guides/runner-contract.mdx', ['initial-contract-notes-unreleased']],
    ['site/src/content/docs/guides/runner-container.mdx', ['published-candidate-and-acceptance-evidence']],
    ['examples/runner/container/MAINTAINERS.md', ['update-and-eventual-publication-policy']],
  ]) {
    for (const id of ids) assert.ok(anchors(read(path)).has(id), `${path}: preserve #${id}`);
  }
});

test('public image acceptance binds the literal digest to source, platform and verifier evidence', () => {
  const guide = read('site/src/content/docs/guides/runner-container.mdx');
  const record = guide.split('## Published image and acceptance evidence')[1]?.split('## Host and build requirements')[0];
  assert.ok(record, 'published image record must exist');
  assert.equal(record.match(/ghcr\.io\/microsoft\/waza-runner-reference@sha256:[a-f0-9]{64}/g)?.length, 1);
  assert.match(record, /ghcr\.io\/microsoft\/waza-runner-reference@sha256:ded068a636dc721b8e38dc2a68bcbcbdb0d4b33ce5da8d182d562d189a712a39/);
  assert.match(record, /`93e80d7fdcbf9206215366d6ecca7ed08d26b2b3`/);
  assert.match(record, /Declared runtime platform \| `linux\/amd64`/);
  assert.match(record, /actions\/runs\/41825487985/);
  assert.match(record, /Passed \[verifier result\]\(https:\/\/github\.com\/microsoft\/waza\/actions\/runs\/41826111839\): 1\/1 test, 0 skipped/);
  assert.match(record, /verifier anonymously pulled this exact digest on Linux AMD64/);
  assert.match(record, /provenance against successful publisher attempt 1 and its finalization/);
  assert.match(record, /review was \*\*waived for\s+this one publication, not performed\*\*/);
  assert.match(record, /not an ARM64 or Windows claim/);
  assert.match(record, /not.*live-provider, billing or inference-quality evidence/);
  assert.match(record, /Dockerfile does not inject `main\.version`; the source default is\s+`dev`/);
});

test('tagged source builds explicitly stamp the release rather than trusting stale metadata', () => {
  const makefile = read('Makefile');
  assert.match(makefile, /^VERSION\?=0\.1\.0$/m);
  assert.match(makefile, /^LDFLAGS=-ldflags "-X main\.version=\$\(VERSION\)"$/m);
  assert.match(makefile, /go build \$\(LDFLAGS\) -o \$\(BUILD_DIR\)\/\$\(BINARY_NAME\) \.\/cmd\/waza/);
  const release = read('.github/workflows/release.yml');
  assert.match(release, /VERSION="\$\{GITHUB_REF_NAME#v\}"/);
  assert.match(release, /-ldflags "-X main\.version=\$\{VERSION\}"/);
  for (const path of ['README.md', 'site/src/content/docs/reference/releases.mdx']) {
    const text = read(path);
    assert.match(text, /git clone --branch v0\.40\.1 https:\/\/github\.com\/microsoft\/waza\.git/, path);
    assert.match(text, /make build VERSION=0\.40\.1/, path);
    assert.match(text, /`version\.txt` and `extension\.yaml`/, path);
    assert.match(text, /defaults to `VERSION=0\.1\.0`/, path);
    assert.match(text, /`e0c251df896bee4e0f9b38a693a6105bf7eadd19`/, path);
  }
});

test('published workflow guides do not require unreleased main for v0.39.0 features', () => {
  const paths = [
    'README.md', 'docs/GETTING-STARTED.md', 'docs/GUIDE.md',
    'docs/TUTORIAL.md', 'docs/CONTROLLED-COMPARISONS.md',
    'site/src/content/docs/releases/v0-40-0.mdx',
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
