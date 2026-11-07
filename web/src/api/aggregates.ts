import Ajv2020 from "ajv/dist/2020";
import schema from "../../../schemas/manifest-aggregate-1.0.schema.json";
import evidenceSchema from "../../../schemas/execution-evidence-1.0.schema.json";
import attemptSchema from "../../../schemas/execution-attempt-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";
import { sourceContextConsistent, type SourceContext } from "../lib/sourceContext";
import type { EvidenceCategory, EvidenceState } from "./executionEvidence";

export interface EvidenceInput { manifest_sha256: string; evidence_sha256: string }
export interface AdmittedCategory {
  category: EvidenceCategory; state: EvidenceState; declared_state?: EvidenceState;
  verification: string; reason?: string; observed_count?: string;
}
interface RecordedUsage {
  state: EvidenceState;
  declared_state?: EvidenceState;
  verification: string;
  reason?: string;
  recorded?: {
    turns: number; input_tokens: number; output_tokens: number;
    cache_read_tokens: number; cache_write_tokens: number; premium_requests: number;
    ai_credits?: number; model_metrics?: Record<string, { ai_credits?: number }>;
  };
}
export interface EvidenceCandidate {
  evidence_sha256: string; state: string; origin_manifest?: string; source_manifests: string[];
  declared_categories: {
    category: EvidenceCategory; declared_state: EvidenceState; declared_source: string;
    declared_context: string; declared_observed_count?: string; declared_reason?: string;
    declared_limit?: { observed: string; total?: string };
  }[];
  categories: AdmittedCategory[];
  usage?: RecordedUsage;
  source_context?: SourceContext;
  diagnostics: { code: string; message: string }[];
}
export interface LoadedAggregate {
  report: ManifestAggregate;
  rawJSON: string;
  exactUsage: Record<string, Record<string, string>>;
}

export interface ManifestAggregate {
  kind: "waza.manifest-aggregate";
  schema_version: "1.0";
  publication: "derived";
  verification: "content-and-declared-context-only";
  completion: "complete" | "incomplete";
  compatibility: "compatible" | "incompatible" | "unknown";
  members: {
    manifest_sha256: string;
    state: string;
    logical?: { run: string; work: string };
    outcome?: string;
    verdicts?: { identifier: string; passed: boolean }[];
    diagnostics: { code: string; message: string }[];
  }[];
  state_counts: Record<string, number>;
  conflicts: { logical: { run: string; work: string }; manifests: string[] }[];
  diagnostics: { code: string; message: string }[];
  evidence?: {
    completion: "incomplete"; verification: "content-and-declared-context-only";
    qualification: "recorded-category-shape-only"; usage_totals: "withheld";
    members: { manifest_sha256: string; state: string; candidates: EvidenceCandidate[] }[];
    declaration_state_counts: Partial<Record<EvidenceCategory, Partial<Record<EvidenceState, number>>>>;
    category_state_counts: Partial<Record<EvidenceCategory, Partial<Record<EvidenceState, number>>>>;
    diagnostics: { code: string; message: string }[];
  };
}

export class AggregateError extends Error {}

const ajv = new Ajv2020({ strict: false });
for (const resource of [evidenceSchema, attemptSchema, manifestSchema, terminalSchema]) ajv.addSchema(resource);
const validate = ajv.compile<ManifestAggregate>(schema);

