import { expect, test, type Page } from "@playwright/test";
import type { ManifestReceipt } from "../src/api/manifests";

const digest = "a".repeat(64);
const nextDigest = "b".repeat(64);
const attempt = `attempt-${"c".repeat(64)}`;
const privateLocation = "https://private-provider.example/secret /private/operator/store";

// These controlled receipts exercise browser admission and rendering; producer
// and stored-content verification are covered separately by the Go API tests.
function receipt(overrides: Partial<ManifestReceipt> = {}): ManifestReceipt {
  return {
    manifest_sha256: digest,
    attempt,
    publication: "finalized",
    integrity: "verified",
    verification: "content-and-declared-context-only",
    artifacts: [{
      name: "terminal.json",
      sha256: "d".repeat(64),
      size_bytes: 512,
      media_type: "application/json",
      integrity: "verified",
    }],
    diagnostics: [],
    ...overrides,
  };
}

function panel(page: Page) {
  return page.getByRole("region", { name: "Manifest publication inspection" });
}

function result(page: Page) {
  return panel(page).getByRole("status", { name: "Manifest inspection receipt" });
}

async function inspect(page: Page, value = digest) {
  await panel(page).getByRole("textbox", { name: "Manifest SHA-256 digest" }).fill(value);
  await panel(page).getByRole("button", { name: "Inspect manifest", exact: true }).click();
}

