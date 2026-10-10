const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const { execFileSync } = require("node:child_process");
const ts = require("../web/node_modules/typescript");

const ref = "d82cba11265919996660e02d348bb92a3ff1fd4d";
const root = process.env.WAZA_FROZEN_READER_DIR;
if (!root || process.argv.length < 5) {
  throw new Error("Set WAZA_FROZEN_READER_DIR to an empty scratch directory and supply calibrated and offline reports.");
}
fs.mkdirSync(root, { recursive: true });
if (fs.readdirSync(root).length) throw new Error("Frozen reader scratch directory must be empty.");
for (const source of [
  "web/src/types/assurance.ts",
  "web/src/lib/strictJson.ts",
  "schemas/grader-assurance-1.0.schema.json",
  "schemas/grader-reference-1.0.schema.json",
  "schemas/evidence-manifest-1.0.schema.json",
]) {
  const bytes = execFileSync("git", ["show", `${ref}:${source}`]);
  const target = path.join(root, source.replace(/\.ts$/, ".js"));
  fs.mkdirSync(path.dirname(target), { recursive: true });
  fs.writeFileSync(target, source.endsWith(".ts") ? ts.transpileModule(bytes.toString("utf8"), {
    compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
  }).outputText : bytes);
  console.log(`${ref}:${source}: ${crypto.createHash("sha256").update(bytes).digest("hex")}`);
}
const { parseAssuranceReport } = require(path.resolve(root, "web/src/types/assurance.js"));
let calibrated = 0;
let offline = 0;
for (const filename of process.argv.slice(2)) {
  const bytes = fs.readFileSync(filename);
  const report = JSON.parse(bytes);
  if (report.schema_version !== "1.0" && report.schema_version !== "1.1") {
    throw new Error(`Unexpected report version for ${filename}`);
  }
  let accepted = false;
  try {
    parseAssuranceReport(bytes.toString("utf8"));
    accepted = true;
  } catch (error) {
    if (error.message !== "Unsupported or incomplete assurance report.") throw error;
  }
  if (accepted !== (report.schema_version === "1.0")) {
    throw new Error(`Frozen actual reader unexpected admission for ${filename}`);
  }
  if (accepted) offline++; else calibrated++;
  console.log(`${filename}: version=${report.schema_version}, admitted=${accepted}, sha256=${crypto.createHash("sha256").update(bytes).digest("hex")}`);
}
if (!calibrated || !offline) throw new Error("Both calibrated rejection and offline acceptance controls are required.");
