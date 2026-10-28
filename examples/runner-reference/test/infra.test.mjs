import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { chmod, mkdir, readFile, readdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import {
  checked, errorText, hashFile, imageReference, processResult, runOptions, temporaryDirectory,
} from '../infra.mjs';
import { hydratePayload, linuxPayload, snapshot } from '../build.mjs';
import { cases, verifyConformance } from '../conformance.mjs';

const fixture = fileURLToPath(new URL('./process-fixture.mjs', import.meta.url));
const root = fileURLToPath(new URL('../../..', import.meta.url));
const reference = `ghcr.io/microsoft/waza-runner-reference@sha256:${'a'.repeat(64)}`;

async function dockerDouble(directory) {
  const docker = path.join(directory, 'docker');
  await writeFile(docker, `#!/bin/sh\nexec "${process.execPath}" "${fixture}" docker "$@"\n`);
  await chmod(docker, 0o700);
  return {
    ...process.env, PATH: `${directory}${path.delimiter}${process.env.PATH}`,
    REFERENCE_TEST_LOG: path.join(directory, 'calls'),
  };
}

test('immutable pins distinguish published references from explicit local IDs', () => {
  assert.equal(imageReference(reference), reference);
  assert.equal(imageReference(`sha256:${'b'.repeat(64)}`, true), `sha256:${'b'.repeat(64)}`);
  for (const invalid of [
    '', reference + '\n', ' ' + reference, reference.replace('sha256:', 'sha512:'),
    reference.replace(/a$/, 'A'), reference.replace(/a$/, ''), reference + 'a',
    `sha256:${'a'.repeat(64)}`, 'ghcr.io/microsoft/waza-runner-reference:latest',
    reference.replace('microsoft/', 'untrusted/'), reference.replace('ghcr.io/', 'ghcr.io.evil/'),
  ]) assert.throws(() => imageReference(invalid), /Expected/);
  for (const invalid of [reference, 'latest', 'sha256:bad']) {
    assert.throws(() => imageReference(invalid, true), /Local mode/);
  }
});

test('combined failures retain primary and cleanup diagnostics at the CLI boundary', () => {
  assert.equal(errorText(new AggregateError([
    new Error('primary invocation failure'), new Error('container cleanup failure'),
  ], 'Both operations failed')), 'Both operations failed\nprimary invocation failure\ncontainer cleanup failure');
  assert.equal(errorText('failure'), 'failure');
});

test('bounded process preserves nonzero status, stdin and separate stderr', async () => {
  const result = await processResult(process.execPath, [fixture, 'echo-failure'], { input: '{"unit":2}' });
  assert.equal(result.status, 7);
  assert.equal(result.signal, null);
  assert.equal(result.stdout, '{"unit":2}');
  assert.equal(result.stderr, 'intentional transport test failure\n');
  await assert.rejects(checked(process.execPath, [fixture, 'echo-failure']), /failed \(7\)/);
});

test('bounded process rejects missing commands, output overflow and deadline exhaustion', async () => {
  await assert.rejects(processResult('/waza-reference-command-does-not-exist', []), /ENOENT/);
  await assert.rejects(processResult(process.execPath, [fixture, 'flood'], { maxBytes: 16 }), /output bytes/);
  const start = Date.now();
  await assert.rejects(processResult(process.execPath, [fixture, 'hang'], { timeout: 500 }), /exceeded 500 ms/);
  assert.ok(Date.now() - start < 5_000, 'TERM-resistant subprocess must be killed, not left running');
});

test('temporary directories are removed on success and failures', async () => {
  let success;
  await temporaryDirectory('waza-reference-test-', async (directory) => {
    success = directory;
    await writeFile(path.join(directory, 'owned'), 'only task-owned scratch');
  });
  await assert.rejects(readdir(success), /ENOENT/);
  let failure;
  await assert.rejects(temporaryDirectory('waza-reference-test-', async (directory) => {
    failure = directory;
    throw new Error('primary failure');
  }), /primary failure/);
  await assert.rejects(readdir(failure), /ENOENT/);
});

test('LFS hydration verifies committed digest and size instead of embedding pointer bytes', async () => {
  await temporaryDirectory('waza-reference-test-', async (directory) => {
    const source = path.join(directory, 'payload');
    const destination = path.join(directory, 'pointer');
    const payload = 'verified embedded fixture bytes';
    const digest = createHash('sha256').update(payload).digest('hex');
    const pointer = `version https://git-lfs.github.com/spec/v1\noid sha256:${digest}\nsize ${payload.length}\n`;
    await writeFile(source, payload);
    await writeFile(destination, pointer);
    await hydratePayload(source, destination);
    assert.equal(await hashFile(destination), digest);
    await writeFile(destination, pointer);
    await writeFile(source, 'corrupt');
    await assert.rejects(hydratePayload(source, destination), /Unhydrated or corrupt/);
    await writeFile(source, pointer);
    await writeFile(destination, pointer);
    await assert.rejects(hydratePayload(source, destination), /Unhydrated or corrupt/);
    await writeFile(source, payload);
    await writeFile(destination, pointer.replace(`size ${payload.length}`, 'size 0'));
    await assert.rejects(hydratePayload(source, destination), /Unhydrated or corrupt/);
    await writeFile(destination, 'not a committed LFS pointer');
    await assert.rejects(hydratePayload(source, destination), /Expected committed LFS pointer/);
  });
});

test('payload inventory includes glibc and musl Linux amd64 bundles but not other platforms', () => {
  for (const name of [
    'zcopilot_1.0.101_linux_amd64.zst', 'zcopilot_1.0.101_linuxmusl_amd64.zst',
    'zcopilotruntime_1.0.101_linux-x64.so.zst',
    'zcopilotruntimeassets_1.0.101_linuxmusl-x64.tgz',
    'zcopilotruntimewrapper_1.0.101_linux-x64_copilot-runtime.zst',
  ]) assert.equal(linuxPayload(`internal/embedded/${name}`), true, name);
  for (const name of ['zcopilot_darwin_arm64.zst', 'zcopilot_linux_amd64.go', 'linux-arm64.tgz']) {
    assert.equal(linuxPayload(`internal/embedded/${name}`), false, name);
  }
});

test('clean source snapshot excludes ignored/untracked state and rejects dirty tracked source', async () => {
  await temporaryDirectory('waza-reference-test-', async (directory) => {
    const checkout = path.join(directory, 'checkout');
    await mkdir(checkout);
    const git = (args) => checked('git', args, { cwd: checkout });
    await git(['init', '--quiet']);
    await git(['config', 'user.name', 'Reference Test']);
    await git(['config', 'user.email', 'reference-test@example.invalid']);
    await git(['config', 'commit.gpgsign', 'false']);
    await git(['config', 'filter.lfs.clean', 'git-lfs clean -- %f']);
    await git(['config', 'filter.lfs.smudge', 'git-lfs smudge -- %f']);
    await mkdir(path.join(checkout, 'internal/embedded'), { recursive: true });
    await writeFile(path.join(checkout, '.gitattributes'), '*.zst filter=lfs -text\n');
    await writeFile(path.join(checkout, '.gitignore'), 'ignored\n');
    const bundle = 'internal/embedded/zcopilot_linux_amd64.zst';
    await writeFile(path.join(checkout, bundle), 'test-only embedded bytes');
    await writeFile(path.join(checkout, 'tracked'), 'committed source');
    await git(['add', '.gitattributes', '.gitignore', bundle, 'tracked']);
    await git(['commit', '--quiet', '-m', 'test fixture']);
    assert.match(await git(['show', `HEAD:${bundle}`]), /^version https:\/\/git-lfs.github.com\/spec\/v1\n/);
    await writeFile(path.join(checkout, 'ignored'), 'must not copy');
    await writeFile(path.join(checkout, 'untracked'), 'must not copy');
    const output = path.join(directory, 'output');
    await mkdir(output);
    const result = await snapshot(checkout, output);
    assert.equal(result.revision, await git(['rev-parse', 'HEAD']));
    assert.deepEqual(result.payloads, [bundle]);
    assert.equal(await readFile(path.join(result.context, bundle), 'utf8'), 'test-only embedded bytes');
    assert.equal(await readFile(path.join(result.context, 'tracked'), 'utf8'), 'committed source');
    for (const name of ['.git', 'ignored', 'untracked']) {
      await assert.rejects(readFile(path.join(result.context, name)), /ENOENT/);
    }
    await writeFile(path.join(checkout, 'tracked'), 'uncommitted source');
    await assert.rejects(snapshot(checkout, output), /clean tracked checkout/);
  });
});

test('fixture cases exercise exact selection, rejected adjacent work, failure and malformed streams', async () => {
  const request = JSON.parse(await readFile(new URL('../request.json', import.meta.url), 'utf8'));
  const variants = cases(request);
  assert.deepEqual(variants.map(({ name }) => name), [
    'selected', 'invalid-shard', 'invalid-task', 'invalid-trial', 'invalid-attempt',
    'evaluation-failure', 'malformed', 'extra-document',
  ]);
  assert.deepEqual(JSON.parse(variants[0].input), request);
  assert.deepEqual(request.selection, { shard: 'shard-a', task: 'selected', trial: 2, attempt: 2 });
  assert.match(request.input.eval, /adjacent-must-not-be-opened\/\*\.yaml/);
  assert.equal(request.allow_paid_execution, false);
  for (const variant of variants.slice(1, 5)) {
    const field = variant.name.slice('invalid-'.length);
    assert.notEqual(JSON.parse(variant.input).selection[field], request.selection[field]);
  }
  assert.match(JSON.parse(variants[5].input).input.task, /required-output-not-produced-762/);
  assert.doesNotMatch(JSON.parse(variants[5].input).input.task, /    - hello/);
  assert.throws(() => JSON.parse(variants[6].input));
  assert.throws(() => JSON.parse(variants[7].input));
  assert.throws(() => verifyConformance([]), /BLOCKED/);
  assert.throws(() => verifyConformance([{ status: 0, stdout: '{}' }]), /BLOCKED/);
});

test('container command uses local stdio only, and cleanup targets the returned ID on failure', async () => {
  await temporaryDirectory('waza-reference-test-', async (directory) => {
    const env = { ...await dockerDouble(directory), REFERENCE_TEST_STATUS: '7' };
    const log = env.REFERENCE_TEST_LOG;
    const driver = `import {invokeContainer} from ${JSON.stringify(new URL('../infra.mjs', import.meta.url).href)};
      console.log(JSON.stringify(await invokeContainer(${JSON.stringify(reference)}, ['runner','run'], 'transport-input')));`;
    const result = await checked(process.execPath, ['--input-type=module', '-e', driver], { env });
    const recorded = JSON.parse(result);
    assert.equal(recorded.status, 7);
    assert.equal(recorded.stdout, 'transport-input');
    const calls = (await readFile(log, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(calls.length, 3);
    assert.deepEqual(calls[0].slice(3), ['--interactive', ...runOptions(), '--pull=missing', reference, 'runner', 'run']);
    assert.deepEqual(calls[1], ['start', '--attach', '--interactive', 'c'.repeat(64)]);
    assert.deepEqual(calls[2], ['rm', '--force', 'c'.repeat(64)]);
    assert.ok(!calls[0].includes('--mount'));
    assert.ok(!calls[0].includes('--volume'));
    const failedCleanup = await processResult(process.execPath, ['--input-type=module', '-e', driver], {
      env: { ...env, REFERENCE_TEST_CLEANUP_STATUS: '1' },
    });
    assert.notEqual(failedCleanup.status, 0);
    assert.match(failedCleanup.stderr, /Cleanup failed for container c{64}/);
    await writeFile(log, '');
    const failedCreate = await processResult(process.execPath, ['--input-type=module', '-e', driver], {
      env: { ...env, REFERENCE_TEST_CREATE_STATUS: '1' },
    });
    assert.notEqual(failedCreate.status, 0);
    assert.match(failedCreate.stderr, /intentional transport test create failure/);
    const createCalls = (await readFile(log, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(createCalls.length, 1, 'Do not delete an unverified container name after failed creation');
    assert.equal(createCalls[0][0], 'create');
    await writeFile(log, '');
    const localDriver = driver.replace(JSON.stringify(reference), JSON.stringify(`sha256:${'b'.repeat(64)}`));
    assert.notEqual(localDriver, driver);
    await checked(process.execPath, ['--input-type=module', '-e', localDriver], { env });
    const localCalls = (await readFile(log, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.ok(localCalls[0].includes('--pull=never'), 'Unknown local IDs must never fall back to registry pull');
  });
});

test('real conformance entrypoint refuses missing public execution capabilities before capture', async () => {
  await temporaryDirectory('waza-reference-test-', async (directory) => {
    const env = await dockerDouble(directory);
    const reply = path.join(directory, 'reply.json');
    await writeFile(reply, JSON.stringify({ schema_version: '1.0', versions: ['1.0'], capabilities: ['negotiation'] }));
    const output = path.join(directory, 'output');
    const result = await processResult(process.execPath, [
      path.join(root, 'examples/runner-reference/conformance.mjs'), reference, output,
    ], { env: { ...env, REFERENCE_TEST_REPLY: reply } });
    assert.equal(result.status, 1);
    assert.equal(result.stdout, '');
    assert.match(result.stderr, /Public runner lacks required exact-unit\/mock-text\/terminal capabilities/);
    await assert.rejects(readdir(output), /ENOENT/);
    const calls = (await readFile(env.REFERENCE_TEST_LOG, 'utf8')).trim().split('\n').map((line) => JSON.parse(line));
    assert.equal(calls.length, 3, 'Only capabilities should be invoked, then its exact container cleaned up');
    assert.deepEqual(calls[0].slice(-2), ['runner', 'capabilities']);
    assert.deepEqual(calls[2], ['rm', '--force', 'c'.repeat(64)]);
  });
});

test('real CLI rejects malformed pins before invoking any container command', async () => {
  const result = await processResult(process.execPath, [
    path.join(root, 'examples/runner-reference/conformance.mjs'),
    'ghcr.io/microsoft/waza-runner-reference:latest', '/not-created',
  ]);
  assert.equal(result.status, 1);
  assert.match(result.stderr, /tags and local IDs are not published pins/);
  assert.equal(result.stdout, '');
});

test('recipe pins complete toolchains, retains real web builds and disables native launch', async () => {
  const dockerfile = await readFile(new URL('../Dockerfile', import.meta.url), 'utf8');
  const from = dockerfile.split('\n').filter((line) => line.startsWith('FROM '));
  assert.equal(from.length, 3);
  assert.match(from[0], /^FROM node:24\.15\.0-bookworm-slim@sha256:[a-f0-9]{64} AS dashboard$/);
  assert.match(from[1], /^FROM golang:1\.26\.4-bookworm@sha256:[a-f0-9]{64} AS binary$/);
  assert.equal(from[2], 'FROM scratch');
  assert.match(dockerfile, /^RUN npm ci --no-audit --no-fund$/m);
  assert.match(dockerfile, /^RUN npm run build$/m);
  assert.match(dockerfile, /^COPY --from=dashboard \/src\/web\/dist\/ web\/dist\/$/m);
  assert.match(dockerfile, /COPILOT_CLI_PATH=\/native-runtime-unavailable/);
  assert.match(dockerfile, /^USER 65532:65532$/m);
});
