import Ajv2020 from "ajv/dist/2020";
import evidenceSchema from "../../../schemas/execution-evidence-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export type EvidenceCategory = "conversation" | "tool" | "grader" | "reliability" | "usage" | "provenance";
export type EvidenceState = "present" | "unavailable" | "incomplete" | "redacted" | "conflicting";

interface WireRecord {
  category: EvidenceCategory;
  state: EvidenceState;
  source: "authored" | "mock" | "cached" | "live" | "unknown";
  context: "declared" | "observed" | "unknown";
  artifacts: string[];
  observed_count?: number;
  reason?: string;
  limit?: { observed: number; total?: number };
}

export interface EvidenceRecord extends Omit<WireRecord, "observed_count" | "limit"> {
  observed_count?: string;
  limit?: { observed: string; total?: string };
}

interface WireDocument {
  kind: "waza.execution-evidence";
  schema_version: "1.0";
  publication: "derived";
  origin_manifest: string;
  execution_source?: { execution_id: string; manifest_digest: string };
  attempt_artifact?: string;
  categories: WireRecord[];
  lineage: { manifest: string; relationship: "retry" | "resume" | "cache" | "aggregation" }[];
}

interface SourceReceipt {
  manifest_sha256: string;
  attempt?: string;
  publication: "finalized" | "unfinalized" | "conflict";
  integrity: "verified" | "unverified" | "failed";
  verification: "content-and-declared-context-only";
  artifacts: never[];
  diagnostics: never[];
}

interface WireReceipt {
  evidence_sha256: string;
  publication: "derived";
  integrity: "verified" | "unverified" | "failed";
  verification: "content-and-declared-context-only";
  document?: WireDocument;
  sources: SourceReceipt[];
  diagnostics: { code: string; message: string }[];
}

export interface EvidenceReceipt extends Omit<WireReceipt, "document"> {
  document?: Omit<WireDocument, "categories"> & { categories: EvidenceRecord[] };
}

export class ExecutionEvidenceError extends Error {}

const digest = { type: "string", pattern: "^[0-9a-f]{64}$" };
const diagnostics = { type: "array", items: {
  type: "object", additionalProperties: false, required: ["code", "message"],
  properties: { code: { type: "string" }, message: { type: "string" } },
} };
const ajv = new Ajv2020({ strict: false });
for (const schema of [evidenceSchema, attemptSchema, manifestSchema, terminalSchema]) ajv.addSchema(schema);
const validate = ajv.compile<WireReceipt>({
  type: "object", additionalProperties: false,
  required: ["evidence_sha256", "publication", "integrity", "verification", "sources", "diagnostics"],
  properties: {
    evidence_sha256: digest, publication: { const: "derived" },
    integrity: { enum: ["verified", "unverified", "failed"] },
    verification: { const: "content-and-declared-context-only" },
    document: { $ref: evidenceSchema.$id },
    diagnostics,
    sources: { type: "array", maxItems: 129, items: {
      type: "object", additionalProperties: false,
      required: ["manifest_sha256", "publication", "integrity", "verification", "artifacts", "diagnostics"],
      properties: {
        manifest_sha256: digest, attempt: { type: "string", pattern: "^attempt-[0-9a-f]{64}$" },
        publication: { enum: ["finalized", "unfinalized", "conflict"] },
        integrity: { enum: ["verified", "unverified", "failed"] },
        verification: { const: "content-and-declared-context-only" },
        artifacts: { type: "array", maxItems: 0 }, diagnostics: { type: "array", maxItems: 0 },
      },
    } },
  },
  allOf: [{
    if: { properties: { integrity: { const: "verified" } } },
    then: { required: ["document"], properties: {
      diagnostics: { maxItems: 0 },
      sources: { minItems: 1, items: { properties: { publication: { const: "finalized" }, integrity: { const: "verified" } } } },
    } },
    else: { not: { required: ["document"] }, properties: { diagnostics: { minItems: 1 } } },
  }],
});

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function integerText(value: unknown): string {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]*)$/.test(value) || BigInt(value) > 9223372036854775807n) {
    throw new ExecutionEvidenceError("Evidence API returned invalid integer evidence.");
  }
  return value;
}

