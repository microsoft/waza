import { expect, test, type Page } from "@playwright/test";
import fixture from "./fixtures/execution-telemetry.json" with { type: "json" };

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution telemetry" });
}

async function read(page: Page, report: unknown) {
  await panel(page).getByRole("textbox", { name: "Telemetry report JSON" }).fill(JSON.stringify(report));
  await panel(page).getByRole("button", { name: "Read telemetry report" }).click();
}

test.describe("Execution telemetry in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows correlation identifiers, drops and export diagnostics", async ({ page }) => {
    await read(page, fixture);
    const result = panel(page).getByRole("status", { name: "Execution telemetry report" });
    const ids = result.getByRole("definition");
    await expect(ids.filter({ hasText: fixture.correlation.trace_id })).toBeVisible();
    await expect(ids.filter({ hasText: fixture.correlation.root_span_id })).toBeVisible();
    await expect(ids.filter({ hasText: fixture.correlation.scheduler_work_id })).toBeVisible();

    for (const name of ["Attempt spans", "Artifact spans", "Signal counts", "Export diagnostics"]) {
      await expect(result.getByRole("region", { name: `${name} table` })).toHaveAttribute("tabindex", "0");
    }
    const attempts = result.getByRole("table", { name: "Attempt spans" });
    await expect(attempts.getByRole("row")).toHaveCount(fixture.correlation.attempts.length + 1);
    const retry = fixture.correlation.attempts.find((attempt) => attempt.selected)!;
    const retryRow = attempts.getByRole("row").filter({ hasText: retry.span_id });
    await expect(retryRow).toContainText("retry");
    await expect(retryRow.getByRole("link")).toHaveAttribute("href", `/api/v1/manifests/${retry.manifest_sha256}`);

    await expect(result.getByRole("table", { name: "Artifact spans" }).getByRole("row"))
      .toHaveCount(fixture.correlation.artifacts.length + 1);
    await expect(result.getByRole("list", { name: "Aggregation members" }).getByRole("listitem"))
      .toHaveCount(fixture.correlation.aggregation.members.length);
    await expect(result.getByRole("list", { name: "Safeguard drops" })).toContainText("payload_excluded × 4");

    await expect(result).toContainText("one or more exports did not succeed");
    const failed = result.getByRole("table", { name: "Export diagnostics" }).getByRole("row")
      .filter({ hasText: "otlp_http" }).filter({ hasText: "failed" });
    await expect(failed).toHaveCount(1);
    await expect(failed).toContainText("HTTP 503 · http_status");
  });

  test("rejects reports that carry payload-shaped fields", async ({ page }) => {
    await read(page, { ...fixture, prompt: "SECRET" });
    await expect(panel(page).getByRole("alert")).toContainText("does not match the waza.execution-telemetry 1.0 schema");
    await expect(panel(page).getByRole("status")).toHaveCount(0);
  });

  test("rejects internally inconsistent counts", async ({ page }) => {
    const signals = { ...fixture.signals, traces: { ...fixture.signals.traces, emitted: fixture.signals.traces.emitted + 1 } };
    await read(page, { ...fixture, signals });
    await expect(panel(page).getByRole("alert")).toContainText("do not add up to the emitted count");
  });

  test("rejects resource identifiers that disagree with correlation", async ({ page }) => {
    await read(page, { ...fixture, resource: { ...fixture.resource, "waza.scheduler.work_id": "queue/other" } });
    await expect(panel(page).getByRole("alert")).toContainText("scheduler work ID does not match");
  });
});
