import { readFileSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

export async function updateCastingPair(castingDir, mutate, readerPath = process.env.SQUAD_SDK_READER) {
  if (!readerPath) throw new Error('Set SQUAD_SDK_READER to the verified Squad 1.0.0 durable-registry.js');
  const reader = path.resolve(readerPath);
  const packageRoot = path.resolve(path.dirname(reader), '../..');
  const pkg = JSON.parse(readFileSync(path.join(packageRoot, 'package.json'), 'utf8'));
  if (pkg.name !== '@bradygaster/squad-sdk' || pkg.version !== '1.0.0') {
    throw new Error('Casting updates require the verified @bradygaster/squad-sdk@1.0.0 package');
  }
  const sdk = await import(pathToFileURL(reader).href);
  const { parseAgentProvenanceRegistry } = await import(
    pathToFileURL(path.join(path.dirname(reader), 'agent-provenance.js')).href);
  const release = sdk.acquireCastingRegistryLock(castingDir, 'waza-coordinator-mutation');
  try {
    const previous = sdk.prepareCastingRegistryPairLocked(castingDir);
    const revision = (previous.registry?.revision ?? 0) + 1;
    const registry = structuredClone(previous.registry ?? {
      schema: 'squad-agent-provenance/v1',
      schema_version: 1,
      agents: {},
    });
    const history = structuredClone(previous.history ?? {
      universe_usage_history: [],
      assignment_cast_snapshots: {},
    });
    registry.revision = revision;
    registry.generated_at = new Date().toISOString();
    history.registry_revision = revision;
    const result = mutate(registry, history);
    if (result?.then) throw new Error('Casting mutation must be synchronous while holding the writer lock');
    const provenance = parseAgentProvenanceRegistry(registry);
    if (provenance.completeness !== 'complete') {
      throw new Error(`Invalid casting provenance: ${JSON.stringify(provenance.diagnostics)}`);
    }
    sdk.commitCastingRegistryPair(castingDir, previous.registryRaw, registry,
      previous.historyRaw, history, revision);
    return sdk.readCastingRegistryPair(castingDir);
  } finally {
    release();
  }
}
