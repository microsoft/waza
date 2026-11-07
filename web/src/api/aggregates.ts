import Ajv2020 from "ajv/dist/2020";
import schema from "../../../schemas/manifest-aggregate-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

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
}

export class AggregateError extends Error {}

const validate = new Ajv2020({ strict: false }).compile<ManifestAggregate>(schema);

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

export async function aggregateManifests(manifests: string[], signal: AbortSignal): Promise<ManifestAggregate> {
  const expected = [...new Set(manifests)].sort();
  let response: Response;
  let text: string;
  try {
    response = await fetch("/api/v1/aggregates", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ manifests }),
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
  return value;
}
