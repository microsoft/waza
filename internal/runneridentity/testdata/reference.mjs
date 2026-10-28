// Independent ABI oracle: explicit typed values, no YAML parser or Go imports.
// Run without arguments to verify frozen vectors; --write is for ABI authors.
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';

const version = 'sha256-waza-c14n-v1';
const integer = value => ({ integer: value });
const float = value => ({ float: value });
const scalar = (tag, text) => `${tag}${Buffer.byteLength(text)}:${text}`;
function encode(value) {
  if (value === null) return 'n';
  if (value === true) return 'b1';
  if (value === false) return 'b0';
  if (typeof value === 'string') return scalar('s', value);
  if (typeof value !== 'object') throw new Error('Oracle requires explicitly typed exact values');
  if (Array.isArray(value)) return `a${value.length}:` + value.map(encode).join('');
  if (Object.hasOwn(value, 'integer')) return scalar('i', value.integer);
  if (Object.hasOwn(value, 'float')) return scalar('f', value.float);
  const keys = Object.keys(value).sort((a, b) => Buffer.compare(Buffer.from(a), Buffer.from(b)));
  return `m${keys.length}:` + keys.map(key => encode(key) + encode(value[key])).join('');
}
function hash(namespace, fields) {
  const payload = `${version}\0${namespace}\0a${fields.length}:` + fields.map(field => scalar('s', field)).join('');
  return createHash('sha256').update(payload, 'utf8').digest('hex');
}
const evalSource = 'schemaVersion: "2.0"\nscenario: identities\nname: Identities\nconfig: {executor: mock, model: mock-model, trials_per_task: 3, max_attempts: 3}\ntasks: ["ignored/*.yaml"]\n';
const evalTree = {
  schemaVersion: '2.0', scenario: 'identities', name: 'Identities',
  config: { executor: 'mock', model: 'mock-model', trials_per_task: integer('3'), max_attempts: integer('3') },
  tasks: ['ignored/*.yaml'],
};
const taskSource = 'id: selected\nname: Selected\ninputs: {prompt: hello}\n';
const taskTree = { id: 'selected', name: 'Selected', inputs: { prompt: 'hello' } };
const base = {
  schema_version: '1.0', versions: ['1.0'],
  required_capabilities: ['exact-unit', 'inline.mock-text'], optional_capabilities: [],
  run_key: 'identity-conformance',
  selection: { shard: 'shard-a', task: 'selected', trial: 1, attempt: 1 },
  input: { shard: 'shard-a', eval: evalSource, task: taskSource },
  timeout_ms: 60000, allow_paid_execution: false,
};
const cases = [];
function add(name, mutate = () => {}, evalValue = evalTree, taskValue = taskTree, profile = 'exact-unit') {
  const request = structuredClone(base);
  mutate(request);
  const canonicalEval = encode(evalValue);
  const canonicalTask = encode(taskValue);
  const run = hash('run', [request.schema_version, request.run_key, String(request.timeout_ms), canonicalEval]);
  const shard = hash('shard', [run, request.input.shard]);
  const task = hash('task', [shard, request.selection.task, canonicalTask]);
  const trial = hash('trial', [task, String(request.selection.trial)]);
  const attempt = hash('attempt', [trial, String(request.selection.attempt)]);
  cases.push({
    name, profile, request, canonical_eval: canonicalEval, canonical_task: canonicalTask,
    identity: {
      version, canonical_digest: hash('input', [run, shard, task, trial, attempt]),
      run: `run-${run}`, shard: `shard-${shard}`, task: `task-${task}`, trial: `trial-${trial}`, attempt: `attempt-${attempt}`,
    },
  });
}
add('baseline');
add('presentation-equivalent', request => {
  request.input.eval = '# same declarations\r\n{tasks: ["ignored/*.yaml"], config: {max_attempts: 0x3, trials_per_task: 03, model: mock-model, executor: mock}, name: Identities, scenario: identities, schemaVersion: "2.0"}\r\n';
  request.input.task = '---\r\ninputs:\r\n  prompt: "he\\u006clo"\r\nname: Selected\r\nid: selected\r\n';
});
add('run-key', request => { request.run_key = 'another-run'; });
add('timeout', request => { request.timeout_ms = 60001; });
add('eval-source', request => {
  request.input.eval = evalSource.replace('mock-model', 'other-model');
}, { ...evalTree, config: { ...evalTree.config, model: 'other-model' } });
add('shard', request => { request.selection.shard = request.input.shard = 'shard-b'; });
add('task-source', request => {
  request.input.task = taskSource.replace('hello', 'goodbye');
}, evalTree, { ...taskTree, inputs: { prompt: 'goodbye' } });
add('task-key', request => {
  request.selection.task = 'other';
  request.input.task = taskSource.replace('selected', 'other');
}, evalTree, { ...taskTree, id: 'other' });
add('trial', request => { request.selection.trial = 2; });
add('attempt', request => { request.selection.attempt = 2; });
add('negotiation-only', request => {
  request.versions = ['9.0', '1.0'];
  request.required_capabilities = ['negotiation'];
  request.optional_capabilities = ['future'];
  request.allow_paid_execution = true;
});
// Canonical-only controls do not claim exact-unit admission or execution.
for (const [name, source, value] of [
  ['absent', '{}', {}],
  ['null', '{v: null}', { v: null }],
  ['empty-string', '{v: ""}', { v: '' }],
  ['false', '{v: false}', { v: false }],
  ['zero-int', '{v: 0}', { v: integer('0') }],
  ['zero-float', '{v: 0.0}', { v: float('0e0') }],
  ['negative-zero-float', '{v: -0.0}', { v: float('-0e0') }],
  ['empty-map', '{v: {}}', { v: {} }],
  ['empty-array', '{v: []}', { v: [] }],
  ['large-int', '{v: 9007199254740993}', { v: integer('9007199254740993') }],
  ['nearby-large-int', '{v: 9007199254740992}', { v: integer('9007199254740992') }],
  ['decimal-precision', '{v: !!float 1.2345678901234567890123456789}', { v: float('12345678901234567890123456789e-28') }],
  ['unicode-composed', '{v: "\\u00e9"}', { v: '\u00e9' }],
  ['unicode-decomposed', '{v: "e\\u0301"}', { v: 'e\u0301' }],
  ['text-lf', '{v: "a\\nb"}', { v: 'a\nb' }],
  ['text-crlf', '{v: "a\\r\\nb"}', { v: 'a\r\nb' }],
  ['array-order-a', '{v: [a, b]}', { v: ['a', 'b'] }],
  ['array-order-b', '{v: [b, a]}', { v: ['b', 'a'] }],
]) {
  add(name, request => { request.input.task = source; }, evalTree, value, 'canonical-only');
}
const output = JSON.stringify(cases, null, 2) + '\n';
const path = new URL('./vectors.json', import.meta.url);
if (process.argv[2] === '--write') {
  writeFileSync(path, output);
  console.log(`Wrote ${cases.length} independently calculated vectors.`);
} else {
  const frozen = readFileSync(path, 'utf8').replaceAll('\r\n', '\n');
  if (frozen !== output) throw new Error('Frozen identity vectors differ from independent reference');
  console.log(`Verified ${cases.length} frozen vectors.`);
}
