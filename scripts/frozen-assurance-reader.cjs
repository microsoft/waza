const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const ts = require("../web/node_modules/typescript");

const readerRefs = [
  "d82cba11265919996660e02d348bb92a3ff1fd4d",
  "91146d4f53c986365e56cdbf503a10331c54d98a",
];
const snapshotRoot = path.resolve(__dirname, "../testdata/frozen-assurance-readers");
const commonSources = [
  "web/src/types/assurance.ts",
  "web/src/lib/strictJson.ts",
  "schemas/grader-assurance-1.0.schema.json",
  "schemas/grader-reference-1.0.schema.json",
  "schemas/evidence-manifest-1.0.schema.json",
];

function acquireFrozenReader(ref, directory, sourceRoot = snapshotRoot) {
  if (!readerRefs.includes(ref)) throw new Error(`Unknown frozen reader: ${ref}`);
  const manifest = JSON.parse(fs.readFileSync(path.join(sourceRoot, "manifest.json"), "utf8"));
  if (manifest.repository !== "https://github.com/microsoft/waza") {
    throw new Error("Unexpected frozen reader source repository.");
  }
  const sources = [...commonSources];
  if (ref === readerRefs[1]) sources.push("schemas/grader-assurance-1.1.schema.json");
  const identities = manifest.readers?.[ref];
  if (!identities || JSON.stringify(Object.keys(identities).sort()) !== JSON.stringify([...sources].sort())) {
    throw new Error(`Incomplete frozen reader manifest: ${ref}`);
  }
  // Verify the entire immutable source set before transpiling or initializing it.
  const verified = sources.map(source => {
    const bytes = fs.readFileSync(path.join(sourceRoot, ref, source));
    const sha256 = crypto.createHash("sha256").update(bytes).digest("hex");
    const blob = crypto.createHash("sha1").update(`blob ${bytes.length}\0`).update(bytes).digest("hex");
    if (sha256 !== identities[source].sha256 || blob !== identities[source].git_blob) {
      throw new Error(`Frozen reader identity mismatch: ${ref}:${source}`);
    }
    return { source, bytes, sha256, blob };
  });
  fs.mkdirSync(directory, { recursive: true });
  if (fs.readdirSync(directory).length) throw new Error("Frozen reader scratch directory must be empty.");
  for (const { source, bytes, sha256, blob } of verified) {
    const target = path.join(directory, source.replace(/\.ts$/, ".js"));
    let output = bytes;
    if (source.endsWith(".ts")) {
      const result = ts.transpileModule(bytes.toString("utf8"), {
        fileName: source,
        reportDiagnostics: true,
        compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
      });
      const errors = result.diagnostics.filter(item => item.category === ts.DiagnosticCategory.Error);
      if (errors.length) {
        throw new Error(`Frozen reader transpilation failed: ${ref}:${source}: ${errors.map(item =>
          ts.flattenDiagnosticMessageText(item.messageText, "\n")).join("; ")}`);
      }
      output = result.outputText;
    }
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, output);
    console.log(`${ref}:${source}: ${sha256} git_blob=${blob}`);
  }
  // Module import compiles the historical schemas; setup errors must not become report rejections.
  const reader = require(path.resolve(directory, "web/src/types/assurance.js"));
  if (typeof reader.parseAssuranceReport !== "function") {
    throw new Error(`Frozen reader export missing: ${ref}`);
  }
  return reader;
}

module.exports = { acquireFrozenReader, readerRefs, snapshotRoot };
