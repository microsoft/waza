import { createHash, randomBytes, timingSafeEqual } from 'node:crypto';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, open, lstat, unlink, rmdir } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { answer, json, object, requestBody, serviceError } from '../src/scripts/chat-contract.ts';

export const upstream = 'https://func-chat-fskwox3ttikuc.azurewebsites.net/api/ask';
export const adapterPath = '/waza/__private-chat';
const scope = [
  '--subscription', '5ca082f3-38f2-4bb0-b0a4-c401184ae3a8',
  '--resource-group', 'rg-waza-chat-preview', '--name', 'func-chat-fskwox3ttikuc',
  '--function-name', 'ask',
];
const execute = promisify(execFile);

export async function azureKey(operation, name) {
  const args = ['functionapp', 'function', 'keys', operation, ...scope, '--only-show-errors'];
  if (operation !== 'list') args.push('--key-name', name);
  args.push('--output', operation === 'list' ? 'json' : 'none');
  try {
    // Capture both streams. Never print CLI output/errors, which may contain key material.
    const { stdout } = await execute('az', args, {
      timeout: 30000, maxBuffer: 1024 * 1024,
      env: { ...process.env, AZURE_CORE_COLLECT_TELEMETRY: 'false', AZURE_CORE_LOG_LEVEL: 'critical' },
    });
    if (operation === 'list') {
      const keys = JSON.parse(stdout);
      if (!object(keys) || typeof keys[name] !== 'string' || !keys[name]) throw new Error();
      return keys[name];
    }
  } catch {
    throw new Error(`Ask-scoped key ${operation} failed; no Azure output was logged.`);
  }
}

export async function temporaryKey(run = azureKey) {
  const name = `docs-local-${randomBytes(12).toString('hex')}`;
  let key;
  const revoke = async () => { await run('delete', name); key = undefined; };
  try {
    await run('set', name);
    key = await run('list', name);
    return { name, value: () => key, revoke };
  } catch {
    try { await revoke(); } catch {
      throw new Error(`Preview startup failed; manual ask-key revocation required: ${name}`);
    }
    throw new Error('Preview key creation failed; temporary ask key revoked.');
  }
}

export async function loginFile() {
  const directory = await mkdtemp(join(tmpdir(), 'waza-private-preview-'));
  const path = join(directory, 'login.json');
  let handle;
  try {
    handle = await open(path, 'wx', 0o600);
    const identity = await handle.stat();
    const login = { username: 'waza-preview', password: randomBytes(32).toString('base64url') };
    await handle.writeFile(JSON.stringify(login));
    await handle.close();
    handle = undefined;
    return {
      path, login,
      async remove() {
        const current = await lstat(path);
        if (current.dev !== identity.dev || current.ino !== identity.ino || !current.isFile()) {
          throw new Error('Preview login was replaced; refusing to remove it.');
        }
        await unlink(path);
        await rmdir(directory);
      },
    };
  } catch {
    if (handle) await handle.close();
    // Leave uncertain entries alone rather than deleting replacements.
    throw new Error(`Could not create private preview login. Inspect residue at ${directory}`);
  }
}

export function askBody(value) {
  if (!object(value) || Object.keys(value).some((key) => !['question', 'history', 'requestId', 'document'].includes(key))
      || typeof value.question !== 'string' || !Array.isArray(value.history)
      || typeof value.requestId !== 'string' || !/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value.requestId)
      || value.history.some((entry) => !object(entry) || Object.keys(entry).some((key) => !['role', 'content'].includes(key))
        || !['user', 'assistant'].includes(entry.role) || typeof entry.content !== 'string')) {
    throw new Error('Invalid private preview request.');
  }
  const document = value.document;
  if (document !== undefined && (!object(document)
      || Object.keys(document).some((key) => !['kind', 'yaml'].includes(key))
      || typeof document.kind !== 'string' || typeof document.yaml !== 'string' || !document.yaml.trim())) {
    throw new Error('Invalid private preview document.');
  }
  const validated = JSON.parse(requestBody(value.question, value.history, document?.kind ?? 'eval', document?.yaml ?? '', value.requestId));
  delete validated.requestId;
  return JSON.stringify(validated);
}

function reply(res, status, value) {
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(JSON.stringify(value));
}
const failure = (code, message) => ({ version: 1, error: { code, message } });

async function readBody(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 32768) throw new Error('Request exceeds 32768 bytes.');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

