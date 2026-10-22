import { fileURLToPath } from 'node:url';
import { resolve } from 'node:path';
import { loginFile, PreviewLoginError, startPrivateServer, temporaryKey } from './private-chat-server.mjs';

export async function launchPreview({ createLogin = loginFile, createLease = temporaryKey, port = 4324 } = {}) {
  let credentials;
  let lease;
  let server;
  let adapter;
  let shutdown;
  let ready = false;
  let stopRequested = false;
  async function stop() {
    if (shutdown) return shutdown;
    shutdown = (async () => {
      adapter?.stop();
      let failed = false;
      try { await lease?.revoke(); } catch {
        failed = true;
        console.error(`Ask-key revocation failed. Revoke the ask-scoped key manually: ${lease.name}`);
      }
      try { await server?.stop(); } catch {
        failed = true;
        console.error('Preview server shutdown failed.');
      }
      try { await credentials?.remove(); } catch {
        failed = true;
        console.error(`Login cleanup failed. Inspect: ${credentials.path}`);
      }
      process.exitCode = failed ? 1 : 0;
    })();
    return shutdown;
  }
  for (const signal of ['SIGINT', 'SIGTERM']) {
    process.on(signal, () => {
      stopRequested = true;
      if (ready) void stop();
    });
  }
  try {
    credentials = await createLogin();
    lease = await createLease();
    console.log(`Temporary ask key name (not value): ${lease.name}`);
    ({ server, adapter } = await startPrivateServer({ port, login: credentials.login, key: lease.value }));
    ready = true;
    if (stopRequested) await stop();
    else {
      console.log(`Private docs preview: http://127.0.0.1:${port}/waza/quick-start/`);
      console.log(`Browser Basic-auth login file (0600): ${credentials.path}`);
      console.log(`Preview PID: ${process.pid}`);
      console.log('Stop with SIGTERM to this PID; shutdown revokes the key and removes the login file.');
    }
  } catch (error) {
    console.error(error instanceof PreviewLoginError
      || (error instanceof Error && error.message.startsWith('Preview startup failed; manual ask-key'))
      ? error.message : 'Private preview startup failed. No Azure output or credentials were logged.');
    await stop();
    process.exitCode = 1;
  }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await launchPreview();
}
