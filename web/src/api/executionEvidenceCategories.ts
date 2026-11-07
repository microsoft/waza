import { parseStrictJSON } from "../lib/strictJson.ts";
import { sourceContextConsistent, type SourceContext } from "../lib/sourceContext.ts";
import type { EvidenceCategory, EvidenceState } from "./executionEvidence";

export interface RecordedCategory {
  category: EvidenceCategory;
  state: EvidenceState;
  declared_state?: EvidenceState;
  verification: string;
  reason?: string;
  observed_count?: string;
  usage?: { input_tokens: string; output_tokens: string; ai_credits?: string };
}

export interface RecordedCategories {
  categories: RecordedCategory[];
  sourceVerified: boolean;
  admissionComplete: boolean;
  sourceContext?: SourceContext;
}

const names = ["conversation", "tool", "grader", "reliability", "usage", "provenance"] as const;
const states = ["present", "unavailable", "incomplete", "redacted", "conflicting"] as const;
const qualifiers = [
  "unverified", "declared-state-only", "recorded-degradation-only",
  "source-content-and-recorded-category-shape-only", "source-content-and-recorded-usage-shape-only",
];
const countFields = ["turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"];
const modelCounts = countFields.filter((field) => field !== "turns");

export class RecordedCategoryError extends Error {}
function invalid(): never { throw new RecordedCategoryError("Recorded category API returned malformed or inconsistent evidence."); }
function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function keys(value: Record<string, unknown>, allowed: string[]) {
  if (Object.keys(value).some((key) => !allowed.includes(key))) invalid();
}
function state(value: unknown): EvidenceState {
  if (typeof value !== "string" || !states.some((item) => item === value)) return invalid();
  return value as EvidenceState;
}
function integer(value: unknown): string {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]*)$/.test(value) || BigInt(value) > 9223372036854775807n) return invalid();
  return value;
}
function number(value: unknown): string {
  if (typeof value !== "string" || !/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/.test(value) ||
      !Number.isFinite(Number(value)) || Number(value) < 0) return invalid();
  return value;
}
function numericType(value: unknown) {
  if (typeof value !== "number" || !Number.isFinite(value)) invalid();
}
function usage(value: unknown, original: unknown, categoryState: EvidenceState): RecordedCategory["usage"] {
  if (!object(value) || !object(original)) return invalid();
  keys(value, [...countFields, "premium_requests", "ai_credits", "provider", "provider_host", "model_metrics"]);
  for (const field of countFields) { numericType(original[field]); integer(value[field]); }
  numericType(original.premium_requests);
  number(value.premium_requests);
  for (const field of ["provider", "provider_host"]) {
    if (field in value && typeof value[field] !== "string") invalid();
  }
  const credits = value.ai_credits === undefined ? undefined : number(value.ai_credits);
  if (credits !== undefined) numericType(original.ai_credits);
  let complete = credits !== undefined;
  if (value.model_metrics === undefined) complete = false;
  else {
    if (!object(value.model_metrics) || !object(original.model_metrics)) return invalid();
    if (Object.keys(value.model_metrics).length === 0) complete = false;
    for (const [model, metrics] of Object.entries(value.model_metrics)) {
      const originalMetrics = original.model_metrics[model];
      if (!model.trim() || !object(metrics) || !object(originalMetrics)) return invalid();
      keys(metrics, [...modelCounts, "request_count", "request_cost", "ai_credits"]);
      for (const field of modelCounts) { numericType(originalMetrics[field]); integer(metrics[field]); }
      numericType(originalMetrics.request_count);
      numericType(originalMetrics.request_cost);
      number(metrics.request_count);
      number(metrics.request_cost);
      if (metrics.ai_credits === undefined) complete = false;
      else { numericType(originalMetrics.ai_credits); number(metrics.ai_credits); }
    }
  }
  if ((categoryState === "present") !== complete || !["present", "incomplete"].includes(categoryState)) invalid();
  return {
    input_tokens: integer(value.input_tokens), output_tokens: integer(value.output_tokens),
    ...(credits === undefined ? {} : { ai_credits: credits }),
  };
}

