import { copyFile, mkdir, readFile, readdir, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { checked, digestPattern, errorText, hashFile, temporaryDirectory } from './infra.mjs';

const root = fileURLToPath(new URL('../..', import.meta.url));

export function linuxPayload(name) {
  return /^internal\/embedded\/.*(?:linux_amd64|linuxmusl_amd64|linux-x64|linuxmusl-x64).*\.(?:zst|tgz)$/.test(name);
}

export async function hydratePayload(source, destination) {
  const pointer = await readFile(destination, 'utf8');
  const match = /^version https:\/\/git-lfs.github.com\/spec\/v1\noid sha256:([a-f0-9]{64})\nsize ([0-9]+)\n$/.exec(pointer);
  if (!match) throw new Error(`Expected committed LFS pointer: ${destination}`);
  await copyFile(source, destination);
  if (String((await stat(destination)).size) !== match[2] || await hashFile(destination) !== match[1]) {
    throw new Error(`Unhydrated or corrupt LFS payload: ${source}; hydrate the reviewed checkout with git lfs pull`);
  }
}

export async function snapshot(checkout, destination) {
  const revision = await checked('git', ['rev-parse', 'HEAD'], { cwd: checkout });
  if (!/^[a-f0-9]{40}$/.test(revision)) throw new Error('Expected full committed source SHA');
  const dirty = await checked('git', ['status', '--porcelain', '--untracked-files=no'], { cwd: checkout });
  if (dirty) throw new Error('Build requires a clean tracked checkout; commit intended source changes first');
  const archive = path.join(destination, 'source.tar');
  await checked('git', ['archive', '--format=tar', `--output=${archive}`, revision], {
    cwd: checkout, env: { ...process.env, GIT_LFS_SKIP_SMUDGE: '1' },
  });
  const context = path.join(destination, 'source');
  await mkdir(context);
  await checked('tar', ['-xf', archive, '-C', context], { timeout: 120_000 });
  const payloads = (await readdir(path.join(context, 'internal/embedded')))
    .map((name) => `internal/embedded/${name}`).filter(linuxPayload);
  if (payloads.length === 0) throw new Error('No Linux amd64 embedded payloads found in committed source');
  for (const name of payloads) {
    await hydratePayload(path.join(checkout, name), path.join(context, name));
  }
  const epoch = await checked('git', ['show', '-s', '--format=%ct', revision], { cwd: checkout });
  if (!/^[0-9]+$/.test(epoch)) throw new Error('Invalid source timestamp');
  return { revision, epoch, context, payloads };
}

export async function build(output) {
  if (process.platform !== 'linux' || process.arch !== 'x64') {
    throw new Error('Build evidence requires native Linux x86_64; macOS/ARM and emulation are not covered');
  }
  const daemon = await checked('docker', ['info', '--format', '{{.OSType}}/{{.Architecture}}'], { timeout: 15_000 });
  if (daemon !== 'linux/x86_64') throw new Error(`Expected native linux/amd64 Docker daemon, got ${daemon}`);
  await checked('docker', ['buildx', 'version']);
  await mkdir(output);
  return temporaryDirectory('waza-reference-build-', async (temporary) => {
    const { revision, epoch, context, payloads } = await snapshot(root, temporary);
    const tag = `waza-runner-reference:prep-${randomUUID()}`;
    const archive = path.join(temporary, 'image.tar');
    const buildIdFile = path.join(temporary, 'buildx-image-id');
    await checked('docker', [
      'buildx', 'build', '--platform=linux/amd64', '--no-cache',
      '--provenance=false', '--sbom=false',
      '--output', `type=docker,rewrite-timestamp=true,dest=${archive}`,
      '--tag', tag, '--iidfile', buildIdFile,
      '--build-arg', `SOURCE_DATE_EPOCH=${epoch}`,
      '--build-arg', `REVISION=${revision}`,
      '--file', path.join(context, 'examples/runner-reference/Dockerfile'), context,
    ], { timeout: 30 * 60_000 });
    await checked('docker', ['load', '--input', archive], { timeout: 120_000 });
    const imageId = await checked('docker', ['image', 'inspect', '--format', '{{.Id}}', tag]);
    const buildId = (await readFile(buildIdFile, 'utf8')).trim();
    if (!digestPattern.test(imageId) || !digestPattern.test(buildId)) throw new Error('Invalid Docker/Buildx image ID');
    const receipt = { revision, epoch, imageId, buildId, localTag: tag, platform: 'linux/amd64', payloads };
    await writeFile(path.join(output, 'build.json'), JSON.stringify(receipt, null, 2) + '\n', { flag: 'wx' });
    return receipt;
  });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    if (process.argv.length !== 3) throw new Error('Usage: node examples/runner-reference/build.mjs /absolute/new-output-directory');
    const output = process.argv[2];
    if (!path.isAbsolute(output)) throw new Error('Output directory must be absolute and not already exist');
    console.log(JSON.stringify(await build(output), null, 2));
  } catch (error) {
    console.error(errorText(error));
    process.exitCode = 1;
  }
}
