import { readFile } from "node:fs/promises";
import { parseReleaseCollections } from "../../src/api/release";

export const nativeScenarios = [
  "passed", "retry", "initialize", "execute", "shutdown", "disconnect",
  "empty-output", "unknown-usage", "unknown-budget", "reached-budget",
] as const;

export async function nativeReleaseCollections(scenario: typeof nativeScenarios[number] | "accepted" | "carriage-return") {
  const path = new URL(`../../../internal/controlledcomparison/testdata/native-api-${scenario}.json`, import.meta.url);
  const wire: unknown = JSON.parse(await readFile(path, "utf8"));
  return parseReleaseCollections(wire);
}
