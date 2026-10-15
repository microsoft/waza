import { test, expect } from "@playwright/test";
import { acceptedReleaseCollection, RELEASE_COLLECTIONS } from "./fixtures/release-data";

test("release dimensions and incomplete accounting remain distinct", async ({ page }) => {
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: RELEASE_COLLECTIONS }));
  await page.goto("/#/release");
  await expect(page.getByRole("heading", { name: "Release policies", exact: true })).toBeVisible();
  await expect(page.getByText("Selected policy: NOT PASSED", { exact: true })).toBeVisible();
  await expect(page.getByText("Required attributable assurance is missing.")).toBeVisible();
  await expect(page.getByText("Required first-attempt golden evidence is missing.")).toBeVisible();
  await expect(page.getByText("Final attributable credits are unavailable.")).toBeVisible();
  await expect(page.getByText("Independent complete clusters are not established.")).toBeVisible();
  await expect(page.getByText(/Attempts: 1 started, 0 complete/)).toBeVisible();
  await expect(page.getByText(/First-attempt rate: unavailable/)).toBeVisible();
  await expect(page.getByText(/9007199254740993/)).toBeVisible();
  await expect(page.getByText(/ai_credits.*unavailable/)).toBeVisible();
});

test("unsupported release artifact never displays a pass", async ({ page }) => {
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({
    json: [{ ...RELEASE_COLLECTIONS[0], decision: { ...RELEASE_COLLECTIONS[0].decision, kind: "future", accepted: true } }],
  }));
  await page.goto("/#/release");
  await expect(page.getByRole("alert")).toHaveText("Unsupported release decision; not passed.");
  await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
});

test("release API errors are visible", async ({ page }) => {
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ status: 500, json: { error: "invalid local artifact" } }));
  await page.goto("/#/release");
  await expect(page.getByRole("alert")).toHaveText("Release collection API failed (500)");
});

for (const malformed of ["numeric_usage", "missing_dimension", "missing_accounting", "unavailable_zero", "partial_pass",
  "missing_usage_arm", "missing_usage_axis", "invalid_numeric_string", "missing_reasons"]) {
  test(`malformed release view rejects ${malformed}`, async ({ page }) => {
    const collection = malformed.startsWith("missing_usage") ? acceptedReleaseCollection() : structuredClone(RELEASE_COLLECTIONS[0]);
    const decision: Record<string, unknown> = { ...collection.decision };
    if (malformed === "numeric_usage") {
      decision.usage = { baseline: [{ axis: "input_tokens", availability: "available", observation: "final", value: 9007199254740992 }] };
    } else if (malformed === "unavailable_zero") {
      decision.usage = { baseline: [{ axis: "ai_credits", availability: "unavailable", observation: "unknown", value: "0" }] };
    } else if (malformed === "partial_pass") {
      decision.accepted = true;
    } else if (malformed === "missing_usage_arm") {
      decision.usage = { baseline: collection.decision.usage.baseline };
    } else if (malformed === "missing_usage_axis") {
      decision.usage = { ...collection.decision.usage, candidate: collection.decision.usage.candidate?.filter((axis) => axis.axis !== "ai_credits") };
    } else if (malformed === "invalid_numeric_string") {
      decision.usage = { baseline: [{ axis: "input_tokens", availability: "available", observation: "partial", value: "NaN" }] };
    } else if (malformed === "missing_reasons") {
      decision.compatibility = { state: "compatible" };
    } else {
      delete decision[malformed === "missing_dimension" ? "assurance" : "accounting"];
    }
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: [{ ...collection, decision }] }));
    await page.goto("/#/release");
    await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
    await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
  });
}

test("complete accepted server view remains displayable without recomputing policy", async ({ page }) => {
  const collection = acceptedReleaseCollection();
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: [collection] }));
  await page.goto("/#/release");
  await expect(page.getByText("Selected policy: PASSED", { exact: true })).toBeVisible();
  await expect(page.getByText(/9007199254740993/)).toHaveCount(2);
  await expect(page.getByText(/9007199254740993/).first()).toBeVisible();
});
