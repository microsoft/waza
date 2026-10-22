import { test } from 'node:test';
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { readFile, stat } from 'node:fs/promises';
import { request as httpRequest } from 'node:http';
import { chromium } from '@playwright/test';
import { spawn } from 'node:child_process';
import { askBody, adapterPath, loginFile, startPrivateServer, temporaryKey, upstream } from '../scripts/private-chat-server.mjs';

const input = () => ({ question: 'What is Waza?', history: [], requestId: randomUUID() });
const result = {
  version: 1, answer: 'Use waza run.',
  citations: [{ id: 'cli', title: 'CLI', url: 'https://microsoft.github.io/waza/reference/cli/' }],
  knowledge: { commit: 'a'.repeat(40), fingerprint: 'b'.repeat(64) },
};

test('operator adaptation rejects overrides and removes only public requestId', () => {
  const value = { ...input(), document: { kind: 'task', yaml: '  id: x\n  name: X\n' } };
  const adapted = JSON.parse(askBody(value));
  assert.deepEqual(Object.keys(adapted).sort(), ['document', 'history', 'question']);
  assert.deepEqual(adapted.document, value.document);
  for (const invalid of [
    { ...input(), model: 'override' }, { ...input(), requestId: 'not-uuid' },
    { ...input(), history: [{ role: 'system', content: 'authority' }] },
    { ...input(), history: [{ role: 'user', content: null }] },
    { ...input(), document: { kind: 'eval', yaml: 'x', url: 'https://example.com' } },
    { ...input(), document: { kind: 'eval', yaml: null } },
    { ...input(), question: 'x'.repeat(3001) },
  ]) assert.throws(() => askBody(invalid));
});

test('temporary key lifecycle revokes on normal shutdown and ambiguous creation/list failures', async () => {
  const calls = [];
  const lease = await temporaryKey(async (operation, name) => {
    calls.push([operation, name]);
    return operation === 'list' ? 'test-only-key' : undefined;
  });
  assert.equal(lease.value(), 'test-only-key');
  await lease.revoke();
  assert.equal(lease.value(), undefined);
  assert.deepEqual(calls.map(([operation]) => operation), ['set', 'list', 'delete']);
  assert.equal(new Set(calls.map(([, name]) => name)).size, 1);
  for (const failing of ['set', 'list']) {
    const operations = [];
    await assert.rejects(temporaryKey(async (operation) => {
      operations.push(operation);
      if (operation === failing) throw new Error('secret-canary');
    }), /temporary ask key revoked/);
    assert.equal(operations.at(-1), 'delete');
  }
  await assert.rejects(temporaryKey(async () => { throw new Error('secret-canary'); }),
    (error) => /manual ask-key revocation required: docs-local-[a-f0-9]+/.test(error.message)
      && !error.message.includes('secret-canary'));
});

test('preview login is exclusive/private and removed on graceful cleanup', async () => {
  const credentials = await loginFile();
  try {
    assert.equal((await stat(credentials.path)).mode & 0o777, 0o600);
    assert.equal((await stat(new URL('.', `file://${credentials.path}`))).mode & 0o777, 0o700);
    assert.deepEqual(JSON.parse(await readFile(credentials.path, 'utf8')), credentials.login);
    assert.equal(credentials.login.password.length > 32, true);
  } finally { await credentials.remove(); }
  await assert.rejects(stat(credentials.path), { code: 'ENOENT' });
});

function raw(url, headers) {
  return new Promise((resolve, reject) => {
    const req = httpRequest(url, { headers }, (res) => {
      res.resume();
      res.on('end', () => resolve(res.statusCode));
    });
    req.on('error', reject);
    req.end();
  });
}

