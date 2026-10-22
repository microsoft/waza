import { test } from 'node:test';
import assert from 'node:assert/strict';
import { answer, boundedHistory, bytes, json, requestBody, serviceError } from '../src/scripts/chat-contract.ts';
import {
  publicChatConfig, parsePublicChatConfig,
} from '../src/scripts/chat-public-config.ts';

const publicApi = 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api';

test('prepared public transport uses only the fixed deployment and official explicit challenge script', () => {
  for (const api of [publicApi, `${publicApi}/`]) {
    assert.deepEqual(parsePublicChatConfig(api, 'approved-public-sitekey'), {
      api: publicApi, sitekey: 'approved-public-sitekey', origin: 'https://microsoft.github.io',
      challengeScript: 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit',
    });
  }
});

test('public transport rejects endpoint overrides and invalid challenge configuration explicitly', () => {
  for (const api of [
    undefined, '', 'not a URL', publicApi.replace('https:', 'http:'),
    'https://other.example/api', `${publicApi}/ask`, `${publicApi}?code=not-a-real-key`,
    `${publicApi}#fragment`, publicApi.replace('https://', 'https://user:password@'),
    publicApi.replace('https://', 'https://@'),
  ]) {
    assert.throws(() => parsePublicChatConfig(api, 'sitekey'), /public chat|Public chat/);
  }
  for (const sitekey of [' key ', 'key\ninjected', '<script>']) {
    assert.throws(() => parsePublicChatConfig(publicApi, sitekey), /Turnstile sitekey/);
  }
});

test('public transport accepts an absent or empty optional sitekey without changing its endpoint or origin', () => {
  for (const sitekey of [undefined, '']) {
    const config = parsePublicChatConfig(publicApi, sitekey);
    assert.equal(config.api, publicApi);
    assert.equal(config.origin, 'https://microsoft.github.io');
    assert.equal(config.sitekey, undefined);
    assert.deepEqual(publicChatConfig({
      DEV: false, PUBLIC_CHAT_API_URL: publicApi, PUBLIC_CHAT_TURNSTILE_SITEKEY: sitekey,
    }), config);
  }
});

test('production uses public configuration without a source approval switch', () => {
  const env = { PUBLIC_CHAT_API_URL: publicApi, PUBLIC_CHAT_TURNSTILE_SITEKEY: 'sitekey' };
  assert.deepEqual(publicChatConfig({ DEV: false, ...env }), parsePublicChatConfig(publicApi, 'sitekey'));
  assert.equal(publicChatConfig({ DEV: true, ...env }), undefined);
  assert.equal(publicChatConfig({ DEV: false }), undefined);
  assert.equal(publicChatConfig({ DEV: true }), undefined);
  assert.deepEqual(publicChatConfig({ DEV: false, PUBLIC_CHAT_API_URL: publicApi }), parsePublicChatConfig(publicApi));
  assert.throws(() => publicChatConfig({ DEV: false, PUBLIC_CHAT_TURNSTILE_SITEKEY: 'sitekey' }), /API URL/);
});

const history = (content) => [{ role: 'user', content }];
const request = (question = 'Question', prior = [], yaml = '') =>
  requestBody(question, prior, 'eval', yaml, '12345678-1234-4234-8234-123456789abc');

test('question uses pre-trim Unicode count; YAML is byte-exact and independently bounded', () => {
  assert.equal(JSON.parse(request('😀'.repeat(3000))).question, '😀'.repeat(3000));
  assert.throws(() => request(' '.repeat(3000) + 'x'), /3000 Unicode/);
  assert.throws(() => request('😀'.repeat(3001)), /3000 Unicode/);
  assert.throws(() => request(' \n '), /Enter a question/);
  for (const yaml of ['  id: x\n  name: X\n', 'value: |+\n  text\n\n\n', 'é'.repeat(4096)]) {
    assert.equal(JSON.parse(request(' x ', [], yaml)).document.yaml, yaml);
  }
  assert.throws(() => request('x', [], 'é'.repeat(4097)), /8192 UTF-8/);
  assert.throws(() => request('x', [], 'a'.repeat(8193)), /8192/);
  assert.equal(JSON.parse(request('x', [], ' \n ')).document, undefined);
  assert.throws(() => requestBody('x', [], 'unsupported', 'x', 'id'), /document type/);
});

