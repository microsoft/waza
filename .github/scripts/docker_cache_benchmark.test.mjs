import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import crypto from 'node:crypto';
import { test } from 'node:test';
import { spawn, spawnSync } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { performance } from 'node:perf_hooks';
import { fileURLToPath } from 'node:url';
import { runInNewContext } from 'node:vm';
import { activation, admitLivePR, request, baselineSHA, markerPrefix, admitPath, admitBytes, nativeDigest,
  admitResources, admitNativeHost, compileEvidence, pinEvidence,   pairedSummary, selectedPath, order, Measurement, execute, redact, buildkitConfig, maxSkippedSamplerTicks } from './docker_cache_benchmark.mjs';

const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const gitBlob = bytes => crypto.createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex');
const candidate = 'b'.repeat(40);
const nonce = '12345678-1234-4234-8234-123456789abc';
const marker = `<!-- waza-cache-measurement:v1 candidate=${candidate} baseline=${baselineSHA} request=${nonce} -->`;
const env = () => ({ GITHUB_ACTIONS: 'true', GITHUB_EVENT_NAME: 'pull_request', GITHUB_REPOSITORY: 'microsoft/waza',
  GITHUB_ACTOR: 'spboyer', GITHUB_RUN_ATTEMPT: '1', GITHUB_RUN_ID: '1234', CACHE_START_SECONDS: String(Math.floor(Date.now() / 1000)) });
const event = () => ({ action: 'edited', number: 836, sender: { login: 'spboyer' }, repository: { full_name: 'microsoft/waza' },
  changes: { body: { from: '# Preserved template\n' } },
  pull_request: { number: 836, state: 'open', body: `# Preserved template\n\n${marker}\n`,
    head: { sha: candidate, repo: { full_name: 'microsoft/waza' } },
    base: { ref: 'main', repo: { full_name: 'microsoft/waza' } } } });

test('actual edited-body shape admits only one canonical new parent activation', () => {
  const selected = activation(event(), 'spboyer', '1');
  assert.deepEqual(selected, { candidate, baseline: baselineSHA, nonce, pr: 836 });
  admitLivePR(event().pull_request, selected);
  assert.equal(request(env(), event()).candidate, candidate);
  for (const mutate of [
    e => { e.action = 'synchronize'; },
    e => { e.action = 'opened'; },
    e => { e.changes = { title: { from: 'Old title' } }; },
    e => { e.changes.body.from = null; },
    e => { e.changes.body.from = 42; },
    e => { e.changes.body.from += marker; },
    e => { e.pull_request.body = '# Removed marker'; },
    e => { e.pull_request.body += `\n${marker}`; },
    e => { e.pull_request.body += `\n${markerPrefix}broken -->`; },
    e => { e.pull_request.body = marker.replace('v1 ', 'v2 '); },
    e => { e.pull_request.body = marker.replace(baselineSHA, 'a'.repeat(40)); },
    e => { e.pull_request.body = marker.replace(nonce, 'not-a-nonce'); },
    e => { e.pull_request.head.sha = 'c'.repeat(40); },
    e => { e.pull_request.head.repo.full_name = 'fork/waza'; },
    e => { e.pull_request.base.ref = 'develop'; },
    e => { e.repository.full_name = 'fork/waza'; },
    e => { e.sender.login = 'another-user'; },
    e => { e.number = 837; },
    e => { e.pull_request.state = 'closed'; },
  ]) {
    const fixture = event(); mutate(fixture);
    assert.equal(activation(fixture, 'spboyer', '1'), undefined, JSON.stringify(fixture));
  }
  assert.equal(activation(event(), 'spboyer', '2'), undefined);
  assert.equal(activation(event(), 'another-user', '1'), undefined);
  assert.throws(() => request({ ...env(), GITHUB_EVENT_NAME: 'workflow_dispatch' }, event()));
  assert.throws(() => request({ ...env(), CACHE_START_SECONDS: '0' }, event()));
  for (const mutate of [
    p => { p.head.sha = 'c'.repeat(40); }, p => { p.body = marker.replace('v1', 'v2'); },
    p => { p.auto_merge = {}; }, p => { p.merged_at = 'now'; },
  ]) { const p = event().pull_request; mutate(p); assert.throws(() => admitLivePR(p, selected)); }
});

test('source admission checks real Git blobs, LFS payloads, modes and all selected differences', () => {
  const bytes = Buffer.from('real payload');
  const pointer = `version https://git-lfs.github.com/spec/v1\noid sha256:${hash(bytes)}\nsize ${bytes.length}\n`;
  const e = { path: 'internal/embedded/a.tgz', oid: gitBlob(Buffer.from(pointer)) };
  assert.equal(admitBytes(e, bytes, pointer).lfs, hash(bytes));
  assert.throws(() => admitBytes(e, Buffer.from('pointer instead'), pointer));
  assert.throws(() => admitBytes({ ...e, oid: '0'.repeat(40) }, bytes, pointer));
  assert.throws(() => admitBytes(e, bytes, pointer.replace('size 12', 'size 13')));
  const plain = { path: 'web/package-lock.json', oid: gitBlob(bytes) };
  assert.equal(admitBytes(plain, bytes, '').bytes, bytes.length);
  assert.throws(() => admitBytes(plain, Buffer.from('different'), ''));
  const lf = Buffer.from('unchanged\n');
  assert.throws(() => admitBytes({ ...plain, oid: gitBlob(lf) }, Buffer.from('unchanged\r\n'), ''),
    /Git blob/, 'workflow oracle normalization must not admit changed payload bytes');
  admitPath('.github/workflows/docker-ci.yml', '100644');
  for (const [name, mode] of [['../x', '100644'], ['/x', '100644'], ['web/.env', '100644'],
    ['web/.env.local', '100644'], ['web/.npmrc', '100644'], ['link', '120000'], ['key.pem', '100644']]) assert.throws(() => admitPath(name, mode));
  for (const name of ['Dockerfile', '.github/scripts/new-test.mjs', 'docs/CI-CD-GUIDE.md']) assert.equal(selectedPath(name, [name]), true);
  assert.equal(selectedPath('internal/embedded/payload.tgz', []), true);
  assert.equal(selectedPath('examples/other/.env', []), false);
});

test('native manifests and actual resource/accounting fields fail closed', () => {
  admitNativeHost('linux', 'x64');
  for (const host of [['win32', 'x64'], ['darwin', 'x64'], ['linux', 'arm64']]) {
    assert.throws(() => admitNativeHost(...host), /native Linux AMD64 host required/);
  }
  const digest = `sha256:${'a'.repeat(64)}`;
  const manifest = { manifests: [{ digest, platform: { os: 'linux', architecture: 'amd64' } }] };
  assert.equal(nativeDigest(manifest), digest);
  assert.throws(() => nativeDigest({ manifests: [...manifest.manifests, ...manifest.manifests] }));
  assert.throws(() => nativeDigest({ manifests: [{ digest, platform: { os: 'linux', architecture: 'arm64' } }] }));
  const good = { quota: 200000, period: 100000, memory: 8589934592, swap: 0, owned: 18 * 1024 ** 3, hostFree: 20 * 1024 ** 3 };
  admitResources(good);
  for (const bad of [{ quota: 400000 }, { memory: 4294967296 }, { memory: 17179869184 }, { swap: 1 }, { owned: good.owned + 1 },
    { hostFree: good.hostFree - 1 }, { owned: NaN }, { hostFree: NaN }]) assert.throws(() => admitResources({ ...good, ...bad }));
});

const log = cached => `#1 [web-builder 3/7] RUN npm ci --silent
#1 ${cached ? 'CACHED' : 'DONE 30.5s'}
#2 [web-builder 7/7] RUN npm run build
#2 ${cached ? 'CACHED' : 'DONE 10.0s'}
#3 [builder 6/8] RUN CGO_ENABLED=0 GOOS=linux go build -ldflags '-s -w' -o waza ./cmd/waza
#3 DONE 5.3s
`;
test('warm production compile must execute after Go mutation while both web commands reuse', () => {
  assert.equal(compileEvidence(log(true), true).compileSeconds, 5.3);
  assert.equal(compileEvidence(log(false), false).web[0].cached, false);
  for (const bad of [log(true).replace('#3 DONE 5.3s', '#3 CACHED'), log(false),
    log(true).replace('CGO_ENABLED=0', 'CGO_ENABLED=1'), log(true).replace('#3 DONE 5.3s', '#3 ERROR')]) assert.throws(() => compileEvidence(bad, true));
});

test('pin proof identifies actual frontend/base resolution, not an unrelated digest string', () => {
  const pins = Object.fromEntries(['docker/dockerfile:1', 'node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19']
    .map((name, i) => [name, `${name.split(':')[0]}@sha256:${String(i + 1).repeat(64)}`]));
  const proof = `#1 resolve image config for docker-image://docker.io/${pins['docker/dockerfile:1']}\n` +
    ['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19'].map((name, i) => `#${i + 2} [context ${name}] load metadata for ${pins[name]}\n#${i + 2} resolve docker.io/library/${pins[name]} 0.0s done\n`).join('');
  assert.deepEqual(pinEvidence(proof, pins).frontend, pins['docker/dockerfile:1']);
  for (const bad of [proof.replace('resolve image config for docker-image://', 'echo '),
    proof.replace(pins['docker/dockerfile:1'], 'docker/dockerfile:1'), proof.replace('[context node:22-alpine]', '[other]'),
    proof.replace('resolve docker.io/library/node@', 'echo docker.io/library/node@')]) assert.throws(() => pinEvidence(bad, pins));
});

