/**
 * Shared YAML parse boundary for AgentV configs.
 *
 * Why this exists:
 * - We use the `yaml` package (eemeli/yaml). In its YAML 1.2 default mode it
 *   leaves the `<<` merge key as a literal sibling key instead of merging the
 *   referenced map into the parent. That leaks `<<` into downstream consumers
 *   (e.g. JSONL `metadata.governance` artifacts). The YAML 1.1 merge-key
 *   behavior must be opted into via `{ merge: true }`.
 * - Every `*.eval.yaml`, `agentv.config.yaml`, workspace YAML, etc. is parsed
 *   here so behavior is uniform across loaders. Do NOT call `parse` from the
 *   `yaml` package directly elsewhere — funnel through these helpers.
 *
 * To extend:
 *   - For new YAML inputs that should support anchors + merge keys, import
 *     `parseYaml` (object form) or `parseYamlValue` (any-shape form) from here.
 *   - Do not duplicate the `{ merge: true }` option at call sites.
 */
import { parse } from 'yaml';

/** Options forwarded to the `yaml` package. `merge: true` is always set. */
const PARSE_OPTIONS = { merge: true } as const;

/**
 * Parse a YAML document and return its top-level value as `unknown`.
 *
 * Use this when the document may be any shape (string, array, object, etc.).
 * Anchor merges (`<<: *anchor`) are unwrapped into sibling keys.
 */
export function parseYamlValue(content: string): unknown {
  return parse(content, PARSE_OPTIONS) as unknown;
}
