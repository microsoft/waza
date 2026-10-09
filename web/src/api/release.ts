export interface ReleaseDimension {
  state: string;
  reasons?: string[] | null;
}

export interface ReleaseReliability {
  planned_trials: number;
  started_trials: number;
  complete_trials: number;
  started_attempts: number;
  complete_attempts: number;
  first_attempt_passes: number;
  retry_policy_passes: number;
  recovered_trials: number;
  first_attempt_rate: number | null;
  retry_policy_rate: number | null;
}

export interface ReleaseUsage {
  axis: string;
  currency?: string;
  availability: string;
  value?: string | null;
  observation: string;
  reason?: string;
}

export interface ReleaseDecision {
  kind: "waza.release-decision-view";
  version: "1.0";
  accepted: boolean;
  compatibility: ReleaseDimension;
  completeness: ReleaseDimension;
  assurance: ReleaseDimension;
  golden: ReleaseDimension;
  billing: ReleaseDimension;
  statistics: ReleaseDimension;
  operations: ReleaseDimension;
  estimate?: number;
  lower?: number;
  upper?: number;
  half_width?: number;
  planned_clusters: number;
  limitations: string[];
  accounting: Partial<Record<"baseline" | "candidate", ReleaseReliability>>;
  usage: Partial<Record<"baseline" | "candidate", ReleaseUsage[]>>;
}

export interface ReleaseCollection {
  path: string;
  decision: ReleaseDecision;
  error?: string;
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function strings(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === "string");
}

function validCollection(value: unknown): value is ReleaseCollection {
  if (!record(value) || typeof value.path !== "string" ||
      (value.error !== undefined && typeof value.error !== "string") || !record(value.decision)) return false;
  const d = value.decision;
  if (d.kind !== "waza.release-decision-view" || d.version !== "1.0") {
    throw new Error("Unsupported release decision; not passed.");
  }
  if (typeof d.accepted !== "boolean" || typeof d.planned_clusters !== "number" || !Number.isSafeInteger(d.planned_clusters) ||
      d.planned_clusters < 0 || !strings(d.limitations) || !record(d.accounting) || !record(d.usage)) return false;
  const passStates = {
    compatibility: ["compatible"], completeness: ["complete"], assurance: ["not_required"],
    golden: ["passed", "not_required"], billing: ["within_budget", "not_required"],
    statistics: ["noninferiority", "improvement"], operations: ["observed"],
  };
  for (const [name, acceptedStates] of Object.entries(passStates)) {
    const dimension = d[name];
    if (!record(dimension) || typeof dimension.state !== "string" ||
        (dimension.reasons != null && !strings(dimension.reasons))) return false;
    if (d.accepted && !acceptedStates.includes(dimension.state)) return false;
  }
  for (const name of ["estimate", "lower", "upper", "half_width"]) {
    if (d[name] !== undefined && (typeof d[name] !== "number" || !Number.isFinite(d[name]))) return false;
    if (d.accepted && d[name] === undefined) return false;
  }
  if (d.accepted && (value.error || d.accounting.baseline === undefined || d.accounting.candidate === undefined)) return false;
  if (d.accepted && (d.usage.baseline === undefined || d.usage.candidate === undefined)) return false;
  for (const [arm, counts] of Object.entries(d.accounting)) {
    if (!["baseline", "candidate"].includes(arm) || !record(counts)) return false;
    for (const name of ["planned_trials", "started_trials", "complete_trials", "started_attempts", "complete_attempts",
      "first_attempt_passes", "retry_policy_passes", "recovered_trials"]) {
      const count = counts[name];
      if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 0) return false;
    }
    for (const name of ["first_attempt_rate", "retry_policy_rate"]) {
      const rate = counts[name];
      if (rate !== null && (typeof rate !== "number" || !Number.isFinite(rate) || rate < 0 || rate > 1)) return false;
      if (d.accepted && rate === null) return false;
    }
    if (d.accepted && (counts.planned_trials !== counts.started_trials || counts.planned_trials !== counts.complete_trials ||
        counts.started_attempts !== counts.complete_attempts)) return false;
  }
  for (const [arm, axes] of Object.entries(d.usage)) {
    if (!["baseline", "candidate"].includes(arm) || !Array.isArray(axes)) return false;
    const seen = new Set<string>();
    for (const axis of axes) {
      if (!record(axis) || typeof axis.axis !== "string" || typeof axis.availability !== "string" ||
          typeof axis.observation !== "string" || (axis.currency !== undefined && typeof axis.currency !== "string") ||
          (axis.reason !== undefined && typeof axis.reason !== "string") ||
          (axis.value != null && typeof axis.value !== "string")) return false;
      if (axis.availability === "available" && typeof axis.value !== "string") return false;
      if (axis.availability !== "available" && axis.value != null) return false;
      if (!["input_tokens", "output_tokens", "ai_credits", "provider_currency"].includes(axis.axis)) return false;
      if ((axis.axis === "provider_currency") !== (typeof axis.currency === "string" && axis.currency !== "")) return false;
      const key = `${axis.axis}:${axis.currency ?? ""}`;
      if (seen.has(key)) return false;
      seen.add(key);
      if (axis.availability === "unavailable") {
        if (typeof axis.reason !== "string" || axis.reason.trim() === "" || axis.observation !== "unknown") return false;
      } else if (axis.availability === "available") {
        if ((axis.reason !== undefined && axis.reason !== "") ||
            !["partial", "final_complete_attributable"].includes(axis.observation)) return false;
        const token = axis.value;
        if (typeof token !== "string") return false;
        const pattern = axis.axis === "input_tokens" || axis.axis === "output_tokens"
          ? /^(0|[1-9][0-9]*)$/
          : /^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$|^-0(?:\.0+)?(?:[eE][+-]?[0-9]+)?$/;
        if (!pattern.test(token)) return false;
      } else return false;
    }
    if (d.accepted && !["input_tokens:", "output_tokens:", "ai_credits:"].every((key) => seen.has(key))) return false;
  }
  return true;
}

export async function fetchReleaseCollections(): Promise<ReleaseCollection[]> {
  const response = await fetch("/api/release-collections");
  if (!response.ok) {
    throw new Error(`Release collection API failed (${response.status})`);
  }
  const collections: unknown = await response.json();
  if (!Array.isArray(collections) || !collections.every(validCollection)) {
    throw new Error("Invalid release decision view; not passed.");
  }
  return collections;
}
