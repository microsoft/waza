interface ExecutionSource { execution_id: string; manifest_digest: string }
export interface ParentContext {
  state: string;
  verification: "unverified" | "content-and-declared-context-only";
  relationship: "retry" | "branch";
  source?: ExecutionSource;
}
export interface SourceContext {
  state: string;
  verification: "unverified" | "content-and-declared-context-only";
  source?: ExecutionSource;
  parent_context?: ParentContext;
}

const states = new Set(["verified", "missing", "unfinalized", "conflicting", "corrupt",
  "incompatible", "limit_exceeded", "unavailable", "timeout", "cancelled"]);
function object(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function executionSource(value: unknown): value is ExecutionSource {
  return object(value) && Object.keys(value).length === 2 &&
    typeof value.execution_id === "string" && /^exec-[0-9a-f]{64}$/.test(value.execution_id) &&
    typeof value.manifest_digest === "string" && /^[0-9a-f]{64}$/.test(value.manifest_digest);
}

// Consumers still validate their own enclosing response and native categories.
// This gate reconciles only the one-hop, output-only current source context.
export function sourceContextConsistent(value: unknown, origin: string | undefined,
  operationIncomplete: boolean): value is SourceContext | undefined {
  if (value === undefined) return true;
  if (!object(value) || Object.keys(value).some(key =>
    !["state", "verification", "source", "parent_context"].includes(key)) ||
    typeof value.state !== "string" || !states.has(value.state)) return false;
  const source = value.source;
  if (source !== undefined && (!executionSource(source) || source.manifest_digest !== origin)) return false;
  if (value.state === "verified") {
    if (!executionSource(source) || value.verification !== "content-and-declared-context-only") return false;
  } else if (value.verification !== "unverified" || (source !== undefined && !operationIncomplete)) return false;
  const parent = value.parent_context;
  if (parent === undefined) return true;
  if (value.state !== "verified" || !executionSource(source) || !object(parent) ||
    Object.keys(parent).some(key => !["state", "verification", "relationship", "source"].includes(key)) ||
    typeof parent.state !== "string" || !states.has(parent.state) ||
    (parent.relationship !== "retry" && parent.relationship !== "branch")) return false;
  if (parent.state === "verified") {
    return parent.relationship === "retry" && parent.verification === "content-and-declared-context-only" &&
      executionSource(parent.source) && parent.source.execution_id !== source.execution_id &&
      parent.source.manifest_digest !== source.manifest_digest;
  }
  return parent.verification === "unverified" && parent.source === undefined &&
    (parent.relationship === "retry" ? operationIncomplete : parent.state === "unavailable");
}