function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'waza835-offline-'));
  t.after(() => fs.rmSync(root, { recursive: true }));
  return root;
}
test('production snapshot preserves all selected deltas and admits32 real payloads before copying', async t => {
  const root = temporary(t), repo = path.join(root, 'repository'), out = path.join(root, 'evidence');
  fs.mkdirSync(repo); fs.mkdirSync(out);
  const records = new Map();
  const rows = [];
  for (let i = 0; i < 32; i++) {
    const name = `internal/embedded/payload-${i}.tgz`, bytes = Buffer.from(`payload-${i}`);
    const pointer = Buffer.from(`version https://git-lfs.github.com/spec/v1\noid sha256:${hash(bytes)}\nsize ${bytes.length}\n`);
    const oid = gitBlob(pointer); records.set(oid, pointer); rows.push(`100644 blob ${oid}\t${name}`);
    fs.mkdirSync(path.dirname(path.join(repo, name)), { recursive: true }); fs.writeFileSync(path.join(repo, name), bytes);
  }
  const changed = '.github/scripts/docker_cache_benchmark.mjs', code = Buffer.from('export const candidate = true;\n'), oid = gitBlob(code);
  records.set(oid, code);
  const site = 'site/tests/documentation.test.mjs', oldSite = Buffer.from('baseline source guard\n'), newSite = Buffer.from('corrected source guard\n');
  records.set(gitBlob(oldSite), oldSite); records.set(gitBlob(newSite), newSite);
  const m = new Measurement(request(env(), event()), out, repo, fixtureClient((tool, args) => {
    assert.equal(tool, 'git');
    let stdout;
    if (args[0] === 'ls-tree') stdout = Buffer.from([...rows,
      `100644 blob ${gitBlob(args[3] === candidate ? newSite : oldSite)}\t${site}`,
      ...(args[3] === candidate ? [`100755 blob ${oid}\t${changed}`] : [])].join('\0') + '\0');
    else if (args[0] === 'rev-parse') stdout = Buffer.from('a'.repeat(40) + '\n');
    else { assert.equal(args[0], 'cat-file'); stdout = records.get(args[2]); assert.ok(stdout); }
    return { status: 0, stdout };
  }));
  m.hostStorage = () => {};
  m.state.differencePaths = [changed, site];
  const A = await m.snapshot(baselineSHA, 'A'), B = await m.snapshot(candidate, 'B');
  assert.equal(A.entries.length, 33);
  assert.equal(B.entries.length, 34);
  assert.equal(B.entries.filter(e => e.lfs).length, 32);
  assert.equal(fs.existsSync(path.join(A.dir, changed)), false);
  assert.equal(hash(fs.readFileSync(path.join(B.dir, changed))), hash(code));
  assert.equal(hash(fs.readFileSync(path.join(A.dir, site))), hash(oldSite));
  assert.equal(hash(fs.readFileSync(path.join(B.dir, site))), hash(newSite));
  assert.equal(B.entries.find(e => e.path === changed).mode, '100755', 'canonical executable Git mode preserved on every host');
  assert.equal(fs.statSync(path.join(B.dir, changed)).mode & 0o777, process.platform === 'win32' ? 0o666 : 0o755);
  await m.verifyContext(B);
  fs.writeFileSync(path.join(B.dir, '.npmrc'), 'unadmitted');
  await assert.rejects(() => m.verifyContext(B), /unadmitted input appeared/);
  fs.unlinkSync(path.join(B.dir, '.npmrc'));
  fs.writeFileSync(path.join(B.dir, changed), 'changed after admission');
  await assert.rejects(() => m.verifyContext(B), /admitted input changed/);
  fs.writeFileSync(path.join(repo, 'internal/embedded/payload-0.tgz'), 'not the payload');
  await assert.rejects(() => m.snapshot(candidate, 'bad'), /LFS payload mismatch/);
  const preexisting = path.join(out, 'context-user');
  fs.mkdirSync(preexisting); fs.writeFileSync(path.join(preexisting, 'preserved'), 'user');
  await assert.rejects(() => m.snapshot(candidate, 'user'));
  assert.equal(fs.readFileSync(path.join(preexisting, 'preserved'), 'utf8'), 'user');
});
class OfflineMeasurement extends Measurement {
  constructor(root, fail = 0) {
    super({ ...request(env(), event()) }, root, root, () => { throw new Error('external command forbidden'); });
    this.fail = fail;
    for (const variant of ['A', 'B']) {
      const dir = path.join(root, variant); fs.mkdirSync(path.join(dir, 'cmd/waza'), { recursive: true });
      const bytes = Buffer.from('package main\n');
      fs.writeFileSync(path.join(dir, 'cmd/waza/main.go'), bytes);
      const stat = fs.statSync(dir);
      this.state.contexts.push({ variant, dir, dev: stat.dev, ino: stat.ino, entries: [
        { path: 'cmd/waza/main.go', mode: '100644', oid: gitBlob(bytes), sha256: hash(bytes), bytes: bytes.length },
      ] });
    }
    this.calls = [];
  }
  async authorization() { this.calls.push('authorization'); }
  admit() { this.calls.push('admission'); }
  hostStorage() {}
  async builder(index) { this.calls.push(`fresh-${index}`); return { name: `owned-${index}` }; }
  async build(builder, variant, round, phase) {
    const p = path.join(this.state.contexts.find(c => c.variant === variant && c.round === round).dir, 'cmd/waza/measurement_cache_input.go');
    if (phase === 'cold') assert.equal(fs.existsSync(p), false);
    else assert.equal(fs.readFileSync(p, 'utf8'), `package main\n\nconst measurementCacheInput = "835-round-${round}"\n`);
    this.calls.push(`${round}-${variant}-${phase}-${builder.name}`);
    const code = this.state.attempts.length + 1 === this.fail ? 1 : 0;
    this.state.attempts.push({ variant, round, phase, code, wallSeconds: 5, evidence: compileEvidence(log(phase === 'warm'), phase === 'warm'),
      runtimePassed: code === 0, assets: [{ path: 'index.html', sha256: 'abc' }] });
    if (code) throw new Error('recorded failing build');
  }
  removeBuilder(builder) { this.calls.push(`remove-${builder.name}`); }
  cleanup() { this.calls.push('cleanup'); }
}
function fixtureContainerCopy(value, containers) {
  const separator = value.indexOf(':');
  if (separator < 1) return undefined;
  const id = value.slice(0, separator);
  const container = containers.get(id);
  return container ? { container, path: value.slice(separator + 1) } : undefined;
}
function daemonFixture(t, change = () => {}) {
  const m = new OfflineMeasurement(temporary(t));
  m.builder = Measurement.prototype.builder;
  m.removeBuilder = Measurement.prototype.removeBuilder;
  m.cleanup = Measurement.prototype.cleanup;
  const containers = new Map(), volumes = new Map(), privateMetadata = new Map(), sharedMetadata = new Map(), calls = [];
  const registries = new Map([[undefined, sharedMetadata], [m.buildxRoot, privateMetadata]]);
  const model = { containers, volumes, metadata: privateMetadata, sharedMetadata, registries, calls, environments: [],
    creates: 0, pulls: 0, workers: 0, runtimeCreates: 0, loaded: new Map() };
  m.state.host = { dockerRoot: '/var/lib/docker' }; m.state.initialImages = [];
  m.state.pins['moby/buildkit:buildx-stable-1'] = `moby/buildkit@sha256:${'1'.repeat(64)}`;
  for (const [i, name] of ['docker/dockerfile:1', 'node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19'].entries()) {
    m.state.pins[name] = `${name.split(':')[0]}@sha256:${String(i + 3).repeat(64)}`;
  }
  fs.writeFileSync(path.join(m.root, 'buildkit.toml'), buildkitConfig);
  m.api = async () => event().pull_request;
  const run = (tool, args, options) => {
    calls.push([tool, ...args]);
    const metadata = tool === 'docker' && args[0] === 'buildx' ? registries.get(options?.env?.BUILDX_CONFIG) : privateMetadata;
    if (tool === 'docker' && args[0] === 'buildx') {
      assert.equal(options.env.BUILDX_CONFIG, path.resolve(m.buildxRoot), 'every actual Buildx client uses the exclusively acquired private root');
      assert.equal(options.env.DOCKER_CONFIG, process.env.DOCKER_CONFIG, 'Docker config/auth/context is not copied or replaced');
      model.environments.push({ command: [tool, ...args], root: options.env.BUILDX_CONFIG });
    }
    const result = { status: 0, stdout: '' };
    if (tool === 'df') result.stdout = 'Filesystem 1024-blocks Used Available Capacity Mounted\nhost 999999999 1 999999998 1% /\ndocker 999999999 1 999999998 1% /docker\n';
    else if (tool === 'du') result.stdout = '1\towned\n';
    else if (tool === 'uptime') result.stdout = 'authored load\n';
    else if (tool === 'go') result.stdout = '\tCGO_ENABLED=0\n\tGOOS=linux\n\tGOARCH=amd64\n';
    else {
      assert.equal(tool, 'docker', 'fixture never launches external Docker/Go/providers');
      if (args[0] === 'pull') {
        model.pulls++;
        const ref = args.at(-1);
        model.loaded.set(ref, { Id: `sha256:${'2'.repeat(64)}`, Size: 1024, Os: 'linux', Architecture: 'amd64',
          RepoDigests: [ref] });
        result.stdout = `${ref}: Pull complete\n`;
      } else if (args[0] === 'create') {
        if (args[args.indexOf('--entrypoint') + 1] === '/bin/sh') {
          const id = (++model.runtimeCreates + 200).toString(16).padStart(64, '0');
          containers.set(id, { Id: id, Image: args[args.indexOf('--entrypoint') + 2], Name: `/${args[args.indexOf('--name') + 1]}`,
            Config: { Labels: { 'com.waza.cache.owner': m.config.owner } }, runtime: true,
            HostConfig: { CpuQuota: 200000, CpuPeriod: 100000, Memory: 536870912, MemorySwap: 536870912, NetworkMode: 'none', ReadonlyRootfs: true } });
          result.stdout = `${id}\n`; change({ m, model, tool, args, result }); return result;
        }
        const id = (++model.creates).toString(16).padStart(64, '0'), volume = (model.creates + 100).toString(16).padStart(64, '0');
        const name = args[args.indexOf('--name') + 1];
        const x = daemonInspect(m, id, volume);
        x.Name = `/${name}`; x.State.Running = false; x.configBytes = undefined;
        containers.set(id, x);
        volumes.set(volume, { Name: volume, Driver: 'local', Scope: 'local', Options: {}, Labels: {}, CreatedAt: `authored-${model.creates}` });
        result.stdout = `${id}\n`;
      } else if (args[0] === 'inspect') {
        const x = containers.get(args.at(-1)) ?? [...containers.values()].find(c => (c.legacyManaged || c.runtime) && c.Name.slice(1) === args.at(-1));
        assert.ok(x, 'candidate inspect uses returned ID, never name adoption');
        result.stdout = args.includes('--format') ? 'null' : JSON.stringify([x]);
      } else if (args[0] === 'cp') {
        const source = fixtureContainerCopy(args[1], containers);
        const destination = fixtureContainerCopy(args[2], containers);
        if (source) {
          if (source.path === '/usr/local/bin/waza') fs.writeFileSync(args[2], Buffer.from('\x7fELFauthored binary'));
          else if (source.path.endsWith('/web/dist')) {
            fs.mkdirSync(path.join(args[2], 'assets'), { recursive: true });
            fs.writeFileSync(path.join(args[2], 'index.html'), '<script src="assets/app.js"></script>');
            fs.writeFileSync(path.join(args[2], 'assets/app.js'), 'authored asset');
          } else fs.writeFileSync(args[2], source.container.configBytes);
        } else {
          assert.ok(destination, 'Docker copy destination uses a known fixture container');
          destination.container.configBytes = fs.readFileSync(args[1], 'utf8');
        }
      } else if (args[0] === 'start') {
        const x = containers.get(args.at(-1));
        if (x.runtime) result.stdout = 'waza version dev\nRuntime static/version/cache/source checks passed\n';
        else x.State.Running = true;
      } else if (args[0] === 'pause' || args[0] === 'unpause') containers.get(args[1]).State.Paused = args[0] === 'pause';
      else if (args[0] === 'ps') result.stdout = [...containers.values()].map(x => args.includes('{{.ID}}') ? x.Id : x.Name.slice(1)).join('\n');
      else if (args[0] === 'volume') result.stdout = args[1] === 'inspect' ? JSON.stringify([volumes.get(args[2])]) : [...volumes.keys()].join('\n');
      else if (args[0] === 'image') {
        if (args[1] === 'ls') result.stdout = [...model.loaded.keys()].join('\n');
        else if (args[1] === 'rm') model.loaded.delete(args[2]);
        else result.stdout = JSON.stringify([model.loaded.get(args[2]) ?? { Id: `sha256:${'2'.repeat(64)}`, Size: 1024 }]);
      }
      else if (args[0] === 'exec') {
        const x = containers.get(args[1]); assert.ok(x);
        if (args[2] === 'cat') result.stdout = args[3] === '/buildkitd.toml' ? x.configBytes : '<script src="assets/app.js"></script>';
        else if (args[2] === 'buildctl') {
          model.workers++;
          // buildctl v0.34.2 templates serialize client.WorkerInfo's JSON tags.
          result.stdout = JSON.stringify([{ id: 'authored-worker', platforms: [{ os: 'linux', architecture: 'amd64' }] }]);
        } else if (args[2] === 'find') {
          const root = '/var/lib/buildkit/runc-overlayfs/snapshots/snapshots/1/fs/build/web/dist';
          result.stdout = args.includes('-path') ? `${root}/index.html\n` : `${root}/index.html\n${root}/assets/app.js\n`;
        } else if (args[2] === 'sha256sum') result.stdout = hash(args[3].endsWith('index.html') ? '<script src="assets/app.js"></script>' : 'authored asset') + '\n';
        else if (args[2] === 'buildkitd') result.stdout = 'buildkitd github.com/moby/buildkit v0.34.2 authored-version\n';
        else result.stdout = '200000 100000\n8589934592\n0\n1024\n2048\n1\t/var/lib/buildkit\n';
      } else if (args[0] === 'buildx') {
        if (args[1] === 'ls') result.stdout = [...metadata.keys()].join('\n');
        else if (args[1] === 'create') {
          const name = args[args.indexOf('--name') + 1];
          if (args[args.indexOf('--driver') + 1] === 'docker-container') {
            const id = (++model.creates).toString(16).padStart(64, '0'), volume = `buildx_buildkit_${name}0_state`;
            const x = daemonInspect(m, id, volume); x.Name = `/buildx_buildkit_${name}0`; x.legacyManaged = true;
            x.Args.push('--allow-insecure-entitlement=network.host');
            containers.set(id, x); volumes.set(volume, { Name: volume, Driver: 'local', Options: {} });
            metadata.set(name, `docker-container://${id}`);
          } else {
            if (metadata.has(name) && !args.includes('--node') && !args.includes('--append')) {
              result.status = 1; result.stderr = 'existing instance but no append mode';
              change({ m, model, tool, args, result, options }); return result;
            }
            metadata.set(name, args.at(-1));
          }
          result.stdout = `${name}\n`;
        }
        else if (args[1] === 'inspect') result.stdout = `Name:       ${args[2]}\nDriver:     remote\n\nNodes:\nName:       ${args[2]}0\nEndpoint:   ${metadata.get(args[2])}\nStatus:     running\n`;
        else if (args[1] === 'rm') metadata.delete(args[2]);
        else if (args[1] === 'build') {
          const tag = args[args.indexOf('--tag') + 1];
          model.loaded.set(tag, { Id: `sha256:${'e'.repeat(64)}`, Size: 128, Os: 'linux', Architecture: 'amd64',
            Config: { Labels: { 'com.waza.cache.owner': m.config.owner } } });
          result.stdout = log(m.state.attempts.at(-1).phase === 'warm') +
            `#4 resolve image config for docker-image://docker.io/${m.state.pins['docker/dockerfile:1']}\n` +
            ['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19'].map((name, i) =>
              `#${i + 5} [context ${name}] load metadata for ${m.state.pins[name]}\n#${i + 5} resolve docker.io/library/${m.state.pins[name]} 0.0s done\n`).join('');
        } else assert.equal(args[1], 'du');
      } else if (args[0] === 'rm') {
        const x = containers.get(args.at(-1));
        if (args.includes('-v')) volumes.delete(x.Mounts[0].Name);
        containers.delete(x.Id);
      } else assert.fail(`unsupported authored command ${args.join(' ')}`);
    }
    change({ m, model, tool, args, result, options });
    return result;
  };
  m.spawn = fixtureClient(run);
  return { m, model, run };
}
test('Docker copy fixture distinguishes Windows drive paths from known container references', () => {
  const id = '1'.repeat(64);
  const container = { configBytes: 'authored config' };
  const containers = new Map([[id, container]]);
  assert.equal(fixtureContainerCopy('C:\\waza\\buildkit.toml', containers), undefined);
  assert.deepEqual(fixtureContainerCopy(`${id}:/buildkitd.toml`, containers), {
    container,
    path: '/buildkitd.toml',
  });
});
function daemonInspect(m, id, volume) {
  return { Id: id, Image: `sha256:${'2'.repeat(64)}`, Path: 'buildkitd', Args: ['--debug', '--config', '/buildkitd.toml'],
    State: { Running: true, Paused: false, Restarting: false, Dead: false },
    Mounts: [{ Type: 'volume', Driver: 'local', RW: true, Destination: '/var/lib/buildkit', Name: volume }],
    Config: { Image: m.state.pins['moby/buildkit:buildx-stable-1'], Env: [`WAZA835_OWNER=${m.config.owner}`],
      Labels: { 'com.waza.cache.owner': m.config.owner }, Entrypoint: ['buildkitd'], Cmd: ['--debug', '--config', '/buildkitd.toml'] },
    HostConfig: { Privileged: true, Init: true, CapAdd: null, CapDrop: null, SecurityOpt: null, Binds: null, Devices: [], DeviceRequests: null,
      VolumesFrom: null, UsernsMode: '', PidMode: '', IpcMode: 'private', ReadonlyRootfs: false, Tmpfs: null,
      RestartPolicy: { Name: 'no', MaximumRetryCount: 0 }, NetworkMode: 'bridge',
      Mounts: [{ Type: 'volume', Target: '/var/lib/buildkit', VolumeOptions: { NoCopy: true } }],
      CpuQuota: 200000, CpuPeriod: 100000, Memory: 8589934592, MemorySwap: 8589934592 },
    NetworkSettings: { Networks: { bridge: {} } } };
}
test('anonymous cache mount admits empty Docker DriverConfig and rejects custom drivers', t => {
  const { m } = daemonFixture(t);
  const volume = 'a'.repeat(64);
  const inspect = driver => {
    const x = daemonInspect(m, '1'.repeat(64), volume);
    if (driver !== undefined) x.HostConfig.Mounts[0].VolumeOptions.DriverConfig = driver;
    return x;
  };
  for (const accepted of [undefined, {}, { Name: '', Options: {} }]) assert.equal(m.cacheMount(inspect(accepted)).Name, volume);
  for (const rejected of [{ Name: 'nfs' }, { Options: { type: 'tmpfs' } }, { Extra: true }, 'local'])
    assert.throws(() => m.cacheMount(inspect(rejected)), /container-coupled anonymous cache mount required/);
});
test('daemon policy admits only the Docker privileged label default in SecurityOpt', t => {
  const { m } = daemonFixture(t);
  const volume = 'b'.repeat(64);
  const builder = { ownershipAcquired: true, volume };
  for (const accepted of [null, [], ['label=disable']]) {
    const x = daemonInspect(m, '1'.repeat(64), volume);
    x.HostConfig.SecurityOpt = accepted;
    m.builderPolicy(builder, x);
  }
  for (const rejected of [['seccomp=unconfined'], ['label=disable', 'apparmor=unconfined'], ['label=type:spc_t']]) {
    const x = daemonInspect(m, '1'.repeat(64), volume);
    x.HostConfig.SecurityOpt = rejected;
    assert.throws(() => m.builderPolicy(builder, x), /daemon privilege\/config\/mount\/network policy/);
  }
});
test('production execute reaches real deny-configured builder and six distinct container-coupled cache states', async t => {
  const { m, model } = daemonFixture(t);
  const captureModes = [], sampledCapture = m.sampledCapture.bind(m);
  m.sampledCapture = (builder, paused, operation) => {
    captureModes.push(paused);
    return sampledCapture(builder, paused, operation);
  };
  m.build = Measurement.prototype.build;
  assert.equal(await execute(m), 0, m.state.failures.join('\n'));
  assert.equal(m.state.attempts.length, 12);
  assert.deepEqual(m.state.attempts.filter(a => a.phase === 'cold').map(a => a.variant), order.flat());
  assert.equal(new Set(m.state.builders.map(b => b.volume)).size, 6);
  assert.ok(m.state.builders.every(b => b.removed && b.workers.length && b.configSHA256 === hash(buildkitConfig)));
  assert.equal(model.creates, 6);
  assert.equal(model.pulls, 6);
  assert.equal(model.containers.size + model.volumes.size + model.metadata.size, 0);
  assert.deepEqual(model.calls.filter(c => c[1] === 'pull' ||
    (c[1] === 'create' && c[c.indexOf('--entrypoint') + 1] === 'buildkitd')).map(c => c[1]),
  Array.from({ length: 6 }, () => ['pull', 'create']).flat());
  for (const command of model.calls.filter(c => c[1] === 'create' && c[c.indexOf('--entrypoint') + 1] === 'buildkitd')) {
    assert.ok(command.includes('--pull=never') && command.includes('--privileged') && command.includes('--init'));
    assert.ok(command.includes('type=volume,target=/var/lib/buildkit,volume-nocopy'));
    assert.equal(command.some(arg => /bind|security-opt|cap-add|userns|device/.test(arg)), false);
    const pull = model.calls.findIndex(c => c[1] === 'pull' && c.at(-1) === command.at(-4));
    assert.ok(pull >= 0 && pull < model.calls.indexOf(command), 'verified pinned pull precedes no-pull daemon creation');
  }
  assert.ok(model.calls.filter(c => c[1] === 'buildx' && c[2] === 'create').every(c => c.includes('remote')));
  assert.ok(model.environments.length > 12);
  assert.ok(['ls', 'create', 'inspect', 'du', 'build', 'rm'].every(operation =>
    model.environments.some(e => e.command[2] === operation)), 'command, logged, build and cleanup clients use the real private-root launch path');
  assert.equal(model.environments.every(e => e.root === path.resolve(m.buildxRoot)), true);
  assert.ok(m.state.retainedDirectories.some(e => e.dir === m.buildxRoot && e.rootIdentityMatches));
  assert.deepEqual(captureModes, Array.from({ length: 12 }, () => [true, false]).flat(),
    'every binary capture is sampled while paused and every asset capture while active');
});
test('approved current-main activation reaches production source admission without widening its allowlist',
  { skip: process.platform !== 'linux' || process.arch !== 'x64' ? 'Production allocation admission requires native Linux AMD64; portable activation controls remain selected' : false }, async t => {
    const previousHost = process.env.DOCKER_HOST;
    delete process.env.DOCKER_HOST;
    t.after(() => {
      if (previousHost === undefined) delete process.env.DOCKER_HOST;
      else process.env.DOCKER_HOST = previousHost;
    });
    for (const extra of [undefined, 'cmd/waza/unapproved.go']) await t.test(extra ?? 'integrated candidate', async () => {
      const incoming = event();
      incoming.pull_request.body = incoming.pull_request.body.replace(baselineSHA, 'eda88ccd7d2a59fefafa7662a95828c9977dbc16');
      const root = temporary(t);
      for (const name of ['Dockerfile', '.dockerignore', '.github/workflows/docker-ci.yml', '.github/scripts/docker_cache_benchmark.mjs']) {
        fs.mkdirSync(path.dirname(path.join(root, name)), { recursive: true });
        fs.writeFileSync(path.join(root, name), 'authored recipe\n');
      }
      const m = new Measurement(request(env(), incoming), root, root, () => { throw new Error('external process forbidden'); });
      const differences = ['Dockerfile', '.github/scripts/docker_cache_test.go', '.github/workflows/docker-ci.yml',
        '.github/scripts/docker_cache_benchmark.mjs', '.github/scripts/docker_cache_benchmark.test.mjs',
        '.github/scripts/docker_ci_test.go', 'docs/CI-CD-GUIDE.md', 'site/tests/documentation.test.mjs',
        ...(extra ? [extra] : [])];
      m.command = async (tool, args) => {
        if (tool === 'git') {
          if (args[0] === 'rev-parse') return candidate;
          if (args[0] === 'status' || args[0] === 'merge-base') return '';
          assert.equal(args[0], 'diff');
          assert.deepEqual(args.slice(-2), ['eda88ccd7d2a59fefafa7662a95828c9977dbc16', candidate]);
          return differences.map(name => args.includes('--name-status') ? `M\t${name}` : name).join('\n');
        }
        if (tool === 'uptime') return 'authored shared load';
        assert.equal(tool, 'docker');
        if (args[0] === 'info') return JSON.stringify({ OSType: 'linux', Architecture: 'amd64',
          DockerRootDir: '/var/lib/docker', MemTotal: 8 * 1024 ** 3, NCPU: 2 });
        if (args[0] === 'context') return JSON.stringify([{ Endpoints: { docker: { Host: 'unix:///var/run/docker.sock' } } }]);
        if (args[0] === 'image') return '';
        assert.deepEqual(args, ['buildx', 'version']);
        return 'authored Buildx version';
      };
      let admitted = false;
      m.hostStorage = async () => { admitted = true; throw new Error('stop after source admission; no allocation'); };
      await assert.rejects(() => m.admit(), extra ? /unapproved source difference/ : /stop after source admission/);
      assert.equal(admitted, extra === undefined);
      assert.deepEqual(m.state.differencePaths, differences);
      assert.equal(m.state.builders.length, 0);
    });
    const historical = event();
    historical.pull_request.body = historical.pull_request.body.replace(baselineSHA, 'a69f78a35dcd9fe4eff366b3d351786ca3bc2511');
    assert.throws(() => request(env(), historical), /invalid rising-edge activation/);
  });