test.describe("Lab immutable manifest publication", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("inspects the digest with GET and shows only declared-context content verification", async ({ page }) => {
    const methods: string[] = [];
    await page.route(`**/api/v1/manifests/${digest}`, (route) => {
      methods.push(route.request().method());
      return route.fulfill({ json: receipt() });
    });
    await inspect(page);
    await expect(result(page)).toBeVisible();
    await expect(result(page).locator("dd")).toHaveText([digest, attempt, "finalized", "verified", "200"]);
    const rows = result(page).getByRole("row");
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(1).locator("th, td")).toHaveText([
      "terminal.json", "verified", "d".repeat(64), "512", "application/json",
    ]);
    await expect(panel(page)).toContainText("Content and declared context only; not execution attestation.");
    await expect(result(page)).toContainText("No diagnostics reported.");
    expect(methods).toEqual(["GET"]);
  });

  test("verified bytes can be unfinalized and explicit zero sizes remain zero", async ({ page }) => {
    const value = receipt({
      publication: "unfinalized",
      artifacts: [
        ...receipt().artifacts,
        { name: "empty.txt", sha256: "e".repeat(64), size_bytes: 0, media_type: "text/plain", integrity: "verified" },
      ],
    });
    await page.route("**/api/v1/manifests/*", (route) => route.fulfill({ json: value }));
    await inspect(page);
    await expect(result(page).locator("dd").nth(2)).toHaveText("unfinalized");
    await expect(result(page).getByRole("row")).toHaveCount(3);
    await expect(result(page).getByRole("row").nth(2).locator("th, td")).toHaveText([
      "empty.txt", "verified", "e".repeat(64), "0", "text/plain",
    ]);
  });

  test("rereads damaged content rather than retaining a successful publication receipt", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/manifests/*", (route) => {
      reads++;
      return route.fulfill(reads === 1 ? { json: receipt() } : {
        status: 409,
        json: receipt({
          integrity: "failed",
          artifacts: [
            { ...receipt().artifacts[0]!, integrity: "corrupt" },
            { name: "capture.txt", sha256: "e".repeat(64), size_bytes: 20, media_type: "text/plain", integrity: "missing" },
          ],
          diagnostics: [
            { code: "corrupt_object", message: "terminal.json: Stored bytes or metadata do not match the declaration." },
            { code: "missing_object", message: "capture.txt: Upload the declared object and retry; no result has been verified." },
          ],
        }),
      });
    });
    await inspect(page);
    await expect(result(page).locator("dd").nth(3)).toHaveText("verified");
    await panel(page).getByRole("button", { name: "Inspect manifest", exact: true }).click();
    await expect(result(page).locator("dd").nth(3)).toHaveText("failed");
    await expect(result(page).locator("dd").nth(4)).toHaveText("409");
    await expect(result(page).getByRole("row").nth(1).getByRole("cell").first()).toHaveText("corrupt");
    await expect(result(page).getByRole("row").nth(2).getByRole("cell").first()).toHaveText("missing");
    await expect(result(page).getByRole("listitem")).toHaveText([
      "corrupt_object: terminal.json: Stored bytes or metadata do not match the declaration.",
      "missing_object: capture.txt: Upload the declared object and retry; no result has been verified.",
    ]);
    expect(reads).toBe(2);
  });

  test("keeps conflicting publication and logical attempt diagnostics inspectable", async ({ page }) => {
    await page.route("**/api/v1/manifests/*", (route) => route.fulfill({
      status: 409,
      json: receipt({
        publication: "conflict",
        integrity: "failed",
        diagnostics: [{
          code: "publication_conflict",
          message: "The finalization record names different content. Preserve the record and inspect the storage conflict; never overwrite.",
        }],
      }),
    }));
    await inspect(page);
    await expect(result(page).locator("dd").nth(2)).toHaveText("conflict");
    await expect(result(page).locator("dd").nth(3)).toHaveText("failed");
    await expect(result(page)).toContainText("publication_conflict: The finalization record names different content.");
    await expect(result(page).getByRole("row").nth(1).getByRole("cell").first()).toHaveText("verified");
  });

  for (const scenario of [
    { status: 404, code: "missing_object", message: "Upload the declared object and retry; no result has been verified." },
    { status: 400, code: "invalid_declaration", message: "Provide a valid v1 manifest and full lowercase SHA-256 digest." },
    { status: 503, code: "storage_error", message: "Storage could not be read or committed. Check operator diagnostics and retry without replacing evidence." },
  ]) {
    test(`shows an inspectable ${scenario.status} receipt without fabricating artifacts or attempt`, async ({ page }) => {
      const value = receipt({
        publication: "unfinalized", integrity: "unverified", artifacts: [],
        diagnostics: [{ code: scenario.code, message: scenario.message }],
      });
      delete value.attempt;
      await page.route("**/api/v1/manifests/*", (route) => route.fulfill({ status: scenario.status, json: value }));
      await inspect(page);
      await expect(result(page).locator("dd")).toHaveText([digest, "Not available", "unfinalized", "unverified", String(scenario.status)]);
      await expect(result(page)).toContainText("No artifact declarations available.");
      await expect(result(page).getByRole("listitem")).toHaveText(`${scenario.code}: ${scenario.message}`);
      await expect(panel(page).getByRole("alert")).toHaveCount(0);
    });
  }

  test("rejects invalid digest entry locally without requesting or retaining old evidence", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/manifests/*", (route) => {
      reads++;
      return route.fulfill({ json: receipt() });
    });
    await inspect(page);
    await expect(result(page)).toBeVisible();
    for (const invalid of ["", "a".repeat(63), "A".repeat(64), "../private/path", `${digest} `]) {
      await inspect(page, invalid);
      await expect(panel(page).getByRole("alert")).toContainText("Enter a full lowercase SHA-256 digest");
      await expect(result(page)).toHaveCount(0);
    }
    expect(reads).toBe(1);
  });

  const malformed: { name: string; body: string }[] = [
    { name: "missing field", body: JSON.stringify({ ...receipt(), diagnostics: undefined }) },
    { name: "null artifacts", body: JSON.stringify({ ...receipt(), artifacts: null }) },
    { name: "null attempt", body: JSON.stringify({ ...receipt(), attempt: null }) },
    { name: "invalid publication", body: JSON.stringify({ ...receipt(), publication: "published" }) },
    { name: "execution attestation", body: JSON.stringify({ ...receipt(), verification: "execution-attested" }) },
    { name: "unknown property", body: JSON.stringify({ ...receipt(), provider_url: privateLocation }) },
    { name: "wrong digest", body: JSON.stringify({ ...receipt(), manifest_sha256: nextDigest }) },
    { name: "invalid artifact hash", body: JSON.stringify({ ...receipt(), artifacts: [{ ...receipt().artifacts[0], sha256: "not-a-hash" }] }) },
    { name: "string artifact size", body: JSON.stringify({ ...receipt(), artifacts: [{ ...receipt().artifacts[0], size_bytes: "512" }] }) },
    { name: "negative artifact size", body: JSON.stringify({ ...receipt(), artifacts: [{ ...receipt().artifacts[0], size_bytes: -1 }] }) },
    { name: "unsafe artifact size", body: JSON.stringify({ ...receipt(), artifacts: [{ ...receipt().artifacts[0], size_bytes: Number.MAX_SAFE_INTEGER + 1 }] }) },
    { name: "wrong artifact integrity", body: JSON.stringify({ ...receipt(), artifacts: [{ ...receipt().artifacts[0], integrity: "failed" }] }) },
    { name: "malformed diagnostic", body: JSON.stringify({ ...receipt(), diagnostics: [{ code: "storage_error", message: 4 }] }) },
    { name: "duplicate JSON key", body: JSON.stringify(receipt()).replace('"integrity":"verified"', '"integrity":"failed","integrity":"verified"') },
    { name: "contradictory success", body: JSON.stringify(receipt({ integrity: "failed" })) },
  ];

  for (const scenario of malformed) {
    test(`rejects ${scenario.name} rather than defaulting receipt fields`, async ({ page }) => {
      await page.route("**/api/v1/manifests/*", (route) => route.fulfill({
        contentType: "application/json", body: scenario.body,
      }));
      await inspect(page);
      await expect(panel(page).getByRole("alert")).toContainText(/Manifest API returned (a malformed|an invalid or inconsistent) receipt/);
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page)).not.toContainText(privateLocation);
    });
  }

  test("input changes clear old receipts and discard late responses from the prior digest", async ({ page }) => {
    let release!: () => void;
    const blocked = new Promise<void>((resolve) => { release = resolve; });
    let started!: () => void;
    const requested = new Promise<void>((resolve) => { started = resolve; });
    await page.route(`**/api/v1/manifests/${digest}`, async (route) => {
      started();
      await blocked;
      await route.fulfill({ json: receipt() });
    });
    await page.route(`**/api/v1/manifests/${nextDigest}`, (route) => route.fulfill({
      json: receipt({ manifest_sha256: nextDigest, publication: "unfinalized" }),
    }));
    try {
      await inspect(page);
      await requested;
      await panel(page).getByRole("textbox", { name: "Manifest SHA-256 digest" }).fill(nextDigest);
      await expect(result(page)).toHaveCount(0);
      await panel(page).getByRole("button", { name: "Inspect manifest", exact: true }).click();
      await expect(result(page).locator("dd").first()).toHaveText(nextDigest);
      release();
      await expect(panel(page).getByRole("button", { name: "Inspect manifest", exact: true })).toBeEnabled();
      await expect(result(page).locator("dd").first()).toHaveText(nextDigest);
    } finally {
      release();
    }
    await panel(page).getByRole("textbox", { name: "Manifest SHA-256 digest" }).fill(digest);
    await expect(result(page)).toHaveCount(0);
  });

  for (const failure of ["malformed", "HTTP", "transport"] as const) {
    test(`reload ${failure} error clears previous success and hides private transport details`, async ({ page }) => {
      let reads = 0;
      await page.route("**/api/v1/manifests/*", (route) => {
        reads++;
        return route.fulfill(reads === 1 ? { json: receipt() } : {
          status: failure === "HTTP" ? 502 : 503,
          contentType: "text/plain", body: privateLocation,
        });
      });
      await inspect(page);
      await expect(result(page)).toBeVisible();
      if (failure === "transport") {
        await page.evaluate((location) => {
          const original = window.fetch;
          window.fetch = (input, init) => String(input).includes("/api/v1/manifests/")
            ? Promise.reject(new Error(location))
            : original(input, init);
        }, privateLocation);
      }
      await panel(page).getByRole("button", { name: "Inspect manifest", exact: true }).click();
      await expect(panel(page).getByRole("alert")).toContainText("No previous receipt is shown.");
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page)).not.toContainText(privateLocation);
      await expect(panel(page)).not.toContainText("private-provider.example");
      await expect(panel(page)).not.toContainText("/private/operator/store");
    });
  }

  test("synthetic demo cannot inspect manifests or manufacture publication success", async ({ page }) => {
    let requests = 0;
    await page.route("**/api/v1/manifests/**", (route) => {
      requests++;
      return route.fulfill({ json: receipt() });
    });
    await page.goto("/#/lab/demo/runner");
    await expect(panel(page)).toContainText("Manifest inspection is unavailable in the synthetic demo.");
    await expect(panel(page).getByRole("textbox")).toHaveCount(0);
    await expect(panel(page).getByRole("button")).toHaveCount(0);
    await expect(result(page)).toHaveCount(0);
    expect(requests).toBe(0);
  });

  test("discovers persisted pointers without claiming their integrity and clears obsolete catalogs", async ({ page }) => {
    let reads = 0;
    await page.route("**/api/v1/manifests", (route) => {
      reads++;
      return route.fulfill(reads === 1 ? {
        json: { manifest_sha256: [digest], truncated: true, verification: "unverified-pointers-only", diagnostics: [] },
      } : { status: 502, body: privateLocation });
    });
    const find = panel(page).getByRole("button", { name: "Find stored manifests", exact: true });
    await find.click();
    await expect(panel(page)).toContainText("Unverified declaration pointers.");
    await expect(panel(page)).toContainText("Discovery is truncated");
    await panel(page).getByRole("button", { name: digest, exact: true }).click();
    await expect(panel(page).getByRole("textbox", { name: "Manifest SHA-256 digest" })).toHaveValue(digest);
    await expect(result(page)).toHaveCount(0);
    await find.click();
    await expect(panel(page).getByRole("alert")).toContainText("No previous catalog is shown.");
    await expect(panel(page).getByRole("button", { name: digest, exact: true })).toHaveCount(0);
    await expect(panel(page)).not.toContainText(privateLocation);
    expect(reads).toBe(2);
  });
});
