const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { TextDecoder } = require('node:util');
const { parseStrictJSON } = require('../../web/src/lib/strictJson.ts');

const interpreter = path.join(__dirname, 'pr-completeness.cjs');
const decode = (bytes) => parseStrictJSON(new TextDecoder('utf-8', { fatal: true }).decode(bytes));

function requireValue(condition, message) {
  if (!condition) throw new Error(message);
}

function readJSON(file) {
  requireValue(fs.lstatSync(file).isFile(), `Expected regular JSON file: ${path.basename(file)}`);
  return decode(fs.readFileSync(file));
}

function execute(command, args) {
  const result = spawnSync(command, args, { encoding: 'buffer', timeout: 60_000, maxBuffer: 128 * 1024 * 1024 });
  if (result.stderr?.length) process.stderr.write(result.stderr);
  requireValue(!result.error && result.signal === null && Number.isInteger(result.status), 'Evidence command did not complete');
  return result;
}

function invoke(args) {
  const result = execute(process.execPath, [interpreter, ...args]);
  requireValue(result.status === 0, 'PR source/plan admission failed');
  return decode(result.stdout);
}

function writeJSON(file, value) {
  fs.writeFileSync(file, `${JSON.stringify(value)}\n`, { flag: 'wx' });
}

function plan(directory) {
  const value = invoke(['plan']);
  fs.mkdirSync(directory);
  const file = path.join(directory, 'plan.json');
  writeJSON(file, value);
  const outputs = invoke(['outputs', file]);
  requireValue(process.env.GITHUB_OUTPUT, 'Missing workflow output destination');
  fs.appendFileSync(process.env.GITHUB_OUTPUT,
    Object.entries(outputs).map(([name, value]) => `${name}=${value}\n`).join(''));
  return outputs;
}

function receipts(directory) {
  if (!fs.existsSync(directory)) return [];
  requireValue(fs.lstatSync(directory).isDirectory(), 'Receipt download root is not a directory');
  return fs.readdirSync(directory).sort().map((name) => {
    const artifact = path.join(directory, name);
    requireValue(fs.lstatSync(artifact).isDirectory(), 'Receipt artifact is not a directory');
    const files = fs.readdirSync(artifact);
    requireValue(files.length === 1 && files[0] === 'receipt.json', 'Receipt artifact must contain exactly receipt.json');
    const receipt = readJSON(path.join(artifact, 'receipt.json'));
    requireValue(receipt !== null && typeof receipt === 'object' && receipt.artifact_name === name,
      'Downloaded artifact name does not match receipt');
    return receipt;
  });
}

function admit(planDirectory, receiptDirectory, resultDirectory) {
  requireValue(fs.lstatSync(planDirectory).isDirectory(), 'Plan download root is not a directory');
  const files = fs.readdirSync(planDirectory);
  requireValue(files.length === 1 && files[0] === 'plan.json', 'Plan artifact must contain exactly plan.json');
  const planFile = path.join(planDirectory, 'plan.json');
  const outputs = invoke(['outputs', planFile]);
  requireValue(outputs.plan_artifact === process.env.WAZA_COMPLETENESS_PLAN, 'Plan artifact name does not match current run/attempt');
  const value = readJSON(planFile);
  requireValue(typeof process.env.WAZA_COMPLETENESS_NEEDS === 'string', 'Missing caller dependency inventory');
  const needs = parseStrictJSON(process.env.WAZA_COMPLETENESS_NEEDS);
  const inventory = receipts(receiptDirectory);
  fs.mkdirSync(resultDirectory);

  // Read current PR state only after local evidence is assembled, immediately
  // before the interpreter repeats source/path admission and evaluates it.
  requireValue(typeof process.env.GH_TOKEN === 'string' && process.env.GH_TOKEN.length > 0,
    'Missing admission read token');
  const response = execute('gh', ['api', '--hostname', 'github.com', `repos/microsoft/waza/pulls/${value.source.pr}`]);
  requireValue(response.status === 0, 'Current PR API evidence is unavailable');
  const input = { plan: value, needs, receipts: inventory, current_pr: decode(response.stdout) };
  const inputFile = path.join(resultDirectory, 'input.json');
  writeJSON(inputFile, input);
  const evaluation = execute(process.execPath, [interpreter, 'evaluate', inputFile]);
  requireValue(evaluation.status === 0 || evaluation.status === 1, 'Completeness interpreter did not complete normally');
  const result = decode(evaluation.stdout);
  requireValue(result.kind === 'waza.pr-completeness-result' &&
    typeof result.passed === 'boolean' && evaluation.status === (result.passed ? 0 : 1),
  'Completeness result and exit status disagree');
  writeJSON(path.join(resultDirectory, 'result.json'), result);
  if (process.env.GITHUB_STEP_SUMMARY) {
    fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY,
      `## PR completeness: ${result.passed ? 'passed' : 'unavailable'}\n\n` +
      '| Family | Evidence |\n| --- | --- |\n' +
      Object.entries(result.scopes).map(([family, status]) => `| ${family} | ${status} |\n`).join('') +
      `\nDiagnostics: ${result.diagnostics.length}. See the result artifact for details.\n`);
  }
  return result;
}

if (require.main === module) {
  try {
    const args = process.argv.slice(2);
    let result;
    if (args[0] === 'plan' && args.length === 2) result = plan(args[1]);
    else {
      requireValue(args[0] === 'admit' && args.length === 4,
        'Usage: pr-completeness-admission.cjs plan DIRECTORY | admit PLAN_DIRECTORY RECEIPT_DIRECTORY RESULT_DIRECTORY');
      result = admit(...args.slice(1));
    }
    process.stdout.write(`${JSON.stringify(result)}\n`);
    if (result.passed === false) process.exitCode = 1;
  } catch (error) {
    process.stderr.write(`PR completeness admission: ${error.message}\n`);
    if (process.env.GITHUB_STEP_SUMMARY) {
      fs.appendFileSync(process.env.GITHUB_STEP_SUMMARY,
        '## PR completeness: unavailable\n\nSource, applicability, artifact transfer, or current PR admission failed. See the failing step; no passing result was produced.\n');
    }
    process.exitCode = 1;
  }
}
