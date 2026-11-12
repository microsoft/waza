import Ajv2020 from "ajv/dist/2020";
import aggregateSchema from "../../../schemas/manifest-aggregate-1.0.schema.json";
import manifestSchema from "../../../schemas/artifact-manifest-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface AggregateInput {
  manifest_sha256: string;
  evidence_sha256?: string;
}

export interface AggregateReport {
  kind: "waza.manifest-aggregate";
  schema_version: "1.0";
  state: "complete" | "incomplete" | "conflicting";
  verification: "current-content-and-original-record-context-only";
  members: (AggregateInput & {
    state: "admitted" | "omitted" | "conflicting";
    outcome?: string;
    execution_id?: string;
    parent?: { execution_id: string; relationship: "retry" | "branch" };
    lineage?: { manifest: string; relationship: "retry" | "resume" | "cache" | "aggregation" }[];
    binding?: {
      identity: {
        version: string; canonical_digest: string; run: string; shard: string;
        task: string; trial: string; attempt: string;
      };
      selection: { shard: string; task: string; trial: number; attempt: number };
      execution_mode: string;
      grader_ids: string[];
    };
    categories: {
      category: string;
      state: string;
      declared_state?: string;
      source: string;
      context: string;
      verification: string;
      reason?: string;
      observed_count?: number;
    }[];
    diagnostics: string[];
  })[];
  groups: { profile_sha256: string; members: AggregateInput[] }[];
  counts: {
    input_members: number;
    unique_verified_manifests: number;
    unique_verified_execution_records: number;
    member_states: Record<string, number>;
    member_outcomes: Record<string, number>;
  };
  usage: {
    state: "complete" | "unavailable" | "conflicting";
    population: "unique-verified-execution-records";
    executions: number;
    recorded_historical_usage: {
      turns: number;
      input_tokens: number;
      output_tokens: number;
      cache_read_tokens: number;
      cache_write_tokens: number;
      premium_requests: number;
      ai_credits?: number;
    } | null;
    diagnostics: string[];
  };
}

export interface AggregateResult {
  report: AggregateReport;
  canonical: string;
  status: number;
}

export class AggregateError extends Error {}

const validateReport = new Ajv2020({ strict: false })
  .addSchema(terminalSchema)
  .addSchema(manifestSchema)
  .compile<AggregateReport>(aggregateSchema);

function referenceKey(input: AggregateInput): string {
  return `${input.manifest_sha256}:${input.evidence_sha256 ?? ""}`;
}

function memberUsageEligible(member: AggregateReport["members"][number]): boolean {
  return !!member.execution_id && !member.diagnostics.includes("cached_historical_usage_excluded") &&
    !member.categories.some((category) => category.source === "cached") &&
    member.categories.some((category) =>
      category.category === "usage" && category.state === "present" && category.declared_state === "present" &&
      category.verification === "source-content-and-recorded-usage-shape-only" && category.observed_count === 1);
}

function sorted(values: string[], compare: (a: string, b: string) => number = (a, b) => a < b ? -1 : a > b ? 1 : 0): boolean {
  return values.every((value, index) => index === 0 || compare(values[index - 1]!, value) <= 0);
}

function compareUTF8(a: string, b: string): number {
  const left = new TextEncoder().encode(a);
  const right = new TextEncoder().encode(b);
  for (let index = 0; index < Math.min(left.length, right.length); index++) {
    if (left[index] !== right[index]) return left[index]! - right[index]!;
  }
  return left.length - right.length;
}

async function bindingProfile(binding: NonNullable<AggregateReport["members"][number]["binding"]>): Promise<string> {
  const { version, canonical_digest, run, shard, task, trial, attempt } = binding.identity;
  const selection = binding.selection;
  // Match the Go typed binding order and encoding/json string escaping.
  const wire = JSON.stringify({
    identity: { version, canonical_digest, run, shard, task, trial, attempt },
    selection: { shard: selection.shard, task: selection.task, trial: selection.trial, attempt: selection.attempt },
    execution_mode: binding.execution_mode, grader_ids: binding.grader_ids,
  }).replace(/[<>&\u2028\u2029]/g, (character) => `\\u${character.charCodeAt(0).toString(16).padStart(4, "0")}`);
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(wire));
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function parseAggregateReferences(text: string): AggregateInput[] {
  const lines = text.trim().split(/\r?\n/);
  if (!text.trim() || lines.length > 128) {
    throw new AggregateError("Enter 1 to 128 explicit manifest references.");
  }
  const unique = new Map<string, AggregateInput>();
  for (const line of lines) {
    const fields = line.trim().split(/\s+/);
    if (fields.length < 1 || fields.length > 2 || fields.some((field) => !/^[0-9a-f]{64}$/.test(field))) {
      throw new AggregateError("Each line must contain a full lowercase manifest digest and optionally an evidence digest.");
    }
    const manifest = fields[0];
    if (!manifest) throw new AggregateError("A manifest digest is required.");
    const input: AggregateInput = { manifest_sha256: manifest };
    if (fields[1]) input.evidence_sha256 = fields[1];
    unique.set(referenceKey(input), input);
  }
  return [...unique.entries()].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([, input]) => input);
}

