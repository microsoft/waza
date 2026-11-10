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
    binding?: {
      identity: { run: string; task: string; trial: string; attempt: string };
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
      signal: AbortSignal.any([signal, AbortSignal.timeout(35_000)]),
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
    value = parseStrictJSON(text);
  } catch {
    throw new AggregateError("Aggregate API returned malformed JSON.");
  }
  if (!validateReport(value)) throw new AggregateError("Aggregate API returned an invalid report.");
  const expected = [...new Set(inputs.map(referenceKey))].sort();
  const received = value.members.map(referenceKey);
  const memberCounts: Record<string, number> = {};
  const outcomes: Record<string, number> = {};
  for (const member of value.members) {
    memberCounts[member.state] = (memberCounts[member.state] ?? 0) + 1;
    if (member.outcome) outcomes[member.outcome] = (outcomes[member.outcome] ?? 0) + 1;
  }
  const equalCounts = (a: Record<string, number>, b: Record<string, number>) =>
    JSON.stringify(Object.entries(a).sort()) === JSON.stringify(Object.entries(b).sort());
  if (JSON.stringify(expected) !== JSON.stringify(received) ||
      value.counts.input_members !== received.length ||
      !equalCounts(memberCounts, value.counts.member_states) ||
      !equalCounts(outcomes, value.counts.member_outcomes) ||
      value.counts.unique_verified_execution_records !== new Set(value.members.flatMap((m) => m.execution_id ? [m.execution_id] : [])).size ||
      value.counts.unique_verified_manifests !== new Set(value.members.filter((m) => m.binding).map((m) => m.manifest_sha256)).size ||
      (response.status === 200) !== (value.state === "complete")) {
    throw new AggregateError("Aggregate API returned inconsistent membership or population counts.");
  }
  return { report: value, canonical: text, status: response.status };
}
