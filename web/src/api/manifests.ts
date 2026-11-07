import Ajv2020 from "ajv/dist/2020";
import { parseStrictJSON } from "../lib/strictJson";

export interface ManifestReceipt {
  manifest_sha256: string;
  attempt?: string;
  publication: "unfinalized" | "finalized" | "conflict";
  integrity: "unverified" | "verified" | "failed";
  verification: "content-and-declared-context-only";
  artifacts: {
    name: string;
    sha256: string;
    size_bytes: number;
    media_type: string;
    integrity: "verified" | "unverified" | "missing" | "corrupt";
  }[];
  diagnostics: { code: string; message: string }[];
}

export interface ManifestInspection {
  status: number;
  receipt: ManifestReceipt;
}

export interface ManifestCatalog {
  manifest_sha256: string[];
  truncated: boolean;
  verification: "unverified-pointers-only";
  diagnostics: { code: string; message: string }[];
}

export class ManifestInspectionError extends Error {}

const validateCatalog = new Ajv2020({ strict: false }).compile<ManifestCatalog>({
  type: "object",
  additionalProperties: false,
  required: ["manifest_sha256", "truncated", "verification", "diagnostics"],
  properties: {
    manifest_sha256: { type: "array", maxItems: 1000, uniqueItems: true,
      items: { type: "string", pattern: "^[0-9a-f]{64}$" } },
    truncated: { type: "boolean" },
    verification: { const: "unverified-pointers-only" },
    diagnostics: { type: "array", items: { type: "object", additionalProperties: false,
      required: ["code", "message"], properties: { code: { type: "string" }, message: { type: "string" } } } },
  },
});

const validateReceipt = new Ajv2020({ strict: false }).compile<ManifestReceipt>({
  type: "object",
  additionalProperties: false,
  required: ["manifest_sha256", "publication", "integrity", "verification", "artifacts", "diagnostics"],
  properties: {
    manifest_sha256: { type: "string", pattern: "^[0-9a-f]{64}$" },
    attempt: { type: "string", pattern: "^attempt-[0-9a-f]{64}$" },
    publication: { enum: ["unfinalized", "finalized", "conflict"] },
    integrity: { enum: ["unverified", "verified", "failed"] },
    verification: { const: "content-and-declared-context-only" },
    artifacts: {
      type: "array",
      maxItems: 128,
      items: {
        type: "object",
        additionalProperties: false,
        required: ["name", "sha256", "size_bytes", "media_type", "integrity"],
        properties: {
          name: { type: "string", maxLength: 128, pattern: "^[a-zA-Z0-9][a-zA-Z0-9._-]*$" },
          sha256: { type: "string", pattern: "^[0-9a-f]{64}$" },
          size_bytes: { type: "integer", minimum: 0, maximum: 67108864 },
          media_type: { type: "string", minLength: 1, maxLength: 128 },
          integrity: { enum: ["verified", "unverified", "missing", "corrupt"] },
        },
      },
    },
    diagnostics: {
      type: "array",
      items: {
        type: "object",
        additionalProperties: false,
        required: ["code", "message"],
        properties: { code: { type: "string" }, message: { type: "string" } },
      },
    },
  },
  allOf: [
    {
      if: { properties: { integrity: { const: "verified" } } },
      then: {
        properties: { diagnostics: { maxItems: 0 }, artifacts: { minItems: 1, items: { properties: { integrity: { const: "verified" } } } } },
      },
    },
    {
      if: { properties: { integrity: { const: "failed" } } },
      then: { properties: { diagnostics: { minItems: 1 } } },
    },
  ],
});

export async function inspectManifest(digest: string, signal: AbortSignal): Promise<ManifestInspection> {
  let response: Response;
  let text: string;
  try {
    response = await fetch(`/api/v1/manifests/${encodeURIComponent(digest)}`, {
      method: "GET",
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    });
    text = await response.text();
  } catch {
    // Transport errors may contain private provider URLs or filesystem paths.
    throw new ManifestInspectionError("Manifest inspection could not be loaded. Check the server connection and retry.");
  }

  if (![200, 400, 404, 409, 503].includes(response.status)) {
    throw new ManifestInspectionError(`Manifest API returned HTTP ${response.status}.`);
  }
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new ManifestInspectionError("Manifest API returned a malformed receipt.");
  }
  if (!validateReceipt(value) || value.manifest_sha256 !== digest ||
      (response.status === 200 && (value.integrity !== "verified" || value.publication === "conflict")) ||
      (response.status !== 200 && value.integrity === "verified")) {
    throw new ManifestInspectionError("Manifest API returned an invalid or inconsistent receipt.");
  }
  return { status: response.status, receipt: value };
}

export async function discoverManifests(signal: AbortSignal): Promise<ManifestCatalog> {
  let response: Response;
  let text: string;
  try {
    response = await fetch("/api/v1/manifests", {
      signal: AbortSignal.any([signal, AbortSignal.timeout(10_000)]),
    });
    text = await response.text();
  } catch {
    throw new ManifestInspectionError("Manifest discovery could not be loaded. Check the server connection and retry.");
  }
  if (response.status !== 200 && response.status !== 503) {
    throw new ManifestInspectionError(`Manifest discovery returned HTTP ${response.status}.`);
  }
  let value: unknown;
  try {
    value = parseStrictJSON(text);
  } catch {
    throw new ManifestInspectionError("Manifest discovery returned malformed data.");
  }
  if (!validateCatalog(value) || (response.status !== 200 && value.manifest_sha256.length !== 0)) {
    throw new ManifestInspectionError("Manifest discovery returned invalid data.");
  }
  return value;
}
