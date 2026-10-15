const fs = require("node:fs");
const crypto = require("node:crypto");
const { acquireFrozenReader, readerRefs } = require("./frozen-assurance-reader.cjs");

const ref = readerRefs[0];
const root = process.env.WAZA_FROZEN_READER_DIR;
if (!root || process.argv.length < 5) {
  throw new Error("Set WAZA_FROZEN_READER_DIR to an empty scratch directory and supply calibrated and offline reports.");
}
const { parseAssuranceReport } = acquireFrozenReader(ref, root);
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
