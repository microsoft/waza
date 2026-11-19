import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const fixture = JSON.parse(readFileSync(join(root, 'examples/runner/unit.json'), 'utf8'));

function command(args, options = {}) {
  const result = spawnSync('docker', args, {
    cwd: root, encoding: 'utf8', timeout: 120_000, maxBuffer: 8 * 1024 * 1024, ...options,
  });
  assert.ifError(result.error);
  assert.equal(result.signal, null, result.stderr);
  return result;
}

test('real image: public CLI, exact selection, local I/O and negative controls', { timeout: 1_800_000 }, () => {
  const work = mkdtempSync(join(tmpdir(), 'waza-container-'));
  try {
    // Neither host credentials nor a checkout is mounted into the runtime.
    const iid = join(work, 'image.id');
    const registry = process.env.NPM_CONFIG_REGISTRY;
    const build = command([
      'build', '--file', 'examples/runner/container/Dockerfile', '--iidfile', iid,
      ...(registry ? ['--build-arg', `NPM_CONFIG_REGISTRY=${registry}`] : []), '.',
    ], { timeout: 1_500_000, stdio: 'inherit' });
    assert.equal(build.status, 0, 'container build failed');
    const buildReference = readFileSync(iid, 'utf8').trim();
    assert.match(buildReference, /^sha256:[a-f0-9]{64}$/);
    const inspect = command(['image', 'inspect', buildReference]);
    assert.equal(inspect.status, 0, inspect.stderr);
    const inspected = JSON.parse(inspect.stdout)[0];
    const image = inspected.Id;
    assert.match(image, /^sha256:[a-f0-9]{64}$/);
    const config = inspected.Config;
    assert.equal(inspected.Os, 'linux');
    assert.ok(['amd64', 'arm64'].includes(inspected.Architecture));
    assert.equal(config.Labels?.['org.opencontainers.image.source'], 'https://github.com/microsoft/waza');
    assert.equal(config.User, '65532:65532');
    assert.deepEqual(config.Entrypoint, ['/waza']);
    console.log(`Tested local image ID: ${image}; image: ${inspected.Os}/${inspected.Architecture}; host: ${process.platform}/${process.arch}`);

    const base = [
      'run', '--rm', '--pull=never', '--network=none', '--read-only',
      '--cap-drop=ALL', '--security-opt=no-new-privileges', '--pids-limit=64',
      '--memory=256m', '--cpus=1', '--tmpfs', '/tmp:rw,noexec,nosuid,size=32m,mode=1777',
      '-i', image,
    ];
    function invoke(action, input) {
      const result = command([...base, 'runner', action], {
        input: typeof input === 'string' ? input : `${JSON.stringify(input)}\n`,
      });
      if (result.status === 0) assert.equal(result.stderr, '', result.stderr);
      assert.equal(result.stdout.trim().split('\n').length, 1, 'one JSON response');
      return { result, body: JSON.parse(result.stdout) };
    }
    const capabilities = invoke('capabilities', '');
    assert.equal(capabilities.result.status, 0);
    for (const name of ['exact-unit', 'inline.mock-text', 'terminal-result']) {
      assert.ok(capabilities.body.capabilities.includes(name));
    }
    const validated = invoke('validate', fixture);
    assert.equal(validated.result.status, 0);
    assert.equal(validated.body.status, 'ready');
    assert.equal(validated.body.engine_calls, 0);
    assert.equal(validated.body.adjacent_work_selected, false);
    assert.deepEqual(validated.body.resolved, fixture.selection);

    function run(request, exit, outcome, calls) {
      const response = invoke('run', request);
      const body = response.body;
      assert.equal(response.result.status, exit, response.result.stdout);
      assert.equal(body.kind, 'waza.runner-terminal');
      assert.equal(body.schema_version, '1.0');
      assert.equal(body.exit_code, exit);
      assert.equal(body.outcome, outcome);
      assert.equal(response.result.stderr, exit === 0 ? '' : `runner run: ${outcome}\n`);
      assert.equal(body.engine_calls, calls);
      assert.ok(Array.isArray(body.diagnostics));
      if (calls === 1) {
        assert.deepEqual(body.selection, request.selection);
        assert.deepEqual(body.observation.selection, request.selection);
        assert.equal(body.observation.execution_mode, 'mock');
        assert.equal(body.observation.engine_calls, 1);
        assert.equal(body.observation.run_number, request.selection.trial);
        assert.equal(body.observation.attempts, request.selection.attempt);
        assert.equal(body.observation.status, exit === 0 ? 'passed' : 'failed');
        assert.equal(body.observation.validations.length, 1);
        assert.equal(body.observation.validations[0].passed, exit === 0);
        assert.equal(body.failure_boundary, 'none');
        assert.ok(body.identity);
      } else {
        assert.equal(body.observation, undefined);
        assert.equal(body.identity, undefined);
        assert.equal(body.selection, undefined);
        assert.ok(body.diagnostics.length > 0);
      }
      return response;
    }
    writeFileSync(join(work, 'request.json'), `${JSON.stringify(fixture)}\n`, { flag: 'wx' });
    const successful = run(JSON.parse(readFileSync(join(work, 'request.json'), 'utf8')), 0, 'success', 1);
    assert.deepEqual(successful.body.identity, validated.body.identity);
    writeFileSync(join(work, 'terminal.json'), successful.result.stdout, { flag: 'wx' });
    assert.deepEqual(JSON.parse(readFileSync(join(work, 'terminal.json'), 'utf8')), successful.body);

    const last = structuredClone(fixture);
    last.selection.trial = 3;
    last.selection.attempt = 3;
    run(last, 0, 'success', 1);
    const failed = structuredClone(fixture);
    failed.input.task = failed.input.task.replace('- hello', '- absent-marker');
    assert.notEqual(failed.input.task, fixture.input.task);
    run(failed, 1, 'evaluation_failure', 1);
    for (const [field, value] of [['shard', 'neighbor'], ['task', 'neighbor'], ['trial', 4], ['attempt', 4]]) {
      const invalid = structuredClone(fixture);
      invalid.selection[field] = value;
      run(invalid, 6, 'invalid_input', 0);
    }
    const external = structuredClone(fixture);
    external.input.task = external.input.task.replace('prompt: hello', 'prompt_file: /input/neighbor.yaml');
    assert.notEqual(external.input.task, fixture.input.task);
    run(external, 6, 'invalid_input', 0);
    const native = structuredClone(fixture);
    native.required_capabilities.push('inline.native-text');
    native.allow_paid_execution = true;
    run(native, 5, 'unsupported_capability', 0);
    run('{', 6, 'invalid_input', 0);

    // The same image is the external adapter entrypoint: one descriptor in, one result out.
    const descriptor = readFileSync(join(root, 'examples/runner/adapter/submit.json'), 'utf8');
    const translated = command([...base, 'runner', 'adapter', 'translate'], { input: descriptor });
    assert.equal(translated.status, 0, translated.stdout);
    const translation = JSON.parse(translated.stdout);
    assert.equal(translation.status, 'translated');
    assert.deepEqual(translation.call.command, ['runner', 'submit']);
    const adapterRun = [...base.slice(0, -2), '--tmpfs', '/store:rw,noexec,nosuid,size=32m,mode=1777', '-i', image];
    const dispatched = command([...adapterRun, 'runner', 'adapter', 'dispatch', '--store', '/store'], { input: descriptor });
    assert.equal(dispatched.status, 0, `${dispatched.stdout}\n${dispatched.stderr}`);
    const adapted = JSON.parse(dispatched.stdout);
    assert.equal(adapted.kind, 'waza.adapter-result');
    assert.equal(adapted.status, 'terminal');
    assert.equal(adapted.dispatched, true);
    assert.equal(adapted.work_id, 'example-queue/item-0001');
    assert.match(adapted.manifest_digest, /^[a-f0-9]{64}$/);
    const scheduled = JSON.parse(descriptor);
    scheduled.priority = 1;
    const refused = command([...adapterRun, 'runner', 'adapter', 'dispatch', '--store', '/store'], { input: JSON.stringify(scheduled) });
    assert.equal(refused.status, 6, refused.stdout);
    assert.equal(JSON.parse(refused.stdout).dispatched, false);
    assert.deepEqual(readdirSync(work).sort(), ['image.id', 'request.json', 'terminal.json']);
  } finally {
    rmSync(work, { recursive: true, force: true });
  }
});
