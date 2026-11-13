import Ajv2020 from "ajv/dist/2020";
import reportSchema from "../../../schemas/provenance-verification-1.0.schema.json";
import statementSchema from "../../../schemas/provenance-statement-1.0.schema.json";
import terminalSchema from "../../../schemas/runner-terminal-1.0.schema.json";
import { parseStrictJSON } from "../lib/strictJson";

export interface ProvenanceRef { path: string; sha256: string; media_type: string }

export interface ProvenancePredicate {
  profile: string;
  waza: { version: string };
  runner: { api_version: string; request_schema_version: string; image: { reference: string; digest: string } };
  inputs: { request: ProvenanceRef; identity: { canonical_digest: string; run: string } };
  policy: ProvenanceRef;
  runtime: { execution_mode: string; executor: string; model: string };
  artifacts: { manifest: ProvenanceRef; objects: ProvenanceRef[] };
  outcome: { artifact: ProvenanceRef; outcome: string; exit_code: number; failure_boundary: string };
  bindings: (ProvenanceRef & { kind: string })[];
}

export interface ProvenanceCheck {
  id: string;
  status: "passed" | "failed" | "skipped";
  code: string;
  message: string;
}

export interface ProvenanceMember {
  path: string;
  role: string;
  sha256?: string;
  status: string;
}

export interface ProvenanceReport {
  kind: "waza.provenance-verification";
  schema_version: "1.0";
  status: "verified" | "failed";
  exit_code: 0 | 1;
  signer?: { mode: string; key_id: string; trusted: boolean };
  statement?: { path: string; envelope_sha256: string; payload_sha256?: string; predicate_type?: string };
  predicate?: ProvenancePredicate;
  members: ProvenanceMember[];
  checks: ProvenanceCheck[];
}

export interface ProvenanceExpectations {
  image_digest?: string;
  waza_version?: string;
  policy_sha256?: string;
}

const ajv = new Ajv2020({ strict: false });
ajv.addSchema(terminalSchema);
ajv.addSchema(statementSchema);
const validate = ajv.compile<ProvenanceReport>(reportSchema);

export function parseProvenanceReport(text: string): ProvenanceReport {
  const value = parseStrictJSON(text);
  if (!validate(value)) throw new Error("Provenance verification response does not match its schema.");
  return value;
}

/** Maps a selected bundle directory to member paths relative to its root. */
export async function readBundleFiles(files: Iterable<File>): Promise<Map<string, Uint8Array>> {
  const out = new Map<string, Uint8Array>();
  for (const file of files) {
    const relative = (file.webkitRelativePath || file.name).split("/").slice(1).join("/") || file.name;
    out.set(relative, new Uint8Array(await file.arrayBuffer()));
  }
  return out;
}

function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

export async function verifyProvenance(
  files: Map<string, Uint8Array>, publicKey: string, expect: ProvenanceExpectations, signal: AbortSignal,
): Promise<{ result: ProvenanceReport; json: string }> {
  if (files.size === 0) throw new Error("Select a provenance bundle directory first.");
  const body = JSON.stringify({
    public_key: publicKey.trim(),
    expect: Object.fromEntries(Object.entries(expect).filter(([, value]) => value)),
    files: Object.fromEntries([...files].map(([path, data]) => [path, base64(data)])),
  });
  const response = await fetch("/api/v1/provenance/verify", {
    method: "POST", headers: { "Content-Type": "application/json" }, body,
    signal: AbortSignal.any([signal, AbortSignal.timeout(60_000)]),
  });
  const json = await response.text();
  if (!response.ok) {
    let message = `Verification request failed (HTTP ${response.status}).`;
    try {
      const error = JSON.parse(json) as { error?: string };
      if (error.error) message = error.error;
    } catch { /* non-JSON error body keeps the generic message */ }
    throw new Error(message);
  }
  const result = parseProvenanceReport(json);
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code)) {
    throw new Error("Provenance verification transport status contradicts its result.");
  }
  return { result, json };
}
