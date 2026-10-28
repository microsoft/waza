import { readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { errorText, imageReference, invokeContainer } from './infra.mjs';

export async function smoke(image, local = false) {
  imageReference(image, local);
  const request = JSON.parse(await readFile(new URL('./request.json', import.meta.url), 'utf8'));
  const advertised = await invokeContainer(image, ['runner', 'capabilities'], '');
  if (advertised.status !== 0 || advertised.signal) {
    throw new Error(`Public capabilities command failed: ${advertised.stderr}`);
  }
  const capabilities = JSON.parse(advertised.stdout);
  if (!Array.isArray(capabilities.capabilities) ||
      !request.required_capabilities.every((value) => capabilities.capabilities.includes(value))) {
    throw new Error('Public runner lacks required exact-unit/mock-text/terminal capabilities; integrate #758-#760 before conformance');
  }
  const negotiated = await invokeContainer(image, ['runner', 'negotiate'], JSON.stringify(request));
  if (negotiated.status !== 0 || negotiated.signal) {
    throw new Error(`Public negotiation command failed: ${negotiated.stderr}`);
  }
  const response = JSON.parse(negotiated.stdout);
  if (response.kind !== 'waza.runner-negotiation' || response.schema_version !== '1.0' ||
      response.status !== 'compatible' || response.negotiation?.version !== '1.0' ||
      !Array.isArray(response.negotiation.selected_capabilities) ||
      !request.required_capabilities.every((value) => response.negotiation.selected_capabilities.includes(value))) {
    throw new Error('Negotiation did not select the complete required public contract');
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2);
    const local = args[0] === '--local';
    if (local) args.shift();
    if (args.length !== 1) throw new Error('Usage: node examples/runner-reference/smoke.mjs [--local] IMAGE');
    await smoke(args[0], local);
    console.log('Public capability/negotiation smoke passed; this does not prove execution or terminal conformance.');
  } catch (error) {
    console.error(errorText(error));
    process.exitCode = 1;
  }
}
