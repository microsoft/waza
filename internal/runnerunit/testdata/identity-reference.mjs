// Independent producer oracle: manually typed source trees, no Go/YAML parser.
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";

const version = "sha256-waza-c14n-v1";
const integer = (value) => ({ integer: String(value) });
const frame = (tag, text) => `${tag}${Buffer.byteLength(text)}:${text}`;
function encode(value) {
  if (typeof value === "string") return frame("s", value);
  if (Array.isArray(value)) return `a${value.length}:` + value.map(encode).join("");
  if (Object.hasOwn(value, "integer")) return frame("i", value.integer);
  const keys = Object.keys(value).sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)));
  return `m${keys.length}:` + keys.map((key) => encode(key) + encode(value[key])).join("");
}
function hash(namespace, fields) {
  return createHash("sha256").update(`${version}\0${namespace}\0${encode(fields)}`).digest("hex");
}
const baseline = JSON.parse(readFileSync(new URL("../../../examples/runner/unit.json", import.meta.url), "utf8"));
const evalTree = {
  schemaVersion: "2.0", scenario: "runner-example", name: "Runner example",
  config: { executor: "mock", model: "mock-model", trials_per_task: integer(3), max_attempts: integer(3), timeout_seconds: integer(30) },
  tasks: ["adjacent-must-not-be-opened/*.yaml"],
};
const taskTree = { id: "selected", name: "Selected task", inputs: { prompt: "hello" }, expected: { output_contains: ["hello"] } };
const cases = [];
function add(name, mutate = () => {}, evalValue = evalTree, taskValue = taskTree) {
  const request = structuredClone(baseline);
  mutate(request);
  const canonicalEval = encode(evalValue);
  const canonicalTask = encode(taskValue);
  const run = hash("run", [request.schema_version, request.run_key, String(request.timeout_ms), canonicalEval]);
  const shard = hash("shard", [run, request.input.shard]);
  const task = hash("task", [shard, request.selection.task, canonicalTask]);
  const trial = hash("trial", [task, String(request.selection.trial)]);
  const attempt = hash("attempt", [trial, String(request.selection.attempt)]);
  cases.push({ name, request, canonical_eval: canonicalEval, canonical_task: canonicalTask, identity: {
    version, canonical_digest: hash("input", [run, shard, task, trial, attempt]),
    run: `run-${run}`, shard: `shard-${shard}`, task: `task-${task}`, trial: `trial-${trial}`, attempt: `attempt-${attempt}`,
  } });
}
add("baseline");
add("presentation", (r) => {
  r.input.eval = '# equivalent\r\n{tasks: ["adjacent-must-not-be-opened/*.yaml"], name: Runner example, scenario: runner-example, schemaVersion: "2.0", config: {timeout_seconds: 30, max_attempts: 0x3, trials_per_task: 03, model: mock-model, executor: mock}}\r\n';
  r.input.task = '---\r\nexpected: {output_contains: [hello]}\r\ninputs: {prompt: "he\\u006clo"}\r\nname: Selected task\r\nid: selected\r\n';
});
add("offers-and-consent", (r) => {
  r.versions = ["9.0", "1.0"];
  r.required_capabilities = ["inline.mock-text", "exact-unit", "negotiation"];
  r.optional_capabilities = ["future"];
  r.allow_paid_execution = true;
});
add("run-key", (r) => { r.run_key = "another-run"; });
add("timeout", (r) => { r.timeout_ms++; });
add("eval-source", (r) => { r.input.eval = r.input.eval.replace("mock-model", "other-model"); },
  { ...evalTree, config: { ...evalTree.config, model: "other-model" } });
add("shard", (r) => { r.selection.shard = r.input.shard = "shard-b"; });
add("task-source", (r) => { r.input.task = r.input.task.replace("prompt: hello", "prompt: goodbye"); },
  evalTree, { ...taskTree, inputs: { prompt: "goodbye" } });
add("task-key", (r) => {
  r.selection.task = "other";
  r.input.task = r.input.task.replace("id: selected", "id: other");
}, evalTree, { ...taskTree, id: "other" });
add("trial", (r) => { r.selection.trial = 3; });
add("attempt", (r) => { r.selection.attempt = 3; });
const output = JSON.stringify(cases, null, 2) + "\n";
const target = new URL("./identities.json", import.meta.url);
if (process.argv[2] === "--write") {
  writeFileSync(target, output);
} else if (readFileSync(target, "utf8").replaceAll("\r\n", "\n") !== output) {
  throw new Error("Frozen producer identity oracle differs; review the ABI, do not regenerate to repair tests");
}
console.log(`Verified ${cases.length} producer identity vectors.`);
