import Ajv2020 from "ajv/dist/2020";
import addFormats from "ajv-formats";
import isolationSchema from "../../../schemas/execution-isolation-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export type IsolationDimension = "filesystem" | "environment" | "network" | "subprocess" | "remote_tools";

export interface IsolationProbe {
  provider: string;
  outcome: "enforced" | "violated" | "inconclusive";
  detail: string;
}

export interface IsolationCapability {
  dimension: IsolationDimension;
  requested: "unrestricted" | "restricted";
  available: boolean;
  effective: "unrestricted" | "restricted" | "unavailable";
  enforcement_source?: string;
  probes: IsolationProbe[];
  diagnostic?: { code: "no_provider" | "probe_violated" | "probe_inconclusive"; message: string };
}

export interface IsolationProvider {
  name: string;
  status: "available" | "absent" | "unavailable";
  reason?: string;
  enforced_by?: string;
}

export interface IsolationPreflight {
  kind: "waza.execution-isolation-preflight";
  schema_version: "1.0";
  checked_at: string;
  decision: "admitted" | "rejected";
  providers: IsolationProvider[];
  dimensions: IsolationCapability[];
}

export const isolationDimensions: IsolationDimension[] = ["filesystem", "environment", "network", "subprocess", "remote_tools"];

const ajv = new Ajv2020({ strict: false });
addFormats(ajv);
const validate = ajv.compile(isolationSchema);

// Rejects reports a preflight could not have produced: the schema covers
// shape and per-dimension consistency; these checks bind dimensions to probes
// and providers so no boundary is shown as enforced without its evidence.
function contradiction(report: IsolationPreflight): string | undefined {
  if (report.dimensions.some((entry, index) => entry.dimension !== isolationDimensions[index])) {
    return "Dimensions are missing, duplicated or out of order.";
  }
  const available = new Set(report.providers.filter((provider) => provider.status === "available").map((provider) => provider.name));
  if (new Set(report.providers.map((provider) => provider.name)).size !== report.providers.length) {
    return "Providers are duplicated.";
  }
  for (const entry of report.dimensions) {
    if (entry.probes.some((probe) => !available.has(probe.provider))) {
      return `${entry.dimension} has a probe from a provider that is not available.`;
    }
    const outcomes = new Set(entry.probes.map((probe) => probe.outcome));
    if (entry.available) {
      const source = entry.probes.find((probe) => probe.provider === entry.enforcement_source);
      if (source?.outcome !== "enforced" || outcomes.has("violated")) {
        return `${entry.dimension} is shown as enforced without a passing conformance probe.`;
      }
      continue;
    }
    const expected = outcomes.has("violated") ? "probe_violated" : outcomes.has("enforced")
      ? undefined : outcomes.size > 0 ? "probe_inconclusive" : "no_provider";
    if (entry.diagnostic?.code !== expected) {
      return `${entry.dimension} diagnostic does not match its probes.`;
    }
  }
  return undefined;
}

export function parseIsolationPreflight(text: string): IsolationPreflight {
  const value = parseStrictJSON(text);
  if (!validate(value) || (value as { kind?: unknown }).kind !== "waza.execution-isolation-preflight") {
    throw new Error("Not a valid waza.execution-isolation-preflight 1.0 report.");
  }
  const report = value as IsolationPreflight;
  const problem = contradiction(report);
  if (problem) throw new Error(`Isolation report is inconsistent: ${problem}`);
  return report;
}
