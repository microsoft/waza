import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { bindMountArgs, runNamedContainer } from './native-lifecycle.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const repository = 'ghcr.io/microsoft/waza-runner-reference';
const fixture = JSON.parse(readFileSync(join(root, 'examples/runner/unit.json'), 'utf8'));
const copilotFixture = join(root, 'examples/runner/copilot-unit.json');
const sourceRevision = process.env.WAZA_IMAGE_SOURCE_REVISION ?? '';
const imageDigest = process.env.WAZA_IMAGE_DIGEST ?? '';
const imageReference = `${repository}@${imageDigest}`;
let dockerEnv = process.env;

function command(args, options = {}) {
  const result = spawnSync('docker', args, {
    cwd: root, encoding: 'utf8', timeout: 1_500_000, maxBuffer: 8 * 1024 * 1024,
    env: dockerEnv, ...options,
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null, result.stderr);
  return result;
}

function buildProbe(packagePath, output) {
  const result = spawnSync('go', ['build', '-trimpath', '-buildvcs=false', '-o', output, packagePath], {
    cwd: root, encoding: 'utf8', timeout: 300_000, maxBuffer: 8 * 1024 * 1024,
    env: { ...process.env, CGO_ENABLED: '0', GOOS: 'linux', GOARCH: 'amd64' },
  });
  assert.ifError(result.error);
  assert.equal(result.status, 0, result.stdout + result.stderr);
  chmodSync(output, 0o755);
}

function platformName(value) {
  return ({ aarch64: 'arm64', x86_64: 'amd64' })[value] ?? value;
}

function assertRuntimeContainer(container, mounts) {
  assert.equal(container.Config.Image, imageReference);
  assert.equal(container.Config.User, '65532:65532');
  assert.equal(container.HostConfig.NetworkMode, 'none');
  assert.equal(container.HostConfig.ReadonlyRootfs, true);
  assert.deepEqual(container.HostConfig.CapDrop, ['ALL']);
  assert.ok(container.HostConfig.SecurityOpt.includes('no-new-privileges'));
  assert.equal(container.State.Running, false);
  assert.equal(container.Mounts.length, mounts.length);
  for (const [destination, source, writable] of mounts) {
    const mount = container.Mounts.find(item => item.Destination === destination);
    assert.ok(mount, `missing mount ${destination}`);
    assert.equal(mount.Source, source);
    assert.equal(mount.RW, writable);
  }
}

test('public immutable runner digest supports mock and bundled native execution', { timeout: 1_800_000 }, () => {
  assert.match(sourceRevision, /^[a-f0-9]{40}$/, 'source revision must be a full lowercase commit SHA');
  assert.match(imageDigest, /^sha256:[a-f0-9]{64}$/, 'image must be identified by an immutable SHA-256 digest');
  assert.equal(process.platform, 'linux');
  assert.equal(process.arch, 'x64', 'published reference claims Linux AMD64 only');

  const work = mkdtempSync(join(tmpdir(), 'waza-published-reference-'));
  const dockerConfig = join(work, 'anonymous-docker-config');
  mkdirSync(dockerConfig, { mode: 0o700 });
  dockerEnv = { ...process.env, DOCKER_CONFIG: dockerConfig };
  for (const key of ['ACTIONS_RUNTIME_TOKEN', 'DOCKER_AUTH_CONFIG', 'GHCR_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN']) {
    delete dockerEnv[key];
  }

  try {
    const daemon = command(['info', '--format', '{{.OSType}}/{{.Architecture}}']);
    assert.equal(daemon.status, 0, daemon.stderr);
    const [os, rawArchitecture] = daemon.stdout.trim().split('/');
    assert.equal(os, 'linux');
    assert.equal(platformName(rawArchitecture), 'amd64',
      'verification requires a native Linux AMD64 Docker daemon');

    const pullStarted = Date.now();
    const pulled = command(['pull', '--platform=linux/amd64', imageReference]);
    assert.equal(pulled.status, 0,
      `anonymous pull failed; package may not be public: ${pulled.stderr}`);
    assert.equal(readdirSync(dockerConfig).length, 0,
      'anonymous pull must not create Docker credentials');
    console.log(`anonymous pull completed in ${Date.now() - pullStarted} ms\n${pulled.stdout.trim()}`);

    const inspected = command(['image', 'inspect', imageReference]);
    assert.equal(inspected.status, 0, inspected.stderr);
    const image = JSON.parse(inspected.stdout)[0];
    assert.ok(image.RepoDigests.includes(imageReference),
      'pulled image must retain the exact immutable registry reference');
    assert.equal(image.Os, 'linux');
    assert.equal(image.Architecture, 'amd64');
    assert.equal(image.Config.Labels?.['org.opencontainers.image.source'], 'https://github.com/microsoft/waza');
    assert.equal(image.Config.Labels?.['org.opencontainers.image.revision'], sourceRevision);
    assert.equal(image.Config.User, '65532:65532');
    assert.deepEqual(image.Config.Entrypoint, ['/waza']);

    const manifest = command(['buildx', 'imagetools', 'inspect', '--raw', imageReference]);
    assert.equal(manifest.status, 0, manifest.stderr);
    const manifestJSON = JSON.parse(manifest.stdout);
    if (Array.isArray(manifestJSON.manifests)) {
      const platforms = [];
      for (const item of manifestJSON.manifests) {
        const os = item.platform?.os;
        const architecture = item.platform?.architecture;
        if (os === 'unknown' && architecture === 'unknown' &&
            item.annotations?.['vnd.docker.reference.type'] === 'attestation-manifest') {
          continue;
        }
        platforms.push(`${os}/${architecture}`);
      }
      assert.deepEqual(platforms, ['linux/amd64'],
        'registry manifest must contain only the claimed runtime platform');
    }

    const mockName = `waza-published-mock-${process.pid}`;
    const mock = runNamedContainer(command, mockName, [
      'run', '--name', mockName, '--pull=never', '--network=none', '--read-only',
      '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=64',
      '--memory=256m', '--cpus=1',
      '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=32m,mode=1777',
      '--tmpfs', '/home/waza/.copilot:rw,exec,nosuid,nodev,size=1g,uid=65532,gid=65532,mode=700',
      '-i', imageReference, 'runner', 'run',
    ], { input: `${JSON.stringify(fixture)}\n`, env: dockerEnv }, (result, container) => {
      assertRuntimeContainer(container, []);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.equal(result.stderr, '');
      const terminal = JSON.parse(result.stdout);
      assert.equal(terminal.kind, 'waza.runner-terminal');
      assert.equal(terminal.outcome, 'success');
      assert.equal(terminal.engine_calls, 1);
      assert.equal(terminal.observation.execution_mode, 'mock');
      assert.deepEqual(terminal.selection, fixture.selection);
      assert.deepEqual(terminal.observation.selection, fixture.selection);
      return terminal;
    });
    assert.equal(mock.outcome, 'success');

    const harness = join(work, 'native-bundle-probe');
    const positive = join(work, 'sdk-positive');
    buildProbe('./.github/scripts/testdata/native-bundle-probe/harness', harness);
    buildProbe('./.github/scripts/testdata/native-bundle-probe/positive', positive);
    const evidence = join(work, 'evidence');
    mkdirSync(evidence, { mode: 0o777 });
    chmodSync(evidence, 0o777);
    const nativeName = `waza-published-native-${process.pid}`;
    const helperMounts = [
      ['/native-bundle-probe', harness, false],
      ['/sdk-positive', positive, false],
      ['/input/copilot-unit.json', copilotFixture, false],
      ['/evidence', evidence, true],
    ];
    const native = runNamedContainer(command, nativeName, [
      'run', '--name', nativeName, '--pull=never', '--network=none', '--read-only',
      '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=128',
      '--memory=2g', '--cpus=1',
      '--tmpfs', '/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777',
      ...bindMountArgs(helperMounts),
      '--entrypoint', '/native-bundle-probe', '-i', imageReference,
    ], { env: dockerEnv }, (result, container) => {
      assertRuntimeContainer(container, helperMounts);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.match(result.stdout, /"cases":6,"event":"all_contract_assertions_passed"/);
      return result.stdout;
    });
    assert.match(native, /"event":"network_none_control"/);

    const files = readdirSync(evidence).sort();
    assert.deepEqual(files, ['provider-requests.json', 'reports.json']);
    for (const file of files) {
      const info = statSync(join(evidence, file));
      assert.equal(info.mode & 0o777, 0o644, `${file} must be readable but not writable across UIDs`);
      assert.doesNotThrow(() => readFileSync(join(evidence, file), 'utf8'));
      assert.notEqual(info.uid, process.getuid(),
        `${file} must be written by the image's non-root container user`);
    }
    const reports = JSON.parse(readFileSync(join(evidence, 'reports.json'), 'utf8'));
    const requests = JSON.parse(readFileSync(join(evidence, 'provider-requests.json'), 'utf8'));
    assert.equal(reports.length, 6);
    assert.equal(requests.length, 6);
    assert.deepEqual(reports.map(report => report.name).sort(), [
      'hang', 'hang_stopped', 'reply', 'reply-after-positive', 'sdk-positive', 'sigterm',
    ]);
    const expected = new Map([
      ['reply', [0, 'success']],
      ['hang', [4, 'timeout']],
      ['hang_stopped', [4, 'timeout']],
      ['sigterm', [3, 'cancelled']],
      ['sdk-positive', [0, undefined]],
      ['reply-after-positive', [0, 'success']],
    ]);
    for (const report of reports) {
      const [code, outcome] = expected.get(report.name) ?? [];
      assert.notEqual(code, undefined, `unexpected case ${report.name}`);
      assert.equal(report.code, code, report.stderr);
      assert.equal(report.provider_requests, 1);
      assert.deepEqual(report.processes_after_exit, {});
      assert.equal(report.externally_killed, false);
      assert.ok(report.elapsed_ms < (report.name === 'reply' ? 20_000 : 8000),
        `${report.name} exceeded the bounded deadline`);
      if (outcome) {
        const terminal = JSON.parse(report.stdout);
        assert.equal(terminal.outcome, outcome);
        assert.equal(terminal.exit_code, code);
        assert.equal(terminal.engine_calls, 1);
      }
      assert.equal(report.hook_sentinel,
        report.name === 'sdk-positive' || report.name === 'reply-after-positive'
          ? 'sessionStart\nuserPromptSubmitted\n' : '');
      console.log(`native ${report.name}: expected exit ${code}${outcome ? ` (${outcome})` : ''}`);
    }
    const positiveRequest = requests.find(request => request.mode === 'sdk-positive');
    assert.ok(positiveRequest, 'the native SDK positive control must reach the synthetic provider');
    assert.equal(requests.filter(request => request.mode === 'sdk-positive').length, 1);
    for (const request of requests) {
      const body = JSON.parse(request.body);
      assert.equal(body.model, 'offline-model');
      assert.equal(request.path, '/v1/chat/completions');
      assert.equal(request.mode === 'sdk-positive',
        request.body.includes('OFFLINE_HOME_INSTRUCTION_SENTINEL') &&
        request.body.includes('OFFLINE_ANCESTOR_INSTRUCTION_SENTINEL') &&
        request.body.includes('OFFLINE_ANCESTOR_AGENT_SENTINEL'));
      assert.ok(!request.body.includes('offline-synthetic-not-a-credential'));
    }

    console.log(`Verified anonymous ${imageReference}; source ${sourceRevision}; linux/amd64; mock exact-unit and bundled native synthetic-provider controls passed`);
  } finally {
    rmSync(work, { recursive: true, force: true });
    dockerEnv = process.env;
  }
});
