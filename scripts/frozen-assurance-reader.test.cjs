const assert = require("node:assert/strict");
const crypto = require("node:crypto");
const fs = require("node:fs");
const path = require("node:path");
const { execFileSync, spawnSync } = require("node:child_process");
const { test } = require("node:test");
const { acquireFrozenReader, readerRefs, snapshotRoot } = require("./frozen-assurance-reader.cjs");

const scratch = path.resolve(__dirname, "../.cache", `frozen-reader-tests-${process.pid}`);
fs.mkdirSync(scratch, { recursive: true });
test.after(() => fs.rmSync(scratch, { recursive: true }));
let counter = 0;

function setup(t) {
  const directory = path.join(scratch, String(++counter));
  const sources = path.join(directory, "sources");
  fs.cpSync(snapshotRoot, sources, { recursive: true });
  t.after(() => fs.rmSync(directory, { recursive: true }));
  return { sources, output: path.join(directory, "output") };
}

function updateManifest(sources, update) {
  const filename = path.join(sources, "manifest.json");
  const manifest = JSON.parse(fs.readFileSync(filename, "utf8"));
  update(manifest);
  fs.writeFileSync(filename, JSON.stringify(manifest));
}

function replaceSource(sources, source, bytes) {
  fs.writeFileSync(path.join(sources, readerRefs[0], source), bytes);
  updateManifest(sources, manifest => {
    manifest.readers[readerRefs[0]][source] = {
      sha256: crypto.createHash("sha256").update(bytes).digest("hex"),
      git_blob: crypto.createHash("sha1").update(`blob ${Buffer.byteLength(bytes)}\0`).update(bytes).digest("hex"),
    };
  });
}

for (const ref of readerRefs) {
  test(`initializes exact historical reader ${ref}`, t => {
    const { sources, output } = setup(t);
    const { parseAssuranceReport } = acquireFrozenReader(ref, output, sources);
    assert.throws(() => parseAssuranceReport("{}"), /^Error: Unsupported or incomplete assurance report\.$/);
  });
}

test("rejects unknown reader", t => {
  const { sources, output } = setup(t);
  assert.throws(() => acquireFrozenReader("main", output, sources), /Unknown frozen reader/);
});

for (const source of Object.keys(require("../testdata/frozen-assurance-readers/manifest.json").readers[readerRefs[1]])) {
  for (const failure of ["missing", "tampered"]) {
    test(`fails closed on ${failure} ${source}`, t => {
      const { sources, output } = setup(t);
      const filename = path.join(sources, readerRefs[1], source);
      if (failure === "missing") fs.unlinkSync(filename);
      else fs.appendFileSync(filename, "\n ");
      assert.throws(() => acquireFrozenReader(readerRefs[1], output, sources),
        failure === "missing" ? /ENOENT/ : /Frozen reader identity mismatch/);
      assert.equal(fs.existsSync(output), false);
    });
  }
}

for (const field of ["sha256", "git_blob"]) {
  test(`rejects wrong ${field}`, t => {
    const { sources, output } = setup(t);
    updateManifest(sources, manifest => {
      manifest.readers[readerRefs[0]]["web/src/types/assurance.ts"][field] = "0".repeat(field === "sha256" ? 64 : 40);
    });
    assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources), /Frozen reader identity mismatch/);
  });
}

for (const failure of ["missing manifest", "malformed manifest", "missing reader", "missing source identity", "extra source", "wrong repository"]) {
  test(`rejects ${failure}`, t => {
    const { sources, output } = setup(t);
    const filename = path.join(sources, "manifest.json");
    if (failure === "missing manifest") fs.unlinkSync(filename);
    else if (failure === "malformed manifest") fs.writeFileSync(filename, "{");
    else updateManifest(sources, manifest => {
      if (failure === "missing reader") delete manifest.readers[readerRefs[0]];
      if (failure === "missing source identity") delete manifest.readers[readerRefs[0]]["web/src/types/assurance.ts"];
      if (failure === "extra source") manifest.readers[readerRefs[0]]["unexpected.ts"] = {};
      if (failure === "wrong repository") manifest.repository = "other";
    });
    assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources),
      /ENOENT|JSON|Incomplete frozen reader manifest|Unexpected frozen reader source repository/);
  });
}

