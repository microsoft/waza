import { expect, test, type Page } from "@playwright/test";
import { copyFile, readFile } from "node:fs/promises";
import fixtures from "./fixtures/checkpoint-operations.json" with { type: "json" };
import type { CheckpointOperationResult } from "../src/api/checkpointResults";

interface Fixture {
  path: string;
  request: { request: unknown };
  admission: unknown;
  http_status: number;
  response: string;
}

function panel(page: Page) {
  return page.getByRole("region", { name: "Checkpoint creation and recovery" });
}

function result(page: Page) {
  return panel(page).getByLabel("Checkpoint operation result", { exact: true });
}

function parsed(fixture: Fixture): CheckpointOperationResult {
  return JSON.parse(fixture.response);
}

function closeFailure(fixture: Fixture): Fixture {
  const copy = structuredClone(fixture);
  const operationError = parsed(copy);
  operationError.schema_version = "1.1";
  operationError.status = "operation_error";
  operationError.exit_code = 2;
  operationError.error_code = "checkpoint_storage_close_failed";
  operationError.compatibility_result = "unavailable";
  operationError.compatibility_differences = [];
  operationError.restored_units = null;
  operationError.remaining_work = null;
  operationError.diagnostics = [];
  copy.response = JSON.stringify(operationError);
  copy.http_status = 500;
  return copy;
}

async function prepare(page: Page, fixture: Fixture, response = fixture.response, status = fixture.http_status, exit?: string) {
  const methods: string[] = [];
  await page.route("**/api/v1/runner/validate", (route) => {
    expect(route.request().postDataJSON()).toEqual(fixture.request.request);
    return route.fulfill({ json: fixture.admission });
  });
  await page.route("**/api/v1/checkpoints**", (route) => {
    methods.push(route.request().method());
    expect(route.request().method()).toBe("POST");
    expect(new URL(route.request().url()).pathname).toBe(fixture.path);
    expect(route.request().postDataJSON()).toEqual(fixture.request);
    return route.fulfill({
      status, body: response, contentType: "application/json",
      headers: { "X-Waza-Exit-Code": exit ?? String(parsed(fixture).exit_code) },
    });
  });
  await panel(page).getByRole("textbox", { name: "Captured checkpoint submission (JSON)" }).fill(JSON.stringify(fixture.request, null, 2));
  if (fixture.path.endsWith("/resume")) {
    await panel(page).getByRole("textbox", { name: "Checkpoint digest to resume" }).fill(fixture.path.split("/")[4]!);
  }
  return methods;
}

async function run(page: Page, fixture: Fixture) {
  await panel(page).getByRole("button", {
    name: fixture.path.endsWith("/resume") ? "Resume checkpoint" : "Create checkpoints and run unit", exact: true,
  }).click();
}

