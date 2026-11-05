const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
const { acquireFrozenReader, readerRefs } = require("./frozen-assurance-reader.cjs");

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
  { ref: readerRefs[0], allowCalibrated: false },
  { ref: readerRefs[1], allowCalibrated: true },
]) {
  const directory = path.join(root, reader.ref);
  const { parseAssuranceReport } = acquireFrozenReader(reader.ref, directory);
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