// Only qualified metadata is exposed. Original numeric tokens are retained;
// declarations and partial observations never become an authoritative total.
export function parseRecordedCategories(text: string, expectedDigest: string, status: number): RecordedCategories {
  if (text.length > 2 * 1024 * 1024 || ![200, 400, 404, 409, 503].includes(status)) return invalid();
  const wire = parseStrictJSON(text, "all");
  const original = parseStrictJSON(text);
  if (!object(wire) || wire.evidence_sha256 !== expectedDigest || wire.publication !== "derived" ||
      wire.verification !== "content-and-declared-context-only" ||
      !["verified", "unverified", "failed"].includes(String(wire.integrity))) return invalid();
  keys(wire, ["evidence_sha256", "publication", "integrity", "verification", "document", "sources", "diagnostics", "categories", "source_context"]);
  const sourceVerified = wire.integrity === "verified";
  if (!Array.isArray(wire.sources) || !Array.isArray(wire.diagnostics) || !Array.isArray(wire.categories) ||
      wire.categories.length !== names.length || (status === 200 && !sourceVerified) ||
      wire.sources.length > 129 || (status === 200 && wire.diagnostics.length !== 0) ||
      (status !== 200 && wire.diagnostics.length === 0) ||
      (!sourceVerified && ("document" in wire || wire.diagnostics.length === 0))) return invalid();
  if (!object(original) || !Array.isArray(original.categories)) return invalid();
  const originals = original.categories;
  const declared = new Map<EvidenceCategory, EvidenceState>();
  if (sourceVerified) {
    if (!object(wire.document) || !Array.isArray(wire.document.categories) || wire.document.categories.length !== names.length ||
        wire.document.kind !== "waza.execution-evidence" || wire.document.schema_version !== "1.0" ||
        wire.document.publication !== "derived" || typeof wire.document.origin_manifest !== "string" ||
        !/^[0-9a-f]{64}$/.test(wire.document.origin_manifest)) return invalid();
    const sources = new Set<string>();
    for (const source of wire.sources) {
      if (!object(source) || source.integrity !== "verified" || source.publication !== "finalized" ||
          source.verification !== "content-and-declared-context-only" ||
          !Array.isArray(source.artifacts) || source.artifacts.length !== 0 ||
          !Array.isArray(source.diagnostics) || source.diagnostics.length !== 0 ||
          typeof source.manifest_sha256 !== "string" || !/^[0-9a-f]{64}$/.test(source.manifest_sha256) ||
          sources.has(source.manifest_sha256)) return invalid();
      sources.add(source.manifest_sha256);
    }
    if (!sources.has(wire.document.origin_manifest)) return invalid();
    for (const record of wire.document.categories) {
      if (!object(record) || !names.some((name) => name === record.category)) return invalid();
      const category = record.category as EvidenceCategory;
      if (declared.has(category)) return invalid();
      declared.set(category, state(record.state));
    }
  }
  const origin = object(wire.document) && typeof wire.document.origin_manifest === "string" ? wire.document.origin_manifest : undefined;
  if (!sourceContextConsistent(wire.source_context, origin, status !== 200) ||
      (!sourceVerified && wire.source_context !== undefined)) return invalid();
  const context = wire.source_context;
  if (context !== undefined) {
    if (!object(wire.document) || !object(wire.document.execution_source) ||
        (context.source && context.source.execution_id !== wire.document.execution_source.execution_id) ||
        (status === 200 && context.state !== "verified")) return invalid();
  }
  const seen = new Set<EvidenceCategory>();
  const categories = wire.categories.map((record, index): RecordedCategory => {
    if (!object(record) || !names.some((name) => name === record.category)) return invalid();
    const originalRecord: unknown = originals[index];
    if (!object(originalRecord)) return invalid();
    keys(record, ["category", "state", "declared_state", "verification", "reason", "observed_count", "usage"]);
    const category = record.category as EvidenceCategory;
    if (seen.has(category) || !qualifiers.includes(String(record.verification))) return invalid();
    seen.add(category);
    const categoryState = state(record.state);
    const declaredState = record.declared_state === undefined ? undefined : state(record.declared_state);
    if (sourceVerified ? declaredState !== declared.get(category) : declaredState !== undefined) return invalid();
    if (record.reason !== undefined && (typeof record.reason !== "string" || !/^[a-z_]{1,128}$/.test(record.reason))) return invalid();
    const count = record.observed_count === undefined ? undefined : integer(record.observed_count);
    if (count !== undefined) numericType(originalRecord.observed_count);
    const unqualified = record.verification === "unverified" || record.verification === "declared-state-only";
    if ((!sourceVerified || unqualified || ["unavailable", "redacted"].includes(categoryState)) &&
        (count !== undefined || record.usage !== undefined)) return invalid();
    if (!sourceVerified && (categoryState !== "unavailable" || !unqualified)) return invalid();
    if (unqualified && categoryState !== "unavailable" && categoryState !== declaredState) return invalid();
    if (categoryState === "present" && !unqualified && count === undefined) return invalid();
    if (record.usage !== undefined && (category !== "usage" ||
        record.verification !== "source-content-and-recorded-usage-shape-only" || count !== "1")) return invalid();
    return {
      category, state: categoryState, verification: String(record.verification),
      ...(declaredState === undefined ? {} : { declared_state: declaredState }),
      ...(record.reason === undefined ? {} : { reason: String(record.reason) }),
      ...(count === undefined ? {} : { observed_count: count }),
      ...(record.usage === undefined ? {} : { usage: usage(record.usage, originalRecord.usage, categoryState) }),
    };
  });
  return { categories, sourceVerified, admissionComplete: status === 200,
    ...(context === undefined ? {} : { sourceContext: context }) };
}

export async function inspectRecordedCategories(digest: string, signal: AbortSignal): Promise<RecordedCategories> {
  let response: Response;
  try {
    response = await fetch(`/api/v1/evidence/${encodeURIComponent(digest)}/categories`, {
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    });
    return parseRecordedCategories(await response.text(), digest, response.status);
  } catch (error) {
    if (error instanceof RecordedCategoryError) throw error;
    throw new RecordedCategoryError("Recorded category inspection could not be loaded or verified. Check the server and retry.");
  }
}