test.describe("Protected checkpoint operations in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  for (const name of ["created", "unicode", "failed", "reused", "retried", "execution_error", "reused_error", "incompatible", "stale", "checkpoint_error", "policy_reordered"] as const) {
    test(`renders and downloads actual protected producer ${name} values`, async ({ page }) => {
      const fixture = fixtures[name];
      const methods = await prepare(page, fixture);
      await run(page, fixture);
      const value = parsed(fixture);
      const submission = value.submission;
      const terminal = submission?.terminal;
      await expect(result(page).locator("dd")).toHaveText([
        value.status, String(value.exit_code), value.source?.boundary ?? "Unavailable",
        value.source_withheld ? "Withheld - capture admission did not succeed"
          : value.source ? "Admitted original declaration" : "Unavailable",
        value.source?.created_at ?? "Unavailable", value.compatibility_result,
        value.compatibility_differences.join(", ") || "None reported",
        String(value.restored_units ?? "Unavailable"), String(value.remaining_work ?? "Unavailable"),
        String(submission?.engine_calls ?? "Unavailable"), terminal?.outcome ?? "Unavailable",
        String(terminal?.engine_calls ?? "Unavailable"), String(terminal?.selection?.attempt ?? "Unavailable"),
        submission?.reservation?.execution_id ?? "Unavailable", submission?.reservation?.parent?.execution_id ?? "Unavailable",
        submission?.reservation?.parent?.relationship ?? "Unavailable", submission?.decision?.policy ?? "Unavailable",
        submission?.decision?.source?.execution_id ?? "Unavailable",
        value.before_checkpoint_sha256 ?? "Unavailable", value.finalized_checkpoint_sha256 ?? "Unavailable",
      ]);
      const download = page.waitForEvent("download");
      await panel(page).getByRole("button", { name: "Download checkpoint result JSON" }).click();
      const file = await download;
      expect(file.suggestedFilename()).toBe("waza-checkpoint-result.json");
      const path = await file.path();
      expect(path).not.toBeNull();
      expect(await readFile(path!, "utf8")).toBe(fixture.response);
      expect(methods).toEqual(["POST"]);
      if (name === "reused") {
        expect(value.exit_code).toBe(1);
        expect(value.submission?.engine_calls).toBe(0);
        expect(value.submission?.terminal?.selection?.attempt).toBe(2);
      }
      if (name === "retried") {
        expect(value.source?.boundary).toBe("before-unit");
        expect(value.submission?.reservation?.parent?.execution_id).toBe(value.source?.execution_id);
        expect(value.submission?.reservation?.execution_id).not.toBe(value.source?.execution_id);
        expect(value.submission?.engine_calls).toBe(1);
        expect(value.submission?.terminal?.selection?.attempt).toBe(3);
      }
      if (name === "incompatible" || name === "stale") {
        expect(value.source).toBeUndefined();
        expect(value.restored_units).toBeNull();
        expect(value.remaining_work).toBeNull();
      }
    });
  }

  test("renders a retained finalized result when closing storage returns a v1.1 operation error", async ({ page }) => {
    const fixture = closeFailure(fixtures.reused);
    const methods = await prepare(page, fixture);
    await run(page, fixture);
    const rendered = result(page);
    await expect(rendered).toHaveAttribute("role", "alert");
    await expect(rendered).toContainText("Sanitized operation errorcheckpoint_storage_close_failed");
    await expect(rendered).toContainText("New engine calls0");
    await expect(rendered).toContainText("Original terminal outcome");
    expect(methods).toEqual(["POST"]);
  });

  for (const control of [
    { name: "caller identity", mutate: (value: CheckpointOperationResult) => {
      value.submission!.caller!.canonical_digest = "0".repeat(64);
    } },
    { name: "terminal identity", mutate: (value: CheckpointOperationResult) => {
      value.submission!.terminal!.identity!.canonical_digest = "0".repeat(64);
    } },
    { name: "completion reservation", mutate: (value: CheckpointOperationResult) => {
      value.submission!.completion!.reservation_digest = "0".repeat(64);
    } },
    { name: "decision caller", mutate: (value: CheckpointOperationResult) => {
      value.submission!.decision!.binding.identity.canonical_digest = "0".repeat(64);
    } },
  ]) {
    test(`rejects a v1.1 operation error with a mismatched ${control.name}`, async ({ page }) => {
      const fixture = closeFailure(fixtures.reused);
      const value = parsed(fixture);
      control.mutate(value);
      fixture.response = JSON.stringify(value);
      await prepare(page, fixture);
      await run(page, fixture);
      await expect(panel(page).getByRole("alert")).toBeVisible();
      await expect(result(page)).toHaveCount(0);
    });
  }

  for (const control of [
    { name: "raw report", fixture: "created", mutate: (value: CheckpointOperationResult) => Reflect.set(value.submission!, "report", fixtures.created.admission) },
    { name: "retagged original terminal", fixture: "reused", mutate: (value: CheckpointOperationResult) => { value.submission!.terminal!.selection!.attempt = 3; } },
    { name: "foreign source execution", fixture: "reused", mutate: (value: CheckpointOperationResult) => { value.source!.execution_id = parsed(fixtures.created).submission!.reservation!.execution_id; } },
    { name: "foreign source completion digest", fixture: "reused", mutate: (value: CheckpointOperationResult) => { value.source!.completion_digest = "0".repeat(64); } },
    { name: "foreign distinct execution decision", fixture: "created", mutate: (value: CheckpointOperationResult) => {
      value.submission!.decision!.execution_id = parsed(fixtures.failed).submission!.reservation!.execution_id;
    } },
    { name: "foreign decision caller", fixture: "created", mutate: (value: CheckpointOperationResult) => {
      value.submission!.decision!.binding.identity.canonical_digest = "0".repeat(64);
    } },
    { name: "paired fresh original attempt retagging", fixture: "created", mutate: (value: CheckpointOperationResult) => {
      const attempt = "attempt-" + "0".repeat(64);
      value.submission!.reservation!.binding.identity.attempt = attempt;
      value.submission!.terminal!.identity!.attempt = attempt;
    } },
    { name: "foreign reuse execution mode", fixture: "reused", mutate: (value: CheckpointOperationResult) => {
      value.submission!.decision!.binding.execution_mode = "copilot";
    } },
    ...(["reused", "reused_error"] as const).map((fixture) => ({
      name: `${fixture} decision selection attempt`, fixture, mutate: (value: CheckpointOperationResult) => {
        expect(value.submission!.decision!.binding.selection.attempt).toBe(3);
        expect(value.submission!.reservation!.binding.selection.attempt).toBe(2);
        value.submission!.decision!.binding.selection.attempt = 1;
      },
    })),
    { name: "contradictory observed grading status", fixture: "created", mutate: (value: CheckpointOperationResult) => {
      value.submission!.terminal!.observation!.validations[0]!.passed = false;
    } },
    { name: "invented restored zero", fixture: "incompatible", mutate: (value: CheckpointOperationResult) => { value.restored_units = 0; } },
    { name: "noncanonical original time", fixture: "reused", mutate: (value: CheckpointOperationResult) => { value.source!.created_at = "2026-11-07T16:00:00.000Z"; } },
    { name: "same digest at different boundaries", fixture: "created", mutate: (value: CheckpointOperationResult) => { value.finalized_checkpoint_sha256 = value.before_checkpoint_sha256; } },
    { name: "mismatched caller identity", fixture: "created", mutate: (value: CheckpointOperationResult) => { value.submission!.caller!.canonical_digest = "0".repeat(64); } },
    { name: "contradictory original exit", fixture: "created", mutate: (value: CheckpointOperationResult) => { value.exit_code = 1; value.submission!.exit_code = 1; } },
    { name: "contradictory new call count", fixture: "created", mutate: (value: CheckpointOperationResult) => { value.submission!.engine_calls = 0; } },
    { name: "unknown error grader", fixture: "execution_error", mutate: (value: CheckpointOperationResult) => {
      value.submission!.terminal!.observation!.validations = [{ identifier: "unadmitted", passed: false }];
    } },
    { name: "duplicate error grader", fixture: "execution_error", mutate: (value: CheckpointOperationResult) => {
      const verdict = { identifier: value.submission!.reservation!.binding.grader_ids[0]!, passed: false };
      value.submission!.terminal!.observation!.validations = [verdict, { ...verdict }];
    } },
    ...(["created", "checkpoint_error"] as const).flatMap((fixture) => [
      { name: `${fixture} foreign completion execution`, fixture, mutate: (value: CheckpointOperationResult) => {
        value.submission!.completion!.execution_id = parsed(fixtures.failed).submission!.reservation!.execution_id;
      } },
      { name: `${fixture} foreign completion reservation`, fixture, mutate: (value: CheckpointOperationResult) => {
        value.submission!.completion!.reservation_digest = "0".repeat(64);
      } },
    ]),
  ] as const) {
    test(`rejects ${control.name} without rendering or exporting success`, async ({ page }) => {
      const fixture = fixtures[control.fixture];
      const value = parsed(fixture);
      control.mutate(value);
      await prepare(page, fixture, JSON.stringify(value), fixture.http_status, String(value.exit_code));
      await run(page, fixture);
      await expect(panel(page).getByRole("alert")).toBeVisible();
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
    });
  }

  for (const name of ["reused", "reused_error", "retried"] as const) {
    test(`rejects ${name} consent-only request change with unchanged work identity`, async ({ page }) => {
      const fixture = structuredClone(fixtures[name]);
      expect(fixture.request.request.allow_paid_execution).toBe(false);
      fixture.request.request.allow_paid_execution = true;
      expect(fixture.request).not.toEqual(fixtures[name].request);
      expect(fixture.admission).toEqual(fixtures[name].admission);
      const methods = await prepare(page, fixture);
      await run(page, fixture);
      await expect(panel(page).getByRole("alert")).toContainText("source policy");
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
      expect(methods).toEqual(["POST"]);
    });
  }

  for (const field of ["versions", "required_capabilities", "optional_capabilities", "allow_paid_execution"] as const) {
    for (const presence of ["null", "omitted"] as const) {
      test(`rejects ${presence} ${field} despite contradictory ready admission`, async ({ page }) => {
        const fixture = structuredClone(fixtures.reused);
        if (presence === "null") Reflect.set(fixture.request.request, field, null);
        else expect(Reflect.deleteProperty(fixture.request.request, field)).toBe(true);
        expect(fixture.request).not.toEqual(fixtures.reused.request);
        const methods = await prepare(page, fixture);
        await run(page, fixture);
        await expect(panel(page).getByRole("alert")).toContainText("invalid negotiation policy");
        await expect(result(page)).toHaveCount(0);
        await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
        expect(methods).toEqual(["POST"]);
      });
    }
  }

  test("native reordered policy fixture changes every negotiation array but retains original failed work", async ({ page }) => {
    const fixture = fixtures.policy_reordered;
    const request = fixture.request.request;
    for (const values of [request.versions, request.required_capabilities, request.optional_capabilities]) {
      expect(values.length).toBeGreaterThan(1);
      expect(values).not.toEqual([...values].sort());
    }
    const value = parsed(fixture);
    expect(value.compatibility_result).toBe("compatible");
    expect(value.submission!.engine_calls).toBe(0);
    expect(value.submission!.reservation!.binding.selection.attempt).toBe(2);
    expect(value.submission!.decision!.binding.selection.attempt).toBe(3);
    expect(value.exit_code).toBe(1);
    await prepare(page, fixture);
    await run(page, fixture);
    await expect(result(page)).toBeVisible();
    const download = page.waitForEvent("download");
    await panel(page).getByRole("button", { name: "Download checkpoint result JSON" }).click();
    const path = await (await download).path();
    expect(path).not.toBeNull();
    expect(await readFile(path!, "utf8")).toBe(fixture.response);
  });

  for (const control of ["same-work checkpoint digest", "canonical source creation time"] as const) {
    test(`rejects changed ${control} before rendering or downloading`, async ({ page }) => {
      const fixture = { ...fixtures.reused };
      const value = parsed(fixture);
      let response = fixture.response;
      if (control === "same-work checkpoint digest") {
        const original = parsed(fixtures.failed);
        expect(original.submission?.reservation?.execution_id).toBe(value.source?.execution_id);
        expect(original.submission?.reservation?.logical.work).toBe(value.source?.compatibility.work);
        expect(original.before_checkpoint_sha256).toMatch(/^[a-f0-9]{64}$/);
        fixture.path = `/api/v1/checkpoints/${original.before_checkpoint_sha256}/resume`;
        expect(fixture.path).not.toBe(fixtures.reused.path);
      } else {
        const changed = "2010-01-02T03:04:05.123456789Z";
        expect(value.source!.created_at).not.toBe(changed);
        value.source!.created_at = changed;
        response = JSON.stringify(value);
      }
      const methods = await prepare(page, fixture, response);
      await run(page, fixture);
      await expect(panel(page).getByRole("alert")).toContainText("requested checkpoint digest");
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
      expect(methods).toEqual(["POST"]);
    });
  }

  for (const name of ["created", "checkpoint_error"] as const) {
    test(`rejects a source-less completed ${name} resume before rendering or downloading`, async ({ page }) => {
      const digest = "0".repeat(64);
      const fixture = { ...fixtures[name], path: `/api/v1/checkpoints/${digest}/resume` };
      const value = parsed(fixture);
      expect(value.submission?.status).toBe("completed");
      expect(value.source).toBeUndefined();
      expect(digest).not.toBe(value.before_checkpoint_sha256);
      expect(digest).not.toBe(value.finalized_checkpoint_sha256);
      const methods = await prepare(page, fixture);
      await run(page, fixture);
      await expect(panel(page).getByRole("alert")).toContainText("completed resume without its checkpoint source");
      await expect(result(page)).toHaveCount(0);
      await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
      expect(methods).toEqual(["POST"]);
    });
  }

  test("rejects resume-only state replayed as a create response", async ({ page }) => {
    const fixture = { ...fixtures.reused, path: "/api/v1/checkpoints" };
    expect(parsed(fixture).status).toBe("reused");
    const methods = await prepare(page, fixture);
    await run(page, fixture);
    await expect(panel(page).getByRole("alert")).toContainText("create response contains resume-only state");
    await expect(result(page)).toHaveCount(0);
    await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
    expect(methods).toEqual(["POST"]);
  });

  for (const name of ["reused", "retried"] as const) {
    test(`admits ${name} source with independently reordered declaration keys`, async ({ page }) => {
      const fixture = fixtures[name];
      const response = JSON.stringify(parsed(fixture), (_key, value: unknown) =>
        value !== null && typeof value === "object" && !Array.isArray(value)
          ? Object.fromEntries(Object.entries(value).reverse()) : value);
      expect(response).not.toBe(fixture.response);
      const methods = await prepare(page, fixture, response);
      await run(page, fixture);
      await expect(result(page)).toBeVisible();
      const download = page.waitForEvent("download");
      await panel(page).getByRole("button", { name: "Download checkpoint result JSON" }).click();
      const file = await download;
      const path = await file.path();
      expect(path).not.toBeNull();
      expect(await readFile(path!, "utf8")).toBe(response);
      expect(methods).toEqual(["POST"]);
    });
  }

  test("invalid digest never sends admission or operation requests", async ({ page }) => {
    let requests = 0;
    await page.route("**/api/v1/checkpoints**", () => { requests++; });
    await page.route("**/api/v1/runner/validate", () => { requests++; });
    for (const digest of ["", "A".repeat(64), "a".repeat(63), "a".repeat(64) + " "]) {
      await panel(page).getByRole("textbox", { name: "Checkpoint digest to resume" }).fill(digest);
      await panel(page).getByRole("button", { name: "Resume checkpoint", exact: true }).click();
      await expect(panel(page).getByRole("alert")).toContainText("full lowercase SHA-256");
    }
    expect(requests).toBe(0);
  });

  test("reload failure clears original success and withholds private malformed provider text", async ({ page }) => {
    const fixture = fixtures.created;
    await prepare(page, fixture);
    await run(page, fixture);
    await expect(result(page)).toBeVisible();
    await page.route("**/api/v1/checkpoints**", (route) => route.fulfill({
      status: 503, body: "https://private-provider.example/secret /private/operator/store",
    }));
    await run(page, fixture);
    await expect(panel(page).getByRole("alert")).toBeVisible();
    await expect(result(page)).toHaveCount(0);
    await expect(panel(page)).not.toContainText("private-provider");
    await expect(panel(page)).not.toContainText("/private/operator/store");
    await expect(panel(page).getByRole("button", { name: "Download checkpoint result JSON" })).toHaveCount(0);
  });

  test("editing a pending request aborts old admission without dispatching or resurrecting a result", async ({ page }) => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    let admissions = 0;
    let operations = 0;
    await page.route("**/api/v1/runner/validate", async (route) => {
      admissions++;
      await gate;
      await route.fulfill({ json: fixtures.created.admission });
    });
    await page.route("**/api/v1/checkpoints**", () => { operations++; });
    await run(page, fixtures.created);
    await expect.poll(() => admissions).toBe(1);
    await panel(page).getByRole("textbox", { name: "Captured checkpoint submission (JSON)" }).fill(JSON.stringify(fixtures.failed.request));
    release();
    await expect(panel(page).getByRole("button", { name: "Create checkpoints and run unit", exact: true })).toBeEnabled();
    await expect(result(page)).toHaveCount(0);
    expect(operations).toBe(0);
  });

  test("header and HTTP contradictions cannot become completed results", async ({ page }) => {
    for (const [status, exit] of [[200, "6"], [409, "0"]] as const) {
      await prepare(page, fixtures.created, fixtures.created.response, status, exit);
      await run(page, fixtures.created);
      await expect(panel(page).getByRole("alert")).toContainText("transport status contradicts");
      await expect(result(page)).toHaveCount(0);
    }
  });

  test("synthetic demo has no operation inputs or mutable requests", async ({ page }) => {
    const calls: string[] = [];
    page.on("request", (request) => {
      if (request.url().includes("/api/v1/checkpoints")) calls.push(request.method());
    });
    await page.goto("/#/lab/demo/runner");
    await expect(panel(page)).toContainText("unavailable in the synthetic demo");
    await expect(panel(page).getByRole("textbox")).toHaveCount(0);
    await expect(panel(page).getByRole("button")).toHaveCount(0);
    expect(calls).toEqual([]);
  });

  test("records the producer-backed failed-original reuse screenshot", async ({ page }) => {
    await prepare(page, fixtures.reused);
    await run(page, fixtures.reused);
    await expect(result(page)).toContainText("evaluation_failure");
    const path = "../docs/images/dashboard-lab-checkpoint.png";
    await panel(page).screenshot({ path, animations: "disabled" });
    await copyFile(path, "../site/public/images/dashboard-lab-checkpoint.png");
  });
});
