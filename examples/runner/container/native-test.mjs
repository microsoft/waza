import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const fixture = JSON.parse(readFileSync(join(root, 'examples/runner/copilot-unit.json'), 'utf8'));

function command(args, options = {}) {
  const result = spawnSync('docker', args, {
    cwd: root, encoding: 'utf8', timeout: 60_000, maxBuffer: 8 * 1024 * 1024, ...options,
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null, result.stderr);
  return result;
}

test('real CLI/container boundary with synthetic SDK peer and distinct bundled-runtime smoke', { timeout: 1_800_000 }, () => {
  const work = mkdtempSync(join(tmpdir(), 'waza-native-container-'));
  const images = {};
  try {
    for (const target of ['reference', 'synthetic-test', 'native-smoke']) {
      const iid = join(work, `${target}.id`);
      const registry = process.env.NPM_CONFIG_REGISTRY;
      const build = command([
        'build', '--file', 'examples/runner/container/Dockerfile', '--target', target,
        '--iidfile', iid,
        ...(registry ? ['--build-arg', `NPM_CONFIG_REGISTRY=${registry}`] : []), '.',
      ], { timeout: 1_500_000, stdio: 'inherit' });
      assert.equal(build.status, 0, `${target} build failed`);
      const inspected = command(['image', 'inspect', readFileSync(iid, 'utf8').trim()]);
      assert.equal(inspected.status, 0, inspected.stderr);
      const image = JSON.parse(inspected.stdout)[0];
      assert.equal(image.Os, 'linux');
      assert.ok(['amd64', 'arm64'].includes(image.Architecture));
      assert.equal(image.Config.User, '65532:65532');
      assert.deepEqual(image.Config.Entrypoint, ['/waza']);
      images[target] = image.Id;
      console.log(`${target}: ${image.Id}; native ${image.Os}/${image.Architecture}; host ${process.platform}/${process.arch}`);
    }

    function invoke(target, request, mode, action = 'run', extra = []) {
      const name = `waza-native-${randomUUID()}`;
      const synthetic = mode !== undefined;
      const output = join(work, name);
      if (synthetic) {
        mkdirSync(output);
        chmodSync(output, 0o777);
      }
      try {
        // Only a fresh empty output directory is mounted; never input/config/credentials.
        const result = command([
          'run', '--name', name, '--pull=never', '--network=none', '--read-only',
          '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=64',
          `--memory=${synthetic ? '256m' : '1536m'}`, '--cpus=1',
          '--tmpfs', `/tmp:rw,exec,nosuid,size=${synthetic ? '64m' : '1g'},mode=1777`,
          '-e', 'HOME=/tmp', '-e', 'COPILOT_HOME=/tmp', '-e', 'TMPDIR=/tmp',
          ...(synthetic ? [
            '-e', 'COPILOT_CLI_PATH=/synthetic-runtime', '-e', `WAZA_SYNTHETIC_MODE=${mode}`,
            '-e', 'WAZA_SYNTHETIC_LOG=/evidence/rpc.ndjson',
            '--mount', `type=bind,source=${output},target=/evidence`,
          ] : []),
          ...extra, '-i', images[target], ...(action === 'smoke' ? ['-test.run=^TestPath$', '-test.v'] : ['runner', action]),
        ], { input: request === null ? '' : `${JSON.stringify(request)}\n` });
        const inspect = command(['inspect', name]);
        assert.equal(inspect.status, 0, inspect.stderr);
        const container = JSON.parse(inspect.stdout)[0];
        assert.equal(container.HostConfig.NetworkMode, 'none');
        assert.equal(container.HostConfig.ReadonlyRootfs, true);
        if (synthetic) {
          assert.equal(container.Mounts.length, 1);
          assert.equal(container.Mounts[0].Source, output);
          assert.equal(container.Mounts[0].Destination, '/evidence');
          assert.equal(container.Mounts[0].RW, true);
        } else {
          assert.deepEqual(container.Mounts, []);
        }
        assert.equal(container.State.Running, false);
        const log = join(output, 'rpc.ndjson');
        const entries = existsSync(log)
          ? readFileSync(log, 'utf8').trim().split('\n').map(line => JSON.parse(line))
          : [];
        return { result, entries };
      } finally {
        // A timed-out Docker client must not leave its specifically named container running.
        const removed = command(['rm', '-f', name]);
        assert.equal(removed.status, 0, removed.stderr);
      }
    }

    const request = structuredClone(fixture);
    request.input.eval = request.input.eval.replace('gpt-4.1', 'synthetic-no-provider');
    assert.notEqual(request.input.eval, fixture.input.eval);
    const admitted = invoke('synthetic-test', request, undefined, 'validate');
    assert.equal(admitted.result.status, 0, admitted.result.stderr);
    const identity = JSON.parse(admitted.result.stdout).identity;
    for (const [mode, outcome, exit, calls] of [
      ['success', 'success', 0, 1],
      ['failed-check', 'evaluation_failure', 1, 1],
      ['session-error', 'infrastructure_failure', 2, 1],
      ['timeout', 'timeout', 4, 1],
      ['startup-failure', 'infrastructure_failure', 2, 0],
      ['bad-protocol', 'infrastructure_failure', 2, 0],
      ['options-failure', 'infrastructure_failure', 2, 1],
      ['unsupported', 'unsupported_capability', 5, 0],
    ]) {
      const input = structuredClone(request);
      if (mode === 'timeout') input.timeout_ms = 5000;
      if (mode === 'unsupported') input.required_capabilities.push('synthetic.unsupported');
      const { result, entries } = invoke('synthetic-test', input, mode);
      assert.equal(result.status, exit, result.stdout + result.stderr);
      const terminal = JSON.parse(result.stdout);
      assert.equal(terminal.kind, 'waza.runner-terminal');
      assert.equal(terminal.schema_version, '1.0');
      assert.equal(terminal.outcome, outcome);
      assert.equal(terminal.exit_code, exit);
      assert.equal(terminal.engine_calls, calls);
      assert.ok(Array.isArray(terminal.diagnostics));
      if (mode === 'unsupported') {
        assert.equal(entries.length, 0, 'admission rejection must not start a runtime');
        assert.equal(terminal.identity, undefined);
      } else {
        assert.deepEqual(terminal.selection, input.selection);
        // timeout_ms participates in identity; compare its own admission, not another request.
        const validation = mode === 'timeout' ? invoke('synthetic-test', input, undefined, 'validate') : admitted;
        assert.equal(validation.result.status, 0);
        assert.deepEqual(terminal.identity, mode === 'timeout' ? JSON.parse(validation.result.stdout).identity : identity);
        const counts = {};
        for (const entry of entries) counts[entry.method] = (counts[entry.method] ?? 0) + 1;
        const expected = { 'fixture.start': 1 };
        if (mode !== 'startup-failure') expected.connect = 1;
        if (calls === 1) {
          Object.assign(expected, { 'auth.getStatus': 1, 'session.create': 1, 'session.options.update': 1 });
          if (mode === 'options-failure') Object.assign(expected, {
            'session.detach': 1, 'runtime.shutdown': 1,
          });
          else expected['session.send'] = 1;
          if (mode !== 'timeout' && mode !== 'options-failure') Object.assign(expected, {
            'session.usage.getMetrics': 1, 'session.detach': 1, 'session.delete': 1, 'runtime.shutdown': 1,
          });
        }
        assert.deepEqual(counts, expected, 'no neighboring work, retry, resume or unexpected method');
        for (const entry of entries) {
          if (entry.method === 'session.send') assert.equal(entry.params.prompt, 'hello');
          if (entry.method === 'session.create') {
            assert.equal(entry.params.model, 'synthetic-no-provider');
            assert.equal(entry.params.enableSkills, false);
            assert.deepEqual(entry.params.availableTools, []);
            for (const field of [
              'enableConfigDiscovery', 'enableFileHooks', 'enableOnDemandInstructionDiscovery',
              'enableSessionStore', 'enableHostGitOperations',
            ]) assert.equal(entry.params[field], false, `${field} must explicitly disable runtime defaults`);
            assert.equal(entry.params.skipCustomInstructions, true);
            assert.equal(entry.params.skipEmbeddingRetrieval, true);
            assert.deepEqual(entry.params.memory, { enabled: false });
          }
          if (entry.method === 'session.options.update') assert.equal(entry.params.skipCustomInstructions, true);
        }
        if (mode === 'options-failure') {
          assert.ok(terminal.diagnostics.some(item => item.message.includes('execute/create_failed')));
          assert.ok(!JSON.stringify(terminal).includes('synthetic options update rejection'));
        }
        if (['success', 'failed-check'].includes(mode)) {
          assert.equal(terminal.observation.execution_mode, 'copilot');
          assert.deepEqual(terminal.observation.selection, input.selection);
          assert.equal(terminal.observation.run_number, input.selection.trial);
          assert.equal(terminal.observation.attempts, input.selection.attempt);
          assert.deepEqual(terminal.observation.validations, [{ identifier: '_output_contains', passed: mode === 'success' }]);
        }
      }
      console.log(`synthetic-only ${mode}: ${outcome}; engine calls=${calls}`);
    }

    const noCredentials = structuredClone(request);
    noCredentials.timeout_ms = 15000;
    const scratch = invoke('reference', noCredentials);
    assert.equal(scratch.result.status, 2, scratch.result.stdout + scratch.result.stderr);
    const failed = JSON.parse(scratch.result.stdout);
    assert.equal(failed.outcome, 'infrastructure_failure');
    assert.equal(failed.engine_calls, 0);
    assert.ok(failed.diagnostics.some(item => item.message.includes('initialize/start_failed')));
    console.log('reference scratch image: real bundled runtime cannot start; no native inference assurance');

    const smoke = invoke('native-smoke', null, undefined, 'smoke', [
      '--entrypoint', '/embedded.test', '-e', 'WAZA_EMBEDDED_TEST=install',
    ]);
    assert.equal(smoke.result.status, 0, smoke.result.stdout + smoke.result.stderr);
    assert.match(smoke.result.stdout, /bundled CLI 1\.0\.101: native ping\/status\/session\.create\/session\.getMessages\/session\.detach\/session\.delete passed/);
    assert.match(smoke.result.stdout, /provider requests=0/);
    console.log('Alpine smoke: actual bundled runtime protocol/session lifecycle only; no Send/inference/provider requests');
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});
