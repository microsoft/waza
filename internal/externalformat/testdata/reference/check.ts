import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import assert from "node:assert/strict";

const checksums = {
  "eval-file.schema.ts": "ce8d433fac359995b1af8f12f12afc9477cdd386b0bd90409804758075705ea8",
  "yaml-loader.ts": "cd2460bd31c5ebda01282a756f9a57bf0a33af77e5e9a3d0b785f17186d8a29e",
  "assertions.ts": "152446e80042b29597eb9d124860a6eb67587be1d642b50c101af3711c9ecb02",
  "scoring.ts": "167936782bc74f93ccef9b026a94da46ca0a53759dd2f90a3df0ffcf2acbf59e",
};
for (const [file, digest] of Object.entries(checksums)) {
  assert.equal(createHash("sha256").update(readFileSync(new URL(file, import.meta.url))).digest("hex"), digest);
}

// No provider is constructed. Only the pinned pure parser/schema/grader run.
globalThis.fetch = () => { throw new Error("reference must not use a network"); };
const { EvalFileSchema } = await import("./eval-file.schema.ts");
const { parseYamlValue } = await import("./yaml-loader.ts");
const { runContainsAssertion } = await import("./assertions.ts");
const { scoreToVerdict } = await import("./scoring.ts");
const source = readFileSync(new URL("../contains.eval.yaml", import.meta.url), "utf8");
const suite = EvalFileSchema.parse(parseYamlValue(source));
assert.equal(suite.tests.length, 3);
const controls = new Map([
  ["world", ["Hello world", "prefix world suffix", "Hello World", ""]],
  ["unicode", ["café 🌍", "cafe\u0301 🌍", "CAFÉ 🌍", ""]],
  ["exact", ["A\r\nB", "A\nB", "A\rB", "prefix A\r\nB suffix"]],
]);
const observations = [];
for (const test of suite.tests) {
  assert.equal(test.assert.length, 1);
  assert.equal(test.assert[0].type, "contains");
  assert.deepEqual(Object.keys(test.assert[0]).sort(), ["type", "value"]);
  const outputs = controls.get(test.id);
  assert.ok(outputs, "every admitted case has explicit output controls");
  const results = outputs.map((output) => {
    const result = runContainsAssertion(output, test.assert[0].value);
    return { id: test.id, output, score: result.score, verdict: scoreToVerdict(result.score) };
  });
  assert.ok(results.some((result) => result.score === 0));
  assert.ok(results.some((result) => result.score === 1));
  observations.push(...results);
}
assert.equal(
  EvalFileSchema.safeParse(parseYamlValue(source.replace("requires:", "input: []\nrequires:"))).success,
  false,
);
for (const value of ["0", "false"]) {
  const alternate = EvalFileSchema.parse(parseYamlValue(source.replace("value: world", `value: "${value}"`)));
  const assertion = alternate.tests[0].assert[0];
  assert.equal(assertion.value, value);
  assert.equal(runContainsAssertion(value, assertion.value).score, 1);
  assert.equal(runContainsAssertion("", assertion.value).score, 0);
}
// These are genuinely upstream-admitted fields, not just malformed YAML/type
// controls. The Go profile rejects their presence rather than stripping them.
for (const [field, value] of [
  ["weight", 0], ["weight", 1], ["min_score", 0.8],
  ["required", true], ["required", false], ["negate", false],
]) {
  const alternate = EvalFileSchema.parse(parseYamlValue(
    source.replace("value: world", `value: world\n        ${field}: ${value}`),
  ));
  assert.equal(alternate.tests[0].assert[0][field], value);
}
const observed = {
  upstream: "EntityProcess/agentv@68df6d2206cea61f721d0445b405efcb5e94aff0",
  fixture: "Waza-authored complete conformance suite; not an unchanged upstream suite",
  parser: "parseYamlValue + EvalFileSchema.parse",
  grader: "runContainsAssertion + scoreToVerdict",
  provider_execution: "not invoked; no provider/model/tool execution or compatibility claim",
  observations,
};
if (process.argv.includes("--verify")) {
  const frozen = JSON.parse(readFileSync(new URL("./observed.json", import.meta.url), "utf8"));
  assert.deepEqual(observed, frozen);
} else {
  console.log(JSON.stringify(observed, null, 2));
}