function object(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function integer(value: unknown): string {
  if (typeof value !== "string" || !/^(0|[1-9][0-9]*)$/.test(value) || BigInt(value) > 9223372036854775807n) {
    throw new AggregateError("Aggregate API returned invalid integer evidence.");
  }

  return value;
}

function categoryAdmitted(category: AdmittedCategory): boolean {
  if (["source-content-and-recorded-category-shape-only", "source-content-and-recorded-usage-shape-only"].includes(category.verification)) {
    return category.state !== "present" || category.observed_count !== undefined;
  }
  return category.verification === "recorded-degradation-only" && category.state === "incomplete" && category.observed_count === undefined;
}

function exactUsageFields(value: unknown): Record<string, string> {
  if (!object(value)) throw new AggregateError("Aggregate API returned malformed recorded usage.");
  const fields: Record<string, string> = {};
  for (const name of ["turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"]) {
    fields[name] = integer(value[name]);
  }
  for (const name of ["premium_requests", "ai_credits"]) {
    if (value[name] !== undefined) fields[name] = String(value[name]);
  }
  if (value.model_metrics !== undefined) {
    if (!object(value.model_metrics)) throw new AggregateError("Aggregate API returned malformed model usage.");
    const models = Object.entries(value.model_metrics).sort(([a], [b]) => compareUTF8(a, b)).map(([name, model]) => {
      if (!object(model)) throw new AggregateError("Aggregate API returned malformed model usage.");
      const values = Object.entries(model).sort(([a], [b]) => compareUTF8(a, b)).map(([key, entry]) => {
        if (["input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens"].includes(key)) integer(entry);
        return `${JSON.stringify(key)}:${String(entry)}`;
      });
      return `${JSON.stringify(name)}:{${values.join(",")}}`;
    });
    fields.model_metrics_json = `{${models.join(",")}}`;
  }
  return fields;
}

function compareUTF8(left: string, right: string): number {
  const encoder = new TextEncoder();
  const a = encoder.encode(left);
  const b = encoder.encode(right);
  for (let index = 0; index < Math.min(a.length, b.length); index++) {
    const difference = a[index]! - b[index]!;
    if (difference !== 0) return difference;
  }
  return a.length - b.length;
}

export async function aggregateManifests(manifests: string[], signal: AbortSignal, evidence?: EvidenceInput[]): Promise<LoadedAggregate> {
  const expected = [...new Set(manifests)].sort();
  let response: Response;
  let text: string;
  try {
    response = await fetch("/api/v1/aggregates", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ manifests, ...(evidence === undefined ? {} : { evidence }) }),
      signal: AbortSignal.any([signal, AbortSignal.timeout(120_000)]),
    });
    text = await response.text();
  } catch {
    throw new AggregateError("Aggregation could not be loaded. Check the server connection and retry.");
  }
  if (![200, 409, 503].includes(response.status)) {
    throw new AggregateError(`Aggregate API returned HTTP ${response.status}.`);
  }
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new AggregateError("Aggregate API returned malformed JSON.");
  }
  if (!validate(value) || value.members.length !== expected.length ||
      value.members.some((member, index) => member.manifest_sha256 !== expected[index]) ||
      (response.status === 200 && value.completion !== "complete") ||
      (response.status !== 200 && value.completion === "complete")) {
    throw new AggregateError("Aggregate API returned an invalid or inconsistent report.");
  }
  const counts: Record<string, number> = {};
  for (const member of value.members) {
    counts[member.state] = (counts[member.state] ?? 0) + 1;
    if (member.outcome !== undefined && member.state !== "conflicting" && member.state !== member.outcome) {
      throw new AggregateError("Aggregate API returned a contradictory recorded outcome.");
    }
    let previousIdentifier = "";
    for (const verdict of member.verdicts ?? []) {
      if (compareUTF8(verdict.identifier, previousIdentifier) <= 0) {
        throw new AggregateError("Aggregate API returned unsorted or duplicate grader verdicts.");
      }
      previousIdentifier = verdict.identifier;
    }
  }
  if (Object.keys(counts).length !== Object.keys(value.state_counts).length ||
      Object.entries(counts).some(([state, count]) => value.state_counts[state] !== count)) {
    throw new AggregateError("Aggregate API returned inconsistent member counts.");
  }
  let previousConflict = "";
  for (const conflict of value.conflicts) {
    const key = `${conflict.logical.run}\0${conflict.logical.work}`;
    if (key <= previousConflict) {
      throw new AggregateError("Aggregate API returned unsorted or duplicate conflicts.");
    }
    previousConflict = key;
    if (conflict.manifests.some((digest, index) => {
      const member = value.members.find((item) => item.manifest_sha256 === digest);
      return !member || member.state !== "conflicting" ||
        member.logical?.run !== conflict.logical.run || member.logical.work !== conflict.logical.work ||
        (index > 0 && digest <= conflict.manifests[index - 1]!);
    })) {
      throw new AggregateError("Aggregate API returned inconsistent conflict membership.");
    }
  }
  if ((evidence !== undefined) !== (value.evidence !== undefined)) {
    throw new AggregateError("Aggregate API returned inconsistent evidence selection.");
  }
  const exactUsage: LoadedAggregate["exactUsage"] = {};
  if (value.evidence) {
    const preserved: unknown = parseStrictJSON(text, true);
    if (!object(preserved) || !object(preserved.evidence) || !Array.isArray(preserved.evidence.members) ||
        value.evidence.members.length !== expected.length) {
      throw new AggregateError("Aggregate API returned inconsistent evidence membership.");
    }
    const originals = preserved.evidence.members;
    const counts: Record<string, Record<string, number>> = {};
    const admittedCounts: Record<string, Record<string, number>> = {};
    let candidateCount = 0;
    for (const [index, member] of value.evidence.members.entries()) {
      const requested = [...new Set((evidence ?? []).filter((pair) => pair.manifest_sha256 === member.manifest_sha256)
        .map((pair) => pair.evidence_sha256))].sort();
      const state = requested.length === 0 ? "not_selected" : requested.length > 1 ? "conflicting" : member.candidates[0]?.state;
      const original = originals[index];
      if (member.manifest_sha256 !== expected[index] || member.state !== state ||
          member.candidates.length !== requested.length || !object(original) || !Array.isArray(original.candidates)) {
        throw new AggregateError("Aggregate API returned inconsistent evidence selection.");
      }
      const originalCandidates = original.candidates;
      for (const [candidateIndex, candidate] of member.candidates.entries()) {
        candidateCount++;
        if (candidate.evidence_sha256 !== requested[candidateIndex]) {
          throw new AggregateError("Aggregate API returned unsorted or unrequested sidecars.");
        }
        if ((candidate.source_context !== undefined && candidate.state !== "verified") ||
            !sourceContextConsistent(candidate.source_context, candidate.origin_manifest, value.completion === "incomplete")) {
          throw new AggregateError("Aggregate API returned contradictory original source context.");
        }
        if (candidate.state !== "verified") continue;
        if (candidate.origin_manifest !== member.manifest_sha256 ||
            !candidate.source_manifests.includes(member.manifest_sha256) ||
            candidate.source_manifests.some((digest, i) => i > 0 && digest <= candidate.source_manifests[i - 1]!)) {
          throw new AggregateError("Aggregate API returned inconsistent evidence origins.");
        }
        let previous = "";
        for (const declaration of candidate.declared_categories) {
          if (declaration.category <= previous) throw new AggregateError("Aggregate API returned duplicate or unsorted categories.");
          previous = declaration.category;
          if (declaration.declared_observed_count !== undefined) integer(declaration.declared_observed_count);
          if (declaration.declared_limit) {
            integer(declaration.declared_limit.observed);
            if (declaration.declared_limit.total !== undefined) integer(declaration.declared_limit.total);
          }
          if (member.state === "verified") {
            const states = counts[declaration.category] ??= {};
            states[declaration.declared_state] = (states[declaration.declared_state] ?? 0) + 1;
          }
        }
        const usage = candidate.usage;
        if (!usage) throw new AggregateError("Aggregate API omitted recorded usage availability.");
        candidate.categories.forEach((category, categoryIndex) => {
          const declared = candidate.declared_categories[categoryIndex];
          if (!declared || category.category !== declared.category || category.declared_state !== declared.declared_state) {
            throw new AggregateError("Aggregate API returned contradictory category declarations.");
          }
          if (category.state === "present" && category.observed_count === undefined &&
              ["source-content-and-recorded-category-shape-only", "source-content-and-recorded-usage-shape-only"].includes(category.verification)) {
            throw new AggregateError("Aggregate API omitted a native present category count.");
          }
          if (category.verification === "recorded-degradation-only" &&
              (category.state !== "incomplete" || category.observed_count !== undefined)) {
            throw new AggregateError("Aggregate API returned contradictory recorded degradation.");
          }
          if (category.state === "present" &&
              !["source-content-and-recorded-category-shape-only", "source-content-and-recorded-usage-shape-only"].includes(category.verification) &&
              (category.category !== "usage" || category.verification !== "declared-state-only" ||
                category.reason !== "declared_present_empty" || category.observed_count !== undefined)) {
            throw new AggregateError("Aggregate API returned unqualified present categories.");
          }
          if (category.observed_count !== undefined) {
            integer(category.observed_count);
            if (!categoryAdmitted(category)) throw new AggregateError("Aggregate API fabricated category counts.");
          }
          if (category.category === "usage" &&
              (category.state !== usage.state || category.declared_state !== usage.declared_state ||
                category.verification !== usage.verification || category.reason !== usage.reason)) {
            throw new AggregateError("Aggregate API returned contradictory usage admission.");
          }
          if (member.state === "verified" && categoryAdmitted(category)) {
            const states = admittedCounts[category.category] ??= {};
            states[category.state] = (states[category.state] ?? 0) + 1;
          }
        });
        if (usage.recorded) {
          if (usage.verification !== "source-content-and-recorded-usage-shape-only" || usage.declared_state !== "present" ||
              !["present", "incomplete"].includes(usage.state) ||
              (usage.state === "present" && (usage.recorded.ai_credits === undefined ||
                !usage.recorded.model_metrics || Object.keys(usage.recorded.model_metrics).length === 0 ||
                Object.values(usage.recorded.model_metrics).some((model) => model.ai_credits === undefined)))) {
            throw new AggregateError("Aggregate API returned unqualified recorded usage.");
          }
          const rawCandidate = originalCandidates[candidateIndex];
          if (!object(rawCandidate) || !object(rawCandidate.usage)) throw new AggregateError("Aggregate API returned malformed usage.");
          exactUsage[candidate.evidence_sha256] = exactUsageFields(rawCandidate.usage.recorded);
        } else if (usage.state === "present" &&
            (usage.reason !== "declared_present_empty" || usage.verification !== "declared-state-only")) {
          throw new AggregateError("Aggregate API fabricated present usage.");
        }
      }
    }
    const reportedCounts = value.evidence.declaration_state_counts;
    if (candidateCount > 128 || Object.keys(counts).length !== Object.keys(reportedCounts).length ||
        Object.entries(counts).some(([category, states]) => {
          const reported = reportedCounts[category as EvidenceCategory];
          return !reported || Object.keys(states).length !== Object.keys(reported).length ||
            Object.entries(states).some(([state, count]) => reported[state as EvidenceState] !== count);
        })) throw new AggregateError("Aggregate API returned inconsistent declaration counts.");
    const nativeCounts = value.evidence.category_state_counts;
    if (Object.keys(admittedCounts).length !== Object.keys(nativeCounts).length ||
        Object.entries(admittedCounts).some(([category, states]) => {
          const reported = nativeCounts[category as EvidenceCategory];
          return !reported || Object.keys(states).length !== Object.keys(reported).length ||
            Object.entries(states).some(([state, count]) => reported[state as EvidenceState] !== count);
        })) throw new AggregateError("Aggregate API returned inconsistent native category counts.");
  }
  return { report: value, rawJSON: text, exactUsage };
}

