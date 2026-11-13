import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const hash = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const blob = bytes => crypto.createHash('sha1').update(`blob ${bytes.length}\0`).update(bytes).digest('hex');
const shaPattern = /^[a-f0-9]{40}$/;
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const containerIDPattern = /^[a-f0-9]{64}$/;
const configPath = '/buildkitd.toml';
export const buildkitConfig = 'insecure-entitlements = []\n\n[worker.oci]\nmax-parallelism = 1\ngc = true\nreservedSpace = "1GB"\nmaxUsedSpace = "16GB"\nminFreeSpace = "4GB"\n';
export const order = [['A', 'B'], ['B', 'A'], ['A', 'B']];
// A sample may overlap at most this many five-second ticks (bounded by the 15s probe timeout) before stopping.
export const maxSkippedSamplerTicks = 3;
export const baselineSHA = 'eda88ccd7d2a59fefafa7662a95828c9977dbc16';
export const markerPrefix = '<!-- waza-cache-measurement:';
const noncePattern = '[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}';
const markerPattern = new RegExp(`^<!-- waza-cache-measurement:v1 candidate=([a-f0-9]{40}) baseline=(${baselineSHA}) request=(${noncePattern}) -->$`, 'm');

export function activation(event, actor, attempt) {
  const pr = event?.pull_request;
  const before = event?.changes?.body?.from;
  const after = pr?.body;
  if (event?.action !== 'edited' || typeof before !== 'string' || typeof after !== 'string' ||
      before.includes(markerPrefix) || !after.includes(markerPrefix) || actor !== 'spboyer' ||
      event.sender?.login !== actor || attempt !== '1' || event.repository?.full_name !== 'microsoft/waza' ||
      pr?.head?.repo?.full_name !== 'microsoft/waza' || pr?.base?.repo?.full_name !== 'microsoft/waza' ||
      event.number !== 836 || pr.number !== 836 || pr.base.ref !== 'main' || pr.state !== 'open') return undefined;
  const matches = [...after.matchAll(new RegExp(markerPattern.source, 'gm'))];
  if (after.split(markerPrefix).length !== 2 || matches.length !== 1 || matches[0][1] !== pr.head.sha) return undefined;
  return { candidate: matches[0][1], baseline: matches[0][2], nonce: matches[0][3], pr: pr.number };
}

export function admitLivePR(live, selected) {
  const markers = typeof live.body === 'string' ? [...live.body.matchAll(new RegExp(markerPattern.source, 'gm'))] : [];
  if (live.state !== 'open' || live.merged_at || live.head?.sha !== selected.candidate ||
      live.head?.repo?.full_name !== 'microsoft/waza' || live.base?.ref !== 'main' ||
      live.base?.repo?.full_name !== 'microsoft/waza' || live.auto_merge ||
      live.body?.split(markerPrefix).length !== 2 || markers.length !== 1 || markers[0][1] !== selected.candidate ||
      markers[0][2] !== selected.baseline || markers[0][3] !== selected.nonce) throw new Error('live PR source/activation changed');
}

export function request(env, event) {
  if (env.GITHUB_ACTIONS !== 'true' || env.GITHUB_EVENT_NAME !== 'pull_request' ||
      env.GITHUB_REPOSITORY !== 'microsoft/waza') throw new Error('explicit edited-body measurement required');
  const selected = activation(event, env.GITHUB_ACTOR, env.GITHUB_RUN_ATTEMPT);
  if (!selected || selected.baseline === selected.candidate || !shaPattern.test(selected.candidate)) throw new Error('invalid rising-edge activation');
  if (!/^\d+$/.test(env.GITHUB_RUN_ID ?? '') || !/^\d+$/.test(env.GITHUB_RUN_ATTEMPT ?? '')) throw new Error('invalid invocation identity');
  const started = Number(env.CACHE_START_SECONDS) * 1000;
  if (!Number.isSafeInteger(started) || started <= 0 || started > Date.now() || Date.now() - started >= 3600000) throw new Error('invalid/exhausted total budget');
  return { ...selected, deadline: started + 3600000,
    owner: `waza835-${env.GITHUB_RUN_ID}-${env.GITHUB_RUN_ATTEMPT}` };
}

// Docker 28 reports an unset volume driver as an empty DriverConfig object.
function defaultDriverConfig(config) {
  if (config === undefined || config === null) return true;
  if (typeof config !== 'object' || Array.isArray(config)) return false;
  return Object.keys(config).every(k => k === 'Name' || k === 'Options') && !config.Name &&
    (config.Options === undefined || config.Options === null || (typeof config.Options === 'object' && !Object.keys(config.Options).length));
}

export function admitPath(name, mode) {
  if (!name || path.isAbsolute(name) || name.split('/').some(p => !p || p === '..') || !['100644', '100755'].includes(mode)) throw new Error(`nonregular/nonlocal input: ${name}`);
  if (/(^|\/)(\.git($|\/)|\.env($|\.)|\.npmrc$|\.netrc$|id_rsa$)|\.(pem|key|p12|pfx)$/.test(name)) throw new Error(`sensitive input name rejected without reading: ${name}`);
}

export function selectedPath(name, differences) {
  return differences.includes(name) || ['.dockerignore', 'go.mod', 'go.sum', 'docs.go', 'version.txt'].includes(name) ||
    /^(cmd|internal|web|schemas|docs\/graders)\//.test(name) ||
    /^examples\/runner\/(unit|copilot-unit)\.json$/.test(name) ||
    /^\.github\/scripts\/testdata\/(runner-runtime|native-bundle-probe)\//.test(name);
}

export function admitBytes(entry, bytes, pointer) {
  if (pointer.startsWith('version https://git-lfs.github.com/spec/v1\n')) {
    const m = /^version https:\/\/git-lfs.github.com\/spec\/v1\noid sha256:([a-f0-9]{64})\nsize (\d+)\n$/.exec(pointer);
    if (!m || blob(Buffer.from(pointer)) !== entry.oid || hash(bytes) !== m[1] || bytes.length !== Number(m[2])) throw new Error(`LFS payload mismatch: ${entry.path}`);
    return { sha256: hash(bytes), lfs: m[1], bytes: bytes.length };
  }
  if (blob(bytes) !== entry.oid) throw new Error(`Git blob mismatch: ${entry.path}`);
  return { sha256: hash(bytes), bytes: bytes.length };
}

export function nativeDigest(index) {
  const matches = index.manifests?.filter(m => m.platform?.os === 'linux' && m.platform?.architecture === 'amd64');
  if (matches?.length !== 1 || !digestPattern.test(matches[0].digest)) throw new Error('one explicit native AMD64 manifest required');
  return matches[0].digest;
}

function admitLimits({ quota, period, memory, swap }) {
  if (quota !== 200000 || period !== 100000 || memory !== 8589934592 || swap !== 0) throw new Error('resource/storage admission failed');
}
function vanishedBuildkitPaths(stderr) {
  const lines = String(stderr ?? '').split('\n').filter(Boolean);
  return lines.length > 0 && lines.every(l => /^du: \/var\/lib\/buildkit\/\S+: No such file or directory$/.test(l));
}
export function admitResources({ quota, period, memory, swap, hostFree, owned }) {
  admitLimits({ quota, period, memory, swap });
  if (!Number.isFinite(hostFree) || hostFree < 20 * 1024 ** 3 || !Number.isFinite(owned) || owned > 18 * 1024 ** 3) throw new Error('resource/storage admission failed');
}

export function compileEvidence(text, warm) {
  const step = /^#(\d+) \[builder \d+\/\d+\] RUN .*CGO_ENABLED=0 GOOS=linux go build .+$/m.exec(text);
  if (!step) throw new Error('production compile absent');
  const done = new RegExp(`^#${step[1]} DONE ([0-9.]+)s$`, 'm').exec(text);
  if (!done || new RegExp(`^#${step[1]} CACHED$`, 'm').test(text)) throw new Error('compile did not execute');
  const web = [];
  for (const command of ['npm ci --silent', 'npm run build']) {
    const id = /^#(\d+) \[web-builder \d+\/\d+\] RUN (.+)$/gm;
    const found = [...text.matchAll(id)].find(m => m[2] === command);
    if (!found) throw new Error(`web step absent: ${command}`);
    const cached = new RegExp(`^#${found[1]} CACHED$`, 'm').test(text);
    if (warm && !cached) throw new Error('warm web/npm reuse absent');
    web.push({ command, cached });
  }
  return { compileSeconds: Number(done[1]), compileStep: step[0], web };
}

export function pinEvidence(text, pins) {
  const escape = value => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const frontend = pins['docker/dockerfile:1'];
  if (!new RegExp(`^#\\d+ resolve image config for docker-image://(?:docker\\.io/)?${escape(frontend)}\\r?$`, 'm').test(text)) throw new Error('effective pinned frontend not observed');
  for (const name of ['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19']) {
    if (!new RegExp(`^#\\d+ \\[context ${escape(name)}\\] load metadata for ${escape(pins[name])}\\r?$`, 'm').test(text) ||
        !new RegExp(`^#\\d+ resolve (?:docker\\.io/library/)?${escape(pins[name])}(?: [0-9.]+s)? done\\r?$`, 'm').test(text)) throw new Error(`effective pinned base not observed: ${name}`);
  }
  return { frontend, bases: Object.fromEntries(['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19'].map(name => [name, pins[name]])) };
}