export async function buildAggregate(inputs: AggregateInput[], signal: AbortSignal): Promise<AggregateResult> {
  let response: Response;
  let text: string;
  try {
    response = await fetch("/api/v1/aggregates", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(inputs),
      // 5s admission + 30s evaluation + 5s delivery + 10s client headroom.
      signal: AbortSignal.any([signal, AbortSignal.timeout(50_000)]),
    });
    text = await response.text();
  } catch {
    throw new AggregateError("Aggregate could not be loaded. Check the server connection and retry.");
  }
  if (response.status !== 200 && response.status !== 409) {
    throw new AggregateError(`Aggregate API returned HTTP ${response.status}; no evaluated report is available.`);
  }
  let value: unknown;
  try {
    const maximumBytes = 32 * 1024 * 1024;
    if (text.length > maximumBytes || new TextEncoder().encode(text).byteLength > maximumBytes) {
      throw new AggregateError("Aggregate report exceeds its byte limit.");
    }
    value = parseStrictJSON(text, maximumBytes);
  } catch {
    throw new AggregateError("Aggregate API returned malformed JSON.");
  }
  if (!validateReport(value)) throw new AggregateError("Aggregate API returned an invalid report.");
  const expected = [...new Set(inputs.map(referenceKey))].sort();
  const received = value.members.map(referenceKey);
  const memberCounts: Record<string, number> = {};
  const outcomes: Record<string, number> = {};
  const profiles = new Map<string, string[]>();
  const executionManifests = new Map<string, string>();
  const manifestOutcomes = new Map<string, string>();
  const manifestProfiles = new Map<string, string>();
  let state = "complete";
  let ordered = sorted(value.usage.diagnostics);
  for (const member of value.members) {
    if (member.outcome) {
      const previousOutcome = manifestOutcomes.get(member.manifest_sha256);
      if (previousOutcome !== undefined && previousOutcome !== member.outcome) ordered = false;
      else manifestOutcomes.set(member.manifest_sha256, member.outcome);
    }
    if (member.execution_id) {
      const previousManifest = executionManifests.get(member.execution_id);
      if (previousManifest !== undefined && previousManifest !== member.manifest_sha256) ordered = false;
      else executionManifests.set(member.execution_id, member.manifest_sha256);
    }
    memberCounts[member.state] = (memberCounts[member.state] ?? 0) + 1;
    if (member.outcome) outcomes[member.outcome] = (outcomes[member.outcome] ?? 0) + 1;
    if (member.state === "conflicting") state = "conflicting";
    else if (member.state !== "admitted" && state !== "conflicting") state = "incomplete";
    ordered &&= sorted(member.diagnostics) &&
      member.categories.every((category, index) => index === 0 || member.categories[index - 1]!.category < category.category);
    if (member.binding) {
      ordered &&= sorted(member.binding.grader_ids, compareUTF8);
      const profile = await bindingProfile(member.binding);
      const previousProfile = manifestProfiles.get(member.manifest_sha256);
      if (previousProfile !== undefined && previousProfile !== profile) ordered = false;
      else manifestProfiles.set(member.manifest_sha256, profile);
      const members = profiles.get(profile) ?? [];
      members.push(referenceKey(member));
      profiles.set(profile, members);
    }
  }
  const expectedGroups = [...profiles.entries()].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0);
  const actualGroups = value.groups.map((group) => [group.profile_sha256, group.members.map(referenceKey)]);
  const executions = new Set(value.members.flatMap((member) => member.execution_id ? [member.execution_id] : [])).size;
  const equalCounts = (a: Record<string, number>, b: Record<string, number>) =>
    JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort());
  if (!ordered || value.state !== state ||
      JSON.stringify(expectedGroups) !== JSON.stringify(actualGroups) ||
      JSON.stringify(expected) !== JSON.stringify(received) ||
      value.counts.input_members !== received.length ||
      !equalCounts(memberCounts, value.counts.member_states) ||
      !equalCounts(outcomes, value.counts.member_outcomes) ||
      value.counts.unique_verified_execution_records !== executions ||
      value.usage.executions !== executions ||
      (value.usage.state === "complete" && (executions === 0 || state !== "complete" ||
        !value.members.every(memberUsageEligible))) ||
      value.counts.unique_verified_manifests !== new Set(value.members.filter((m) => m.binding).map((m) => m.manifest_sha256)).size ||
      (response.status === 200) !== (value.state === "complete")) {
    throw new AggregateError("Aggregate API returned inconsistent membership or population counts.");
  }
  return { report: value, canonical: text, status: response.status };
}