export function aggregateCSV(loaded: LoadedAggregate): string {
  const rows = [["manifest_sha256", "input_state", "recorded_outcome", "evidence_sha256", "evidence_state",
    "usage_state", "usage_verification", "declared_usage_source", "turns", "input_tokens", "output_tokens",
    "cache_read_tokens", "cache_write_tokens", "premium_requests", "ai_credits", "model_metrics_json",
    "usage_totals", "new_operation_billing", "verified_source_manifests_json"]];
  if (loaded.report.evidence) rows[0]!.push("category_evidence_json", "declared_categories_json", "source_context_json");
  loaded.report.members.forEach((member, index) => {
    const candidates = loaded.report.evidence?.members[index]?.candidates;
    for (const candidate of candidates?.length ? candidates : [undefined]) {
      const exact = candidate ? loaded.exactUsage[candidate.evidence_sha256] : undefined;
      rows.push([member.manifest_sha256, member.state, member.outcome ?? "", candidate?.evidence_sha256 ?? "",
        candidate?.state ?? "not_selected", candidate?.usage?.state ?? "unavailable",
        candidate?.usage?.verification ?? "unverified",
        candidate?.declared_categories.find((declaration) => declaration.category === "usage")?.declared_source ?? "",
        ...["turns", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "premium_requests",
          "ai_credits", "model_metrics_json"].map((name) => exact?.[name] ?? ""), "withheld", "unavailable",
        candidate?.source_manifests.length ? JSON.stringify(candidate.source_manifests) : "",
        ...(loaded.report.evidence ? [
          candidate ? JSON.stringify(candidate.categories) : "",
          candidate ? JSON.stringify(candidate.declared_categories) : "",
          candidate?.source_context ? JSON.stringify(candidate.source_context) : "",
        ] : [])]);
    }
  });
  return rows.map((row) => row.map((cell) => /[",\r\n]/.test(cell) ? `"${cell.replace(/"/g, '""')}"` : cell).join(",")).join("\n") + "\n";
}
