"use strict";

const fs = require("node:fs");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const bindingFields = ["repository", "source_sha", "workflow_sha", "run_id", "attempt", "job", "image_os", "image_version"];
const initial = { initial_entries: 0, actual_path_match: true };

function validObservation(observation, identity, cache) {
  if (!observation || observation.schema !== 1 || observation.mode !== "isolated-empty" ||
      observation.platform !== "linux" || observation.phase !== "before-dependencies" ||
      identity.os !== "Linux" ||
      observation.restore !== false || observation.save !== false ||
      !["image_os", "image_version"].every((field) => typeof observation[field] === "string" &&
        /^[A-Za-z0-9._-]+$/.test(observation[field])) ||
      !bindingFields.every((field) => typeof observation[field] === "string" &&
        observation[field].length > 0 && observation[field] === identity[field])) return false;
  const stores = observation.stores;
  if (!stores || typeof stores !== "object" || Array.isArray(stores)) return false;
  const empty = (store) => store && typeof store === "object" && !Array.isArray(store) &&
    Object.keys(store).length === 2 && store.initial_entries === 0 && store.actual_path_match === true;
  if (Object.keys(stores).length !== 3 || Object.keys(observation).length !== 15) return false;
  if (!empty(stores.go_modules) || !empty(stores.go_build) || cache.go_dependencies !== false) return false;
  return stores.npm === "not-applicable" ? cache.node_dependencies === "not-applicable" :
    empty(stores.npm) && cache.node_dependencies === false;
}

function cacheCohort(observation) {
  return {
    schema: observation.schema, mode: observation.mode, platform: observation.platform,
    phase: observation.phase, restore: observation.restore, save: observation.save,
    stores: observation.stores,
    image_os: observation.image_os, image_version: observation.image_version,
  };
}

function checkedCommand(binary, args, options, trimOutput = true) {
  const result = spawnSync(binary, args, { ...options, encoding: "utf8", timeout: 30000, maxBuffer: 1024 * 1024 });
  if (result.error || result.status !== 0) throw new Error(`cache-observation-command-failed:${binary}`);
  return trimOutput ? result.stdout.trim() : result.stdout;
}

function directoryIdentity(directory) {
  const stat = fs.lstatSync(directory);
  if (!stat.isDirectory() || stat.isSymbolicLink() || fs.realpathSync(directory) !== directory ||
      stat.uid !== process.getuid() || (stat.mode & 0o777) !== 0o700) throw new Error("cache-observation-directory-invalid");
  return { device: String(stat.dev), inode: String(stat.ino) };
}

function checkDirectory(directory, expected) {
  const actual = directoryIdentity(directory);
  if (actual.device !== expected.device || actual.inode !== expected.inode) {
    throw new Error("cache-observation-directory-replaced");
  }
}

function runtimeBinding(env) {
  const identity = {
    repository: env.GITHUB_REPOSITORY,
    source_sha: checkedCommand("git", ["rev-parse", "HEAD"], { env }),
    workflow_sha: env.GITHUB_SHA, run_id: env.GITHUB_RUN_ID,
    attempt: env.GITHUB_RUN_ATTEMPT, job: env.GITHUB_JOB,
    image_os: env.ImageOS, image_version: env.ImageVersion,
  };
  if (identity.repository !== "microsoft/waza" ||
      !/^[0-9a-f]{40}$/.test(identity.source_sha) ||
      !/^[0-9a-f]{40}$/.test(identity.workflow_sha || "") ||
      !/^[1-9][0-9]*$/.test(identity.run_id || "") ||
      !/^[1-9][0-9]*$/.test(identity.attempt || "") ||
      !/^[A-Za-z_][A-Za-z0-9_-]*$/.test(identity.job || "") ||
      !["image_os", "image_version"].every((field) => typeof identity[field] === "string" &&
        /^[A-Za-z0-9._-]+$/.test(identity[field])) ||
      !bindingFields.every((field) => typeof identity[field] === "string" &&
        identity[field].length > 0 && identity[field].length <= 1024 && !/[\x00-\x1f]/.test(identity[field]))) {
    throw new Error("cache-observation-identity-unavailable");
  }
  return identity;
}