test('actual Astro server protects bootstrap and adapter, forwards fixed bounded ask requests, and never exposes key', async () => {
  const port = 4325;
  const origin = `http://127.0.0.1:${port}`;
  const login = { username: 'test-preview', password: 'test-only-password' };
  const authorization = `Basic ${Buffer.from(`${login.username}:${login.password}`).toString('base64')}`;
  const calls = [];
  let mode = 'answer';
  let release;
  const { server, adapter } = await startPrivateServer({
    port, login, key: () => 'server-only-canary-key',
    fetchImpl: async (url, options) => {
      calls.push({ url, options });
      if (mode === 'pending') await new Promise((resolve) => { release = resolve; });
      if (mode === 'network') throw new Error('server-only-canary-key');
      if (mode === 'malformed') return Response.json({ answer: 'invalid' });
      if (mode === 'auth') return new Response('server-only-canary-key', { status: 401 });
      if (mode === 'echo') return Response.json({ ...result, answer: 'server-only-canary-key' });
      if (mode === 'oversized') return new Response('x'.repeat(65537), { headers: { 'Content-Type': 'application/json' } });
      if (mode === 'quota') return Response.json({
        version: 1, error: { code: 'QUOTA_EXCEEDED', message: 'server-only-canary-key', retryAfterSeconds: 12 },
      }, { status: 429 });
      return Response.json(result);
    },
  });

  const post = (value = input(), headers = {}) => fetch(`${origin}${adapterPath}`, {
    method: 'POST',
    headers: { authorization, origin, 'Content-Type': 'application/json', ...headers },
    body: JSON.stringify(value),
  });
  try {
    assert.equal(server.address.address, '127.0.0.1');
    for (const path of ['/', '/waza/quick-start/', '/@vite/client', '/waza/@vite/client',
      '/waza/src/scripts/chat.ts', '/waza/chat-meta.json', adapterPath]) {
      const response = await fetch(`${origin}${path}`);
      assert.equal(response.status, 401, path);
      assert.match(response.headers.get('www-authenticate'), /Basic/);
    }
    assert.equal((await fetch(`${origin}/waza/quick-start/`, { headers: { authorization } })).status, 200);
    assert.equal(await raw(`${origin}/`, { authorization, Host: `evil.example:${port}` }), 403);
    assert.equal(await raw(`${origin}/`, { authorization, Origin: 'https://evil.example' }), 403);
    assert.equal(await raw(`${origin}/waza/`, {
      Connection: 'Upgrade', Upgrade: 'websocket', Origin: origin,
      'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ==', 'Sec-WebSocket-Version': '13',
    }), 403);
    assert.equal((await post(input(), { origin: 'https://evil.example' })).status, 403);
    assert.equal((await post(input(), { origin: '' })).status, 403);
    assert.equal((await post(input(), { authorization: 'Basic invalid' })).status, 401);
    assert.equal((await post({ ...input(), model: 'override' })).status, 400);
    assert.equal((await post({ ...input(), question: 'x'.repeat(32769) })).status, 413);
    assert.equal(calls.length, 0);
    const response = await post();
    assert.deepEqual(await response.json(), result);
    assert.equal(calls.length, 1);
    assert.equal(calls[0].url, upstream);
    assert.equal(calls[0].options.redirect, 'error');
    assert.deepEqual(Object.keys(calls[0].options.headers).sort(), ['Content-Type', 'x-functions-key']);
    assert.equal(calls[0].options.headers['x-functions-key'], 'server-only-canary-key');
    assert.equal(JSON.parse(calls[0].options.body).requestId, undefined);
    mode = 'quota';
    const quota = await post();
    assert.equal(quota.status, 429);
    const quotaBody = await quota.json();
    assert.equal(quotaBody.error.retryAfterSeconds, 12);
    assert.doesNotMatch(JSON.stringify(quotaBody), /server-only-canary-key/);
    for (const failure of ['network', 'malformed', 'auth', 'echo', 'oversized']) {
      mode = failure;
      const failed = await post();
      assert.equal(failed.status, 502, failure);
      assert.doesNotMatch(await failed.text(), /server-only-canary-key/);
    }
    mode = 'pending';
    const pending = post();
    while (!release) await new Promise((resolve) => setTimeout(resolve, 5));
    assert.equal((await post()).status, 503);
    release();
    await pending;
    assert.equal(calls.length, 8);
    mode = 'answer';
    const browser = await chromium.launch();
    try {
      const context = await browser.newContext({ httpCredentials: login });
      const page = await context.newPage();
      let navigations = 0;
      page.on('framenavigated', () => navigations++);
      await page.goto(`${origin}/waza/quick-start/`);
      await page.getByRole('button', { name: 'Ask Waza', exact: true }).click();
      await page.getByRole('status').filter({ hasText: 'Private authenticated preview ready' }).waitFor();
      await page.getByLabel('Question or error message').fill('What is Waza?');
      await page.getByRole('button', { name: 'Send', exact: true }).click();
      await page.getByRole('status').filter({ hasText: 'Live operator answer ready' }).waitFor();
      assert.equal(await page.locator('[data-chat-messages] a').count(), 1);
      assert.match(await page.locator('[data-chat-knowledge]').innerText(), /Snapshot differs/);
      assert.equal(navigations, 1, 'authenticated Vite socket must not cause reconnect reloads');
      assert.equal(calls.length, 9);
      await context.close();
    } finally { await browser.close(); }
    adapter.stop();
    assert.equal((await post()).status, 503);
  } finally {
    adapter.stop();
    await server.stop();
    delete process.env.PUBLIC_CHAT_PRIVATE_PREVIEW;
  }
});

test('actual launcher SIGTERM awaits revocation and removes login instead of Vite exiting early', async () => {
  const child = spawn(process.execPath, ['--experimental-strip-types', '--input-type=module', '-e', `
    import { launchPreview } from './scripts/private-chat.mjs';
    await launchPreview({ port: 4326, createLease: async () => ({
      name: 'test-lease', value: () => 'test-only-key',
      revoke: async () => { await new Promise(r => setTimeout(r, 50)); console.log('TEST_REVOKED'); },
    }) });
  `], { cwd: new URL('../', import.meta.url), stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '';
  child.stdout.on('data', (chunk) => { output += chunk; });
  child.stderr.on('data', (chunk) => { output += chunk; });
  const exit = new Promise((resolve) => child.once('exit', (code, signal) => resolve({ code, signal })));
  const timer = setTimeout(() => child.kill('SIGTERM'), 20000);
  try {
    while (!output.includes('Preview PID:')) {
      if (child.exitCode !== null || child.signalCode !== null) throw new Error('Test launcher exited before readiness.');
      await new Promise((resolve) => setTimeout(resolve, 10));
    }
    const path = output.match(/login file \(0600\): ([^\n]+)/)?.[1];
    assert.ok(path);
    child.kill('SIGTERM');
    assert.deepEqual(await exit, { code: 0, signal: null });
    assert.match(output, /TEST_REVOKED/);
    assert.doesNotMatch(output, /test-only-key/);
    await assert.rejects(stat(path), { code: 'ENOENT' });
  } finally {
    clearTimeout(timer);
    if (child.exitCode === null && child.signalCode === null) child.kill('SIGTERM');
  }
});