test('production execute refreshes post-build cache usage before paused capture', async t => {
  for (const cacheGiB of [16, 19]) await t.test(`${cacheGiB}GiB`, async () => {
    const built = new Set(), pausedBytes = [];
    let refreshed = false;
    const { m, model } = daemonFixture(t, ({ m, args, result }) => {
      if (args[0] === 'buildx' && args[1] === 'build') built.add(m.state.builders.at(-1).id);
      if (args[0] === 'exec' && args[2] === 'sh' && args[4]?.includes('du -sk') && built.has(args[1])) {
        const usage = result.stdout.replace(/\n1\t\/var\/lib\/buildkit\n$/,
          `\n${cacheGiB * 1024 ** 2}\t/var/lib/buildkit\n`);
        assert.notEqual(usage, result.stdout, 'fixture must change the actual post-build cache sample');
        result.stdout = usage;
        refreshed = true;
      }
      if (args[0] === 'pause') pausedBytes.push(m.state.builders.at(-1).cacheBytes);
    });
    m.build = Measurement.prototype.build;
    assert.equal(await execute(m), cacheGiB === 16 ? 0 : 1, m.state.failures.join('\n'));
    assert.equal(refreshed, true);
    if (cacheGiB === 16) {
      assert.deepEqual(pausedBytes, Array(12).fill(16 * 1024 ** 3));
      assert.ok(m.state.attempts.every(attempt => attempt.runtimePassed && attempt.assets.length));
    } else {
      assert.deepEqual(pausedBytes, [], 'over-limit cache growth must stop before any paused capture');
      assert.equal(m.state.attempts.length, 1);
      assert.equal(m.state.attempts[0].runtimePassed, undefined);
      assert.equal(m.state.attempts[0].assets, undefined);
      assert.match(m.state.failures.join('\n'), /resource\/storage admission failed/);
    }
    assert.ok(m.state.builders.every(builder => builder.removed));
    assert.equal(model.containers.size + model.volumes.size + model.metadata.size, 0);
  });
});
test('production paused capture charges the tool image consistently with active storage admission', async t => {
  for (const preexisting of [true, false]) await t.test(`preexisting=${preexisting}`, async t => {
    let built = false;
    const { m, model, run } = daemonFixture(t, ({ args, result }) => {
      if (args[0] === 'buildx' && args[1] === 'build') built = true;
      if (built && args[0] === 'exec' && args[2] === 'sh' && args[4]?.includes('du -sk')) {
        const usage = result.stdout.replace(/\n1\t\/var\/lib\/buildkit\n$/,
          `\n${18 * 1024 ** 2 - (preexisting ? 2 : 3)}\t/var/lib/buildkit\n`);
        assert.notEqual(usage, result.stdout);
        result.stdout = usage;
      }
    });
    m.state.initialImages = preexisting ? [`sha256:${'2'.repeat(64)}`] : [];
    m.build = Measurement.prototype.build;
    m.measure = async () => {
      await m.workingContext('A', 1);
      const builder = await m.builder(1);
      await m.build(builder, 'A', 1, 'cold');
      await m.removeBuilder(builder);
    };
    t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
    let ready;
    const runtimeReady = new Promise(resolve => { ready = resolve; });
    const spawn = m.spawn;
    m.spawn = (tool, args, options) => {
      if (tool === 'docker' && args[0] === 'start' && model.containers.get(args.at(-1))?.runtime) {
        const child = fakeChild(), result = run(tool, args, options);
        ready({ child, result });
        return child;
      }
      return spawn(tool, args, options);
    };
    const pending = execute(m);
    const { child, result } = await Promise.race([runtimeReady, pending.then(() => {
      assert.fail(`measurement ended before runtime capture: ${m.state.failures.join('\n')}`);
    })]);
    t.mock.timers.tick(5000);
    await flush();
    child.stdout.emit('data', Buffer.from(result.stdout));
    child.emit('close', result.status, null);
    assert.equal(await pending, 1, 'one controlled attempt is not a complete three-pair measurement');
    assert.deepEqual(m.state.failures, []);
    assert.equal(m.state.initialImages.includes(m.state.builders[0].imageId), preexisting);
    assert.equal(m.state.attempts.length, 1);
    assert.ok(m.state.attempts[0].runtimePassed && m.state.attempts[0].assets.length);
    const records = fs.readFileSync(path.join(m.root, 'resources.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
    const paused = records.filter(record => record.scope?.startsWith('paused owned daemon'));
    assert.equal(paused.length, 1);
    assert.equal(paused[0].owned, 18 * 1024 ** 3 - 896);
    assert.equal(paused[0].owned, records.filter(record => record.builder).at(-1).owned);
    assert.equal(model.containers.size + model.volumes.size + model.metadata.size, 0);
  });
});
test('terminal cancellation after twelve complete attempts survives cleanup and final serialization', async t => {
  for (const timing of ['before cleanup', 'after cleanup']) await t.test(timing, async () => {
    const { m, model } = daemonFixture(t);
    m.build = Measurement.prototype.build;
    const cleanup = m.cleanup.bind(m);
    const cancel = () => { m.stopped = 'cancelled'; m.stopClients('cancelled'); };
    m.cleanup = async () => {
      assert.equal(m.state.attempts.length, 12);
      assert.ok(m.state.attempts.every(attempt => attempt.runtimePassed));
      assert.equal(m.state.failures.length, 0);
      if (timing === 'before cleanup') cancel();
      await cleanup();
      if (timing === 'after cleanup') cancel();
    };
    assert.equal(await execute(m), 1);
    const saved = JSON.parse(fs.readFileSync(path.join(m.root, 'state.json'), 'utf8'));
    assert.equal(saved.summary.complete, true, 'retain observed complete attempts without calling cancellation successful');
    assert.match(saved.failures.join('\n'), /cancelled/);
    assert.ok(saved.finishedAt && saved.cleanupAt);
    assert.equal(model.containers.size + model.volumes.size + model.metadata.size, 0);
  });
});
test('empty local image store pulls and verifies the pinned Linux AMD64 BuildKit image before creation', async t => {
  for (const kind of ['valid', 'pull-failure', 'wrong-platform', 'wrong-digest']) await t.test(kind, async () => {
    const { m, model } = daemonFixture(t, ({ args, result }) => {
      if (kind === 'pull-failure' && args[0] === 'pull') {
        result.status = 1; result.stderr = 'registry unavailable';
      }
      if (args[0] === 'image' && args[1] === 'inspect') {
        const images = JSON.parse(result.stdout);
        if (kind === 'wrong-platform') images[0].Architecture = 'arm64';
        if (kind === 'wrong-digest') images[0].RepoDigests = [`moby/buildkit@sha256:${'9'.repeat(64)}`];
        result.stdout = JSON.stringify(images);
      }
    });
    assert.equal(model.loaded.size, 0);
    m.measure = async () => { await m.builder(1); };
    assert.equal(await execute(m), 1, 'one admitted builder without timed pairs remains an incomplete measurement');
    assert.equal(model.pulls, 1);
    assert.deepEqual(model.calls.find(c => c[1] === 'pull'), [
      'docker', 'pull', '--platform', 'linux/amd64', m.state.pins['moby/buildkit:buildx-stable-1'],
    ]);
    assert.equal(model.creates, kind === 'valid' ? 1 : 0);
    if (kind === 'valid') {
      const builder = m.state.builders[0];
      assert.equal(builder.pulledImage.ref, m.state.pins['moby/buildkit:buildx-stable-1']);
      assert.equal(builder.pulledImage.platform, 'linux/amd64');
      assert.equal(builder.pulledImage.log, 'builder-1-image-pull.log');
      assert.ok(builder.pulledImage.timeoutSeconds > 0 && builder.pulledImage.timeoutSeconds <= 360);
      assert.equal(builder.removed, true);
    } else {
      assert.match(m.state.failures.join('\n'), kind === 'pull-failure' ?
        /pinned BuildKit image pull failed: exit 1; see builder-1-image-pull\.log/ :
        /pinned Linux\/AMD64 BuildKit image verification failed/);
      assert.equal(m.state.builders[0].creationAttempted, false);
    }
  });
});
test('public Docker receipts remain parseable and redact inspected URL queries without changing admission values', async t => {
  const secret = 'PRIVATE-SENTINEL';
  const url = `https://registry.example/token?credential="${secret}"`;
  const { m } = daemonFixture(t, ({ args, result }) => {
    if (args[0] === 'inspect' && !args.includes('--format')) {
      const containers = JSON.parse(result.stdout);
      containers[0].Config.Env.push(`REGISTRY_URL=${url}`);
      result.stdout = JSON.stringify(containers);
    }
  });
  m.measure = async () => { await m.builder(1); };
  assert.equal(await execute(m), 1);
  assert.ok(m.state.builders[0].inspections.some(entry => entry.inspect.Config.Env.includes(`REGISTRY_URL=${url}`)),
    'admission and cleanup retain the original inspected value in memory');
  for (const name of ['state.json', 'builder-1-inspections.json', 'builder-1-admission.json']) {
    const receipt = fs.readFileSync(path.join(m.root, name), 'utf8');
    assert.doesNotMatch(receipt, new RegExp(secret));
    assert.match(receipt, /https:\/\/registry\.example\/token\?\[query-redacted\]/);
    assert.doesNotThrow(() => JSON.parse(receipt), `${name} remains parseable JSON`);
  }
});
test('private metadata preserves shared races and rejects existing private nodes without upsert or adoption', async t => {
  for (const kind of ['shared-after-check', 'private-after-check', 'private-before-check']) await t.test(kind, async () => {
    const competitor = `docker-container://${'9'.repeat(64)}`;
    const { m, model } = daemonFixture(t, ({ m, model, args }) => {
      if (args[0] !== 'buildx' || args[1] !== 'ls') return;
      if (kind === 'shared-after-check') model.sharedMetadata.set(`${m.config.owner}-1`, competitor);
      if (kind === 'private-after-check') model.metadata.set(`${m.config.owner}-1`, competitor);
    });
    const name = `${m.config.owner}-1`;
    if (kind === 'private-before-check') model.metadata.set(name, competitor);
    m.measure = async () => { await m.builder(1); };
    assert.equal(await execute(m), 1);
    const target = kind === 'shared-after-check' ? model.sharedMetadata : model.metadata;
    assert.equal(target.get(name), competitor, 'competing binding survives registration and teardown');
    const creates = model.calls.filter(c => c[1] === 'buildx' && c[2] === 'create');
    assert.equal(creates.length, kind === 'private-before-check' ? 0 : 1);
    assert.ok(creates.every(c => !c.includes('--node') && !c.includes('--append')));
    if (kind === 'shared-after-check') assert.equal(model.metadata.size, 0, 'only invocation-private registration removed');
    else assert.equal(model.calls.some(c => c[1] === 'buildx' && c[2] === 'rm'), false);
    assert.equal(m.state.attempts.length, 0);
  });
});
test('private metadata leaf acquisition and identity controls preserve preexisting and replacement contents', async t => {
  for (const kind of ['preexisting', 'leaf-replaced', 'parent-replaced', 'permissions']) await t.test(kind, async () => {
    const { m, model } = daemonFixture(t);
    if (kind === 'preexisting') fs.mkdirSync(m.buildxRoot);
    else {
      await m.command('docker', ['buildx', 'ls']);
      if (kind === 'leaf-replaced') {
        fs.renameSync(m.buildxRoot, `${m.buildxRoot}-displaced`); fs.mkdirSync(m.buildxRoot);
      } else if (kind === 'parent-replaced') {
        fs.renameSync(m.privateRoot, `${m.privateRoot}-displaced`); fs.mkdirSync(m.privateRoot); fs.mkdirSync(m.buildxRoot);
      } else if (process.platform !== 'win32') fs.chmodSync(m.buildxRoot, 0o755);
    }
    const preserved = path.join(m.buildxRoot, 'competing');
    fs.writeFileSync(preserved, 'preserve original competing bytes');
    m.measure = async () => { await m.builder(1); };
    if (kind === 'permissions' && process.platform === 'win32') {
      assert.equal(m.state.buildxConfig.requestedMode, '0700', 'Windows authored control records request, not native Unix enforcement');
      m.measure = async () => { throw new Error('Unix permission enforcement unsupported on Windows'); };
    }
    assert.equal(await execute(m), 1);
    assert.equal(model.creates, 0);
    assert.equal(fs.readFileSync(preserved, 'utf8'), 'preserve original competing bytes');
    assert.equal(model.calls.some(c => c.includes('rm')), false);
  });
});
test('all Buildx launch forms reject root replacement and preserve Docker environment without fallback', async t => {
  const { m, model } = daemonFixture(t);
  await m.command('docker', ['buildx', 'ls']);
  await m.probe('docker', ['buildx', 'ls']);
  await m.logged(['buildx', 'ls'], 'private-root-log');
  assert.equal(model.environments.length, 3);
  fs.renameSync(m.buildxRoot, `${m.buildxRoot}-displaced`); fs.mkdirSync(m.buildxRoot);
  for (const call of [() => m.command('docker', ['buildx', 'ls']),
    () => m.probe('docker', ['buildx', 'ls']), () => m.logged(['buildx', 'ls'], 'replaced-root-log')]) {
    await assert.rejects(call, /private Buildx configuration replaced/);
  }
  assert.equal(model.environments.length, 3, 'no shared fallback or post-replacement subprocess');
  const code = fs.readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), 'docker_cache_benchmark.mjs'), 'utf8');
  assert.equal(/spawnSync|this\.sync|execSync/.test(code), false, 'no synchronous subprocess bypass remains');
});
test('all Buildx launch forms reject widened private parent mode before launch', async t => {
  for (const form of ['command', 'probe', 'logged']) await t.test(form, async t => {
    const { m, model } = daemonFixture(t);
    model.sharedMetadata.set('competing-shared-registration', 'preserve');
    await m.command('docker', ['buildx', 'ls']);
    const launch = suffix => form === 'logged'
      ? m.logged(['buildx', 'ls'], `parent-mode-${form}-${suffix}`)
      : m[form]('docker', ['buildx', 'ls']);
    await t.test('baseline private mode allows the actual configured client', async () => {
      const count = model.environments.length;
      await launch('baseline');
      assert.equal(model.environments.length, count + 1);
      assert.equal(model.environments.at(-1).root, path.resolve(m.buildxRoot));
      assert.equal(model.sharedMetadata.get('competing-shared-registration'), 'preserve');
      if (process.platform !== 'win32') assert.equal(fs.lstatSync(m.privateRoot).mode & 0o777, 0o700);
      else assert.equal(m.state.buildxConfig.requestedMode, '0700', 'requested Unix mode is not Windows enforcement proof');
    });
    await t.test('Unix parent chmod is rejected without replacing either directory',
      { skip: process.platform === 'win32' ? 'Unix chmod enforcement unsupported on Windows; baseline launch control still executes' : false },
      async () => {
        const parent = fs.lstatSync(m.privateRoot), leaf = fs.lstatSync(m.buildxRoot);
        assert.equal(leaf.mode & 0o777, 0o700);
        fs.chmodSync(m.privateRoot, 0o777);
        const changedParent = fs.lstatSync(m.privateRoot), unchangedLeaf = fs.lstatSync(m.buildxRoot);
        assert.equal(changedParent.mode & 0o777, 0o777);
        assert.equal(changedParent.dev, parent.dev); assert.equal(changedParent.ino, parent.ino);
        assert.equal(unchangedLeaf.dev, leaf.dev); assert.equal(unchangedLeaf.ino, leaf.ino);
        assert.equal(unchangedLeaf.mode & 0o777, 0o700);
        const calls = model.calls.length, environments = model.environments.length;
        await assert.rejects(() => launch('widened'), /private metadata parent.*permissions changed/);
        assert.equal(model.calls.length, calls, 'no subprocess launched before parent-mode admission');
        assert.equal(model.environments.length, environments, 'no private/shared metadata fallback');
        assert.equal(model.sharedMetadata.get('competing-shared-registration'), 'preserve');
        assert.equal(fs.existsSync(path.join(m.privateRoot, `parent-mode-${form}-widened.log`)), false);
      });
  });
});
test('logged bootstrap error/live and never-close clients fail finitely before destructive cleanup', async t => {
  for (const kind of ['never-close', 'error-live', 'cancel-live', 'kill-closes', 'error-then-close']) await t.test(kind, async t => {
    t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
    const { m, model } = daemonFixture(t);
    const launch = m.spawn;
    let child, settled = false;
    m.spawn = (tool, args, options) => {
      if (!args.includes('--bootstrap')) return launch(tool, args, options);
      child = fakeChild();
      if (kind === 'never-close' || kind === 'error-live' || kind === 'cancel-live') child.kill = sig => { child.signals.push(sig); return true; };
      return child;
    };
    m.build = async () => { throw new Error('no timed build'); };
    const pending = execute(m).then(code => { settled = true; return code; });
    await flush(); assert.ok(child);
    if (kind.startsWith('error') || kind === 'cancel-live') {
      if (kind === 'cancel-live') { m.stopped = 'cancelled'; m.stopClients('cancelled'); }
      else child.emit('error', new Error('authored live bootstrap error'));
      await flush();
      assert.equal(model.calls.some(c => c.includes('rm')), false, 'error is not physical close');
      if (kind === 'error-then-close') child.emit('close', 1, null);
      for (let i = 0; i < 3; i++) { t.mock.timers.tick(5000); await flush(); }
    } else for (let i = 0; i < 74; i++) { t.mock.timers.tick(5000); await flush(); }
    assert.equal(await pending, 1);
    assert.equal(settled, true, 'no fixture rescue required to obtain finite failure');
    const digest = JSON.parse(fs.readFileSync(path.join(m.root, 'builder-1-raw-digest.json')));
    if (kind === 'never-close' || kind === 'error-live' || kind === 'cancel-live') {
      assert.equal(child.closed, false);
      assert.equal(m.activeClients.size, 1);
      assert.equal(model.calls.some(c => c.includes('rm')), false);
      assert.equal(digest.complete, false);
      assert.equal(digest.physicalCloseObserved, false);
      assert.ok(m.state.residue.some(r => r.kind === 'unjoined-client'));
      child.emit('close', null, 'SIGKILL');
      assert.equal(m.activeClients.size, 0);
      assert.equal(JSON.parse(fs.readFileSync(path.join(m.root, 'builder-1-raw-digest.json'))).complete, false,
        'late close cannot retroactively make abandoned capture complete');
    } else {
      assert.equal(child.closed, true); assert.equal(m.activeClients.size, 0);
      assert.equal(digest.complete, true);
      assert.ok(model.calls.some(c => c.includes('rm')));
    }
    assert.equal(m.state.attempts.length, 0);
  });
});
test('shared logged capture preserves short writes and propagates stream/write/close/spawn failures', async t => {
    for (const kind of ['short-write', 'zero-write', 'stream-error', 'close-error', 'spawn-throw']) await t.test(kind, async t => {
      const m = new Measurement(request(env(), event()), temporary(t), directory);
      const child = fakeChild(), payload = Buffer.from('exact original output bytes\n');
      const open = fs.openSync, write = fs.writeSync, close = fs.closeSync;
      let logFD, interrupted = false;
      t.mock.method(fs, 'openSync', (file, ...args) => {
        const fd = open(file, ...args);
        if (file === path.join(m.privateRoot, `${kind}.log`) && args[0] === 'wx') logFD = fd;
        return fd;
      });
      t.mock.method(fs, 'writeSync', (fd, ...args) => {
        if (fd === logFD && !child.closed) {
          if (kind === 'zero-write') return 0;
          if (kind === 'short-write' && args[2] > 1) return write(fd, args[0], args[1], 1);
        }
        return write(fd, ...args);
      });
      t.mock.method(fs, 'closeSync', fd => {
        if (kind === 'close-error' && fd === logFD && !interrupted) {
          interrupted = true; close(fd); throw new Error('injected close failure after descriptor close');
        }
        return close(fd);
      });
      m.spawn = () => {
        if (kind === 'spawn-throw') throw new Error('injected unavailable launch');
        queueMicrotask(() => {
          if (kind === 'stream-error') child.stderr.emit('error', new Error('injected stream failure'));
          else child.stdout.emit('data', payload);
          child.emit('close', 0, null);
        });
        return child;
      };
      if (kind === 'spawn-throw') {
        await assert.rejects(() => m.logged(['inspect', 'owned'], kind), /spawn threw/);
        await m.cleanup(); assert.equal(m.activeClients.size, 1);
        assert.ok(m.state.residue.some(r => r.kind === 'unknown-client-creation'));
      } else {
        const result = await m.logged(['inspect', 'owned'], kind);
        assert.equal(result.code, 0, 'physical exit code is not repaired to hide capture failure');
        if (kind === 'short-write') {
          assert.equal(result.reason, undefined);
          assert.deepEqual(fs.readFileSync(path.join(m.privateRoot, `${kind}.log`)), payload);
          assert.equal(JSON.parse(fs.readFileSync(path.join(m.root, `${kind}-raw-digest.json`))).sha256, hash(payload));
        } else assert.ok(result.reason, 'write/stream/close failure cannot become successful capture');
        assert.equal(m.activeClients.size, 0);
      }
    });
});
test('shared runner preserves error/no-PID/close distinctions and includes force/join grace in elapsed evidence', async t => {
  for (const kind of ['error-delayed-close', 'no-pid-error-close', 'no-pid-zero-close', 'output-limit', 'never-close', 'missing-log']) {
    await t.test(kind, async t => {
      const now = Date.now();
      t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
      t.mock.method(performance, 'now', () => Date.now());
      const m = new Measurement(request(env(), event()), temporary(t), directory);
      const child = fakeChild();
      m.spawn = () => child;
      if (kind.startsWith('no-pid')) delete child.pid;
      if (kind === 'never-close') child.kill = sig => { child.signals.push(sig); return true; };
      const pending = m.logged(['inspect', 'owned'], `${kind}-client`);
      if (kind === 'missing-log') {
        fs.unlinkSync(path.join(m.privateRoot, `${kind}-client.log`)); child.emit('close', 0, null);
      } else if (kind === 'no-pid-zero-close') child.emit('close', 0, null);
      else if (kind === 'output-limit') child.stdout.emit('data', { length: 16 * 1024 ** 2 + 1 });
      else if (kind !== 'never-close') {
        child.emit('error', new Error('actual authored process error'));
        await flush(); assert.equal(m.activeClients.size, 1);
        t.mock.timers.tick(1000); child.emit('close', -2, null);
      } else t.mock.timers.tick(360000);
      await flush();
      if (kind === 'output-limit' || kind === 'never-close') {
        t.mock.timers.tick(5000); await flush();
        if (kind === 'never-close') { t.mock.timers.tick(5000); await flush(); }
      }
      const result = await pending;
      assert.ok(result.reason);
      assert.equal(m.state.clients[0].closed, kind !== 'never-close');
      if (kind === 'never-close') {
        assert.equal(result.unjoined, true); assert.equal(result.elapsedSeconds, 370);
        await m.cleanup(); assert.ok(m.state.failures.some(f => f.includes('not joined')));
        child.emit('close', null, 'SIGKILL');
      }
      if (kind === 'output-limit') assert.equal(JSON.parse(fs.readFileSync(path.join(m.root, `${kind}-client-raw-digest.json`))).complete, false);
      if (kind === 'no-pid-zero-close') assert.match(result.reason, /without observed client PID/);
      if (kind === 'missing-log') assert.match(result.reason, /capture finalization failed/);
      if (kind === 'error-delayed-close') assert.equal(result.elapsedSeconds, 1);
    });
  }
});
test('POSIX ignored-TERM client uses the real async command through builder with responsive timers and independent ownership watchdog',
  { skip: process.platform === 'win32' ? 'POSIX signal-ignore behavior unsupported on Windows; portable runner/ownership controls still run' : false }, async t => {
    const { m, model } = daemonFixture(t);
    const launch = m.spawn;
    let child, beats = 0, rescued = false;
    const owner = m.config.owner;
    const receipt = path.join(m.root, 'owned-node.json'), term = path.join(m.root, 'owned-node-term.json');
    m.config.deadline = Date.now() + 120000 + 1500;
    m.spawn = (tool, args, options) => {
      if (args[0] !== 'create') return launch(tool, args, options);
      child = spawn(process.execPath, ['-e', `
        const fs = require('node:fs');
        process.on('SIGTERM', () => fs.writeFileSync(${JSON.stringify(term)}, 'TERM'));
        fs.writeFileSync(${JSON.stringify(receipt)}, JSON.stringify({owner:${JSON.stringify(owner)},pid:process.pid,parent:process.ppid}), {flag:'wx'});
        setInterval(() => {}, 1000);
      `], { stdio: ['ignore', 'pipe', 'pipe'] });
      return child;
    };
    const heartbeat = setInterval(() => { if (child && child.exitCode === null && child.signalCode === null) beats++; }, 50);
    const watchdog = setTimeout(() => {
      if (child && child.exitCode === null && child.signalCode === null) {
        const identity = JSON.parse(fs.readFileSync(receipt));
        assert.equal(identity.owner, owner); assert.equal(identity.pid, child.pid); assert.equal(identity.parent, process.pid);
        rescued = true; child.kill('SIGKILL');
      }
    }, 10000);
    try {
      assert.equal(await execute(m), 1);
      const identity = JSON.parse(fs.readFileSync(receipt));
      assert.equal(identity.owner, owner); assert.equal(identity.pid, child.pid); assert.equal(identity.parent, process.pid);
      assert.equal(fs.readFileSync(term, 'utf8'), 'TERM');
      assert.equal(rescued, false, 'production runner force/join, not external rescue');
      assert.ok(beats >= 3, 'event loop stays responsive while command awaits ignored TERM');
      assert.equal(m.activeClients.size, 0);
      assert.equal(child.signalCode, 'SIGKILL');
      assert.equal(model.calls.some(c => c.includes('rm')), false, 'unconfirmed create stdout never becomes daemon ownership');
      assert.equal(m.state.attempts.length, 0);
    } finally {
      clearInterval(heartbeat); clearTimeout(watchdog);
      if (child && child.exitCode === null && child.signalCode === null) child.kill('SIGKILL');
    }
  });
