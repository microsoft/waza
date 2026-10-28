import assert from 'node:assert/strict';
import { access, chmod, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import test from 'node:test';
import { processResult, temporaryDirectory } from '../infra.mjs';

const binary = process.env.WAZA_REFERENCE_BINARY;
if (!binary || !path.isAbsolute(binary)) {
  throw new Error('Set WAZA_REFERENCE_BINARY to an absolute, freshly built real Waza binary; this test never skips or substitutes a stub');
}
await access(binary);
const request = JSON.parse(await readFile(new URL('../request.json', import.meta.url), 'utf8'));

test('real public negotiation validates the reference input without SDK launch', async () => {
  await temporaryDirectory('waza-reference-public-', async (directory) => {
    const marker = path.join(directory, 'runtime-was-launched');
    const sentinel = path.join(directory, 'reject-runtime');
    await writeFile(sentinel, `#!/bin/sh\nprintf 'unexpected SDK launch\\n' > "${marker}"\nexit 99\n`);
    await chmod(sentinel, 0o700);
    const options = {
      cwd: directory,
      env: {
        ...process.env, NO_COLOR: '1', ENABLE_COPILOT_TESTS: 'false',
        COPILOT_CLI_PATH: sentinel,
      },
    };
    const capabilities = await processResult(binary, ['runner', 'capabilities'], options);
    assert.equal(capabilities.status, 0, capabilities.stderr);
    const advertisement = JSON.parse(capabilities.stdout);
    assert.equal(advertisement.schema_version, '1.0');
    assert.ok(advertisement.versions.includes('1.0'));
    const missing = request.required_capabilities.filter((value) => !advertisement.capabilities.includes(value));
    const result = await processResult(binary, ['runner', 'negotiate'], {
      ...options, input: JSON.stringify(request),
    });
    const response = JSON.parse(result.stdout);
    assert.equal(response.kind, 'waza.runner-negotiation');
    assert.equal(response.status, missing.length ? 'unsupported_capability' : 'compatible', result.stderr);
    assert.equal(result.status, missing.length ? 5 : 0, result.stderr);
    assert.deepEqual(response.negotiation.required_capabilities, request.required_capabilities);
    for (const input of ['{"schema_version":', JSON.stringify(request) + '\n{}']) {
      const invalid = await processResult(binary, ['runner', 'negotiate'], { ...options, input });
      assert.equal(invalid.status, 6);
      assert.equal(JSON.parse(invalid.stdout).status, 'invalid_request');
    }
    await assert.rejects(access(marker), /ENOENT/);
  });
});
