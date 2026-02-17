const vm = require("node:vm");
const fs = require("node:fs");
const process = require("node:process");

// NOTE: if you're editing this file do NOT print to stdout - we parse that in the caller to determine
// what's failed.

const inputData = fs.readFileSync("/dev/stdin", "utf-8");
const data = (JSON.parse(inputData));

// stderr isn't captured by the caller, so you can use this to do some print debugging.
// process.stderr.write(`Received data: ${inputData}\n`);

if (data.debug) {
  process.stderr.write(inputData);
}

const evalContext = {
  output: data.output,
  outcome: data.outcome,
  transcript: data.transcript,
  tool_calls: data.tool_calls,
  errors: data.transcript.filter(
    (t) =>
      (t.type && t.type.includes("error")) ||
      (t.content && String(t.content).includes("error"))
  ),
  duration_ms: data.duration_ms || 0,

  // Expose safe builtins
  Array,
  Object,
  String,
  Number,
  Boolean,
  Math,
  JSON,
  RegExp,
  parseInt,
  parseFloat,
  isNaN,
  isFinite,
  undefined,
  NaN,
  Infinity,
  True: true,
  False: false,
};

vm.createContext(evalContext);

const assertions = data.assertions || [];
const results = [];

for (const assertion of assertions) {
  try {
    const result = vm.runInContext(assertion, evalContext, { timeout: 5000 });
    results.push(!!result ? "" : "fail");
  } catch (err) {
    results.push(String(err));
  }
}

process.stdout.write(JSON.stringify({ results }));