test('actual implicit entitlement replay is rejected by the production caller before any build', async t => {
  const { m, model } = daemonFixture(t, ({ args, result }) => {
    if (args[0] === 'inspect' && !args.includes('--format')) {
      const xs = JSON.parse(result.stdout); xs[0].Args.push('--allow-insecure-entitlement=network.host'); result.stdout = JSON.stringify(xs);
    }
  });
  assert.equal(await execute(m), 1);
  assert.equal(m.state.attempts.length, 0);
  assert.match(m.state.failures.join('\n'), /host networking\/insecure entitlements forbidden/);
  const evidence = JSON.parse(fs.readFileSync(path.join(m.root, 'builder-1-inspections.json')));
  assert.ok(evidence[0].inspect.Args.includes('--allow-insecure-entitlement=network.host'));
  assert.equal(model.creates, 1);
});
test('production daemon admission rejects missing or unadmitted effective fields without starting timing', async t => {
  const mutations = {
    host: x => { x.HostConfig.NetworkMode = 'host'; },
    insecure: x => { x.Args.push('--allow-insecure-entitlement=security.insecure'); },
    missingArgs: x => { delete x.Args; },
    privilege: x => { delete x.HostConfig.Privileged; },
    caps: x => { x.HostConfig.CapAdd = ['SYS_ADMIN']; },
    missingCaps: x => { delete x.HostConfig.CapDrop; },
    security: x => { x.HostConfig.SecurityOpt = ['seccomp=unconfined']; },
    bind: x => { x.HostConfig.Binds = ['/host:/host']; },
    device: x => { x.HostConfig.Devices = [{}]; },
    userns: x => { x.HostConfig.UsernsMode = 'host'; },
    pid: x => { x.HostConfig.PidMode = 'host'; },
    missingNetwork: x => { delete x.NetworkSettings; },
    quota: x => { x.HostConfig.CpuQuota = 400000; },
    swap: x => { x.HostConfig.MemorySwap++; },
    extraMount: x => { x.Mounts.push({ Type: 'bind', Destination: '/host' }); },
    namedCache: x => { x.HostConfig.Mounts[0].Source = x.Mounts[0].Name; x.HostConfig.Mounts[0].Source ??= 'named'; },
    missingID: x => { delete x.Id; },
    image: x => { x.Image = ''; },
    owner: x => { x.Config.Labels = {}; },
    args: x => { x.Args = ['--debug']; },
  };
  for (const [name, mutate] of Object.entries(mutations)) await t.test(name, async () => {
    const { m, model } = daemonFixture(t, ({ args, result }) => {
      if (args[0] === 'inspect' && !args.includes('--format')) { const xs = JSON.parse(result.stdout); mutate(xs[0]); result.stdout = JSON.stringify(xs); }
    });
    assert.equal(await execute(m), 1);
    assert.equal(m.state.attempts.length, 0);
    assert.equal(model.creates, 1, 'never retry daemon creation');
    assert.ok(m.state.failures.length);
  });
});
test('production startup retains config, worker, cgroup, cache and metadata failures before timing', async t => {
  for (const kind of ['local-config', 'copy-config', 'effective-config', 'workers-empty', 'workers-platform', 'workers-malformed',
    'workers-uppercase', 'workers-null-platform',
    'not-running', 'paused', 'restarting', 'cgroups', 'cache', 'remote-binding', 'remote-bootstrap', 'remote-create', 'remote-confirmation']) {
    await t.test(kind, async () => {
      const { m, model } = daemonFixture(t, ({ m, model, args, result }) => {
        if (kind === 'copy-config' && args[0] === 'cp' && fixtureContainerCopy(args[1], model.containers)) {
          fs.writeFileSync(args[2], 'invalid config\n');
        }
        if (kind === 'effective-config' && args[0] === 'exec' && args[2] === 'cat') result.stdout = 'insecure-entitlements = ["network.host"]\n';
        if (args[0] === 'exec' && args[2] === 'buildctl') {
          if (kind === 'workers-empty') result.stdout = '[]';
          if (kind === 'workers-platform') result.stdout = JSON.stringify([{ id: 'worker', platforms: [{ os: 'linux', architecture: 'arm64' }] }]);
          if (kind === 'workers-malformed') result.stdout = 'not-json';
          if (kind === 'workers-uppercase') result.stdout = JSON.stringify([{ ID: 'worker', Platforms: [{ os: 'linux', architecture: 'amd64' }] }]);
          if (kind === 'workers-null-platform') result.stdout = JSON.stringify([{ id: 'worker', platforms: [null] }]);
        }
        if (args[0] === 'start' && ['not-running', 'paused', 'restarting'].includes(kind)) {
          const x = model.containers.get(args[1]);
          if (kind === 'not-running') x.State.Running = false;
          if (kind === 'paused') x.State.Paused = true;
          if (kind === 'restarting') x.State.Restarting = true;
        }
        if (kind === 'cgroups' && args[0] === 'exec' && args[2] === 'sh') result.stdout = result.stdout.replace('200000 100000', '400000 100000');
        if (kind === 'cache' && args[0] === 'buildx' && args[1] === 'du') result.stdout = '[{"record":"already-populated"}]';
        if (kind === 'remote-binding' && args[0] === 'buildx' && args[1] === 'inspect') result.stdout = result.stdout.replace('Driver:     remote', 'Driver:     docker-container');
        if (kind === 'remote-bootstrap' && args.includes('--bootstrap')) { result.status = 1; result.stderr = 'bootstrap failed'; }
        if (['remote-create', 'remote-confirmation'].includes(kind) && args[0] === 'buildx' && args[1] === 'create') {
          if (kind === 'remote-create') { result.status = 1; result.stderr = 'metadata collision'; }
          else result.stdout = 'wrong-name';
        }
      });
      if (kind === 'local-config') fs.writeFileSync(path.join(m.root, 'buildkit.toml'), 'invalid config\n');
      assert.equal(await execute(m), 1);
      assert.equal(m.state.attempts.length, 0);
      assert.ok(m.state.failures.length);
      assert.equal(model.creates, kind === 'local-config' ? 0 : 1, 'one creation only; no restart/fallback/retry');
      if (model.creates) assert.ok(m.state.builders[0].inspections.length, 'actual authored inspect retained even after admission failure');
      if (['effective-config', 'workers-empty', 'workers-platform', 'workers-malformed'].includes(kind)) {
        const cgroups = JSON.parse(fs.readFileSync(path.join(m.root, 'builder-1-cgroups.json')));
        assert.ok(cgroups.length && cgroups[0].usage.startsWith('200000 100000\n8589934592\n0\n'),
          'running-daemon cgroups retained before config or worker guard fails');
      }
    });
  }
});
test('production teardown separates metadata, exact daemon and anonymous-cache failures without retry or adoption', async t => {
  for (const kind of ['metadata-replaced', 'metadata-rm', 'daemon-replaced', 'cache-replaced', 'daemon-rm', 'cache-remains']) {
    await t.test(kind, async () => {
      const { m, model } = daemonFixture(t, ({ m, model, args, result }) => {
        const b = m.state.builders[0];
        if (!b?.cleanupAttempted) return;
        if (kind === 'metadata-replaced' && args[0] === 'buildx' && args[1] === 'inspect') {
          result.stdout = result.stdout.replace(`docker-container://${b.id}`, `docker-container://${'9'.repeat(64)}`);
        }
        if (kind === 'metadata-rm' && args[0] === 'buildx' && args[1] === 'rm') {
          model.metadata.set(b.name, `docker-container://${b.id}`); result.status = 1; result.stderr = 'metadata removal denied';
        }
        if (kind === 'daemon-replaced' && args[0] === 'inspect') {
          const xs = JSON.parse(result.stdout); xs[0].Id = '9'.repeat(64); result.stdout = JSON.stringify(xs);
        }
        if (kind === 'cache-replaced' && args[0] === 'volume' && args[1] === 'inspect') {
          const xs = JSON.parse(result.stdout); xs[0].CreatedAt = 'replacement'; result.stdout = JSON.stringify(xs);
        }
        if (args[0] === 'rm' && ['daemon-rm', 'cache-remains'].includes(kind)) {
          model.volumes.set(b.volume, b.volumeMetadata);
          if (kind === 'daemon-rm') { model.containers.set(b.id, b.inspections[0].inspect); result.status = 1; result.stderr = 'daemon removal denied'; }
        }
      });
      m.measure = async () => { await m.builder(1); };
      assert.equal(await execute(m), 1);
      const b = m.state.builders[0];
      assert.equal(b.cleanupAttempted, true); assert.notEqual(b.removed, true);
      assert.equal(model.calls.filter(c => c[1] === 'rm').length, kind === 'daemon-replaced' ? 0 : 1, 'no hidden cleanup retry');
      assert.equal(model.calls.some(c => c[1] === 'volume' && c[2] === 'rm'), false, 'never delete anonymous cache by a possibly replaced name');
      if (kind.startsWith('metadata')) assert.equal(model.containers.size, 0, 'metadata failure does not prevent safe independent daemon teardown');
      if (kind === 'cache-replaced') assert.ok(model.calls.some(c => c[1] === 'rm' && !c.includes('-v')), 'uncertain cache retained while owned daemon removed');
      assert.ok(m.state.residue.length && m.state.failures.length);
    });
  }
});
test('real load/runtime/snapshot callers reject authored invalid artifacts and retain the failed attempt', async t => {
  for (const kind of ['load-platform', 'runtime-caps', 'runtime-output', 'snapshot-root', 'snapshot-ref', 'snapshot-hash']) await t.test(kind, async () => {
    const { m, model } = daemonFixture(t, ({ args, result }) => {
      if (kind === 'load-platform' && args[0] === 'image' && args[1] === 'inspect' && args[2].includes(':r')) {
        const xs = JSON.parse(result.stdout); xs[0].Architecture = 'arm64'; result.stdout = JSON.stringify(xs);
      }
      if (kind === 'runtime-caps' && args[0] === 'inspect' && !args.includes('--format')) {
        const xs = JSON.parse(result.stdout); if (xs[0].runtime) xs[0].HostConfig.Memory++; result.stdout = JSON.stringify(xs);
      }
      if (kind === 'runtime-output' && args[0] === 'start' && args.includes('-a')) result.stdout = 'waza version another\n';
      if (kind === 'snapshot-root' && args[0] === 'exec' && args[2] === 'find' && args.includes('-path')) result.stdout = '/unowned/index.html\n';
      if (kind === 'snapshot-ref' && args[0] === 'exec' && args[2] === 'cat' && args[3].endsWith('index.html')) result.stdout = '<script src="assets/missing.js"></script>';
      if (kind === 'snapshot-hash' && args[0] === 'exec' && args[2] === 'sha256sum') result.stdout = 'a'.repeat(64);
    });
    m.build = Measurement.prototype.build;
    assert.equal(await execute(m), 1);
    assert.equal(m.state.attempts.length, 1);
    assert.notEqual(m.state.attempts[0].runtimePassed, true);
    assert.equal(m.state.summary.complete, false);
    assert.equal(model.creates, 1);
    assert.ok(m.state.failures.length);
  });
});
test('readiness has one absolute phase deadline and retries only classified not-ready errors within16 attempts', async t => {
  for (const kind of ['delayed', 'delayed-wrapped', 'never-ready', 'permanent', 'wrapped-permanent', 'phase-expired']) await t.test(kind, async t => {
    const now = Date.now(); t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
    const { m, model } = daemonFixture(t, ({ args, result }) => {
      if (args[0] === 'exec' && args[2] === 'buildctl') {
        if (kind === 'phase-expired') t.mock.timers.setTime(now + 360000 - 100);
        if (!kind.startsWith('delayed') || model.workers < 3) {
          result.status = 1;
          result.stderr = kind === 'permanent' ? 'invalid daemon configuration' : kind === 'wrapped-permanent' ?
            'error: failed to list workers: rpc error: code = PermissionDenied desc = access denied' :
            `error: ${kind === 'delayed-wrapped' ? 'failed to list workers: ' : ''}rpc error: code = Unavailable desc = connection error: desc = "transport: Error while dialing: dial unix /run/buildkit/buildkitd.sock: connect: connection refused"`;
        }
      }
    });
    m.measure = async () => { await m.builder(1); };
    const pending = execute(m); await flush();
    if (kind.startsWith('delayed') || kind === 'never-ready') {
      for (let i = 1; i <= 16; i++) { t.mock.timers.tick(120 * i); await flush(); }
    }
    assert.equal(await pending, 1, 'zero builds are not a complete measurement');
    assert.equal(model.creates, 1); assert.equal(m.state.attempts.length, 0);
    assert.equal(model.workers, kind.startsWith('delayed') ? 3 : kind === 'never-ready' ? 16 : 1);
    if (kind.startsWith('delayed')) {
      assert.ok(m.state.builders[0].readyAt);
      assert.deepEqual(m.state.builders[0].workers, [{ id: 'authored-worker', platforms: [{ os: 'linux', architecture: 'amd64' }] }]);
      assert.equal(m.state.builders[0].readinessErrors.length, 2);
    }
    else assert.ok(m.state.failures.length);
    assert.equal(m.state.builders[0].setupDeadline, now + 360000, 'never reset after each create/start/poll/register');
  });
});
test('hung readiness probe is killed and joined before owned daemon teardown within the shared deadline', async t => {
  const now = Date.now(); t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
  const { m, model } = daemonFixture(t);
  const spawn = m.spawn; let hung;
  m.spawn = (tool, args, options) => {
    if (args[0] === 'exec' && args[2] === 'buildctl') { hung = fakeChild(); return hung; }
    return spawn(tool, args, options);
  };
  const pending = execute(m); await flush();
  assert.ok(hung);
  for (let i = 0; i < 3; i++) { t.mock.timers.tick(5000); await flush(); }
  assert.equal(await pending, 1);
  assert.deepEqual(hung.signals, ['SIGKILL']);
  assert.equal(m.activeClients.size, 0);
  assert.equal(model.creates, 1); assert.equal(m.state.attempts.length, 0);
  assert.equal(model.containers.size, 0);
});
test('startup sampler cadence stops slow accounting through the production builder caller', async t => {
  const now = Date.now(); t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
  const { m, model } = daemonFixture(t);
  const spawn = m.spawn, blocked = [];
  m.spawn = (tool, args, options) => {
    if (args[0] === 'exec' && args[2] === 'sh') { const child = fakeChild(); blocked.push(child); return child; }
    return spawn(tool, args, options);
  };
  const pending = execute(m); await flush();
  for (let i = 0; i < 3 + maxSkippedSamplerTicks; i++) { t.mock.timers.tick(5000); await flush(); }
  assert.equal(await pending, 1);
  assert.match(m.state.failures.join('\n'), /startup resource sampler exceeded five-second cadence|resource probe deadline/);
  assert.ok(m.state.samplerOverruns.some(o => o.scope === 'startup'));
  assert.ok(blocked.length >= 2 && blocked.every(child => child.signals.includes('SIGKILL')));
  assert.equal(model.creates, 1); assert.equal(m.state.attempts.length, 0);
});
test('unjoined killed probe bounds its grace and leaves resources untouched rather than pretending teardown', async t => {
  const { m, model } = daemonFixture(t);
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
  const child = fakeChild();
  child.kill = signal => { child.signals.push(signal); return true; };
  m.spawn = () => child;
  const pending = assert.rejects(m.probe('docker', ['inspect', 'owned']), /did not join within five-second kill grace/);
  t.mock.timers.tick(15000); await flush();
  t.mock.timers.tick(5000); await flush(); await pending;
  assert.equal(m.activeClients.size, 1);
  await m.cleanup();
  assert.match(m.state.failures.join('\n'), /clients not joined/);
  assert.equal(model.calls.length, 0, 'no destructive operations until real process close');
  child.emit('close', null, 'SIGKILL'); await flush();
  assert.equal(m.activeClients.size, 0);
});
test('aggregate reserve shortens startup and anonymous cache reuse is rejected through the production loop', async t => {
  const now = Date.now(); t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
  const { m } = daemonFixture(t);
  m.config.deadline = now + 120000 + 1000;
  m.measure = async () => { await m.builder(1); };
  assert.equal(await execute(m), 1);
  assert.equal(m.state.builders[0].setupDeadline, now + 1000);
  const reused = daemonFixture(t, ({ m, model, args, result }) => {
    if (model.creates === 2 && args[0] === 'inspect') {
      const xs = JSON.parse(result.stdout), old = m.state.builders[0].volume;
      xs[0].Mounts[0].Name = old; result.stdout = JSON.stringify(xs);
      model.volumes.set(old, { ...model.volumes.values().next().value, Name: old });
    }
  });
  assert.equal(await execute(reused.m), 1);
  assert.equal(reused.m.state.attempts.length, 2, 'one completed authored pair, then cache identity refusal');
  assert.equal(reused.model.creates, 2, 'no replacement third builder');
  assert.match(reused.m.state.failures.join('\n'), /fresh anonymous local cache identity/);
});
test('production loop retains exactly cold/warm cache then removes six states in AB/BA/AB order', async t => {
  const m = new OfflineMeasurement(temporary(t));
  assert.equal(await execute(m), 0);
  assert.equal(m.state.attempts.length, 12);
  assert.deepEqual(m.state.attempts.filter(a => a.phase === 'cold').map(a => a.variant), order.flat());
  assert.equal(m.calls.filter(c => c.startsWith('fresh-')).length, 6);
  assert.equal(m.calls.filter(c => c.startsWith('remove-')).length, 6);
  assert.deepEqual(m.state.mutations.map(x => x.variant), order.flat());
  assert.equal(m.calls.at(-1), 'cleanup');
  assert.equal(m.state.summary.complete, true);
  assert.equal(m.state.summary.generatedAssetBytesEqual, true);
  assert.equal(m.state.contexts.length, 8, 'two immutable snapshots plus six retained working contexts');
  for (const context of m.state.contexts) {
    const input = path.join(context.dir, 'cmd/waza/measurement_cache_input.go');
    assert.equal(fs.existsSync(input), context.round !== undefined);
    if (context.round === undefined) await m.verifyContext(context);
  }
});
test('failed production attempt is retained, stops the whole allocation and never produces medians', async t => {
  for (const nth of [1, 2, 7, 12]) {
    const m = new OfflineMeasurement(temporary(t), nth);
    assert.equal(await execute(m), 1);
    assert.equal(m.state.attempts.length, nth);
    assert.equal(m.state.attempts.at(-1).code, 1);
    assert.equal(m.state.summary.complete, false);
    assert.equal(m.state.summary.medians, undefined);
    assert.equal(m.calls.at(-1), 'cleanup');
  }
  assert.equal(pairedSummary([]).complete, false);
});
test('prior candidate/request inventories include rerun attempts and reject duplicates or unavailable evidence', async t => {
  for (const kind of ['request', 'candidate', 'rerun', 'unavailable', 'allowed']) {
    const root = temporary(t);
    const m = new Measurement(request(env(), event()), root, root, () => { throw new Error('external command forbidden'); });
    const endpoints = [];
    m.api = async endpoint => {
      endpoints.push(endpoint);
      if (endpoint === 'pulls/836') return event().pull_request;
      if (endpoint.includes('actions/artifacts')) return { artifacts: kind === 'request' ? [{}] : [], total_count: kind === 'request' ? 1 : 0 };
      if (endpoint.includes('/runs?')) return kind === 'unavailable' ? {} :
        { workflow_runs: ['candidate', 'rerun'].includes(kind) ? [{ id: 99 }] : [] };
      assert.match(endpoint, /actions\/runs\/99\/jobs\?filter=all&per_page=100$/);
      return kind === 'rerun' ? { total_count: 2, jobs: [
        { name: 'Container cache measurement', conclusion: 'failure', run_attempt: 1 },
        { name: 'Container cache measurement', conclusion: 'skipped', run_attempt: 2 },
      ] } : { total_count: 1, jobs: [{ name: 'Container cache measurement', conclusion: 'failure' }] };
    };
    if (kind === 'allowed') await m.authorization();
    else await assert.rejects(m.authorization());
    if (['candidate', 'rerun'].includes(kind)) assert.ok(endpoints.some(endpoint => endpoint.includes('filter=all')));
    assert.equal(m.state.attempts.length, 0);
  }
});
test('cleanup refuses changed builder IDs before destructive commands; deadline failures propagate', async t => {
  const root = temporary(t), calls = [];
  const config = request(env(), event());
  const m = new Measurement(config, root, root, fixtureClient((tool, args) => {
    calls.push([tool, ...args]);
    return { status: 0, stdout: JSON.stringify([{ Id: 'competing', Mounts: [{ Destination: '/var/lib/buildkit', Name: 'owned-volume' }],
      Config: { Image: 'pinned', Env: [`WAZA835_OWNER=${config.owner}`] } }]) };
  }));
  m.state.pins['moby/buildkit:buildx-stable-1'] = 'pinned';
  await assert.rejects(() => m.removeBuilder({ id: 'original', imageId: 'image', creationConfirmed: true, metadataCreated: true, ownershipAcquired: true,
    volumeMetadata: { Name: 'owned-volume' }, container: 'owned', volume: 'owned-volume' }), /builder identity changed/);
  assert.equal(calls.some(c => c.includes('rm')), false);
  m.config.deadline = Date.now() - 1;
  await assert.rejects(() => m.command('docker', ['info']));
});
test('production cleanup preserves replacement contexts and records residue instead of success', async t => {
  const root = temporary(t), calls = [];
  const m = new Measurement(request(env(), event()), root, root, fixtureClient((tool, args) => {
    calls.push([tool, ...args]); throw new Error('external operation unavailable');
  }));
  const dir = path.join(root, 'context-owned'); fs.mkdirSync(dir);
  const original = fs.statSync(dir);
  m.state.contexts.push({ dir, dev: original.dev, ino: original.ino });
  fs.renameSync(dir, path.join(root, 'displaced-owned'));
  fs.mkdirSync(dir); fs.writeFileSync(path.join(dir, 'user-replacement'), 'preserve');
  m.state.builders.push({ name: 'owned', container: 'owned-container', metadataCreated: true, ownershipAcquired: true,
    creationAttempted: true, id: 'owned-id', imageId: 'image', volumeMetadata: {} });
  await m.cleanup();
  assert.equal(fs.readFileSync(path.join(dir, 'user-replacement'), 'utf8'), 'preserve');
  assert.ok(m.state.failures.some(x => x.includes('context') && x.includes('directory replaced')));
  assert.ok(m.state.failures.some(x => x.includes('external operation unavailable')));
  assert.equal(calls.some(x => x.includes('rm')), false);
});
test('production execute never adopts matching competitors after failed or unverified creation', async t => {
  for (const kind of ['failed-create', 'wrong-confirmation']) {
    const { m, model } = daemonFixture(t, ({ args, result }) => {
      if (args[0] === 'create') {
        if (kind === 'failed-create') { result.status = 1; result.stderr = 'colliding create'; }
        else result.stdout = 'unverified-response\n';
      }
    });
    assert.equal(await execute(m), 1);
    assert.equal(model.containers.size, 1, 'possible competing/partial container remains untouched');
    assert.equal(m.state.attempts.length, 0);
    const builder = m.state.builders[0];
    assert.equal(builder.creationAttempted, true);
    assert.equal(builder.metadataCreated, false);
    assert.equal(builder.ownershipAcquired, false);
    assert.equal(builder.id, undefined);
    assert.equal(m.state.residue[0].container, builder.container);
    assert.equal(model.calls.some(args => args.includes('rm') || args.includes('prune')), false);
    assert.equal(model.calls.filter(args => args[1] === 'inspect').length, 0, 'no cleanup identity adoption');
    assert.equal(model.calls.at(-1)[1], 'create', 'failed creation causes no subsequent Docker cleanup');
  }
});
test('production cleanup removes only successfully acquired builder identities', async t => {
  const { m, model } = daemonFixture(t);
  m.measure = async () => { await m.builder(1); throw new Error('post-acquisition failure'); };
  assert.equal(await execute(m), 1);
  const builder = m.state.builders[0];
  assert.match(builder.id, /^[a-f0-9]{64}$/);
  assert.equal(builder.ownershipAcquired, true);
  assert.equal(builder.removed, true);
  assert.deepEqual(model.calls.filter(args => args.includes('rm')), [
    ['docker', 'buildx', 'rm', builder.name], ['docker', 'rm', '-f', '-v', builder.id],
  ]);
  assert.ok(fs.existsSync(m.privateRoot), 'private logs retained for host teardown');
});
test('production loop and cleanup preserve competing descendants and warm leaf/parent replacements', async t => {
  const m = new OfflineMeasurement(temporary(t)), replacements = [];
  const originalBuild = m.build.bind(m);
  m.build = async (...args) => {
    await originalBuild(...args);
    const [, variant, round, phase] = args;
    if (round !== 1 || phase !== 'warm') return;
    const dir = m.state.contexts.find(c => c.variant === variant && c.round === round).dir;
    const parent = path.join(dir, 'cmd/waza'), leaf = path.join(parent, 'measurement_cache_input.go');
    if (variant === 'A') fs.renameSync(leaf, `${leaf}.owned-displaced`);
    else { fs.renameSync(parent, `${parent}-owned-displaced`); fs.mkdirSync(parent); }
    fs.writeFileSync(leaf, `competing-${variant}`);
    replacements.push([leaf, `competing-${variant}`]);
  };
  const privateChild = path.join(m.privateRoot, 'private-child');
  fs.mkdirSync(privateChild); fs.writeFileSync(path.join(privateChild, 'original'), 'owned');
  fs.renameSync(privateChild, `${privateChild}-owned-displaced`); fs.mkdirSync(privateChild);
  const privateLeaf = path.join(privateChild, 'competing.json');
  fs.writeFileSync(privateLeaf, 'private competing descendant');
  m.cleanup = () => Measurement.prototype.cleanup.call(m);
  assert.equal(await execute(m), 0);
  for (const [leaf, bytes] of replacements) assert.equal(fs.readFileSync(leaf, 'utf8'), bytes);
  assert.equal(fs.readFileSync(privateLeaf, 'utf8'), 'private competing descendant');
  assert.equal(m.state.retainedDirectories.length, 9);
  assert.ok(m.state.retainedDirectories.every(entry => entry.rootIdentityMatches && entry.descendantsUnverified));
});
test('production execute retains shared images even when absent initially and unused by containers', async t => {
  const root = temporary(t), calls = [];
  const m = new Measurement(request(env(), event()), root, root, fixtureClient((tool, args) => {
    calls.push([tool, ...args]);
    return { status: 0, stdout: args[0] === 'image' ? JSON.stringify([{ Id: 'new-shared-image' }]) : '' };
  }));
  m.state.initialImages = [];
  m.state.pins['moby/buildkit:buildx-stable-1'] = 'pinned';
  m.state.builders.push({ imageId: 'new-shared-image', removed: true });
  m.measure = async () => { throw new Error('existing failure'); };
  assert.equal(await execute(m), 1);
  assert.equal(calls.length, 0, 'no absence-based shared image cleanup or inventory');
  assert.match(m.state.sharedImagesQualification, /does not establish exclusive ownership/);
});

