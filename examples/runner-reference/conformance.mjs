import { mkdir, readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { errorText, imageReference, invokeContainer } from './infra.mjs';
import { smoke } from './smoke.mjs';

export function cases(request) {
  const result = [{ name: 'selected', input: JSON.stringify(request) }];
  for (const [field, value] of [['shard', 'neighbor'], ['task', 'neighbor'], ['trial', 4], ['attempt', 4]]) {
    const invalid = structuredClone(request);
    invalid.selection[field] = value;
    result.push({ name: `invalid-${field}`, input: JSON.stringify(invalid) });
  }
  const failure = structuredClone(request);
  failure.input.task = failure.input.task.replace('    - hello', '    - required-output-not-produced-762');
  result.push({ name: 'evaluation-failure', input: JSON.stringify(failure) });
  result.push({ name: 'malformed', input: '{"schema_version":' });
  result.push({ name: 'extra-document', input: `${JSON.stringify(request)}\n{}` });
  return result;
}

export function verifyConformance() {
  throw new Error('BLOCKED: integrate committed #758 execution evidence, #759 independent identity oracle, and #760 terminal schema/exit verification; raw captures are not conformance proof');
}

export async function capture(image, output, local = false) {
  imageReference(image, local);
  if (!path.isAbsolute(output)) throw new Error('Output directory must be absolute and not already exist');
  const request = JSON.parse(await readFile(new URL('./request.json', import.meta.url), 'utf8'));
  await smoke(image, local);
  await mkdir(output);
  const records = [];
  for (const scenario of cases(request)) {
    const result = await invokeContainer(image, ['runner', 'run'], scenario.input);
    const record = { name: scenario.name, request: scenario.input, ...result };
    await writeFile(path.join(output, `${scenario.name}.json`), JSON.stringify(record, null, 2) + '\n', { flag: 'wx' });
    records.push(record);
  }
  return records;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    const local = args[0] === '--local';
    if (local) args.shift();
    if (args.length !== 2) throw new Error('Usage: node examples/runner-reference/conformance.mjs [--local] IMAGE /absolute/new-output-directory');
    const records = await capture(args[0], args[1], local);
    verifyConformance(records);
  } catch (error) {
    console.error(errorText(error));
    process.exitCode = 1;
  }
}
