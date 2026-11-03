import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import {
  chmodSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { runNamedContainer } from './native-lifecycle.mjs';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const mockFixture = JSON.parse(readFileSync(join(root, 'examples/runner/unit.json'), 'utf8'));

function command(args, options = {}) {
  const result = spawnSync('docker', args, {
    cwd: root, encoding: 'utf8', timeout: 1_500_000, maxBuffer: 8 * 1024 * 1024, ...options,
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null, result.stderr);
  return result;
}

function build(target, work) {
  const iid = join(work, `${target}.id`);
  const registry = process.env.NPM_CONFIG_REGISTRY;
  const result = command([
    'build', '--file', 'examples/runner/container/Dockerfile', '--target', target,
    '--iidfile', iid,
    ...(registry ? ['--build-arg', `NPM_CONFIG_REGISTRY=${registry}`] : []), '.',
  ], { timeout: 1_500_000, stdio: 'inherit' });
  assert.equal(result.status, 0, `${target} build failed`);
  const reference = readFileSync(iid, 'utf8').trim();
  assert.match(reference, /^sha256:[a-f0-9]{64}$/);
  const inspected = command(['image', 'inspect', reference]);
  assert.equal(inspected.status, 0, inspected.stderr);
  return JSON.parse(inspected.stdout)[0];
}

function platformName(value) {
  return ({ aarch64: 'arm64', x86_64: 'amd64' })[value] ?? value;
}

function assertRuntimeContainer(container, evidencePath) {
  assert.equal(container.Config.User, '65532:65532');
  assert.equal(container.HostConfig.NetworkMode, 'none');
  assert.equal(container.HostConfig.ReadonlyRootfs, true);
  assert.deepEqual(container.HostConfig.CapDrop, ['ALL']);
  assert.ok(container.HostConfig.SecurityOpt.includes('no-new-privileges'));
  assert.equal(container.State.Running, false);
  if (evidencePath) {
    assert.equal(container.Mounts.length, 1);
    assert.equal(container.Mounts[0].Source, evidencePath);
    assert.equal(container.Mounts[0].Destination, '/evidence');
    assert.equal(container.Mounts[0].RW, true);
  } else {
    assert.deepEqual(container.Mounts, []);
  }
}

test('native reference supports mock and actual bundled Copilot execution offline', { timeout: 1_800_000 }, () => {
  const work = mkdtempSync(join(tmpdir(), 'waza-native-bundle-'));
  try {
    const daemon = command(['info', '--format', '{{.OSType}}/{{.Architecture}}']);
    assert.equal(daemon.status, 0, daemon.stderr);
    const [os, rawArchitecture] = daemon.stdout.trim().split('/');
    const daemonArchitecture = platformName(rawArchitecture);
    assert.equal(os, 'linux');

    const reference = build('native-reference', work);
    assert.equal(reference.Os, 'linux');
    assert.equal(reference.Architecture, daemonArchitecture,
      'image architecture must match the Docker daemon; cross-architecture emulation is not native evidence');
    assert.equal(reference.Config.Labels?.['org.opencontainers.image.source'], 'https://github.com/microsoft/waza');
    assert.equal(reference.Config.User, '65532:65532');
    assert.deepEqual(reference.Config.Entrypoint, ['/waza']);
    assert.ok(reference.Config.Env.includes('HOME=/home/waza'));
    assert.ok(reference.Config.Env.includes('COPILOT_HOME=/home/waza/.copilot'));
    assert.ok(reference.Config.Env.includes('TMPDIR=/tmp'));
    if (process.env.WAZA_REQUIRE_NATIVE_AMD64 === '1') {
      assert.equal(process.platform, 'linux');
      assert.equal(process.arch, 'x64');
      assert.equal(daemonArchitecture, 'amd64');
      assert.equal(reference.Architecture, 'amd64');
    }
    console.log(`native-reference image ${reference.Id}; container linux/${reference.Architecture}; Docker host ${os}/${daemonArchitecture}; test host ${process.platform}/${process.arch}`);

    const name = `waza-native-mock-${randomUUID()}`;
    const input = `${JSON.stringify(mockFixture)}\n`;
    const mock = runNamedContainer(command, name, [
      'run', '--name', name, '--pull=never', '--network=none', '--read-only',
      '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=64',
      '--memory=256m', '--cpus=1',
      '--tmpfs', '/tmp:rw,noexec,nosuid,nodev,size=32m,mode=1777',
      '--tmpfs', '/home/waza/.copilot:rw,exec,nosuid,nodev,size=1g,uid=65532,gid=65532,mode=700',
      '-i', reference.Id, 'runner', 'run',
    ], { input }, (result, container) => {
      assertRuntimeContainer(container);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.equal(result.stderr, '');
      const terminal = JSON.parse(result.stdout);
      assert.equal(terminal.kind, 'waza.runner-terminal');
      assert.equal(terminal.outcome, 'success');
      assert.equal(terminal.engine_calls, 1);
      assert.equal(terminal.observation.execution_mode, 'mock');
      assert.deepEqual(terminal.selection, mockFixture.selection);
      assert.deepEqual(terminal.observation.selection, mockFixture.selection);
      return terminal;
    });
    assert.equal(mock.outcome, 'success');
    console.log(`native-reference mock exact-unit: success; local image ID ${reference.Id}`);

    const native = build('native-bundle-test', work);
    assert.equal(native.Os, 'linux');
    assert.equal(native.Architecture, daemonArchitecture,
      'native test image architecture must match the Docker daemon');
    assert.equal(native.Config.User, '65532:65532');
    assert.deepEqual(native.Config.Entrypoint, ['/native-bundle-probe']);
    console.log(`native-bundle-test image ${native.Id}; container linux/${native.Architecture}; no emulation`);

    const evidence = join(work, 'evidence');
    mkdirSync(evidence);
    chmodSync(evidence, 0o777);
    const nativeName = `waza-native-bundle-${randomUUID()}`;
    const probe = runNamedContainer(command, nativeName, [
      'run', '--name', nativeName, '--pull=never', '--network=none', '--read-only',
      '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=128',
      '--memory=2g', '--cpus=1',
      '--tmpfs', '/tmp:rw,exec,nosuid,nodev,size=1g,mode=1777',
      '--mount', `type=bind,source=${evidence},target=/evidence`,
      native.Id,
    ], {}, (result, container) => {
      assertRuntimeContainer(container, evidence);
      assert.equal(result.status, 0, result.stdout + result.stderr);
      assert.match(result.stdout, /"cases":6,"event":"all_contract_assertions_passed"/);
      return result.stdout;
    });
    assert.match(probe, /"event":"network_none_control"/);
    const files = readdirSync(evidence).sort();
    assert.deepEqual(files, ['provider-requests.json', 'reports.json']);
    for (const file of files) {
      const info = statSync(join(evidence, file));
      assert.equal(info.mode & 0o777, 0o644, `${file} must be readable but not writable across UIDs`);
      assert.doesNotThrow(() => readFileSync(join(evidence, file), 'utf8'));
      if (process.platform === 'linux' && typeof process.getuid === 'function') {
        assert.notEqual(info.uid, process.getuid(), `${file} must be written by the non-root container UID`);
      }
    }

    const reports = JSON.parse(readFileSync(join(evidence, 'reports.json'), 'utf8'));
    const requests = JSON.parse(readFileSync(join(evidence, 'provider-requests.json'), 'utf8'));
    assert.equal(reports.length, 6);
    assert.equal(requests.length, 6);
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
        assert.equal(terminal.failure_boundary, outcome === 'success' ? 'none' : 'shutdown');
      }
      if (report.name === 'sdk-positive' || report.name === 'reply-after-positive') {
        assert.equal(report.hook_sentinel, 'sessionStart\nuserPromptSubmitted\n');
      } else {
        assert.equal(report.hook_sentinel, '');
      }
    }
    assert.equal(requests.filter(request => request.mode === 'sdk-positive').length, 1);
    for (const request of requests) {
      const body = JSON.parse(request.body);
      assert.equal(body.model, 'offline-model');
      assert.equal(request.path, '/v1/chat/completions');
      assert.equal(request.mode === 'sdk-positive',
        request.body.includes('OFFLINE_HOME_INSTRUCTION_SENTINEL')
          && request.body.includes('OFFLINE_ANCESTOR_INSTRUCTION_SENTINEL')
          && request.body.includes('OFFLINE_ANCESTOR_AGENT_SENTINEL'));
      assert.ok(!request.body.includes('offline-synthetic-not-a-credential'));
    }
    const first = JSON.parse(reports.find(report => report.name === 'reply').stdout);
    const last = JSON.parse(reports.find(report => report.name === 'reply-after-positive').stdout);
    assert.deepEqual(last.identity, first.identity);
    console.log('actual bundled Copilot CLI: native success, discovery/hook positive and suppression controls, exact selection, timeout, stopped-child termination, SIGTERM cancellation, PID cleanup, cross-UID evidence read');
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});
