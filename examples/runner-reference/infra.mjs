import { spawn } from 'node:child_process';
import { createHash, randomUUID } from 'node:crypto';
import { createReadStream } from 'node:fs';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';

export const registry = 'ghcr.io/microsoft/waza-runner-reference';
export const digestPattern = /^sha256:[a-f0-9]{64}$/;

export function errorText(error) {
  if (error instanceof AggregateError) {
    return `${error.message}\n${error.errors.map(errorText).join('\n')}`;
  }
  return error instanceof Error ? error.message : String(error);
}

export function imageReference(value, local = false) {
  const pattern = local
    ? digestPattern
    : /^ghcr\.io\/microsoft\/waza-runner-reference@sha256:[a-f0-9]{64}$/;
  if (typeof value !== 'string' || !pattern.test(value)) {
    throw new Error(local
      ? 'Local mode requires a complete sha256 image ID; this is not a published pin'
      : `Expected ${registry}@sha256:<64 lowercase hex>; tags and local IDs are not published pins`);
  }
  return value;
}

export async function hashFile(file) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest('hex');
}

// Bound both pipes and kill the local process group, not just its shell parent.
export function processResult(command, args, {
  input = '', cwd, env = process.env, timeout = 90_000, maxBytes = 8 << 20,
} = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd, env, detached: process.platform !== 'win32', stdio: ['pipe', 'pipe', 'pipe'],
    });
    const stdout = [];
    const stderr = [];
    let bytes = 0;
    let failure;
    let escalation;
    const signal = (name) => {
      if (!child.pid) return;
      try {
        process.kill(process.platform === 'win32' ? child.pid : -child.pid, name);
      } catch (error) {
        if (error.code !== 'ESRCH') failure ??= error;
      }
    };
    const stop = (error) => {
      if (failure) return;
      failure = error;
      signal('SIGTERM');
      escalation = setTimeout(() => signal('SIGKILL'), 250);
    };
    const timer = setTimeout(() => stop(new Error(`${command} exceeded ${timeout} ms`)), timeout);
    for (const [stream, chunks] of [[child.stdout, stdout], [child.stderr, stderr]]) {
      stream.on('data', (chunk) => {
        bytes += chunk.length;
        if (bytes > maxBytes) stop(new Error(`${command} exceeded ${maxBytes} output bytes`));
        else chunks.push(chunk);
      });
    }
    child.stdin.on('error', (error) => {
      // Early stdin closure belongs to the child's captured status/result.
      if (error.code !== 'EPIPE' && error.code !== 'ERR_STREAM_DESTROYED') stop(error);
    });
    child.on('error', (error) => { failure ??= error; });
    child.on('close', (status, terminationSignal) => {
      if (failure) signal('SIGKILL');
      clearTimeout(timer);
      clearTimeout(escalation);
      if (failure) reject(failure);
      else resolve({
        status, signal: terminationSignal,
        stdout: Buffer.concat(stdout).toString('utf8'),
        stderr: Buffer.concat(stderr).toString('utf8'),
      });
    });
    child.stdin.end(input);
  });
}

export async function checked(command, args, options) {
  const result = await processResult(command, args, options);
  if (result.status !== 0 || result.signal) {
    throw new Error(`${command} ${args[0] ?? ''} failed (${result.status ?? result.signal}): ${result.stderr}`);
  }
  return result.stdout.trim();
}

export function runOptions() {
  return [
    '--platform', 'linux/amd64', '--network=none', '--read-only',
    '--cap-drop=ALL', '--security-opt=no-new-privileges',
    '--memory=256m', '--pids-limit=64',
    '--tmpfs', '/work:rw,noexec,nosuid,size=32m,uid=65532,gid=65532,mode=0700',
  ];
}

export async function invokeContainer(image, args, input) {
  const name = `waza-reference-${randomUUID()}`;
  let id;
  let result;
  let primaryError;
  try {
    id = await checked('docker', [
      'create', '--name', name, '--interactive', ...runOptions(),
      digestPattern.test(image) ? '--pull=never' : '--pull=missing', image, ...args,
    ], { timeout: 30_000 });
    if (!/^[a-f0-9]{64}$/.test(id)) {
      throw new Error(`Docker did not return a container ID; inspect invocation name ${name}`);
    }
    result = await processResult('docker', ['start', '--attach', '--interactive', id], { input });
  } catch (error) {
    primaryError = new Error(`Container invocation ${name}: ${error.message}`, { cause: error });
  }
  if (/^[a-f0-9]{64}$/.test(id ?? '')) {
    try {
      await checked('docker', ['rm', '--force', id], { timeout: 15_000 });
    } catch (error) {
      primaryError = primaryError
        ? new AggregateError([primaryError, error], `Invocation failed and cleanup failed for container ${id}`)
        : new Error(`Cleanup failed for container ${id}: ${error.message}`, { cause: error });
    }
  }
  if (primaryError) throw primaryError;
  return result;
}

export async function temporaryDirectory(prefix, action) {
  const directory = await mkdtemp(path.join(tmpdir(), prefix));
  let result;
  let failure;
  try {
    result = await action(directory);
  } catch (error) {
    failure = error;
  }
  try {
    await rm(directory, { recursive: true });
  } catch (error) {
    throw new AggregateError([...(failure ? [failure] : []), error], `Cleanup failed for temporary directory ${directory}`);
  }
  if (failure) throw failure;
  return result;
}