export function pairedSummary(attempts) {
  const keys = new Set(attempts.map(a => `${a.round}-${a.variant}-${a.phase}`));
  if (attempts.length !== 12 || keys.size !== 12 || attempts.some(a => a.code !== 0 || !a.runtimePassed || !a.evidence || !a.assets?.length)) return { complete: false, reason: 'three complete cold/warm paired rounds required', attempts };
  const pairs = order.map((_, i) => Object.fromEntries(['cold', 'warm'].map(phase => {
    const A = attempts.find(a => a.round === i + 1 && a.variant === 'A' && a.phase === phase);
    const B = attempts.find(a => a.round === i + 1 && a.variant === 'B' && a.phase === phase);
    if (!A || !B) throw new Error('paired inventory incomplete');
    return [phase, { baselineSeconds: A.wallSeconds, candidateSeconds: B.wallSeconds, differenceSeconds: B.wallSeconds - A.wallSeconds,
      baselineCompileSeconds: A.evidence.compileSeconds, candidateCompileSeconds: B.evidence.compileSeconds }];
  })));
  const median = xs => [...xs].sort((a, b) => a - b)[1];
  const referenceAssets = JSON.stringify(attempts[0].assets);
  return { complete: true, pairs, generatedAssetBytesEqual: attempts.every(a => JSON.stringify(a.assets) === referenceAssets),
    medians: Object.fromEntries(['cold', 'warm'].map(p => [p, {
    baselineSeconds: median(pairs.map(r => r[p].baselineSeconds)), candidateSeconds: median(pairs.map(r => r[p].candidateSeconds)),
    differenceSeconds: median(pairs.map(r => r[p].differenceSeconds)),
  }])), qualification: 'Observed native shared-host comparison only; no automatic acceptance threshold or universal reproducibility claim.' };
}

export function redact(text) {
  return text.replace(/https?:\/\/[^\s?"')]+(?:\?[^\s]*)?/g, value => {
    const query = value.indexOf('?');
    const u = new URL(query === -1 ? value : value.slice(0, query));
    return u.origin + u.pathname + (query === -1 ? '' : '?[query-redacted]');
  });
}

function publicJSON(value, space) {
  return JSON.stringify(value, (_key, item) => typeof item === 'string' ? redact(item) : item, space);
}

export function admitNativeHost(platform, architecture) {
  if (platform !== 'linux' || architecture !== 'x64') throw new Error('native Linux AMD64 host required');
}

function storageFree(output) {
  const values = output.trim().split('\n').slice(1).map(r => Number(r.trim().split(/\s+/)[3]) * 1024);
  if (values.length !== 2 || values.some(n => !Number.isFinite(n))) throw new Error('both host/Docker storage samples required');
  return Math.min(...values);
}

