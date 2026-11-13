import Ajv2020 from "ajv/dist/2020";
import telemetrySchema from "../../../schemas/execution-telemetry-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export type TelemetrySignal = "traces" | "metrics" | "logs";

export interface SignalCount {
  emitted: number;
  dropped: number;
  names: Record<string, number>;
}

export interface TelemetryExport {
  signal: TelemetrySignal;
  destination: "file" | "otlp_http";
  target: string;
  state: "exported" | "partial" | "failed" | "skipped";
  http_status?: number;
  error_code?: string;
}

export interface ExecutionTelemetry {
  kind: "waza.execution-telemetry";
  schema_version: "1.0";
  payload_policy: "exclude" | "digest";
  resource: Record<string, string>;
  correlation: {
    trace_id: string;
    root_span_id: string;
    run: string;
    work: string;
    scheduler_work_id?: string;
    identity_digest: string;
    shard: string;
    task: string;
    trial: string;
    attempt: string;
    task_span_id: string;
    attempts: {
      span_id: string;
      execution_id?: string;
      parent_execution_id?: string;
      relationship?: string;
      native_attempt: number;
      status: string;
      outcome?: string;
      manifest_sha256?: string;
      selected: boolean;
    }[];
    artifacts: { span_id: string; name: string; sha256: string; size_bytes: number; terminal: boolean }[];
    aggregation?: {
      span_id: string;
      sha256: string;
      state: string;
      members: { manifest_sha256: string; state: string; trace_id?: string; span_id?: string; execution_id?: string }[];
    };
  };
  sources: { source: string; state: "present" | "unavailable" | "unmatched" }[];
  signals: Record<TelemetrySignal, SignalCount>;
  dropped: { signal: TelemetrySignal; reason: string; count: number }[];
  exports: TelemetryExport[];
}

export class TelemetryError extends Error {}

const validateTelemetry = new Ajv2020({ strict: false }).compile<ExecutionTelemetry>(telemetrySchema);

const signals: TelemetrySignal[] = ["traces", "metrics", "logs"];

// consistent rejects reports whose counts or correlation disagree with themselves.
function consistent(report: ExecutionTelemetry): string | undefined {
  const { correlation, resource } = report;
  if (resource["waza.run.id"] !== correlation.run || resource["waza.work.id"] !== correlation.work) {
    return "Resource run and work do not match the correlation identifiers.";
  }
  if (resource["waza.scheduler.work_id"] !== correlation.scheduler_work_id) {
    return "Resource scheduler work ID does not match the correlation identifier.";
  }
  if (correlation.attempts.filter((attempt) => attempt.selected).length > 1) {
    return "More than one attempt is marked as selected.";
  }
  if (correlation.artifacts.filter((artifact) => artifact.terminal).length > 1) {
    return "More than one artifact is marked as the terminal artifact.";
  }
  for (const signal of signals) {
    const count = report.signals[signal];
    const named = Object.values(count.names).reduce((sum, value) => sum + value, 0);
    if (named !== count.emitted) return `Signal names for ${signal} do not add up to the emitted count.`;
    const dropped = report.dropped
      .filter((drop) => drop.signal === signal && drop.reason !== "attribute_truncated")
      .reduce((sum, drop) => sum + drop.count, 0);
    if (dropped !== count.dropped) return `Drop diagnostics for ${signal} do not add up to the dropped count.`;
  }
  return undefined;
}

export function parseExecutionTelemetry(text: string): ExecutionTelemetry {
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new TelemetryError("Telemetry report is not unambiguous JSON.");
  }
  if (!validateTelemetry(value)) {
    throw new TelemetryError("Telemetry report does not match the waza.execution-telemetry 1.0 schema.");
  }
  const problem = consistent(value);
  if (problem) throw new TelemetryError(problem);
  return value;
}

// exportHealth summarizes export outcomes without exposing receiver content.
export function exportHealth(report: ExecutionTelemetry): "healthy" | "degraded" | "not_exported" {
  if (report.exports.length === 0) return "not_exported";
  return report.exports.every((entry) => entry.state === "exported") ? "healthy" : "degraded";
}
