import Ajv2020 from "ajv/dist/2020";
import reportSchema from "../../../schemas/inventory-verification-1.0.schema.json";
import { lazyValidator } from "../lib/lazyValidator";
import { parseStrictJSON } from "../lib/strictJson";

export type InventorySubjectKind = "image" | "plugin";

export interface InventoryCheck {
  id: "inventory" | "profile" | "subject" | "binding" | "components";
  status: "passed" | "failed" | "skipped";
  code: string;
  message: string;
}

export interface InventoryReport {
  kind: "waza.inventory-verification";
  schema_version: "1.0";
  status: "verified" | "failed";
  exit_code: 0 | 1;
  inventory_sha256?: string;
  expected: { kind: InventorySubjectKind; digest: string; name?: string };
  subject?: { kind: InventorySubjectKind; name: string; digest: string; purl?: string };
  summary?: { total: number; by_type: Record<string, number>; by_ecosystem: Record<string, number> };
  checks: InventoryCheck[];
}

export interface InventoryExpectation {
  kind: InventorySubjectKind;
  digest: string;
  name?: string;
}

const validate = lazyValidator(() => new Ajv2020({ strict: false }).compile<InventoryReport>(reportSchema));

export function parseInventoryReport(text: string): InventoryReport {
  const value = parseStrictJSON(text);
  if (!validate(value)) throw new Error("Inventory verification response does not match its schema.");
  return value;
}

/** Matches the verifier's own inventory limit. */
export const maxInventoryBytes = 16 * 1024 * 1024;

function base64(bytes: Uint8Array): string {
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(binary);
}

/** Verifies an inventory; a null inventory yields the server's inventory_missing report. */
export async function verifyInventory(
  inventory: Uint8Array | null, expect: InventoryExpectation, signal: AbortSignal,
): Promise<{ result: InventoryReport; json: string }> {
  if (inventory && inventory.length > maxInventoryBytes) {
    throw new Error("This inventory exceeds the 16 MiB limit and cannot be verified.");
  }
  const body = JSON.stringify({
    inventory: inventory ? base64(inventory) : null,
    expect: Object.fromEntries(Object.entries(expect).map(([key, value]) => [key, typeof value === "string" ? value.trim() : value]).filter(([, value]) => value)),
  });
  const response = await fetch("/api/v1/inventory/verify", {
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
  const result = parseInventoryReport(json);
  if (response.headers.get("X-Waza-Exit-Code") !== String(result.exit_code)) {
    throw new Error("Inventory verification transport status contradicts its result.");
  }
  return { result, json };
}