export class Measurement {
  constructor(config, root, repository, launch = spawn) {
    this.config = config;
    this.root = root;
    this.repo = repository;
    this.spawn = launch;
    this.privateRoot = path.join(root, '.private-raw');
    fs.mkdirSync(this.privateRoot, { mode: 0o700 });
    this.privateIdentity = fs.statSync(this.privateRoot);
    this.buildxRoot = path.join(this.privateRoot, 'buildx-config');
    this.state = { ...config, attempts: [], builders: [], images: [], containers: [], contexts: [], failures: [], pins: {},
      retainedDirectories: [], residue: [], clients: [],
      filesystemCleanupQualification: 'Source/working contexts and private raw directories are retained for ephemeral host teardown; root identity does not establish descendant ownership.',
      sharedImagesQualification: 'Shared pinned tool/base images are retained; initial absence or lack of containers does not establish exclusive ownership.',
      storageQualification: 'Five-second sampled accounting with 18GiB stop/2GiB margin and GC16GB is NOT a hard20GiB quota. Unsampled transient overruns remain possible.',
      coldScope: 'Each variant/round has fresh owned layer/module/compiler state. Identical pinned base images/frontend may be warm. No layer/GHA imports or exports.' };
    this.stopped = undefined;
    this.activeClients = new Map();
    this.activeSamplers = new Set();
  }
  async api(endpoint) {
    const response = await fetch(`https://api.github.com/repos/microsoft/waza/${endpoint}`, {
      headers: { Authorization: `Bearer ${process.env.GITHUB_TOKEN}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
      redirect: 'error', signal: AbortSignal.timeout(Math.min(15000, this.remaining())),
    });
    if (!response.ok) throw new Error(`read-only GitHub admission failed: HTTP ${response.status}`);
    return response.json();
  }
  async authorization() {
    admitLivePR(await this.api('pulls/836'), this.config);
    const artifacts = await this.api(`actions/artifacts?name=waza835-request-${this.config.nonce}&per_page=100`);
    if (!Array.isArray(artifacts.artifacts) || artifacts.total_count !== 0) throw new Error('prior request artifact exists or inventory unavailable');
    let exhausted = false;
    for (let page = 1; page <= 20; page++) {
      const data = await this.api(`actions/workflows/docker-ci.yml/runs?event=pull_request&head_sha=${this.config.candidate}&per_page=100&page=${page}`);
      if (!Array.isArray(data.workflow_runs)) throw new Error('prior candidate run inventory unavailable');
      for (const run of data.workflow_runs) {
        if (String(run.id) === process.env.GITHUB_RUN_ID) continue;
        const jobs = await this.api(`actions/runs/${run.id}/jobs?filter=all&per_page=100`);
        if (!Array.isArray(jobs.jobs) || jobs.total_count > 100) throw new Error('prior job inventory incomplete');
        if (jobs.jobs.some(j => j.name === 'Container cache measurement' && j.conclusion !== 'skipped')) throw new Error('prior candidate measurement run exists');
      }
      if (data.workflow_runs.length < 100) { exhausted = true; break; }
    }
    if (!exhausted) throw new Error('prior candidate run pagination incomplete');
    this.state.authorization = { liveHead: this.config.candidate, nonce: this.config.nonce, priorCandidateRunsChecked: true,
      priorRetainedRequestArtifactsChecked: true, qualification: 'GitHub delivery is not exactly-once. Expired/deleted request artifacts are not an enduring nonce registry.' };
    this.record();
  }
  record() { fs.writeFileSync(path.join(this.root, 'state.json'), publicJSON(this.state, 2) + '\n'); }
  remaining(cleanup = false) {
    const ms = this.config.deadline - Date.now() - (cleanup ? 0 : 120000);
    if (ms <= 0 || (!cleanup && this.stopped)) throw new Error(this.stopped ?? 'total budget exhausted');
    return ms;
  }
  operationRemaining(cleanup = false) {
    const remaining = Math.min(this.remaining(cleanup), cleanup ? Infinity : (this.setupDeadline ?? Infinity) - Date.now());
    if (remaining <= 0) throw new Error('builder startup/readiness deadline exhausted');
    return remaining;
  }
  clientEnvironment(tool, args) {
    if (tool !== 'docker' || args[0] !== 'buildx') return { ...process.env };
    const parent = fs.lstatSync(this.privateRoot);
    if (!parent.isDirectory() || parent.isSymbolicLink() || parent.dev !== this.privateIdentity.dev ||
        parent.ino !== this.privateIdentity.ino || (process.platform !== 'win32' && (parent.mode & 0o777) !== 0o700)) {
      throw new Error('private metadata parent replaced or permissions changed');
    }
    if (!this.buildxIdentity) {
      fs.mkdirSync(this.buildxRoot, { mode: 0o700 });
      this.buildxIdentity = fs.lstatSync(this.buildxRoot);
      this.state.buildxConfig = { dir: this.buildxRoot, dev: this.buildxIdentity.dev, ino: this.buildxIdentity.ino,
        requestedMode: '0700', qualification: 'Exclusive leaf mkdir and path identity checks only; no rooted-handle race or unread Buildx-store guarantee.' };
      this.record();
    }
    const current = fs.lstatSync(this.buildxRoot);
    if (!current.isDirectory() || current.isSymbolicLink() || current.dev !== this.buildxIdentity.dev ||
        current.ino !== this.buildxIdentity.ino || (process.platform !== 'win32' && (current.mode & 0o777) !== 0o700)) {
      throw new Error('private Buildx configuration replaced or permissions changed');
    }
    return { ...process.env, BUILDX_CONFIG: path.resolve(this.buildxRoot) };
  }
  clientsJoined() {
    if (this.activeClients.size || this.activeSamplers.size) throw new Error('owned clients or samplers have not joined before teardown');
  }
  stopClients(reason) {
    for (const client of this.activeClients.values()) client.stop(reason);
  }
  skipSamplerTick(scope, skipped) {
    if (skipped > maxSkippedSamplerTicks) return false;
    (this.state.samplerOverruns ??= []).push({ at: new Date().toISOString(), scope, skipped });
    this.record();
    return true;
  }
  async joinSampler(task) {
    if (!task) return;
    let timer;
    try {
      await Promise.race([task, new Promise((resolve, reject) => {
        timer = setTimeout(() => reject(new Error('resource sampler did not join within five-second grace')),
          Math.max(0, Math.min(5000, this.config.deadline - Date.now())));
      })]);
    } finally { clearTimeout(timer); }
  }
  async sampledCapture(builder, paused, operation) {
    const controller = new AbortController();
    let sampling, failure, value, operationError;
    const stop = message => {
      failure ??= message;
      this.stopped = failure;
      controller.abort();
      this.stopClients(failure);
    };
    let skipped = 0;
    const interval = setInterval(() => {
      if (sampling) {
        if (this.skipSamplerTick('capture', ++skipped)) return;
        stop('capture resource sampler exceeded five-second cadence'); return;
      }
      skipped = 0;
      sampling = Promise.resolve().then(() => paused ? this.samplePaused(builder, controller.signal) : this.sample(builder, controller.signal))
        .catch(e => { if (!controller.signal.aborted) stop(e.message); });
      const task = sampling;
      this.activeSamplers.add(task);
      task.finally(() => { this.activeSamplers.delete(task); if (sampling === task) sampling = undefined; });
    }, 5000);
    try { value = await operation(); } catch (e) { operationError = e; }
    clearInterval(interval);
    controller.abort();
    try {
      await this.joinSampler(sampling);
    } catch (e) {
      failure ??= e.message;
      this.stopped = failure;
      this.state.residue.push({ kind: 'unjoined-capture-sampler', error: failure });
      operationError ??= e;
    }
    if (operationError && failure && !operationError.message.includes(failure)) {
      operationError = new Error(`${operationError.message}; ${failure}`, { cause: operationError });
    } else if (!operationError && failure) operationError = new Error(failure);
    if (operationError) throw operationError;
    return value;
  }
  async runClient(tool, args, { cleanup = false, signal, timeout = 15000, name, sampler, probe = false } = {}) {
    signal?.throwIfAborted();
    const budget = Math.min(timeout, this.operationRemaining(cleanup));
    const environment = this.clientEnvironment(tool, args);
    const began = performance.now();
    const entry = { tool, args, startedAt: new Date().toISOString(), timeoutSeconds: budget / 1000,
      buildxConfig: tool === 'docker' && args[0] === 'buildx' ? environment.BUILDX_CONFIG : undefined, closed: false };
    this.state.clients.push(entry);
    const stdout = [], stderr = [];
    const log = name && path.join(this.privateRoot, `${name}.log`);
    let fd = log ? fs.openSync(log, 'wx') : undefined;
    let child, reason, timer, force, joinTimer, sampleTimer, sampling, bytes = 0, outputLimited = false;
    let finish, capturing = true;
    const terminal = new Promise(resolve => { finish = resolve; });
    const samplingController = new AbortController();
    const grace = () => Math.max(0, Math.min(5000, this.config.deadline - Date.now()));
    const writeLog = () => {
      if (!name) return;
      const raw = fs.readFileSync(log);
      fs.writeFileSync(path.join(this.root, `${name}-raw-digest.json`), JSON.stringify({
        sha256: hash(raw), bytes: raw.length, complete: entry.closed && !entry.unjoined && !outputLimited && !entry.captureError,
        physicalCloseObserved: entry.closed,
      }));
      fs.writeFileSync(path.join(this.root, `${name}.log`), redact(raw.toString('utf8')));
    };
    const finishCapture = () => {
      capturing = false;
      try {
        if (fd !== undefined) { const ownedFD = fd; fd = undefined; fs.closeSync(ownedFD); }
        writeLog();
      } catch (e) {
        reason ??= `log capture finalization failed: ${e.message}`;
        entry.captureError = redact(e.message);
      }
    };
    const send = sig => {
      entry.signals ??= [];
      entry.signals.push({ signal: sig, at: new Date().toISOString() });
      try { child.kill(sig); } catch (e) { entry.killErrors ??= []; entry.killErrors.push(redact(e.message)); }
    };
    const kill = () => {
      if (entry.closed) return;
      send('SIGKILL');
      if (entry.closed) return;
      joinTimer = setTimeout(() => {
        if (entry.closed) return;
        entry.unjoined = true;
        entry.failure = `${probe ? 'resource probe' : 'owned client'} did not join within five-second kill grace`;
        this.stopped = entry.failure;
        this.state.residue.push({ kind: 'unjoined-client', pid: entry.pid, tool, args, log, error: entry.failure });
        capturing = false;
        // Closing our capture handles/unref does not establish client termination.
        child.stdout.destroy?.(); child.stderr.destroy?.(); child.unref?.();
        finishCapture(); finish();
      }, grace());
    };
    const stop = message => {
      if (reason || entry.closed) return;
      reason = message; entry.failure = message; samplingController.abort();
      if (probe) kill();
      else { send('SIGTERM'); force = setTimeout(kill, grace()); }
    };
    try {
      child = this.spawn(tool, args, { cwd: this.repo, env: environment, stdio: ['ignore', 'pipe', 'pipe'] });
    } catch (e) {
      finishCapture();
      entry.failure = `spawn threw before returning a client: ${redact(e.message)}`;
      entry.unjoined = true;
      this.activeClients.set(entry, { stop: () => {} });
      this.state.residue.push({ kind: 'unknown-client-creation', tool, args, error: entry.failure });
      this.record(); throw new Error(entry.failure);
    }
    entry.pid = child.pid;
    this.activeClients.set(entry, { child, stop });
    for (const [stream, chunks] of [[child.stdout, stdout], [child.stderr, stderr]]) stream.on('data', chunk => {
      if (!capturing) return;
      bytes += chunk.length;
      if (bytes > 16 * 1024 ** 2) { outputLimited = true; stop(`${probe ? 'resource probe' : 'command'} output limit`); return; }
      try {
        if (fd !== undefined) {
          let offset = 0;
          while (offset < chunk.length) {
            const written = fs.writeSync(fd, chunk, offset, chunk.length - offset);
            if (written <= 0) throw new Error('log write made no progress');
            offset += written;
          }
        }
        else chunks.push(Buffer.from(chunk));
      } catch (e) { entry.captureError = redact(e.message); stop(`output capture failed: ${e.message}`); }
    });
    child.stdout.once('error', e => { entry.captureError = redact(e.message); stop(`stdout capture failed: ${e.message}`); });
    child.stderr.once('error', e => { entry.captureError = redact(e.message); stop(`stderr capture failed: ${e.message}`); });
    child.once('error', error => { entry.processError = redact(error.message); stop(error.message); });
    child.once('close', (code, exitSignal) => {
      entry.closed = true; entry.code = code; entry.signal = exitSignal;
      if (code === 0 && (!Number.isSafeInteger(entry.pid) || entry.pid <= 0)) reason ??= 'successful exit without observed client PID';
      entry.wallSeconds = (performance.now() - began) / 1000;
      this.activeClients.delete(entry);
      clearTimeout(timer); clearTimeout(force); clearTimeout(joinTimer);
      finishCapture(); finish();
    });
    timer = setTimeout(() => stop(probe ? 'resource probe deadline' : 'command deadline'), budget);
    const abort = () => stop(probe ? 'resource probe cancelled' : 'command cancelled');
    signal?.addEventListener('abort', abort, { once: true });
    if (signal?.aborted) abort();
    let skipped = 0;
    if (sampler) sampleTimer = setInterval(() => {
      if (sampling) {
        if (this.skipSamplerTick(name ?? tool, ++skipped)) return;
        this.stopped = 'resource sampler exceeded five-second cadence'; stop(this.stopped); return;
      }
      skipped = 0;
      sampling = Promise.resolve().then(() => sampler(samplingController.signal)).catch(e => {
        if (!samplingController.signal.aborted) { this.stopped = e.message; stop(e.message); }
      });
      const task = sampling;
      this.activeSamplers.add(task);
      task.finally(() => { this.activeSamplers.delete(task); if (sampling === task) sampling = undefined; });
    }, 5000);
    try {
      await terminal;
      clearInterval(sampleTimer);
      samplingController.abort();
      if (sampling) {
        try {
          await this.joinSampler(sampling);
        } catch (e) {
          reason ??= e.message; this.stopped = reason;
          this.state.residue.push({ kind: 'unjoined-sampler', tool, args, error: reason });
        }
      }
      entry.elapsedSeconds = (performance.now() - began) / 1000;
      this.record();
      const result = { code: entry.code ?? null, signal: entry.signal ?? null, reason: entry.unjoined ? entry.failure : reason,
        wallSeconds: entry.wallSeconds ?? entry.elapsedSeconds, elapsedSeconds: entry.elapsedSeconds, timeoutSeconds: budget / 1000,
        log: name && `${name}.log`, command: [tool, ...args], unjoined: Boolean(entry.unjoined),
        stdout: Buffer.concat(stdout), stderr: Buffer.concat(stderr) };
      if (name) return result;
      if (result.reason || result.code !== 0) throw Object.assign(
        new Error(`${tool} ${args.join(' ')} failed: ${result.reason ?? result.stderr.toString('utf8')}`),
        { code: result.code, stderr: result.stderr.toString('utf8'), probeFailure: result.reason });
      return result;
    } finally {
      clearTimeout(timer); clearTimeout(force); clearTimeout(joinTimer); clearInterval(sampleTimer);
      signal?.removeEventListener('abort', abort);
    }
  }
  async command(tool, args, cleanup = false, encoding = 'utf8') {
    const result = await this.runClient(tool, args, { cleanup });
    return encoding === null ? result.stdout : result.stdout.toString(encoding);
  }
  async probe(tool, args, signal) {
    return (await this.runClient(tool, args, { signal, probe: true })).stdout.toString('utf8');
  }
  async logged(args, name, sampler) {
    const { stdout, stderr, ...result } = await this.runClient('docker', args, { name, sampler, timeout: 360000 });
    return result;
  }
  async snapshot(ref, variant) {
    const dir = path.join(this.root, `context-${variant}`);
    fs.mkdirSync(dir);
    const st = fs.statSync(dir);
    const context = { dir, dev: st.dev, ino: st.ino, variant, bytes: 0, entries: [] };
    this.state.contexts.push(context); this.record();
    const rows = (await this.command('git', ['ls-tree', '-r', '-z', ref])).split('\0').filter(Boolean);
    for (const row of rows) {
      const m = /^(\d+) blob ([a-f0-9]+)\t(.+)$/.exec(row);
      if (!m) throw new Error('unsupported Git tree entry');
      const entry = { mode: m[1], oid: m[2], path: m[3] };
      if (!selectedPath(entry.path, this.state.differencePaths)) continue;
      admitPath(entry.path, entry.mode);
      const originalBytes = await this.command('git', ['cat-file', 'blob', entry.oid], false, null);
      const original = originalBytes.toString('utf8');
      let bytes;
      if (original.startsWith('version https://git-lfs.github.com/spec/v1\n')) {
        const source = path.join(this.repo, entry.path);
        let parent = this.repo;
        for (const part of entry.path.split('/')) {
          parent = path.join(parent, part);
          if (fs.lstatSync(parent).isSymbolicLink()) throw new Error('LFS input ancestry contains a symlink');
        }
        if (!fs.lstatSync(source).isFile()) throw new Error('LFS working input is not regular');
        bytes = fs.readFileSync(source);
      } else {
        bytes = originalBytes;
      }
      const evidence = admitBytes(entry, bytes, original);
      context.bytes += bytes.length;
      if (context.bytes > 2 * 1024 ** 3) throw new Error('context storage admission exceeded');
      const dest = path.join(dir, entry.path);
      fs.mkdirSync(path.dirname(dest), { recursive: true, mode: 0o755 });
      fs.writeFileSync(dest, bytes, { flag: 'wx', mode: entry.mode === '100755' ? 0o755 : 0o644 });
      fs.utimesSync(dest, 1700000000, 1700000000);
      context.entries.push({ ...entry, ...evidence });
      await this.hostStorage();
    }
    if (context.entries.filter(e => e.lfs).length !== 32) throw new Error('all32 actual LFS payloads required');
    fs.writeFileSync(path.join(this.root, `source-${variant}.json`), JSON.stringify({ ref, tree: (await this.command('git', ['rev-parse', `${ref}^{tree}`])).trim(), ...context }, null, 2));
    return context;
  }
  async verifyContext(context, mutation) {
    const s = fs.lstatSync(context.dir);
    if (s.dev !== context.dev || s.ino !== context.ino || s.isSymbolicLink()) throw new Error('admitted input directory replaced');
    const admitted = new Set(context.entries.map(e => e.path));
    if (mutation) admitted.add(mutation.path);
    const walk = relative => {
      for (const name of fs.readdirSync(path.join(context.dir, relative))) {
        const next = path.join(relative, name), stat = fs.lstatSync(path.join(context.dir, next));
        if (stat.isDirectory()) walk(next);
        else if (!stat.isFile() || !admitted.has(next.split(path.sep).join('/'))) throw new Error(`unadmitted input appeared: ${next}`);
      }
    };
    walk('');
    for (const e of context.entries) {
      const file = path.join(context.dir, e.path), stat = fs.lstatSync(file);
      const expectedMode = process.platform === 'win32' ? 0o666 : (e.mode === '100755' ? 0o755 : 0o644);
      if (!stat.isFile() || (stat.mode & 0o777) !== expectedMode ||
          hash(fs.readFileSync(file)) !== e.sha256) throw new Error(`admitted input changed: ${e.path}`);
      await this.hostStorage();
    }
    if (mutation && hash(fs.readFileSync(path.join(context.dir, mutation.path))) !== mutation.sha256) throw new Error('warm Go mutation changed');
  }
  async workingContext(variant, round) {
    const source = this.state.contexts.find(c => c.variant === variant && c.round === undefined);
    await this.verifyContext(source);
    const dir = path.join(this.root, `context-${variant}-r${round}`);
    fs.mkdirSync(dir);
    const st = fs.statSync(dir);
    const context = { dir, dev: st.dev, ino: st.ino, variant, round, bytes: 0, entries: [] };
    this.state.contexts.push(context); this.record();
    for (const entry of source.entries) {
      const bytes = fs.readFileSync(path.join(source.dir, entry.path));
      if (hash(bytes) !== entry.sha256) throw new Error(`immutable source changed: ${entry.path}`);
      const dest = path.join(dir, entry.path);
      fs.mkdirSync(path.dirname(dest), { recursive: true, mode: 0o755 });
      fs.writeFileSync(dest, bytes, { flag: 'wx', mode: entry.mode === '100755' ? 0o755 : 0o644 });
      fs.utimesSync(dest, 1700000000, 1700000000);
      context.bytes += bytes.length; context.entries.push({ ...entry });
      await this.hostStorage();
    }
    await this.verifyContext(context);
    fs.writeFileSync(path.join(this.root, `source-${variant}-r${round}.json`), JSON.stringify(context, null, 2));
    return context;
  }
  async admit() {
    admitNativeHost(process.platform, process.arch);
    if ((await this.command('git', ['rev-parse', 'HEAD'])).trim() !== this.config.candidate || (await this.command('git', ['status', '--porcelain'])).trim()) throw new Error('candidate checkout differs/dirty');
    await this.command('git', ['merge-base', '--is-ancestor', this.config.baseline, this.config.candidate]);
    const info = JSON.parse(await this.command('docker', ['info', '--format', '{{json .}}']));
    if (info.OSType !== 'linux' || !['x86_64', 'amd64'].includes(info.Architecture)) throw new Error('native Docker AMD64 daemon required');
    const endpoint = process.env.DOCKER_HOST ?? JSON.parse(await this.command('docker', ['context', 'inspect']))[0].Endpoints.docker.Host;
    if (endpoint !== 'unix:///var/run/docker.sock' || !path.isAbsolute(info.DockerRootDir)) throw new Error('local native daemon/storage required');
    this.state.host = { architecture: info.Architecture, memory: info.MemTotal, cpu: info.NCPU, dockerRoot: info.DockerRootDir,
      endpoint, dockerVersion: info.ServerVersion, nodeVersion: process.version,
      buildx: (await this.command('docker', ['buildx', 'version'])).trim(), load: (await this.command('uptime', [])).trim() };
    this.state.initialImages = (await this.command('docker', ['image', 'ls', '--no-trunc', '-q'])).trim().split('\n');
    this.state.recipes = Object.fromEntries(['Dockerfile', '.dockerignore', '.github/workflows/docker-ci.yml', '.github/scripts/docker_cache_benchmark.mjs'].map(p => [p, hash(fs.readFileSync(path.join(this.repo, p)))]));
    this.state.differences = await this.command('git', ['diff', '--name-status', this.config.baseline, this.config.candidate]);
    this.state.differencePaths = (await this.command('git', ['diff', '--name-only', this.config.baseline, this.config.candidate])).trim().split('\n');
    const permitted = ['Dockerfile', '.github/scripts/docker_cache_test.go', '.github/workflows/docker-ci.yml',
      '.github/scripts/docker_cache_benchmark.mjs', '.github/scripts/docker_cache_benchmark.test.mjs',
      '.github/scripts/docker_ci_test.go', 'docs/CI-CD-GUIDE.md', 'site/tests/documentation.test.mjs'];
    if (this.state.differencePaths.some(p => !permitted.includes(p))) throw new Error('unapproved source difference');
    if (info.MemTotal < 8589934592 || info.NCPU < 2) throw new Error('host resources unavailable');
    await this.hostStorage(true);
    const A = await this.snapshot(this.config.baseline, 'A'), B = await this.snapshot(this.config.candidate, 'B');
    const b = new Map(B.entries.map(e => [e.path, e]));
    for (const e of A.entries.filter(e => !this.state.differencePaths.includes(e.path))) {
      if (b.get(e.path)?.oid !== e.oid || b.get(e.path)?.mode !== e.mode) throw new Error(`canonical build input changed: ${e.path}`);
    }
    for (const name of ['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19', 'moby/buildkit:buildx-stable-1', 'docker/dockerfile:1']) {
      const index = JSON.parse(await this.command('docker', ['buildx', 'imagetools', 'inspect', name, '--raw']));
      this.state.pins[name] = `${name.split(':')[0]}@${nativeDigest(index)}`;
    }
    fs.writeFileSync(path.join(this.root, 'buildkit.toml'), buildkitConfig, { flag: 'wx' });
    this.record();
  }
  async hostStorage(force = false) {
    if (!force && Date.now() - (this.lastStorage ?? 0) < 5000) return;
    this.lastStorage = Date.now();
    const hostFree = await this.hostFree();
    const owned = Number((await this.command('du', ['-sk', this.root])).split(/\s+/)[0]) * 1024 + this.state.images.reduce((sum, i) => sum + i.size, 0);
    this.recordHostStorage(hostFree, owned);
  }
  recordHostStorage(hostFree, owned) {
    if (!Number.isFinite(hostFree) || hostFree < 20 * 1024 ** 3 || !Number.isFinite(owned) || owned > 18 * 1024 ** 3) throw new Error('host/storage sampler failed admission');
    fs.appendFileSync(path.join(this.root, 'resources.jsonl'), JSON.stringify({ at: new Date().toISOString(), ownedPartial: owned, hostFree,
      scope: 'host/context/images; active builder cache accounted separately' }) + '\n');
  }
  async hostFree() {
    return storageFree(await this.command('df', ['-Pk', this.root, this.state.host.dockerRoot]));
  }
  checkBuilder(builder, x, cleanup = false) {
    if (!builder.creationConfirmed || !builder.id || (cleanup && !builder.imageId)) throw new Error('builder ownership not acquired');
    if (x?.Id !== builder.id || !digestPattern.test(x.Image ?? '') || (builder.imageId && x.Image !== builder.imageId) ||
        x.Image !== builder.pulledImage?.id ||
        x.Config?.Image !== this.state.pins['moby/buildkit:buildx-stable-1'] ||
        !x.Config.Env?.includes(`WAZA835_OWNER=${this.config.owner}`) ||
        x.Config.Labels?.['com.waza.cache.owner'] !== this.config.owner) throw new Error('builder identity changed');
    if (!builder.imageId) { builder.imageId = x.Image; this.record(); }
    return x;
  }
  acquireBuilder(builder, x, volume) {
    if (!builder.ownershipAcquired) {
      const mount = this.cacheMount(x);
      if (volume.Name !== mount.Name || volume.Driver !== 'local' || volume.Scope !== 'local' ||
          typeof volume.CreatedAt !== 'string' || !volume.CreatedAt ||
          Object.keys(volume.Options ?? {}).length || this.state.builders.some(b => b !== builder && b.volume === volume.Name)) throw new Error('fresh anonymous local cache identity unavailable');
      builder.volume = mount.Name; builder.volumeMetadata = volume;
      builder.cacheOrigin = { kind: 'container-coupled anonymous volume', containerID: builder.id,
        qualification: 'Docker generates the anonymous name; no named-volume-create exclusivity or rooted-handle race guarantee claimed.' };
      builder.ownershipAcquired = true; this.record();
    }
    return x;
  }
  cacheMount(x) {
    const mounts = x.Mounts, spec = x.HostConfig?.Mounts;
    if (!Array.isArray(mounts) || mounts.length !== 1 || !Array.isArray(spec) || spec.length !== 1 ||
        mounts[0].Type !== 'volume' || mounts[0].Destination !== '/var/lib/buildkit' || mounts[0].Driver !== 'local' ||
        mounts[0].RW !== true || !containerIDPattern.test(mounts[0].Name ?? '') ||
        spec[0].Type !== 'volume' || spec[0].Target !== '/var/lib/buildkit' || (spec[0].Source !== undefined && spec[0].Source !== '') ||
        (spec[0].ReadOnly !== undefined && spec[0].ReadOnly !== false) || spec[0].VolumeOptions?.NoCopy !== true ||
        !defaultDriverConfig(spec[0].VolumeOptions.DriverConfig) || Object.keys(spec[0].VolumeOptions.Labels ?? {}).length) throw new Error('container-coupled anonymous cache mount required');
    return mounts[0];
  }
  captureBuilder(builder, x, stage) {
    builder.inspections ??= [];
    builder.inspections.push({ at: new Date().toISOString(), stage, inspect: x });
    fs.writeFileSync(path.join(this.root, `builder-${builder.index}-inspections.json`), publicJSON(builder.inspections, 2));
  }
  builderPolicy(builder, x) {
    const h = x.HostConfig;
    const empty = value => value === null || (Array.isArray(value) && value.length === 0);
    if (!h || h.NetworkMode !== 'bridge' || !Array.isArray(x.Args) ||
        x.Args.some(a => typeof a !== 'string' || a.includes('allow-insecure-entitlement'))) throw new Error('host networking/insecure entitlements forbidden');
    if (h.Privileged !== true || h.Init !== true || !empty(h.CapAdd) || !empty(h.CapDrop) ||
        // Docker records label=disable for every privileged container; any other option is rejected.
        !(empty(h.SecurityOpt) || JSON.stringify(h.SecurityOpt) === '["label=disable"]') ||
        !empty(h.Binds) || !empty(h.Devices) || !empty(h.DeviceRequests) || !empty(h.VolumesFrom) ||
        h.UsernsMode !== '' || h.PidMode !== '' || h.IpcMode !== 'private' || h.ReadonlyRootfs !== false ||
        h.RestartPolicy?.Name !== 'no' || h.RestartPolicy.MaximumRetryCount !== 0 ||
        (h.Tmpfs !== null && (!h.Tmpfs || typeof h.Tmpfs !== 'object' || Array.isArray(h.Tmpfs) || Object.keys(h.Tmpfs).length)) ||
        JSON.stringify(x.Config.Entrypoint) !== '["buildkitd"]' || x.Path !== 'buildkitd' ||
        JSON.stringify(x.Args) !== JSON.stringify(['--debug', '--config', configPath]) ||
        JSON.stringify(x.Config.Cmd) !== JSON.stringify(x.Args) ||
        !x.NetworkSettings?.Networks || JSON.stringify(Object.keys(x.NetworkSettings.Networks)) !== '["bridge"]') throw new Error('unadmitted or unavailable daemon privilege/config/mount/network policy');
    const mount = this.cacheMount(x);
    if (!builder.ownershipAcquired || mount.Name !== builder.volume) throw new Error('owned cache identity changed');
    admitLimits({ quota: h.CpuQuota, period: h.CpuPeriod, memory: h.Memory, swap: h.MemorySwap - h.Memory });
  }
  async ownedBuilder(builder, cleanup = false) {
    const x = JSON.parse(await this.command('docker', ['inspect', builder.id], cleanup))[0];
    if (x?.HostConfig && !Object.hasOwn(x.HostConfig, 'Tmpfs')) {
      x.HostConfig.Tmpfs = JSON.parse(await this.command('docker', ['inspect', '--format', '{{json .HostConfig.Tmpfs}}', builder.id], cleanup));
    }
    this.captureBuilder(builder, x, cleanup ? 'cleanup' : 'admission');
    this.checkBuilder(builder, x, cleanup);
    if (cleanup) return x;
    return this.acquireBuilder(builder, x, builder.ownershipAcquired ? undefined :
      JSON.parse(await this.command('docker', ['volume', 'inspect', this.cacheMount(x).Name]))[0]);
  }
  async probeBuilder(builder, signal) {
    const x = JSON.parse(await this.probe('docker', ['inspect', builder.id], signal))[0];
    if (x?.HostConfig && !Object.hasOwn(x.HostConfig, 'Tmpfs')) {
      x.HostConfig.Tmpfs = JSON.parse(await this.probe('docker', ['inspect', '--format', '{{json .HostConfig.Tmpfs}}', builder.id], signal));
    }
    this.captureBuilder(builder, x, 'sample');
    this.checkBuilder(builder, x);
    this.acquireBuilder(builder, x, builder.ownershipAcquired ? undefined :
      JSON.parse(await this.probe('docker', ['volume', 'inspect', this.cacheMount(x).Name], signal))[0]);
    this.builderPolicy(builder, x);
    return x;
  }
  async sample(builder, signal) {
    const x = await this.probeBuilder(builder, signal);
    builder.cgroupObservations ??= [];
    const usage = await this.cgroupUsage(builder, signal);
    builder.cgroupObservations.push({ at: new Date().toISOString(), usage });
    fs.writeFileSync(path.join(this.root, `builder-${builder.index}-cgroups.json`), JSON.stringify(builder.cgroupObservations, null, 2));
    const lines = usage.trim().split('\n');
    if (lines[0] !== '200000 100000' || lines[1] !== '8589934592' || lines[2] !== '0' ||
        !Number.isFinite(Number(lines[3])) || !Number.isFinite(Number(lines[4]))) throw new Error('actual cgroup limits/observations unavailable');
    builder.cacheBytes = Number(lines.at(-1).split(/\s+/)[0]) * 1024;
    const tool = JSON.parse(await this.probe('docker', ['image', 'inspect', builder.imageId], signal))[0];
    const owned = Number(lines.at(-1).split(/\s+/)[0]) * 1024 + Number((await this.probe('du', ['-sk', this.root, this.privateRoot], signal)).trim().split('\n').reduce((sum, l) => sum + Number(l.split(/\s+/)[0]), 0)) * 1024 +
      this.state.images.reduce((sum, i) => sum + i.size, 0) + (this.state.initialImages.includes(tool.Id) ? 0 : tool.Size);
    const hostFree = storageFree(await this.probe('df', ['-Pk', this.root, this.state.host.dockerRoot], signal));
    admitResources({ quota: x.HostConfig.CpuQuota, period: x.HostConfig.CpuPeriod, memory: x.HostConfig.Memory,
      swap: x.HostConfig.MemorySwap - x.HostConfig.Memory, owned, hostFree });
    fs.appendFileSync(path.join(this.root, 'resources.jsonl'), JSON.stringify({ at: new Date().toISOString(), builder: builder.name, owned, hostFree,
      memoryCurrent: Number(lines[3]), memoryPeak: Number(lines[4]), load: (await this.probe('uptime', [], signal)).trim() }) + '\n');
  }
  async cgroupUsage(builder, signal) {
    // BuildKit may remove snapshots while du walks them; retry only that race.
    for (let attempt = 1; ; attempt++) {
      try {
        return await this.probe('docker', ['exec', builder.id, 'sh', '-c', 'set -eu; cat /sys/fs/cgroup/cpu.max /sys/fs/cgroup/memory.max /sys/fs/cgroup/memory.swap.max /sys/fs/cgroup/memory.current /sys/fs/cgroup/memory.peak; du -sk /var/lib/buildkit'], signal);
      } catch (e) {
        if (attempt >= 3 || e.probeFailure || e.code !== 1 || !vanishedBuildkitPaths(e.stderr)) throw e;
        builder.cgroupObservations.push({ at: new Date().toISOString(), vanishedPathRetry: attempt, stderr: e.stderr });
      }
    }
  }
  async samplePaused(builder, signal) {
    const x = await this.probeBuilder(builder, signal);
    if (!x.State.Paused || !Number.isFinite(builder.cacheBytes)) throw new Error('paused cache snapshot admission unavailable');
    const tool = JSON.parse(await this.probe('docker', ['image', 'inspect', builder.imageId], signal))[0];
    const owned = builder.cacheBytes +
      (await this.probe('du', ['-sk', this.root, this.privateRoot], signal)).trim().split('\n').reduce((sum, line) => sum + Number(line.split(/\s+/)[0]) * 1024, 0) +
      this.state.images.reduce((sum, i) => sum + i.size, 0) + (this.state.initialImages.includes(tool.Id) ? 0 : tool.Size);
    const hostFree = storageFree(await this.probe('df', ['-Pk', this.root, this.state.host.dockerRoot], signal));
    admitResources({ quota: x.HostConfig.CpuQuota, period: x.HostConfig.CpuPeriod, memory: x.HostConfig.Memory,
      swap: x.HostConfig.MemorySwap - x.HostConfig.Memory, owned, hostFree });
    fs.appendFileSync(path.join(this.root, 'resources.jsonl'), JSON.stringify({ at: new Date().toISOString(), owned, hostFree,
      scope: 'paused owned daemon/cache frozen; context/images and host free resampled', load: (await this.probe('uptime', [], signal)).trim() }) + '\n');
  }
  async sampleHostStorage(signal) {
    const hostFree = storageFree(await this.probe('df', ['-Pk', this.root, this.state.host.dockerRoot], signal));
    const pulledTools = new Map(this.state.builders.filter(builder => builder.pulledImage)
      .map(builder => [builder.pulledImage.id, builder.pulledImage]));
    const owned = Number((await this.probe('du', ['-sk', this.root], signal)).split(/\s+/)[0]) * 1024 +
      this.state.images.reduce((sum, image) => sum + image.size, 0) +
      [...pulledTools.values()].reduce((sum, image) => sum + (this.state.initialImages.includes(image.id) ? 0 : image.size), 0);
    this.recordHostStorage(hostFree, owned);
  }
  async builder(index) {
    admitLivePR(await this.api('pulls/836'), this.config);
    const name = `${this.config.owner}-${index}`;
    const container = `${name}-daemon`;
    const builder = { name, container, index, creationAttempted: false, creationConfirmed: false, metadataCreated: false, ownershipAcquired: false };
    this.state.builders.push(builder); this.record();
    this.setupDeadline = Math.min(Date.now() + 360000, Date.now() + this.remaining());
    builder.setupDeadline = this.setupDeadline; this.record();
    const started = performance.now(), samplingController = new AbortController();
    let sampling;
    const stopSetup = message => {
      this.stopped = message; samplingController.abort(); this.stopClients(message);
    };
    let skipped = 0;
    const sampler = setInterval(() => {
      if (sampling) {
        if (this.skipSamplerTick('startup', ++skipped)) return;
        stopSetup('startup resource sampler exceeded five-second cadence'); return;
      }
      skipped = 0;
      sampling = (async () => {
        if (builder.ownershipAcquired) {
          const x = await this.probeBuilder(builder, samplingController.signal);
          if (x.State?.Running) return this.sample(builder, samplingController.signal);
        }
        return this.sampleHostStorage(samplingController.signal);
      })().catch(e => {
        if (!samplingController.signal.aborted) stopSetup(e.message);
      });
      const task = sampling;
      this.activeSamplers.add(task);
      task.finally(() => { this.activeSamplers.delete(task); if (sampling === task) sampling = undefined; });
    }, 5000);
    try {
      if ((await this.command('docker', ['buildx', 'ls'])).includes(name) ||
          (await this.command('docker', ['ps', '-a', '--format', '{{.Names}}'])).split('\n').includes(container)) throw new Error('builder/container already exists');
      const config = path.join(this.root, 'buildkit.toml');
      if (fs.readFileSync(config, 'utf8') !== buildkitConfig) throw new Error('explicit deny config changed');
      const buildkitImage = this.state.pins['moby/buildkit:buildx-stable-1'];
      const pull = await this.logged(['pull', '--platform', 'linux/amd64', buildkitImage], `builder-${index}-image-pull`);
      if (pull.code !== 0 || pull.reason) {
        throw new Error(`pinned BuildKit image pull failed: ${pull.reason ?? `exit ${pull.code}`}; see ${pull.log}`);
      }
      const pulledImage = JSON.parse(await this.command('docker', ['image', 'inspect', buildkitImage]))[0];
      if (!digestPattern.test(pulledImage?.Id) || pulledImage.Os !== 'linux' || pulledImage.Architecture !== 'amd64' ||
          !Array.isArray(pulledImage.RepoDigests) || !pulledImage.RepoDigests.includes(buildkitImage) ||
          !Number.isFinite(pulledImage.Size) || pulledImage.Size <= 0) throw new Error('pinned Linux/AMD64 BuildKit image verification failed');
      builder.pulledImage = { ref: buildkitImage, id: pulledImage.Id, size: pulledImage.Size,
        platform: `${pulledImage.Os}/${pulledImage.Architecture}`, log: pull.log,
        wallSeconds: pull.wallSeconds, timeoutSeconds: pull.timeoutSeconds };
      this.record();
      builder.creationAttempted = true; this.record();
      const id = (await this.command('docker', ['create', '--name', container, '--pull=never', '--privileged', '--init',
        '--restart=no', '--network=bridge', '--cpu-period=100000', '--cpu-quota=200000', '--memory=8g', '--memory-swap=8g',
        '--label', `com.waza.cache.owner=${this.config.owner}`, '--env', `WAZA835_OWNER=${this.config.owner}`,
        '--mount', 'type=volume,target=/var/lib/buildkit,volume-nocopy', '--entrypoint', 'buildkitd',
        this.state.pins['moby/buildkit:buildx-stable-1'], '--debug', '--config', configPath])).trim();
      if (!containerIDPattern.test(id)) throw new Error('daemon creation ID not confirmed');
      builder.id = id; builder.creationConfirmed = true; this.record();
      this.builderPolicy(builder, await this.ownedBuilder(builder));
      await this.command('docker', ['cp', config, `${id}:${configPath}`]);
      const copied = path.join(this.privateRoot, `builder-${index}-config.toml`);
      await this.command('docker', ['cp', `${id}:${configPath}`, copied]);
      builder.configCopy = { sha256: hash(fs.readFileSync(copied)), bytes: fs.statSync(copied).size }; this.record();
      if (fs.readFileSync(copied, 'utf8') !== buildkitConfig) throw new Error('copied deny config differs');
      builder.configSHA256 = hash(fs.readFileSync(copied)); this.record();
      await this.command('docker', ['start', id]);
      await this.ready(builder);
      builder.metadataAttempted = true; this.record();
      const created = await this.command('docker', ['buildx', 'create', '--name', name, '--driver', 'remote', `docker-container://${id}`]);
      if (created.trim() !== name) throw new Error('builder metadata creation not confirmed');
      builder.metadataCreated = true; this.record();
      const boot = await this.logged(['buildx', 'inspect', name, '--bootstrap'], `builder-${index}`);
      if (boot.code !== 0 || boot.reason) throw new Error('remote builder bootstrap failed');
      this.remoteBinding(builder, fs.readFileSync(path.join(this.root, boot.log), 'utf8'));
      const x = await this.ownedBuilder(builder); this.builderPolicy(builder, x);
      const du = (await this.command('docker', ['buildx', 'du', '--builder', name, '--format', 'json'])).trim();
      if (du !== '' && du !== '[]') throw new Error('cold owned cache not empty');
      await this.sample(builder);
      fs.writeFileSync(path.join(this.root, `builder-${index}-admission.json`), publicJSON({ ...builder, cgroups: x.HostConfig,
        version: await this.command('docker', ['exec', id, 'buildkitd', '--version']), cacheEmpty: true }, 2));
      console.log(`Admitted owned builder ${name}; source/resources/pins recorded before timing.`);
      return builder;
    } finally {
      builder.setupWallSeconds = (performance.now() - started) / 1000;
      clearInterval(sampler); samplingController.abort();
      try { await this.joinSampler(sampling); } finally { this.setupDeadline = undefined; this.record(); }
    }
  }
  async ready(builder) {
    for (let attempt = 1; attempt <= 16; attempt++) {
      const x = await this.probeBuilder(builder);
      if (x.State?.Running !== true || x.State.Paused !== false || x.State.Restarting !== false || x.State.Dead !== false) throw new Error('owned daemon not running normally');
      await this.sample(builder);
      const effectiveConfig = await this.probe('docker', ['exec', builder.id, 'cat', configPath]);
      fs.writeFileSync(path.join(this.privateRoot, `builder-${builder.index}-effective-config.toml`), effectiveConfig);
      builder.effectiveConfig = { sha256: hash(effectiveConfig), bytes: Buffer.byteLength(effectiveConfig) }; this.record();
      if (effectiveConfig !== buildkitConfig) throw new Error('effective deny config differs');
      let workers;
      try {
        workers = await this.probe('docker', ['exec', builder.id, 'buildctl', 'debug', 'workers', '--format', '{{json .}}']);
      } catch (e) {
        builder.readinessErrors ??= []; builder.readinessErrors.push({ attempt, error: redact(e.message) }); this.record();
        if (e.probeFailure || e.code !== 1 ||
            !/^(?:error: )?(?:failed to list workers: )?rpc error: code = Unavailable desc = connection error:.*(?:connection refused|no such file or directory)/s.test(e.stderr ?? '') ||
            attempt === 16) throw e;
        await this.sampleHostStorage();
        const delay = 120 * attempt;
        if (this.operationRemaining() <= delay) throw new Error('builder startup/readiness deadline exhausted');
        await new Promise(resolve => setTimeout(resolve, delay));
        continue;
      }
      fs.writeFileSync(path.join(this.root, `builder-${builder.index}-workers.json`), redact(workers));
      builder.workers = JSON.parse(workers); this.record();
      if (!Array.isArray(builder.workers) || !builder.workers.length ||
          builder.workers.some(w => !w || typeof w.id !== 'string' || !w.id || !Array.isArray(w.platforms) ||
            w.platforms.some(p => !p || typeof p.os !== 'string' || typeof p.architecture !== 'string')) ||
          !builder.workers.some(w => w.platforms.some(p => p.os === 'linux' && p.architecture === 'amd64'))) throw new Error('usable native AMD64 worker evidence required');
      builder.readyAt = new Date().toISOString(); this.record();
      return;
    }
  }
  remoteBinding(builder, text) {
    const drivers = [...text.matchAll(/^Driver:\s+(\S+)\s*$/gm)];
    const endpoints = [...text.matchAll(/^Endpoint:\s+(\S+)\s*$/gm)];
    const names = [...text.matchAll(/^Name:\s+(\S+)\s*$/gm)].map(m => m[1]);
    if (drivers.length !== 1 || drivers[0][1] !== 'remote' || endpoints.length !== 1 ||
        endpoints[0][1] !== `docker-container://${builder.id}` ||
        JSON.stringify(names) !== JSON.stringify([builder.name, `${builder.name}0`])) throw new Error('owned remote metadata binding changed');
  }
  async removeBuilder(builder) {
    if (builder.cleanupAttempted) return;
    builder.cleanupAttempted = true; this.record();
    const errors = [];
    const safely = async (kind, operation) => {
      try { this.clientsJoined(); await operation(); } catch (e) {
        errors.push(`${kind}: ${redact(e.message)}`);
        this.state.residue.push({ kind, name: builder.name, id: builder.id, volume: builder.volume, error: redact(e.message) });
      }
    };
    this.clientsJoined();
    if (builder.metadataCreated) await safely('remote metadata', async () => {
      this.remoteBinding(builder, await this.command('docker', ['buildx', 'inspect', builder.name], true));
      await this.command('docker', ['buildx', 'rm', builder.name], true); builder.metadataRemoved = true;
    });
    else if (builder.metadataAttempted) errors.push('unverified remote metadata creation retained');
    if (builder.creationConfirmed && builder.id) await safely('daemon/cache', async () => {
      const x = await this.ownedBuilder(builder, true);
      let removeVolume = false;
      if (builder.ownershipAcquired) {
        try {
          const mount = this.cacheMount(x);
          const volume = JSON.parse(await this.command('docker', ['volume', 'inspect', builder.volume], true))[0];
          if (mount.Name !== builder.volume || JSON.stringify(volume) !== JSON.stringify(builder.volumeMetadata)) throw new Error('owned cache volume replaced; retained');
          removeVolume = true;
        } catch (e) { errors.push(redact(e.message)); }
      } else errors.push('unverified cache origin retained');
      this.clientsJoined();
      await this.command('docker', ['rm', '-f', ...(removeVolume ? ['-v'] : []), builder.id], true);
      if ((await this.command('docker', ['ps', '-a', '--no-trunc', '--format', '{{.ID}}'], true)).split('\n').includes(builder.id)) throw new Error('owned builder container remains');
      builder.daemonRemoved = true;
      if (builder.volume && !(await this.command('docker', ['volume', 'ls', '--format', '{{.Name}}'], true)).split('\n').includes(builder.volume)) builder.cacheRemoved = true;
      else throw new Error('cache remains or identity unavailable; no name-based deletion attempted');
    });
    else if (builder.creationAttempted) errors.push('unverified daemon creation retained without adoption');
    builder.removed = (!builder.metadataCreated || builder.metadataRemoved) && builder.daemonRemoved && builder.cacheRemoved && errors.length === 0;
    this.record();
    if (errors.length) throw new Error(errors.join('; '));
  }
  async artifact(builder, image, key) {
    const container = `${this.config.owner}-${key}`;
    if ((await this.command('docker', ['ps', '-a', '--format', '{{.Names}}'])).split('\n').includes(container)) throw new Error('artifact container exists');
    const id = (await this.command('docker', ['create', '--name', container, '--label', `com.waza.cache.owner=${this.config.owner}`,
      '--pull=never', '--network=none', '--read-only', '--env', 'NO_COLOR=1', '--cpu-period=100000', '--cpu-quota=200000', '--memory=512m', '--memory-swap=512m',
      '--entrypoint', '/bin/sh', image.id, '-c',
      'set -eu; for p in /go/pkg/mod /root/.cache/go-build /build; do if [ -e "$p" ] || [ -L "$p" ]; then echo "Cache/source leak" >&2; exit 1; fi; done; test "$(od -An -tx1 -N4 /usr/local/bin/waza | tr -d " \\n")" = 7f454c46; ldd /bin/sh; /usr/local/bin/waza --version; if o=$(ldd /usr/local/bin/waza 2>&1); then exit 1; else printf "%s\\n" "$o"; printf "%s\\n" "$o" | grep -Fq "/usr/local/bin/waza: Not a valid dynamic program"; fi; echo "Runtime static/version/cache/source checks passed"'])).trim();
    this.state.containers.push({ name: container, id, imageId: image.id }); this.record();
    const admission = JSON.parse(await this.command('docker', ['inspect', id]))[0];
    if (admission.Id !== id || admission.HostConfig.CpuQuota !== 200000 || admission.HostConfig.CpuPeriod !== 100000 ||
        admission.HostConfig.Memory !== 536870912 || admission.HostConfig.MemorySwap !== 536870912 ||
        admission.HostConfig.NetworkMode !== 'none' || !admission.HostConfig.ReadonlyRootfs) throw new Error('actual read-only runtime caps differ');
    fs.writeFileSync(path.join(this.root, `${key}-runtime-admission.json`), JSON.stringify(admission.HostConfig, null, 2));
    await this.sample(builder);
    await this.command('docker', ['pause', builder.id]);
    try {
      const result = await this.logged(['start', '-a', id], `${key}-runtime`, signal => this.samplePaused(builder, signal));
      if (result.code !== 0 || result.reason) throw new Error('loaded artifact validation failed');
      const runtime = fs.readFileSync(path.join(this.root, `${key}-runtime.log`), 'utf8');
      if ([...runtime.matchAll(/^waza version dev\r?$/gm)].length !== 1 || !runtime.includes('Runtime static/version/cache/source checks passed')) throw new Error('actual runtime version/controls missing');
      await this.sampledCapture(builder, true, async () => {
        const dest = path.join(this.root, `${key}.waza`);
        await this.command('docker', ['cp', `${id}:/usr/local/bin/waza`, dest]);
        const bytes = await fs.promises.readFile(dest);
        const buildInfo = await this.command('go', ['version', '-m', dest]);
        for (const setting of ['CGO_ENABLED=0', 'GOOS=linux', 'GOARCH=amd64']) if (!buildInfo.includes(`\t${setting}\n`)) throw new Error(`actual build setting missing: ${setting}`);
        fs.writeFileSync(path.join(this.root, `${key}-binary.json`), JSON.stringify({ bytes: bytes.length, sha256: hash(bytes), buildInfo,
          qualification: 'Whole-binary equality covers embedded bytes; unequal binaries do not identify which embedded assets differ.' }, null, 2));
        image.binary = { sha256: hash(bytes), bytes: bytes.length, buildInfo, runtimeVersion: 'waza version dev',
          vcsUnavailable: !buildInfo.includes('vcs.revision=') };
      });
    } finally {
      this.clientsJoined();
      await this.command('docker', ['unpause', builder.id], true);
    }
  }
  async build(builder, variant, round, phase) {
    const key = `r${round}-${variant.toLowerCase()}-${phase}`;
    const tag = `${this.config.owner}:${key}`;
    if ((await this.command('docker', ['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}'])).split('\n').includes(tag)) throw new Error('artifact tag already exists');
    const context = this.state.contexts.find(c => c.variant === variant && c.round === round);
    const mutation = phase === 'warm' ? this.state.mutations?.find(m => m.variant === variant && m.round === round) : undefined;
    if (phase === 'warm' && !mutation) throw new Error('warm Go mutation evidence absent');
    await this.verifyContext(context, mutation);
    const args = ['buildx', 'build', '--builder', builder.name, '--platform', 'linux/amd64', '--progress', 'plain', '--load', '--tag', tag,
      '--label', `com.waza.cache.owner=${this.config.owner}`,
      '--metadata-file', path.join(this.root, `${key}-metadata.json`),
      // BuildKit's predefined frontend selector does not require a Dockerfile ARG.
      '--build-arg', `BUILDKIT_SYNTAX=${this.state.pins['docker/dockerfile:1']}`];
    for (const name of ['node:22-alpine', 'golang:1.26-alpine', 'alpine:3.19']) args.push('--build-context', `${name}=docker-image://${this.state.pins[name]}`);
    args.push(context.dir);
    const attempt = { key, variant, round, phase, started: new Date().toISOString(), builder: builder.name, mutation };
    this.state.attempts.push(attempt); this.record();
    this.state.images.push({ tag, id: undefined, size: 0 }); this.record();
    Object.assign(attempt, await this.logged(args, `${key}-build`, signal => this.sample(builder, signal))); this.record();
    if ((await this.command('docker', ['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}'])).split('\n').includes(tag)) {
      const x = JSON.parse(await this.command('docker', ['image', 'inspect', tag]))[0];
      if (x.Config.Labels?.['com.waza.cache.owner'] !== this.config.owner) throw new Error('artifact owner label mismatch');
      Object.assign(this.state.images.find(i => i.tag === tag), { id: x.Id, size: x.Size }); this.record();
    }
    if (attempt.code !== 0 || attempt.reason) throw new Error(`recorded failed attempt: ${key}`);
    const text = fs.readFileSync(path.join(this.root, attempt.log), 'utf8');
    attempt.evidence = compileEvidence(text, phase === 'warm');
    attempt.pins = pinEvidence(text, this.state.pins);
    const x = JSON.parse(await this.command('docker', ['image', 'inspect', tag]))[0];
    if (x.Os !== 'linux' || x.Architecture !== 'amd64') throw new Error('artifact platform mismatch');
    const image = this.state.images.find(i => i.tag === tag);
    Object.assign(image, { id: x.Id, size: x.Size }); this.record();
    await this.artifact(builder, image, key);
    attempt.assets = await this.sampledCapture(builder, false, () => this.assets(builder, key));
    attempt.runtimePassed = true; this.record(); await this.sample(builder);
  }
  async assets(builder, key) {
    const roots = (await this.command('docker', ['exec', builder.id, 'find', '/var/lib/buildkit', '-type', 'f', '-path', '*/fs/build/web/dist/index.html', '-print']))
      .trim().split('\n').filter(Boolean).map(p => p.slice(0, -'/index.html'.length));
    if (!roots.length || roots.some(p => !/^\/var\/lib\/buildkit\/runc-(overlayfs|native)\/snapshots\/snapshots\/\d+\/fs\/build\/web\/dist$/.test(p))) throw new Error('completed owned web snapshot unavailable/ambiguous');
    const inventories = [];
    for (const root of roots) {
      const files = (await this.command('docker', ['exec', builder.id, 'find', root, '-type', 'f', '-print'])).trim().split('\n').sort();
      const manifest = [];
      for (const file of files) {
        if (!file.startsWith(root + '/') || file.includes('\n')) throw new Error('nonlocal web asset');
        const digest = (await this.command('docker', ['exec', builder.id, 'sha256sum', file])).slice(0, 64);
        if (!/^[a-f0-9]{64}$/.test(digest)) throw new Error('web asset hashing failed');
        manifest.push({ path: file.slice(root.length + 1), sha256: digest });
      }
      const index = await this.command('docker', ['exec', builder.id, 'cat', `${root}/index.html`]);
      const refs = [...index.matchAll(/(?:src|href)="(?:\.\/|\/)?(assets\/[^"]+)"/g)].map(m => m[1]);
      if (!refs.length || refs.some(ref => !manifest.some(f => f.path === ref))) throw new Error('web snapshot incomplete');
      inventories.push({ root, manifest });
    }
    if (inventories.some(i => JSON.stringify(i.manifest) !== JSON.stringify(inventories[0].manifest))) throw new Error('owned generated web asset inventories disagree');
    const dest = path.join(this.root, `${key}-assets`);
    await this.command('docker', ['cp', `${builder.id}:${inventories[0].root}`, dest]);
    for (const entry of inventories[0].manifest) if (hash(await fs.promises.readFile(path.join(dest, entry.path))) !== entry.sha256) throw new Error('captured web asset bytes changed');
    fs.writeFileSync(path.join(this.root, `${key}-assets.json`), JSON.stringify(inventories, null, 2));
    return inventories[0].manifest;
  }
  async measure() {
    await this.authorization();
    await this.admit();
    let index = 0;
    for (let round = 1; round <= 3; round++) for (const variant of order[round - 1]) {
      const context = await this.workingContext(variant, round);
      const builder = await this.builder(++index);
      await this.build(builder, variant, round, 'cold');
      const file = path.join(context.dir, 'cmd/waza/measurement_cache_input.go');
      const bytes = Buffer.from(`package main\n\nconst measurementCacheInput = "835-round-${round}"\n`);
      fs.writeFileSync(file, bytes, { flag: 'wx', mode: 0o644 }); fs.utimesSync(file, 1700000000, 1700000000);
      this.state.mutations ??= []; this.state.mutations.push({ round, variant, path: 'cmd/waza/measurement_cache_input.go', sha256: hash(bytes) }); this.record();
      await this.build(builder, variant, round, 'warm');
      await this.removeBuilder(builder);
    }
  }
  async cleanup() {
    const failure = async fn => { try { this.clientsJoined(); await fn(); } catch (e) { this.state.failures.push(`cleanup: ${redact(e.message)}`); } };
    if (this.activeClients.size || this.activeSamplers.size) {
      this.state.failures.push('cleanup: owned clients not joined; resources retained without teardown');
      this.state.residue.push({ kind: 'unjoined-clients', builders: this.state.builders, containers: this.state.containers });
      this.record(); return;
    }
    for (const c of this.state.containers) await failure(async () => {
      const x = JSON.parse(await this.command('docker', ['inspect', c.name], true))[0];
      if (x.Id !== c.id || x.Image !== c.imageId || x.Config.Labels?.['com.waza.cache.owner'] !== this.config.owner) throw new Error('artifact container identity changed');
      await this.command('docker', ['rm', '-f', c.id], true);
    });
    for (const b of this.state.builders.filter(b => !b.removed)) {
      if (!b.cleanupAttempted && (b.creationConfirmed || b.metadataCreated)) await failure(() => this.removeBuilder(b));
      if (!b.removed) this.state.residue.push({ kind: 'unverified-builder-creation', name: b.name,
        container: b.container, volume: b.volume, metadataCreated: b.metadataCreated,
        qualification: 'Possible partial or competing resources; identities not acquired, no adoption/deletion attempted.' });
    }
    for (const i of this.state.images) await failure(async () => {
      if (!i.id) {
        if ((await this.command('docker', ['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}'], true)).split('\n').includes(i.tag)) throw new Error(`unverified partial artifact retained: ${i.tag}`);
        return;
      }
      if (JSON.parse(await this.command('docker', ['image', 'inspect', i.tag], true))[0].Id !== i.id) throw new Error('artifact tag replaced');
      await this.command('docker', ['image', 'rm', i.tag], true);
    });
    for (const entry of [...this.state.contexts.map(c => ({ ...c, kind: 'source/working context' })),
      { dir: this.privateRoot, dev: this.privateIdentity.dev, ino: this.privateIdentity.ino, kind: 'private raw logs' },
      ...(this.buildxIdentity ? [{ dir: this.buildxRoot, dev: this.buildxIdentity.dev, ino: this.buildxIdentity.ino, kind: 'private Buildx configuration' }] : [])]) {
      const retained = { dir: entry.dir, kind: entry.kind, descendantsUnverified: true, retainedForEphemeralHostTeardown: true };
      this.state.retainedDirectories.push(retained);
      await failure(() => {
        const s = fs.lstatSync(entry.dir);
        retained.rootIdentityMatches = s.dev === entry.dev && s.ino === entry.ino && s.isDirectory() && !s.isSymbolicLink();
        if (!retained.rootIdentityMatches) throw new Error(`${entry.kind} directory replaced; retained without deleting descendants`);
      });
    }
    this.state.cleanupAt = new Date().toISOString(); this.record();
  }
}

export async function execute(measurement) {
  try { await measurement.measure(); } catch (e) { measurement.state.failures.push(redact(e.message)); } finally { await measurement.cleanup(); }
  const summary = pairedSummary(measurement.state.attempts);
  const binaries = measurement.state.images.filter(i => i.binary).map(i => ({ tag: i.tag, ...i.binary }));
  summary.binaryComparisons = { binaries, allBytesEqual: binaries.length === 12 ? binaries.every(b => b.sha256 === binaries[0].sha256) : null };
  measurement.state.finishedAt = new Date().toISOString();
  measurement.state.overallSeconds = (Date.now() - (measurement.config.deadline - 3600000)) / 1000;
  if (measurement.stopped) measurement.state.failures.push(`terminal stop: ${redact(measurement.stopped)}`);
  if (Date.now() > measurement.config.deadline) measurement.state.failures.push('total deadline exceeded');
  measurement.state.summary = summary; measurement.record();
  return summary.complete && measurement.state.failures.length === 0 ? 0 : 1;
}

async function main() {
  const root = path.resolve(process.env.CACHE_RESULTS ?? '');
  const temp = path.resolve(process.env.RUNNER_TEMP ?? '');
  if (!temp || !root.startsWith(temp + path.sep)) throw new Error('owned artifact directory must be under runner temporary storage');
  if (fs.existsSync(root)) {
    const s = fs.lstatSync(root);
    if (s.isSymbolicLink() || !s.isDirectory() || `${s.dev}:${s.ino}` !== process.env.CACHE_RESULTS_ID) throw new Error('precreated evidence directory identity differs');
  } else fs.mkdirSync(root, { mode: 0o700 });
  fs.writeFileSync(path.join(root, 'measurement-started.json'), JSON.stringify({ at: new Date().toISOString() }), { flag: 'wx' });
  let config;
  try {
    config = request(process.env, JSON.parse(fs.readFileSync(process.env.GITHUB_EVENT_PATH, 'utf8')));
    fs.appendFileSync(process.env.GITHUB_ENV, `CACHE_NONCE=${config.nonce}\n`);
  } catch (e) {
    fs.writeFileSync(path.join(root, 'activation-rejected.json'), JSON.stringify({ error: e.message }));
    throw e;
  }
  const measurement = new Measurement(config, root, process.cwd());
  const stop = () => {
    measurement.stopped = 'cancelled'; measurement.stopClients('cancelled');
  };
  process.on('SIGTERM', stop); process.on('SIGINT', stop);
  try { process.exitCode = await execute(measurement); } finally {
    process.off('SIGTERM', stop); process.off('SIGINT', stop);
  }
}
if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main().catch(e => { console.error(redact(e.message)); process.exitCode = 1; });