test('history, combined UTF-8, and JSON-escaped body budgets are separate limits', () => {
  assert.equal(JSON.parse(request('x', history('😀'.repeat(2000)))).history[0].content, '😀'.repeat(2000));
  assert.throws(() => request('x', history('x'.repeat(2001))), /Conversation context/);
  assert.throws(() => request('x', Array(7).fill(history('x')[0])), /Conversation context/);
  assert.throws(() => request('x', [{ role: 'system', content: 'no' }]), /Conversation context/);
  assert.throws(() => request('x', history('')), /Conversation context/);
  assert.doesNotThrow(() => request('x'.repeat(3000), history('x'.repeat(1096)), 'x'.repeat(8192)));
  assert.throws(() => request('x'.repeat(3000), history('x'.repeat(1097)), 'x'.repeat(8192)), /12288 UTF-8/);
  assert.throws(() => request('x', [], '\u0000'.repeat(8192)), /encoded request/);
  assert.ok(bytes(request()) < 32768);
});

test('history retains complete contiguous messages, never truncated content', () => {
  const entries = Array.from({ length: 8 }, (_, index) => ({ role: 'user', content: `${index}` }));
  assert.deepEqual(boundedHistory(entries), entries.slice(-6));
  assert.deepEqual(boundedHistory([...entries, ...history('x'.repeat(2001))]), []);
  assert.deepEqual(boundedHistory([...history('x'.repeat(2001)), ...entries.slice(-2)]), entries.slice(-2));
});

test('all documented error codes have actionable safe text and preserve cooldown', async () => {
  const codes = [
    'INVALID_REQUEST', 'INVALID_SESSION', 'CHALLENGE_FAILED', 'ORIGIN_DENIED', 'QUOTA_EXCEEDED',
    'REQUEST_DUPLICATE', 'BUSY', 'DISABLED', 'CONFIGURATION_REQUIRED', 'RUNTIME_UNVERIFIED',
    'CLIENT_ID_UNVERIFIED', 'KNOWLEDGE_UNAVAILABLE', 'STORAGE_UNAVAILABLE', 'MODEL_UNAVAILABLE',
    'MODEL_TIMEOUT', 'INVALID_OUTPUT',
  ];
  for (const code of codes) {
    const error = await serviceError(Response.json({
      version: 1, error: { code, message: 'private-canary', retryAfterSeconds: 3 },
    }, { status: 503 }));
    assert.equal(error.retryAfterSeconds, 3, code);
    assert.doesNotMatch(error.message, /private-canary|request failed/, code);
  }
  await assert.rejects(serviceError(Response.json({ error: {} }, { status: 500 })), /invalid error/);
  await assert.rejects(serviceError(Response.json({
    version: 1, error: { code: 'BUSY', message: 'x', retryAfterSeconds: -1 },
  }, { status: 503 })), /invalid retry/);
});

test('bare host auth and non-JSON responses cancel their bodies without exposing content', async () => {
  for (const status of [401, 403, 429, 503]) {
    let cancelled = false;
    const response = new Response(new ReadableStream({ cancel() { cancelled = true; } }), { status });
    const error = await serviceError(response);
    assert.ok(error.message);
    assert.equal(cancelled, true);
  }
  let cancelled = false;
  await assert.rejects(json(new Response(new ReadableStream({ cancel() { cancelled = true; } }))), /non-JSON/);
  assert.equal(cancelled, true);
});

test('JSON reader bounds error and success bodies and cancels an oversized stream', async () => {
  let cancelled = false;
  const response = new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode('x'.repeat(65537))); },
    cancel() { cancelled = true; },
  }), { status: 503, headers: { 'Content-Type': 'application/json' } });
  await assert.rejects(serviceError(response), /oversized/);
  assert.equal(cancelled, true);
  await assert.rejects(json(new Response('{broken', { headers: { 'Content-Type': 'application/json' } })), /invalid response/);
});

test('answer requires provenance and allows only canonical documentation citations', () => {
  const valid = {
    version: 1, answer: 'Plain text', citations: [],
    knowledge: { commit: 'a'.repeat(40), fingerprint: 'b'.repeat(64) },
  };
  assert.equal(answer(valid), true);
  for (const url of [
    'javascript:alert(1)', 'data:text/html,x', 'https://evil.example/waza/',
    'https://microsoft.github.io/another-project/', 'https://microsoft.github.io/waza/../../evil/',
    'https://user@microsoft.github.io/waza/', 'https://microsoft.github.io/waza/?key=x',
  ]) {
    assert.equal(answer({ ...valid, citations: [{ id: 'x', title: 'Title', url }] }), false, url);
  }
  assert.equal(answer({ ...valid, citations: [{
    id: 'cli', title: '<unsafe> is text', url: 'https://microsoft.github.io/waza/reference/cli/#waza-run',
  }] }), true);
  assert.equal(answer({ ...valid, knowledge: undefined }), false);
  assert.equal(answer({ ...valid, validation: { valid: false, errors: ['Syntax only'] } }), true);
});