// Validate the original wire schema, then recover original integer tokens for
// display. Rounded JavaScript numbers are never used as recorded counts.
export function parseExecutionEvidence(text: string, expectedDigest: string, status: number): EvidenceReceipt {
  if (text.length > 2 * 1024 * 1024) {
    throw new ExecutionEvidenceError("Evidence API returned an oversized receipt.");
  }
  const wire: unknown = parseStrictJSON(text);
  if (!validate(wire) || wire.evidence_sha256 !== expectedDigest ||
      (status === 200) !== (wire.integrity === "verified")) {
    throw new ExecutionEvidenceError("Evidence API returned an invalid or inconsistent receipt.");
  }
  if (!wire.document) return { ...wire, document: undefined };
  const preserved = parseStrictJSON(text, true);
  if (!object(preserved) || !object(preserved.document) || !Array.isArray(preserved.document.categories)) {
    throw new ExecutionEvidenceError("Evidence API returned malformed count data.");
  }
  const originals = preserved.document.categories;
  const categories = wire.document.categories.map((record, index): EvidenceRecord => {
    const original: unknown = originals[index];
    if (!object(original)) throw new ExecutionEvidenceError("Evidence API returned malformed category data.");
    const { observed_count, limit, ...rest } = record;
    const count = observed_count === undefined ? undefined : integerText(original.observed_count);
    if (record.state === "present" && ((count === "0") !== (record.artifacts.length === 0))) {
      throw new ExecutionEvidenceError("Evidence API returned inconsistent present-empty evidence.");
    }
    let limits: EvidenceRecord["limit"];
    if (limit) {
      if (!object(original.limit)) throw new ExecutionEvidenceError("Evidence API returned malformed limit data.");
      const observed = integerText(original.limit.observed);
      const total = limit.total === undefined ? undefined : integerText(original.limit.total);
      const bound = record.reason === "artifact_count_limit" ? 128n :
        record.reason === "object_size_limit" ? 67108864n : 268435456n;
      if (BigInt(observed) <= bound || (total !== undefined && BigInt(total) < BigInt(observed))) {
        throw new ExecutionEvidenceError("Evidence API returned inconsistent capture bounds.");
      }
      limits = { observed, ...(total === undefined ? {} : { total }) };
    }
    return { ...rest, ...(count === undefined ? {} : { observed_count: count }), ...(limits ? { limit: limits } : {}) };
  });
  const sourceDigests = new Set(wire.sources.map((source) => source.manifest_sha256));
  if (sourceDigests.size !== wire.sources.length || !sourceDigests.has(wire.document.origin_manifest) ||
      wire.document.lineage.some((link) => link.manifest === wire.document?.origin_manifest || !sourceDigests.has(link.manifest)) ||
      (wire.document.execution_source && wire.document.execution_source.manifest_digest !== wire.document.origin_manifest)) {
    throw new ExecutionEvidenceError("Evidence API returned inconsistent source lineage.");
  }
  return { ...wire, document: { ...wire.document, categories } };
}

export async function inspectExecutionEvidence(digest: string, signal: AbortSignal): Promise<EvidenceReceipt> {
  let response: Response;
  let text: string;
  try {
    response = await fetch(`/api/v1/evidence/${encodeURIComponent(digest)}`, {
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    });
    text = await response.text();
  } catch {
    throw new ExecutionEvidenceError("Evidence inspection could not be loaded. Check the server connection and retry.");
  }
  if (![200, 400, 404, 409, 503].includes(response.status)) {
    throw new ExecutionEvidenceError(`Evidence API returned HTTP ${response.status}.`);
  }
  try {
    return parseExecutionEvidence(text, digest, response.status);
  } catch {
    throw new ExecutionEvidenceError("Evidence API returned malformed, inconsistent, or unverified data.");
  }
}
