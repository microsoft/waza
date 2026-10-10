const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const { execFileSync } = require("node:child_process");
const ts = require("../web/node_modules/typescript");

// Extracted readers resolve their unchanged imports through NODE_PATH.
require.resolve("ajv/dist/2020");
require.resolve("ajv-formats");
const root = process.env.WAZA_FROZEN_PRESERVED_READER_DIR;
if (!root || process.argv.length !== 6) {
  throw new Error("Set NODE_PATH=\"$PWD/web/node_modules\" and WAZA_FROZEN_PRESERVED_READER_DIR to an empty directory and supply positive/failure 1.2, offline 1.0 and calibrated 1.1 actual reports.");
}
fs.mkdirSync(root, { recursive: true });
if (fs.readdirSync(root).length) throw new Error("Frozen reader scratch directory must be empty.");
const inputs = process.argv.slice(2).map(filename => {
  const bytes = fs.readFileSync(filename);
  const report = JSON.parse(bytes);
  console.log(`${filename}: version=${report.schema_version}, sha256=${crypto.createHash("sha256").update(bytes).digest("hex")}`);
  return { filename, bytes, report };
});
if (inputs[0].report.schema_version !== "1.2" || inputs[0].report.state !== "passed" ||
    inputs[1].report.schema_version !== "1.2" || inputs[1].report.state === "passed" ||
    inputs[2].report.schema_version !== "1.0" || inputs[2].report.state !== "passed" ||
    inputs[3].report.schema_version !== "1.1" || inputs[3].report.state !== "passed") {
  throw new Error("Actual positive/nonpass 1.2 and positive 1.0/1.1 controls are required.");
}
for (const reader of [
  { ref: "d82cba11265919996660e02d348bb92a3ff1fd4d", allowCalibrated: false },
  { ref: "91146d4f53c986365e56cdbf503a10331c54d98a", allowCalibrated: true },
]) {
  const directory = path.join(root, reader.ref);
  const sources = [
    "web/src/types/assurance.ts", "web/src/lib/strictJson.ts",
    "schemas/grader-assurance-1.0.schema.json",
    "schemas/grader-reference-1.0.schema.json",
    "schemas/evidence-manifest-1.0.schema.json",
  ];
  if (reader.allowCalibrated) sources.push("schemas/grader-assurance-1.1.schema.json");
  for (const source of sources) {
    const bytes = execFileSync("git", ["show", `${reader.ref}:${source}`]);
    const target = path.join(directory, source.replace(/\.ts$/, ".js"));
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, source.endsWith(".ts") ? ts.transpileModule(bytes.toString("utf8"), {
      compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
    }).outputText : bytes);
    console.log(`${reader.ref}:${source}: ${crypto.createHash("sha256").update(bytes).digest("hex")}`);
  }
  const { parseAssuranceReport } = require(path.resolve(directory, "web/src/types/assurance.js"));
  for (const input of inputs) {
    let accepted = false;
    try {
      parseAssuranceReport(input.bytes.toString("utf8"), reader.allowCalibrated);
      accepted = true;
    } catch (error) {
      if (error.message !== "Unsupported or incomplete assurance report.") throw error;
    }
    const expected = input.report.schema_version === "1.0" ||
      (reader.allowCalibrated && input.report.schema_version === "1.1");
    if (accepted !== expected) throw new Error(`Frozen ${reader.ref} admission mismatch for ${input.filename}`);
    console.log(`${reader.ref}:${input.filename}: admitted=${accepted}`);
  }
}