test("rejects nonempty scratch directory", t => {
  const { sources, output } = setup(t);
  fs.mkdirSync(output);
  fs.writeFileSync(path.join(output, "stale.js"), "");
  assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources), /scratch directory must be empty/);
});

test("transpilation errors are setup failures", t => {
  const { sources, output } = setup(t);
  replaceSource(sources, "web/src/types/assurance.ts", "export const broken = ;");
  assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources), /Frozen reader transpilation failed/);
});

test("schema initialization errors are setup failures", t => {
  const { sources, output } = setup(t);
  replaceSource(sources, "schemas/grader-assurance-1.0.schema.json", '{"type":"not-a-json-schema-type"}');
  assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources), /schema is invalid/);
});

test("missing parser export is a setup failure", t => {
  const { sources, output } = setup(t);
  replaceSource(sources, "web/src/types/assurance.ts", "export const unrelated = true;");
  assert.throws(() => acquireFrozenReader(readerRefs[0], output, sources), /Frozen reader export missing/);
});

test("both readers initialize in independent main and PR checkouts without historical objects", t => {
  const directory = path.join(scratch, String(++counter));
  fs.mkdirSync(directory);
  t.after(() => fs.rmSync(directory, { recursive: true }));
  const seed = path.join(directory, "seed");
  fs.mkdirSync(path.join(seed, "scripts"), { recursive: true });
  fs.copyFileSync(path.resolve(__dirname, "../.gitattributes"), path.join(seed, ".gitattributes"));
  fs.cpSync(snapshotRoot, path.join(seed, "testdata/frozen-assurance-readers"), { recursive: true });
  fs.copyFileSync(__filename.replace(".test.cjs", ".cjs"), path.join(seed, "scripts/frozen-assurance-reader.cjs"));
  const git = (cwd, ...args) => execFileSync("git", args, { cwd, stdio: "pipe" });
  git(seed, "init", "--initial-branch=main");
  git(seed, "add", ".");
  git(seed, "-c", "user.name=Frozen Reader Test", "-c", "user.email=frozen-reader@example.invalid",
    "-c", "commit.gpgsign=false", "commit", "-m", "Squash snapshot without historical objects");
  git(seed, "switch", "-c", "pull-request");
  git(seed, "-c", "user.name=Frozen Reader Test", "-c", "user.email=frozen-reader@example.invalid",
    "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "PR checkout control");
  for (const branch of ["main", "pull-request"]) {
    const checkout = path.join(directory, branch);
    git(directory, "clone", "--no-local", "--single-branch", "--branch", branch, seed, checkout);
    assert.equal(fs.existsSync(path.join(checkout, ".git/objects/info/alternates")), false);
    for (const ref of readerRefs) {
      assert.notEqual(spawnSync("git", ["cat-file", "-e", ref], { cwd: checkout }).status, 0);
    }
    fs.mkdirSync(path.join(checkout, "web"));
    fs.symlinkSync(path.resolve(__dirname, "../web/node_modules"), path.join(checkout, "web/node_modules"), "junction");
    const result = spawnSync(process.execPath, ["-e", `
      const { acquireFrozenReader, readerRefs } = require("./scripts/frozen-assurance-reader.cjs");
      for (const ref of readerRefs) {
        const reader = acquireFrozenReader(ref, ".cache/" + ref);
        require("node:assert/strict").throws(() => reader.parseAssuranceReport("{}"),
          /^Error: Unsupported or incomplete assurance report\\.$/);
      }
    `], { cwd: checkout, encoding: "utf8", env: { ...process.env, NODE_PATH: path.resolve(__dirname, "../web/node_modules") } });
    assert.equal(result.status, 0, result.stderr || result.stdout);
  }
});