function requireMode(env) {
  if (process.platform !== "linux" || env.CI_EVIDENCE_COLD_REQUESTED !== "true" ||
      env.CI_EVIDENCE_COLD_SETUP_CACHE_DISABLED !== "true") {
    throw new Error("cache-observation-mode-unavailable");
  }
}

function probe(state, env) {
  const actual = JSON.parse(checkedCommand("go", ["env", "-json", "GOMODCACHE", "GOCACHE"], { env }));
  if (actual.GOMODCACHE !== state.paths.go_modules || actual.GOCACHE !== state.paths.go_build) {
    throw new Error("cache-observation-go-path-mismatch");
  }
  if (state.paths.npm) {
    const actualNpm = checkedCommand("npm", ["config", "get", "cache"], {
      env, cwd: state.npm_directory,
    }, false).replace(/\r?\n$/, "");
    if (actualNpm !== state.paths.npm) throw new Error("cache-observation-npm-path-mismatch");
  }
}

function writeExclusive(file, contents) {
  if (Buffer.byteLength(contents) > 16384) throw new Error("cache-observation-state-byte-cap");
  const descriptor = fs.openSync(file, "wx", 0o600);
  writeAndClose(descriptor, contents);
}

function writeAndClose(descriptor, contents) {
  let writeError;
  try { fs.writeFileSync(descriptor, contents); } catch (error) { writeError = error; }
  try { fs.closeSync(descriptor); } catch (error) {
    if (writeError) throw new AggregateError([writeError, error], "cache-observation-write-and-close-failed");
    throw error;
  }
  if (writeError) throw writeError;
}

function appendEnvironment(file, contents) {
  if (!file || !path.isAbsolute(file) || /[\x00-\x1f]/.test(file) || fs.realpathSync(file) !== file) {
    throw new Error("cache-observation-environment-file-invalid");
  }
  const descriptor = checkedFile(file, fs.constants.O_WRONLY | fs.constants.O_APPEND);
  writeAndClose(descriptor, contents);
}

function checkedFile(file, flags, privateState = false) {
  const descriptor = fs.openSync(file, flags | fs.constants.O_NOFOLLOW | fs.constants.O_NONBLOCK);
  try {
    const stat = fs.fstatSync(descriptor);
    if (!stat.isFile() || stat.uid !== process.getuid() || stat.nlink !== 1 ||
        (privateState && (stat.size > 16384 || (stat.mode & 0o777) !== 0o600))) {
      throw new Error("cache-observation-file-invalid");
    }
    return descriptor;
  } catch (error) {
    try { fs.closeSync(descriptor); } catch (closeError) {
      throw new AggregateError([error, closeError], "cache-observation-admission-and-close-failed");
    }
    throw error;
  }
}

function prepare(scope, npmDirectory, env = process.env) {
  requireMode(env);
  if (!["go", "go-npm"].includes(scope) || (scope === "go-npm") !== Boolean(npmDirectory)) {
    throw new Error("cache-observation-scope-invalid");
  }
  const parent = fs.realpathSync(env.RUNNER_TEMP || "");
  if (!fs.statSync(parent).isDirectory() || /[\x00-\x1f]/.test(parent)) throw new Error("cache-observation-parent-invalid");
  const root = fs.mkdtempSync(path.join(parent, "waza-ci-cold-"));
  try {
    fs.chmodSync(root, 0o700);
    const paths = { go_modules: path.join(root, "gomod"), go_build: path.join(root, "gobuild") };
    if (scope === "go-npm") paths.npm = path.join(root, "npm");
    const directories = { root: directoryIdentity(root) };
    for (const [store, directory] of Object.entries(paths)) {
      fs.mkdirSync(directory, { mode: 0o700 });
      directories[store] = directoryIdentity(directory);
      if (fs.readdirSync(directory).length !== 0) throw new Error("cache-observation-store-not-empty");
    }
    const state = {
      schema: 1, root, paths, directories,
      npm_directory: npmDirectory ? fs.realpathSync(npmDirectory) : null,
      observation: {
        schema: 1, mode: "isolated-empty", platform: "linux", phase: "before-dependencies",
        restore: false, save: false, ...runtimeBinding(env),
        stores: { go_modules: { ...initial }, go_build: { ...initial },
          npm: paths.npm ? { ...initial } : "not-applicable" },
      },
    };
    const candidateEnv = { ...env, GOMODCACHE: paths.go_modules, GOCACHE: paths.go_build };
    if (paths.npm) candidateEnv.npm_config_cache = paths.npm;
    probe(state, candidateEnv);
    for (const [store, directory] of Object.entries(paths)) checkDirectory(directory, directories[store]);
    checkDirectory(root, directories.root);
    const file = path.join(root, "state.json");
    writeExclusive(file, JSON.stringify(state) + "\n");
    const assignments = [
      `GOMODCACHE=${paths.go_modules}`, `GOCACHE=${paths.go_build}`,
      ...(paths.npm ? [`npm_config_cache=${paths.npm}`] : []),
      `CI_EVIDENCE_COLD_STATE=${file}`,
    ];
    if (assignments.some((assignment) => /[\r\n]/.test(assignment))) {
      throw new Error("cache-observation-environment-path-invalid");
    }
    appendEnvironment(env.GITHUB_ENV, assignments.join("\n") + "\n");
    return root;
  } catch (error) {
    throw new Error(`cache-observation-prepare-failed; retained RUNNER_TEMP/${path.basename(root)}: ${error.code || error.message}`);
  }
}