export function privatePlugin({ port, login, key, fetchImpl = fetch }) {
  const origin = `http://127.0.0.1:${port}`;
  const expected = createHash('sha256').update(`Basic ${Buffer.from(`${login.username}:${login.password}`).toString('base64')}`).digest();
  let busy = false;
  let stopping = false;
  const controllers = new Set();
  const paths = new WeakMap();
  const admitted = (req) => req.socket.remoteAddress === '127.0.0.1'
    && req.headers.host === `127.0.0.1:${port}`
    && (req.headers.origin === undefined || req.headers.origin === origin)
    && req.headers['sec-fetch-site'] !== 'cross-site';
  const authenticated = (req) => timingSafeEqual(expected,
    createHash('sha256').update(req.headers.authorization ?? '').digest());
  const plugin = {
    name: 'waza-authenticated-private-preview',
    enforce: 'pre',
    configureServer(server) {
      server.httpServer.headersTimeout = 10000;
      server.httpServer.requestTimeout = 15000;
      const upgrades = server.httpServer.listeners('upgrade');
      for (const listener of upgrades) server.httpServer.removeListener('upgrade', listener);
      server.httpServer.on('upgrade', (req, socket, head) => {
        if (!admitted(req) || !authenticated(req) || req.headers.origin !== origin) {
          socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
          return;
        }
        for (const listener of upgrades) listener.call(server.httpServer, req, socket, head);
        if (!upgrades.length) socket.destroy();
      });
      const listeners = server.httpServer.listeners('request');
      if (listeners.length !== 1) throw new Error('Cannot establish the preview authentication boundary.');
      const guarded = (req, res) => {
        res.setHeader('Cache-Control', 'no-store');
        res.setHeader('Referrer-Policy', 'no-referrer');
        if (!admitted(req)) {
          reply(res, 403, failure('ORIGIN_DENIED', 'Private preview requires the exact loopback origin.'));
          return;
        }
        if (!authenticated(req)) {
          res.setHeader('WWW-Authenticate', 'Basic realm="Waza private preview", charset="UTF-8"');
          reply(res, 401, failure('INVALID_SESSION', 'Private preview login required.'));
          return;
        }
        paths.set(req, req.url);
        listeners[0].call(server.httpServer, req, res);
      };
      // Guard the HTTP entrypoint, including Vite's outer base/redirect middleware.
      server.httpServer.removeListener('request', listeners[0]);
      server.httpServer.on('request', guarded);
      server.middlewares.use((req, res, next) => {
        const path = paths.get(req);
        if (path !== adapterPath) {
          if (!['GET', 'HEAD'].includes(req.method)) {
            reply(res, 405, failure('INVALID_REQUEST', 'Method not allowed.'));
          } else {
            // Never expose the launcher, tests, or credential-management source through Vite.
            if (/private-chat|\/tests\/|\/@fs\//i.test(path)) {
              reply(res, 404, failure('INVALID_REQUEST', 'Not found.'));
            } else next();
          }
          return;
        }
        if (req.method !== 'POST' || req.headers.origin !== origin
            || !/^application\/json(?:\s*;|$)/i.test(req.headers['content-type'] ?? '')) {
          reply(res, 403, failure('ORIGIN_DENIED', 'Same-origin JSON POST required.'));
          return;
        }
        if (stopping || busy) {
          reply(res, 503, failure('BUSY', 'A request is active or the preview is stopping.'));
          return;
        }
        if (Number(req.headers['content-length']) > 32768) {
          reply(res, 413, failure('INVALID_REQUEST', 'Request exceeds 32768 bytes.'));
          return;
        }
        busy = true;
        const controller = new AbortController();
        controllers.add(controller);
        const timeout = setTimeout(() => controller.abort(), 45000);
        const abortUpload = () => { if (!req.complete) req.destroy(); };
        controller.signal.addEventListener('abort', abortUpload, { once: true });
        const disconnected = () => { if (!res.writableEnded) controller.abort(); };
        res.on('close', disconnected);
        void (async () => {
          try {
            let body;
            try { body = askBody(await readBody(req)); } catch {
              reply(res, 400, failure('INVALID_REQUEST', 'Invalid request or input limits exceeded.'));
              return;
            }
            if (controller.signal.aborted) return;
            const functionKey = key();
            if (!functionKey) throw new Error();
            const response = await fetchImpl(upstream, {
              method: 'POST', redirect: 'error', signal: controller.signal,
              headers: { 'Content-Type': 'application/json', 'x-functions-key': functionKey },
              body,
            });
            if (!response.ok) {
              const error = await serviceError(response);
              const value = failure(
                response.status === 429 ? 'QUOTA_EXCEEDED' : 'MODEL_UNAVAILABLE',
                'Operator preview failed. No automatic retry was made.',
              );
              if (error.retryAfterSeconds !== undefined) value.error.retryAfterSeconds = error.retryAfterSeconds;
              reply(res, response.status === 429 ? 429 : 502, value);
              return;
            }
            const value = await json(response);
            if (!answer(value) || JSON.stringify(value).includes(functionKey)) throw new Error();
            if (!controller.signal.aborted) reply(res, 200, value);
          } catch {
            if (!res.destroyed) reply(res, 502, failure('MODEL_UNAVAILABLE', 'Operator preview failed or timed out. No automatic retry was made.'));
          } finally {
            clearTimeout(timeout);
            controller.signal.removeEventListener('abort', abortUpload);
            res.off('close', disconnected);
            controllers.delete(controller);
            busy = false;
          }
        })();
      });
      return () => {
        if (server.httpServer.listeners('request').length !== 1
            || server.httpServer.listeners('request')[0] !== guarded) {
          throw new Error('Preview HTTP authentication boundary was replaced.');
        }
      };
    },
  };
  return {
    plugin,
    stop() {
      stopping = true;
      for (const controller of controllers) controller.abort();
    },
  };
}

export async function startPrivateServer({ port, login, key, fetchImpl }) {
  const { dev } = await import('astro');
  const adapter = privatePlugin({ port, login, key, fetchImpl });
  process.env.PUBLIC_CHAT_PRIVATE_PREVIEW = 'true';
  process.env.PUBLIC_CHAT_FIXTURES = 'false';
  // Vite otherwise calls process.exit on SIGTERM before our asynchronous key revocation.
  // This dedicated preview process owns shutdown; disable its interactive exit shortcuts too.
  process.env.CI = 'true';
  const signals = new Map(['SIGINT', 'SIGTERM'].map((signal) => [signal, new Set(process.listeners(signal))]));
  let server;
  try { server = await dev({
    root: fileURLToPath(new URL('../', import.meta.url)),
    logLevel: 'silent',
    devToolbar: { enabled: false },
    server: { host: '127.0.0.1', port },
    vite: {
      plugins: [adapter.plugin],
      server: { host: '127.0.0.1', port, strictPort: true, hmr: false, cors: false },
    },
  }); } finally {
    for (const [signal, existing] of signals) {
      for (const listener of process.listeners(signal)) {
        if (!existing.has(listener)) process.off(signal, listener);
      }
    }
  }
  return { server, adapter };
}