const directory = path.dirname(fileURLToPath(import.meta.url));
const normalizeWorkflow = text => text.replace(/\r\n/g, '\n');
const workflow = normalizeWorkflow(fs.readFileSync(path.join(directory, '../workflows/docker-ci.yml'), 'utf8'));
const source = fs.readFileSync(path.join(directory, 'docker_cache_benchmark.mjs'), 'utf8');
function wiring(text) {
  text = normalizeWorkflow(text);
  const start = text.indexOf('\n  docker-build:\n') + 1, end = text.indexOf('\n  cache-measurement:\n');
  assert.ok(start > 0 && end > start, 'canonical ordinary/measurement job boundaries required');
  let ordinary = text.slice(start, end).replaceAll("    if: github.event.action != 'edited'\n", '');
  for (const [normal, metadata] of [['Build and Verify Docker Image', 'Metadata-only Docker validation not selected'],
    ['Local runner container contract', 'Metadata-only runner validation not selected']]) {
    const expression = `name: \${{ github.event.action == 'edited' && '${metadata}' || '${normal}' }}`;
    assert.ok(ordinary.includes(expression), 'distinct edited-only skipped names required');
    ordinary = ordinary.replace(expression, `name: ${normal}`);
  }
  assert.equal(hash(ordinary), '1f3ed9219f4ad498333a46028c669bacf249a9d71030d9f2678cf0207c02674b', 'cdfd ordinary-job frozen oracle');
  assert.match(text, /types: \[opened, synchronize, reopened, edited\]/);
  assert.equal((text.match(/if: github.event.action != 'edited'/g) ?? []).length, 2);
  const measurement = text.slice(end + 1).split(/\n  [\w-]+:\n/)[0];
  for (const gate of ["github.event_name == 'pull_request'", "github.event.action == 'edited'", "github.actor == 'spboyer'",
    "github.run_attempt == 1", "github.event.pull_request.number == 836", "github.event.changes.body != null",
    'startsWith(toJSON(github.event.changes.body.from), \'"\'',
    "!contains(github.event.changes.body.from,", "contains(github.event.pull_request.body,"]) assert.ok(measurement.includes(gate), gate);
  assert.match(measurement, /timeout-minutes: 65/);
  assert.match(measurement, /cancel-in-progress: false/);
  assert.match(measurement, /permissions:\n      contents: read\n      actions: read/);
  assert.match(measurement, /persist-credentials: false/);
  assert.match(measurement, /fetch-depth: 0/);
  assert.match(measurement, /lfs: true/);
  assert.match(measurement, /if: always\(\)/);
  assert.match(measurement, /      - name: Retain partial or final evidence\n        if: always\(\) && env\.CACHE_RESULTS != '' && env\.CACHE_RESULTS_ID != ''\n/);
  assert.match(measurement, /include-hidden-files: false/);
  assert.deepEqual(publicEvidencePatterns(text), [
    'setup.json', 'measurement-started.json', 'activation-rejected.json', 'state.json', 'resources.jsonl',
    'source-*.json', 'builder-*.json', 'builder-*.log', 'r*-*.json', 'r*-*.log', 'r*-*.waza', 'r*-assets/**',
  ], 'explicit public-only artifact selection');
  assert.match(measurement, /actions\/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9/);
  assert.ok(measurement.indexOf('Start aggregate budget') < measurement.indexOf('Checkout frozen'));
  assert.doesNotMatch(measurement.slice(0, measurement.indexOf('\n    steps:\n')), /\$\{\{\s*runner\./,
    'runner context is unavailable in job-level env');
  assert.match(measurement, /      - name: Start aggregate budget before checkout\n        env:\n          CACHE_RESULTS: \$\{\{ runner.temp \}\}\/waza835-results-\$\{\{ github.run_id \}\}-\$\{\{ github.run_attempt \}\}\n/);
  assert.match(measurement, /echo "CACHE_RESULTS=\$CACHE_RESULTS" >> "\$GITHUB_ENV"/);
  assert.match(measurement, /CACHE_RESULTS\/setup\.json/);
  assert.match(measurement, /CACHE_START_SECONDS \+ 3480/);
  assert.match(measurement, /timeout --signal=TERM --kill-after=120s/);
  assert.doesNotMatch(measurement, /cache-from|cache-to|type=gha|workflow_dispatch|pull_request_target|repository_dispatch/);
  assert.doesNotMatch(measurement, /run:.*\$\{\{.*(body|head|sha)/);
}
test('real workflow freezes ordinary jobs and gates the sole edited-body path with durable evidence', () => {
  wiring(workflow);
  for (const [from, to] of [["github.actor == 'spboyer'", "true"], ['github.run_attempt == 1', 'true'],
    ['timeout-minutes: 65', 'timeout-minutes: 90'], ['lfs: true\n          persist-credentials', 'lfs: false\n          persist-credentials'],
    ['types: [opened, synchronize, reopened, edited]', 'types: [edited]'], ['if: always()', 'if: success()'],
    ['include-hidden-files: false', 'include-hidden-files: true'],
    ['${{ env.CACHE_RESULTS }}/state.json', '${{ env.CACHE_RESULTS }}/**'],
    ['Metadata-only Docker validation not selected', 'Build and Verify Docker Image'],
    ['Metadata-only runner validation not selected', 'Local runner container contract'],
    ['CACHE_START_SECONDS + 3480', 'CACHE_START_SECONDS + 3600'],
    ['    steps:\n      - name: Start aggregate budget', '    env:\n      CACHE_RESULTS: ${{ runner.temp }}/invalid-job-context\n    steps:\n      - name: Start aggregate budget'],
    ['echo "CACHE_RESULTS=$CACHE_RESULTS" >> "$GITHUB_ENV"', 'echo "CACHE_RESULTS=wrong" >> "$GITHUB_ENV"'],
    ['startsWith(toJSON(github.event.changes.body.from), \'"\'', 'startsWith(toJSON(github.event.changes.body.from), \'n\''],
    ['docker run --rm waza:ci-', 'docker run --rm wrong:ci-']]) {
    const changed = workflow.replace(from, to); assert.notEqual(changed, workflow); assert.throws(() => wiring(changed));
  }
  assert.doesNotMatch(source, /--cache-from|--cache-to|type=gha|system.*prune|volume.*prune|--network=host|--allow.*network\.host/);
  assert.match(source, /'--driver', 'remote', `docker-container:\/\/\$\{id\}`/);
  assert.match(source, /insecure-entitlements = \[\]/);
  assert.equal(redact('GET https://feed.example/a.tgz?sig=PRIVATE&token=PRIVATE failed'), 'GET https://feed.example/a.tgz?[query-redacted] failed');
  assert.match(source, /const log = name && path\.join\(this\.privateRoot/);
  assert.doesNotMatch(source, /fs\.(rmSync|unlinkSync)\(/, 'production filesystem descendants are not deleted');
});
function publicEvidencePatterns(text) {
  text = normalizeWorkflow(text);
  const upload = text.slice(text.indexOf('      - name: Retain partial or final evidence\n'));
  const block = /          path: \|\n((?:            .+\n)+)/.exec(upload);
  assert.ok(block, 'explicit artifact paths required');
  return block[1].trim().split('\n').map(line => {
    assert.ok(line.trim().startsWith('${{ env.CACHE_RESULTS }}/'));
    return line.trim().slice('${{ env.CACHE_RESULTS }}/'.length);
  });
}
test('actual upload selectors retain public artifacts but exclude populated contexts and private raw logs', t => {
  const root = temporary(t), files = ['setup.json', 'state.json', 'source-A.json', 'builder-1.log',
    'r1-a-cold.log', 'r1-a-cold.waza', 'r1-a-cold-assets/index.html', 'r1-a-cold-assets/assets/app.js',
    'context-A/r1-a-cold.log', 'context-A-r1/web/package-lock.json', '.private-raw/r1-a-cold.log'];
  for (const file of files) {
    fs.mkdirSync(path.dirname(path.join(root, file)), { recursive: true });
    fs.writeFileSync(path.join(root, file), file);
  }
  const selected = fs.globSync(publicEvidencePatterns(workflow), { cwd: root })
    .filter(file => fs.lstatSync(path.join(root, file)).isFile()).map(file => file.split(path.sep).join('/')).sort();
  assert.deepEqual(selected, files.filter(file => !file.startsWith('context-') && !file.startsWith('.private-raw/')).sort());
});
test('production CLI rejects malformed activation without calling Docker, npm, Go or network', t => {
  const root = temporary(t), eventPath = path.join(root, 'event.json'), envPath = path.join(root, 'github-env');
  fs.writeFileSync(eventPath, JSON.stringify({ ...event(), changes: { title: { from: 'old' } } }));
  const result = spawnSync(process.execPath, [path.join(directory, 'docker_cache_benchmark.mjs')], {
    env: { ...env(), PATH: '', RUNNER_TEMP: root, CACHE_RESULTS: path.join(root, 'result'), GITHUB_EVENT_PATH: eventPath, GITHUB_ENV: envPath },
    encoding: 'utf8', timeout: 5000,
  });
  assert.equal(result.status, 1, result.stderr);
  assert.equal(JSON.parse(fs.readFileSync(path.join(root, 'result/activation-rejected.json'))).error, 'invalid rising-edge activation');
});

function workflowScript(name, text = workflow) {
  text = normalizeWorkflow(text);
  const after = text.slice(text.indexOf(`      - name: ${name}\n`));
  const block = after.slice(after.indexOf('        run: |\n') + '        run: |\n'.length).split('\n');
  const lines = [];
  for (const line of block) { if (line && !line.startsWith('          ')) break; lines.push(line.slice(10)); }
  return lines.join('\n');
}
function retentionSelected(environment, text = workflow) {
  const upload = normalizeWorkflow(text).slice(normalizeWorkflow(text).indexOf('      - name: Retain partial or final evidence\n'));
  const condition = /^        if: (.+)$/m.exec(upload)?.[1];
  assert.ok(condition, 'actual upload predicate required');
  return condition.split(' && ').every(term => {
    if (term === 'always()') return true;
    const variable = /^env\.(CACHE_RESULTS|CACHE_RESULTS_ID) != ''$/.exec(term);
    assert.ok(variable, 'unsupported artifact predicate');
    return (environment[variable[1]] ?? '') !== '';
  });
}
test('artifact retention requires both acquired root fields even after owned partial failure', () => {
  for (const environment of [{}, { CACHE_RESULTS: 'planned' }, { CACHE_RESULTS_ID: '1:2' },
    { CACHE_RESULTS: '', CACHE_RESULTS_ID: '1:2' }, { CACHE_RESULTS: 'owned', CACHE_RESULTS_ID: '' }]) {
    assert.equal(retentionSelected(environment), false);
  }
  assert.equal(retentionSelected({ CACHE_RESULTS: 'owned', CACHE_RESULTS_ID: '1:2' }), true);
  const unconditional = workflow.replace("if: always() && env.CACHE_RESULTS != '' && env.CACHE_RESULTS_ID != ''", 'if: always()');
  assert.notEqual(unconditional, workflow);
  assert.equal(retentionSelected({}, unconditional), true, 'negative control exposes unconditional unowned upload');
  assert.throws(() => wiring(unconditional));
});
test('workflow oracle, artifact selectors and scripts admit CRLF without repairing other bytes', () => {
  const crlf = workflow.replace(/\n/g, '\r\n');
  wiring(crlf);
  assert.deepEqual(publicEvidencePatterns(crlf), publicEvidencePatterns(workflow));
  for (const name of ['Start aggregate budget before checkout', 'Admit activation and run bounded measurement']) {
    assert.equal(workflowScript(name, crlf), workflowScript(name));
  }
  assert.throws(() => wiring(crlf.replace('  docker-build:', '   docker-build:')));
});
test('actual setup/watchdog scripts retain early receipts and failure status with no live commands',
  { skip: process.platform === 'win32' ? 'POSIX shell/executable-shebang controls unsupported on Windows; portable Node controls remain selected' : false }, t => {
  const root = temporary(t), bin = path.join(root, 'bin'), result = path.join(root, 'result');
  fs.mkdirSync(bin);
  const stub = (name, script) => fs.writeFileSync(path.join(bin, name), `#!${process.execPath}\n${script}\n`, { mode: 0o755 });
  stub('date', 'process.stdout.write(process.env.FAKE_NOW + "\\n");');
  stub('mkdir', 'require("node:fs").mkdirSync(process.argv.at(-1), {mode:0o700});');
  stub('stat', 'if(process.env.FAKE_STAT_FAILURE)process.exit(74);const s=require("node:fs").statSync(process.argv.at(-1));process.stdout.write(Object.hasOwn(process.env,"FAKE_ID")?process.env.FAKE_ID:`${s.dev}:${s.ino}\\n`);');
  stub('timeout', 'require("node:fs").appendFileSync(process.env.FAKE_CALLS,JSON.stringify(process.argv.slice(2))+"\\n");process.exit(124);');
  const configuration = { PATH: bin, FAKE_NOW: '2000000000', GITHUB_RUN_ID: '1234', GITHUB_RUN_ATTEMPT: '1',
    GITHUB_ENV: path.join(root, 'github-env'), CACHE_RESULTS: result, FAKE_CALLS: path.join(root, 'calls.jsonl') };
  const acquiredEnvironment = file => Object.fromEntries(fs.readFileSync(file, 'utf8').trim().split('\n')
    .map(line => [line.slice(0, line.indexOf('=')), line.slice(line.indexOf('=') + 1)]));
  const competitor = path.join(root, 'competing-result'), competitorEnv = path.join(root, 'competing-env');
  fs.mkdirSync(competitor);
  fs.writeFileSync(path.join(competitor, 'setup.json'), '{"owner":"competitor"}\n');
  fs.writeFileSync(path.join(competitor, 'state.json'), 'competing-public-evidence\n');
  fs.writeFileSync(competitorEnv, 'SENTINEL=unchanged\n');
  const rejected = spawnSync('/bin/bash', ['-c', workflowScript('Start aggregate budget before checkout')], {
    env: { ...configuration, CACHE_RESULTS: competitor, GITHUB_ENV: competitorEnv }, encoding: 'utf8', timeout: 5000,
  });
  assert.notEqual(rejected.status, 0);
  assert.equal(fs.readFileSync(competitorEnv, 'utf8'), 'SENTINEL=unchanged\n', 'failed exclusive mkdir exports no planned path or identity');
  assert.equal(fs.readFileSync(path.join(competitor, 'setup.json'), 'utf8'), '{"owner":"competitor"}\n');
  assert.equal(fs.readFileSync(path.join(competitor, 'state.json'), 'utf8'), 'competing-public-evidence\n');
  assert.deepEqual(fs.readdirSync(competitor).sort(), ['setup.json', 'state.json']);
  assert.equal(retentionSelected(acquiredEnvironment(competitorEnv)), false, 'competing public files never become upload-eligible');
  for (const [name, failure] of [['empty', { FAKE_ID: '' }], ['malformed', { FAKE_ID: 'not-an-identity' }],
    ['failed-stat', { FAKE_STAT_FAILURE: '1' }]]) {
    const unidentified = path.join(root, name), unidentifiedEnv = path.join(root, `${name}-env`);
    fs.writeFileSync(unidentifiedEnv, 'SENTINEL=unchanged\n');
    const failed = spawnSync('/bin/bash', ['-c', workflowScript('Start aggregate budget before checkout')], {
      env: { ...configuration, ...failure, CACHE_RESULTS: unidentified, GITHUB_ENV: unidentifiedEnv }, encoding: 'utf8', timeout: 5000,
    });
    assert.notEqual(failed.status, 0);
    assert.ok(failed.stderr.includes(`Result directory identity unavailable: ${unidentified}`));
    assert.equal(fs.readFileSync(unidentifiedEnv, 'utf8'), 'SENTINEL=unchanged\n');
    assert.equal(retentionSelected(acquiredEnvironment(unidentifiedEnv)), false);
  }
  const setup = spawnSync('/bin/bash', ['-c', workflowScript('Start aggregate budget before checkout')], { env: configuration, encoding: 'utf8', timeout: 5000 });
  assert.equal(setup.status, 0, setup.stderr);
  assert.deepEqual(JSON.parse(fs.readFileSync(path.join(result, 'setup.json'))), { runId: '1234', attempt: '1', startedSeconds: 2000000000 });
  assert.match(fs.readFileSync(configuration.GITHUB_ENV, 'utf8'), /CACHE_RESULTS_ID=\d+:\d+/);
  assert.ok(fs.readFileSync(configuration.GITHUB_ENV, 'utf8').split('\n').includes(`CACHE_RESULTS=${result}`),
    'first-step result path reaches subsequent steps');
  assert.equal(retentionSelected(acquiredEnvironment(configuration.GITHUB_ENV)), true);
  const partial = path.join(root, 'owned-partial'), partialEnv = path.join(root, 'partial-env');
  fs.writeFileSync(partialEnv, 'SENTINEL=unchanged\n');
  const partialSetup = spawnSync('/bin/bash', ['-c', 'printf() { return 73; }\n' + workflowScript('Start aggregate budget before checkout')], {
    env: { ...configuration, CACHE_RESULTS: partial, GITHUB_ENV: partialEnv }, encoding: 'utf8', timeout: 5000,
  });
  assert.equal(partialSetup.status, 73, partialSetup.stderr);
  const partialValues = acquiredEnvironment(partialEnv), partialIdentity = fs.statSync(partial);
  assert.equal(partialValues.CACHE_RESULTS, partial);
  assert.equal(partialValues.CACHE_RESULTS_ID, `${partialIdentity.dev}:${partialIdentity.ino}`);
  assert.equal(partialValues.CACHE_START_SECONDS, '2000000000');
  assert.equal(retentionSelected(partialValues), true, 'owned partial failure retains upload eligibility');
  const before = fs.readFileSync(path.join(result, 'setup.json'), 'utf8');
  const collision = spawnSync('/bin/bash', ['-c', workflowScript('Start aggregate budget before checkout')], { env: configuration, encoding: 'utf8', timeout: 5000 });
  assert.notEqual(collision.status, 0); assert.equal(fs.readFileSync(path.join(result, 'setup.json'), 'utf8'), before);
  const script = workflowScript('Admit activation and run bounded measurement');
  const timed = spawnSync('/bin/bash', ['-c', script], { env: { ...configuration, CACHE_START_SECONDS: '2000000000', FAKE_NOW: '2000000010' }, encoding: 'utf8', timeout: 5000 });
  assert.equal(timed.status, 124, timed.stderr);
  const calls = fs.readFileSync(configuration.FAKE_CALLS, 'utf8').trim().split('\n').map(JSON.parse);
  assert.deepEqual(calls, [['--signal=TERM', '--kill-after=120s', '3470s', 'node', '.github/scripts/docker_cache_benchmark.mjs']]);
  const exhausted = spawnSync('/bin/bash', ['-c', script], { env: { ...configuration, CACHE_START_SECONDS: '2000000000', FAKE_NOW: '2000003480' }, encoding: 'utf8', timeout: 5000 });
  assert.notEqual(exhausted.status, 0);
  assert.equal(fs.readFileSync(configuration.FAKE_CALLS, 'utf8').trim().split('\n').length, 1);
});

function fixtureClient(run) {
  return (tool, args, options) => {
    const child = fakeChild();
    queueMicrotask(() => {
      try {
        const r = run(tool, args, options);
        if (r.stdout) child.stdout.emit('data', Buffer.from(r.stdout));
        if (r.stderr) child.stderr.emit('data', Buffer.from(r.stderr));
        if (r.error) child.emit('error', r.error);
        child.emit('close', r.status, null);
      } catch (e) { child.emit('error', e); child.emit('close', -2); }
    });
    return child;
  };
}
let fakePID = 1000;
function fakeChild() {
  const child = new EventEmitter();
  child.stdout = new EventEmitter(); child.stderr = new EventEmitter();
  child.signals = []; child.closed = false; child.pid = ++fakePID;
  child.once('close', () => { child.closed = true; });
  child.kill = signal => {
    child.signals.push(signal);
    if (signal === 'SIGKILL') queueMicrotask(() => child.emit('close', null, signal));
    return true;
  };
  return child;
}
async function flush() { for (let i = 0; i < 256; i++) await Promise.resolve(); }
function sampleFixture(t) {
  const root = temporary(t);
  const m = new Measurement(request(env(), event()), root, root, () => { throw new Error('blocking sampler command forbidden'); });
  const builder = { name: 'owned-builder', container: 'owned-container', id: '3'.repeat(64), volume: '4'.repeat(64),
    creationConfirmed: true, metadataCreated: true, index: 1,
    pulledImage: { ref: 'pinned-buildkit', id: `sha256:${'2'.repeat(64)}`, size: 1024, platform: 'linux/amd64' } };
  m.state.builders.push(builder);
  m.state.host = { dockerRoot: '/var/lib/docker' }; m.state.initialImages = [];
  m.state.pins['moby/buildkit:buildx-stable-1'] = 'pinned-buildkit';
  const container = daemonInspect(m, builder.id, builder.volume);
  container.State.Paused = true;
  const calls = [];
  const output = (tool, args) => {
    calls.push([tool, ...args]);
    if (tool === 'df') return 'Filesystem 1024-blocks Used Available Capacity Mounted\nhost 999999999 1 999999998 1% /\ndocker 999999999 1 999999998 1% /docker\n';
    if (tool === 'du') return '1\tcontext\n1\tprivate\n';
    if (tool === 'uptime') return 'synthetic shared load\n';
    if (args[0] === 'inspect') return JSON.stringify([container]);
    if (args[0] === 'volume') return JSON.stringify([{ Name: builder.volume, Driver: 'local', Scope: 'local', CreatedAt: 'authored', Options: {} }]);
    if (args[0] === 'image') return JSON.stringify([{ Id: container.Image, Size: 1024 }]);
    assert.equal(args[0], 'exec');
    return '200000 100000\n8589934592\n0\n1024\n2048\n1\t/var/lib/buildkit\n';
  };
  m.spawn = (tool, args) => {
    const child = fakeChild();
    queueMicrotask(() => { child.stdout.emit('data', Buffer.from(output(tool, args))); child.emit('close', 0, null); });
    return child;
  };
  return { m, builder, calls, container };
}
test('active and paused resource samples use actual asynchronous probe and ownership paths', async t => {
  const { m, builder, calls, container } = sampleFixture(t);
  await m.sample(builder);
  assert.equal(builder.ownershipAcquired, true); assert.equal(builder.id, container.Id);
  assert.ok(calls.some(c => c[0] === 'docker' && c[1] === 'volume'));
  await m.samplePaused(builder);
  const records = fs.readFileSync(path.join(m.root, 'resources.jsonl'), 'utf8').trim().split('\n').map(JSON.parse);
  assert.equal(records.length, 2);
  assert.equal(records[0].memoryPeak, 2048); assert.ok(records[0].owned > 0);
  assert.match(records[1].scope, /paused owned daemon/);
  container.HostConfig.CpuQuota = 400000;
  await assert.rejects(() => m.sample(builder), /resource\/storage admission failed/);
});
function failExec(m, failures) {
  const spawn = m.spawn; let execs = 0;
  m.spawn = (tool, args) => {
    if (tool !== 'docker' || args[0] !== 'exec' || execs++ >= failures.length) return spawn(tool, args);
    const child = fakeChild(), { stderr, code = 1 } = failures[execs - 1];
    queueMicrotask(() => { child.stderr.emit('data', Buffer.from(stderr)); child.emit('close', code, null); });
    return child;
  };
  return () => execs;
}
test('cache-size sampling retries only du paths that BuildKit removed mid-walk', async t => {
  const vanished = { stderr: 'du: /var/lib/buildkit/runc-overlayfs/snapshots/snapshots/35: No such file or directory\n' };
  let { m, builder } = sampleFixture(t);
  let execs = failExec(m, [vanished, vanished]);
  await m.sample(builder);
  assert.equal(execs(), 3);
  assert.deepEqual(builder.cgroupObservations.filter(o => o.vanishedPathRetry).map(o => o.vanishedPathRetry), [1, 2]);
  assert.equal(fs.readFileSync(path.join(m.root, 'resources.jsonl'), 'utf8').trim().split('\n').length, 1);
  ({ m, builder } = sampleFixture(t));
  execs = failExec(m, [vanished, vanished, vanished]);
  await assert.rejects(() => m.sample(builder), /No such file or directory/);
  assert.equal(execs(), 3, 'three vanished-path failures stop the run');
  for (const failure of [
    { stderr: 'du: /var/lib/buildkit/x: Permission denied\n' },
    { stderr: 'du: /var/lib/buildkit/a: No such file or directory\ndu: /var/lib/buildkit/b: Input/output error\n' },
    { stderr: 'du: /etc/x: No such file or directory\n' },
    { stderr: 'du: /var/lib/buildkit/a: No such file or directory\n', code: 2 },
    { stderr: '' }]) {
    ({ m, builder } = sampleFixture(t));
    execs = failExec(m, [failure]);
    await assert.rejects(() => m.sample(builder), /failed/);
    assert.equal(execs(), 1, `real accounting failure is not retried: ${JSON.stringify(failure)}`);
    assert.equal(fs.existsSync(path.join(m.root, 'resources.jsonl')), false);
  }
});
test('binary and asset capture sampling stays active across intervals and joins before return', async t => {
  const { m, builder } = sampleFixture(t);
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
  const observed = [];
  m.sample = async (_builder, signal) => { assert.equal(signal.aborted, false); observed.push('active'); };
  m.samplePaused = async (_builder, signal) => { assert.equal(signal.aborted, false); observed.push('paused'); };
  for (const [paused, expected] of [[true, 'paused'], [false, 'active']]) {
    let finish;
    const pending = m.sampledCapture(builder, paused, () => new Promise(resolve => { finish = resolve; }));
    t.mock.timers.tick(5000); await flush();
    t.mock.timers.tick(5000); await flush();
    finish(expected); await flush();
    assert.equal(await pending, expected);
    assert.deepEqual(observed.splice(0), [expected, expected]);
    assert.equal(m.activeSamplers.size, 0);
  }
  let finish, aborted = false;
  m.sample = (_builder, signal) => new Promise(resolve => {
    signal.addEventListener('abort', () => { aborted = true; resolve(); }, { once: true });
  });
  const blocked = m.sampledCapture(builder, false, () => new Promise(resolve => { finish = resolve; }));
  for (let i = 0; i < 2 + maxSkippedSamplerTicks; i++) { t.mock.timers.tick(5000); await flush(); }
  assert.equal(aborted, true, 'overrun beyond the skipped-tick bound stops the capture sampler');
  finish(); await flush();
  await assert.rejects(blocked, /capture resource sampler exceeded five-second cadence/);
  assert.equal(aborted, true);
  assert.equal(m.activeSamplers.size, 0);
});
test('a hung resource probe cannot delay the production command deadline or force-kill grace', async t => {
  const { m, builder } = sampleFixture(t);
  const now = Date.now();
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
  m.config.deadline = now + 120000 + 5500;
  const build = fakeChild(), probe = fakeChild();
  m.spawn = (tool, args) => args[0] === 'buildx' ? build : probe;
  const pending = m.logged(['buildx', 'build'], 'hung-probe-build', signal => m.sample(builder, signal));
  t.mock.timers.tick(5000); await flush();
  t.mock.timers.tick(500); await flush();
  assert.deepEqual(build.signals, ['SIGTERM']); assert.deepEqual(probe.signals, ['SIGKILL']);
  t.mock.timers.tick(4999); assert.deepEqual(build.signals, ['SIGTERM']);
  t.mock.timers.tick(1); await flush();
  const result = await pending;
  assert.deepEqual(build.signals, ['SIGTERM', 'SIGKILL']);
  assert.equal(result.reason, 'command deadline'); assert.equal(result.timeoutSeconds, 5.5);
});
test('production six-minute ceiling and slow-sampler cadence remain fail closed', async t => {
  const { m, builder } = sampleFixture(t);
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
  let build = fakeChild(), probe = fakeChild();
  m.spawn = (tool, args) => args[0] === 'buildx' ? build : probe;
  const ceiling = m.logged(['buildx', 'build'], 'six-minute-build');
  t.mock.timers.tick(360000); await flush();
  assert.deepEqual(build.signals, ['SIGTERM']);
  t.mock.timers.tick(5000); await flush();
  assert.equal((await ceiling).timeoutSeconds, 360);
  build = fakeChild(); probe = fakeChild();
  const slow = m.logged(['buildx', 'build'], 'slow-sampler-build', signal => m.sample(builder, signal));
  for (let i = 0; i < maxSkippedSamplerTicks; i++) { t.mock.timers.tick(5000); await flush(); }
  assert.deepEqual(build.signals, [], 'a briefly overlapping sample is skipped, not fatal');
  t.mock.timers.tick(5000); await flush();
  t.mock.timers.tick(5000); await flush();
  assert.equal(build.signals[0], 'SIGTERM'); assert.ok(probe.signals.includes('SIGKILL'));
  t.mock.timers.tick(5000); await flush();
  assert.match((await slow).reason, /sampler exceeded five-second cadence|resource probe deadline/);
});
test('completion elapsed is captured before joining cancelled sampling', async t => {
  const { m, builder } = sampleFixture(t);
  const now = Date.now();
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now });
  t.mock.method(performance, 'now', () => Date.now());
  const build = fakeChild(), probe = fakeChild();
  probe.kill = signal => { probe.signals.push(signal); return true; };
  m.spawn = (tool, args) => args[0] === 'buildx' ? build : probe;
  let completed = false;
  const pending = m.logged(['buildx', 'build'], 'completed-build', signal => m.sample(builder, signal)).then(r => { completed = true; return r; });
  t.mock.timers.tick(5000); await flush();
  t.mock.timers.tick(300); build.emit('close', 0, null); await flush();
  assert.equal(completed, false); assert.deepEqual(probe.signals, ['SIGKILL']);
  t.mock.timers.tick(4800); probe.emit('close', null, 'SIGKILL'); await flush();
  const result = await pending;
  assert.equal(result.wallSeconds, 5.3); assert.equal(result.reason, undefined);
  assert.equal(m.stopped, undefined, 'completed client disables cadence ticks before joining its cancelled sample');
});
test('resource probes propagate timeout, cancellation, process and output failures', async t => {
  const { m } = sampleFixture(t);
  t.mock.timers.enable({ apis: ['Date', 'setTimeout', 'setInterval'], now: Date.now() });
  const controller = new AbortController(); controller.abort();
  let calls = 0, child;
  m.spawn = () => { calls++; child = fakeChild(); return child; };
  await assert.rejects(() => m.probe('docker', ['inspect'], controller.signal));
  assert.equal(calls, 0);
  const timeout = assert.rejects(m.probe('docker', ['inspect']), /resource probe deadline/);
  t.mock.timers.tick(15000); await flush(); await timeout;
  for (const [trigger, expected] of [
    [c => { c.stderr.emit('data', Buffer.from('probe failed')); c.emit('close', 1); }, /probe failed/],
    [c => { c.emit('error', new Error('spawn failed')); c.emit('close', -2); }, /spawn failed/],
    [c => c.stdout.emit('data', { length: 16 * 1024 ** 2 + 1 }), /output limit/],
  ]) {
    const rejected = assert.rejects(m.probe('docker', ['inspect']), expected);
    trigger(child); await flush(); await rejected;
  }
});
test('runtime deadline remains failure even when the stopped container exits zero', async t => {
  const root = temporary(t), calls = [];
  const m = new Measurement(request(env(), event()), root, root, fixtureClient((tool, args) => {
    calls.push([tool, ...args]);
    assert.equal(tool, 'docker', 'no build-info or other native command after timeout');
    if (args[0] === 'create') return { status: 0, stdout: 'runtime-id\n' };
    if (args[0] === 'inspect') return { status: 0, stdout: JSON.stringify([{ Id: 'runtime-id',
      HostConfig: { CpuQuota: 200000, CpuPeriod: 100000, Memory: 536870912, MemorySwap: 536870912,
        NetworkMode: 'none', ReadonlyRootfs: true } }]) };
    return { status: 0, stdout: '' };
  }));
  m.sample = async builder => { assert.equal(builder.id, 'owned-builder-id'); };
  m.logged = async () => ({ code: 0, reason: 'command deadline' });
  await assert.rejects(() => m.artifact({ id: 'owned-builder-id' }, { id: 'owned-image-id' }, 'r1-a-cold'),
    /loaded artifact validation failed/);
  assert.deepEqual(calls.at(-1), ['docker', 'unpause', 'owned-builder-id']);
  assert.equal(calls.some(c => c[1] === 'cp'), false);
});

function windowsOfflineJob(text) {
  text = normalizeWorkflow(text);
  const start = text.indexOf('\n  windows-offline-contract:\n');
  assert.ok(start >= 0, 'native Windows offline-contract job required');
  assert.ok(start > text.indexOf('\n  cache-measurement:\n'), 'append after measurement without changing its boundary');
  const job = text.slice(start + 1).split(/\n  [\w-]+:\n/)[0];
  const condition = /^    if: (.+)$/m.exec(job)?.[1];
  assert.equal(condition, "github.event_name == 'pull_request' && github.event.action != 'edited'");
  assert.ok(job.includes("name: ${{ github.event_name == 'pull_request' && github.event.action != 'edited' && 'Windows offline container cache controls' || 'Windows offline cache controls not selected' }}"),
    'skipped push/edited jobs must not reuse the executing proof name');
  assert.match(job, /^    runs-on: windows-latest$/m);
  assert.match(job, /^    timeout-minutes: 10$/m);
  assert.match(job, /^    permissions:\n      contents: read\n/m);
  assert.match(job, /^      NO_COLOR: '1'$/m);
  const steps = job.slice(job.indexOf('\n    steps:\n'));
  assert.deepEqual([...steps.matchAll(/^        uses: (.+)$/gm)].map(m => m[1]), [
    'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1',
    'actions/setup-node@949feb2413d6458794dcd2491c4babbbce0c15c1 # v7.1.0',
  ]);
  assert.match(steps, /^          lfs: false$/m);
  assert.match(steps, /^          persist-credentials: false$/m);
  assert.match(steps, /^          node-version: '24'$/m);
  assert.deepEqual([...steps.matchAll(/^        run: (.+)$/gm)].map(m => m[1]),
    ['node --test .github/scripts/docker_cache_benchmark.test.mjs']);
  assert.doesNotMatch(steps, /^\s+if:|continue-on-error|working-directory:|shell:/m);
  assert.doesNotMatch(job, /needs:|actions: write|contents: write|lfs: true|npm|workflow_dispatch/i);
  const parsed = /^github.event_name == '([^']+)' && github.event.action != '([^']+)'$/.exec(condition);
  return (eventName, action) => eventName === parsed[1] && action !== parsed[2];
}
function dockerSourceTriggers(text) {
  // Decode the admitted literal lists/anchor profile, not general YAML or Actions globs.
  const header = normalizeWorkflow(text).split('\njobs:\n')[0];
  const push = header.split('\n  push:\n')[1]?.split('\n  pull_request:\n')[0];
  const pullRequest = header.split('\n  pull_request:\n')[1]?.split('\npermissions:\n')[0];
  assert.ok(push && pullRequest, 'both actual Docker event declarations required');
  const anchored = /^    paths: &docker-paths\n((?:      - '(?:[^'\n]|'')+'\n)+)/m.exec(`${push}\n`);
  assert.ok(anchored, 'literal Docker paths anchor required');
  assert.equal(anchored.index + anchored[0].length, push.length + 1, 'all anchored paths must be decoded');
  const paths = [...anchored[1].matchAll(/^      - '((?:[^']|'')+)'$/gm)].map(match => match[1].replaceAll("''", "'"));
  assert.match(pullRequest, /^    paths: \*docker-paths$/m, 'PR resolves the actual shared path anchor');
  const list = (block, key) => {
    const match = new RegExp(`^    ${key}: \\[([a-z_, -]+)\\]$`, 'm').exec(block);
    assert.ok(match, `literal ${key} list required`);
    return match[1].split(',').map(value => value.trim());
  };
  return { push: { paths, branches: list(push, 'branches') },
    pull_request: { paths, branches: list(pullRequest, 'branches'), types: list(pullRequest, 'types') } };
}
function windowsOwnInputSelected(text, eventName, action, branch, changedPaths) {
  const triggers = dockerSourceTriggers(text)[eventName];
  return Boolean(triggers && triggers.branches.includes(branch) &&
    (eventName !== 'pull_request' || triggers.types.includes(action)) &&
    changedPaths.some(changed => triggers.paths.includes(changed)) &&
    windowsOfflineJob(text)(eventName, action));
}
test('one-leaf benchmark and LFS admission inputs resolve shared source triggers and select native Windows', async t => {
  for (const input of ['.github/scripts/docker_cache_benchmark.mjs', '.github/scripts/docker_cache_benchmark.test.mjs', '.gitattributes']) {
    await t.test(input, () => {
      for (const text of [workflow, workflow.replace(/\r?\n/g, '\r\n')]) {
        for (const action of ['opened', 'synchronize', 'reopened']) {
          assert.equal(windowsOwnInputSelected(text, 'pull_request', action, 'main', [input]), true, `${input}: ${action}`);
        }
      }
      const removed = workflow.replace(`      - '${input}'\n`, '');
      assert.notEqual(removed, workflow, 'removed-own-input negative must change actual workflow');
      assert.equal(windowsOwnInputSelected(removed, 'pull_request', 'synchronize', 'main', [input]), false);
      assert.equal(windowsOwnInputSelected(workflow, 'pull_request', 'synchronize', 'main', [`${input}.extra`]), false);
      assert.equal(windowsOwnInputSelected(workflow, 'pull_request', 'edited', 'main', [input]), false);
      assert.equal(windowsOwnInputSelected(workflow, 'pull_request', 'opened', 'dev', [input]), false);
      assert.equal(windowsOwnInputSelected(workflow, 'push', undefined, 'main', [input]), false);
    });
  }
  assert.throws(() => dockerSourceTriggers(workflow.replace('paths: *docker-paths', 'paths: *unrelated')));
});
test('actual Site runner callbacks admit CRLF and exercise all rejected mutations', async t => {
  const source = fs.readFileSync(path.join(directory, '../../site/tests/documentation.test.mjs'), 'utf8');
  const helperStart = source.indexOf('const runnerCommands = [');
  const helperEnd = source.indexOf("\ntest('local container conformance");
  const callbacksStart = source.indexOf("test('ordinary runner guard admits");
  const callbacksEnd = source.indexOf("\ntest('manual runner publication");
  assert.ok(helperStart >= 0 && helperEnd > helperStart && callbacksStart > helperEnd && callbacksEnd > callbacksStart);
  const callbacks = [];
  let rejectedMutations = 0;
  const observedAssert = new Proxy(assert, { get(target, property) {
    if (property === 'throws') return (...args) => { rejectedMutations++; return target.throws(...args); };
    return Reflect.get(target, property);
  } });
  runInNewContext(`${source.slice(helperStart, helperEnd)}\n${source.slice(callbacksStart, callbacksEnd)}`, {
    assert: observedAssert,
    read: filename => {
      assert.equal(filename, '.github/workflows/docker-ci.yml');
      return workflow.replace(/\r?\n/g, '\r\n');
    },
    test: (name, callback) => callbacks.push({ name, callback }),
  });
  assert.equal(callbacks.length, 2, 'both actual introduced Site callbacks must execute');
  for (const { name, callback } of callbacks) {
    await t.test(name, () => {
      const before = rejectedMutations;
      callback();
      assert.equal(rejectedMutations - before, name.includes('rejects') ? 11 : 0);
    });
  }
});
test('native Windows job directly selects all offline controls only on ordinary PR events', () => {
  const selected = windowsOfflineJob(workflow);
  for (const action of ['opened', 'synchronize', 'reopened']) assert.equal(selected('pull_request', action), true, action);
  for (const [eventName, action] of [['pull_request', 'edited'], ['push', undefined], ['workflow_dispatch', undefined]]) {
    assert.equal(selected(eventName, action), false, eventName);
  }
  windowsOfflineJob(workflow.replace(/\n/g, '\r\n'));
  for (const [from, to] of [
    ['runs-on: windows-latest', 'runs-on: ubuntu-latest'],
    ['timeout-minutes: 10', 'timeout-minutes: 65'],
    ["github.event_name == 'pull_request' && github.event.action != 'edited'", "github.event_name == 'push'"],
    ['Windows offline cache controls not selected', 'Windows offline container cache controls'],
    ['lfs: false', 'lfs: true'],
    ['run: node --test .github/scripts/docker_cache_benchmark.test.mjs', 'run: npm test'],
    ['run: node --test .github/scripts/docker_cache_benchmark.test.mjs', 'if: false\n        run: node --test .github/scripts/docker_cache_benchmark.test.mjs'],
  ]) {
    const changed = workflow.replace(from, to);
    assert.notEqual(changed, workflow); assert.throws(() => windowsOfflineJob(changed));
  }
  const tests = fs.readFileSync(fileURLToPath(import.meta.url), 'utf8');
  for (const text of [tests, tests.replace(/\r?\n/g, '\r\n')]) {
    const skips = [...text.matchAll(/^  \{ skip: (.+) }, t => \{\r?$/gm)];
    assert.equal(skips.length, 1);
    assert.match(skips[0][1], /^process\.platform === 'win32' \? 'POSIX shell\/executable-shebang controls unsupported on Windows; portable Node controls remain selected' : false$/);
  }
  assert.doesNotMatch(tests, /^\s*(test\.skip|test\.todo|t\.skip)\(/m);
  const bridge = fs.readFileSync(path.join(directory, 'docker_ci_test.go'), 'utf8')
    .split('func TestDockerCacheMeasurementOfflineControls(t *testing.T) {')[1].split('\nfunc ')[0];
  assert.ok(bridge.includes('exec.Command("node", "--test", "docker_cache_benchmark.test.mjs")'));
  assert.doesNotMatch(bridge, /runtime\.GOOS|\.Skip(f)?\(/);
});