function readAndClose(descriptor) {
  const buffer = Buffer.alloc(16385);
  let bytes = 0, readError;
  try {
    while (bytes < buffer.length) {
      const read = fs.readSync(descriptor, buffer, bytes, buffer.length - bytes, null);
      if (read === 0) break;
      bytes += read;
    }
    if (bytes > 16384) throw new Error("cache-observation-state-byte-cap");
  } catch (error) { readError = error; }
  try { fs.closeSync(descriptor); } catch (error) {
    if (readError) throw new AggregateError([readError, error], "cache-observation-read-and-close-failed");
    throw error;
  }
  if (readError) throw readError;
  return buffer.subarray(0, bytes).toString("utf8");
}

function observe(identity, env = process.env) {
  requireMode(env);
  const file = env.CI_EVIDENCE_COLD_STATE;
  if (!file || !path.isAbsolute(file)) throw new Error("cache-observation-state-unavailable");
  const descriptor = checkedFile(file, fs.constants.O_RDONLY, true);
  const state = JSON.parse(readAndClose(descriptor));
  if (state.schema !== 1 || path.join(state.root, "state.json") !== file ||
      path.dirname(state.root) !== fs.realpathSync(env.RUNNER_TEMP) ||
      !path.basename(state.root).startsWith("waza-ci-cold-") ||
      !state.paths || !state.directories ||
      env.GOMODCACHE !== state.paths.go_modules || env.GOCACHE !== state.paths.go_build ||
      (state.paths.npm && env.npm_config_cache !== state.paths.npm)) {
    throw new Error("cache-observation-state-path-mismatch");
  }
  checkDirectory(state.root, state.directories.root);
  for (const [store, directory] of Object.entries(state.paths)) {
    if (path.dirname(directory) !== state.root) throw new Error("cache-observation-store-containment");
    checkDirectory(directory, state.directories[store]);
  }
  probe(state, env);
  checkDirectory(state.root, state.directories.root);
  for (const [store, directory] of Object.entries(state.paths)) checkDirectory(directory, state.directories[store]);
  const cache = { test_results: "disabled", go_dependencies: false,
    node_dependencies: state.paths.npm ? false : "not-applicable" };
  if (!validObservation(state.observation, identity, cache)) {
    throw new Error("cache-observation-binding-invalid");
  }
  return { cache, observation: state.observation };
}

if (require.main === module) {
  try {
    const [mode, scope, npmDirectory, ...extra] = process.argv.slice(2);
    if (mode !== "prepare" || extra.length) throw new Error("usage: ci-evidence-cache.cjs prepare go|go-npm [npm directory]");
    prepare(scope, npmDirectory);
  } catch (error) {
    console.error(error.message);
    process.exitCode = 1;
  }
}

module.exports = { validObservation, cacheCohort, prepare, observe, checkedFile, readAndClose };
