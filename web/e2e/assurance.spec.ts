import { test, expect } from "@playwright/test";
import path from "node:path";
import { mockAllAPIs } from "./helpers/api-mock";

function suppliedReport(state = "failed", credits: number | null = null) {
  return {
    kind: "waza.grader-assurance", schema_version: "1.0",
    created_at: "2026-10-09T12:00:00Z", state, reason: "critical_false_acceptance",
    labels_sha256: "a".repeat(64), cache_policy: "no_reuse", limitations: ["Synthetic UI test report; no actual review or paid calls."],
    review: { source_id: "synthetic", current_source_accepted: false, declared_state: "unreviewed", eligible: false },
    bindings: [{ domain: "eval_source_bytes", applicable: true, state: "missing", expected: null, actual: null }],
    calibration: {
      state: "not_assessed", reason: "calibration_not_selected", max_judge_executions: 0, executions: 0,
      billable_calls_bounded: false, cost_bounded: false,
      samples: 0, confidence_supported: false, usage: null, credits,
    },
    domains: [{
      id: "cli", minimum_cases: 4, minimum_agreement: 1, declared_cases: 4, observed_cases: 1,
      expected_checks: 4, observed_checks: 1, agreements: 0, agreement: 0, state: "insufficient_evidence",
    }],
    requirements: [{
      task_id: "task", requirement_id: "state", check: { scope: "eval", grader: "check" },
      state: "failed", reason: "critical_false_acceptance",
      declared_coverage: { good: 1, alternative_valid: 1, critical_bad: 2, intended_negative: 2 },
      observed_coverage: { good: 0, alternative_valid: 0, critical_bad: 1, intended_negative: 1 },
      observations: [{
        case_id: "bad", scenario_id: "target", domain: "cli", classification: "critical_bad",
        source_scope: "authored_finite_output", expected_passed: false, state: "observed",
        reason: "label_disagreement", agreement: false, bindings: null,
        result: { identifier: "check", type: "text", weight: 0, duration_ms: 0, passed: true, score: 1, feedback: "Native callback accepted the candidate." },
        evidence: [{
          artifact_id: "authored-output", pointer: "/output",
          origin: { eval_id: "reference:labels", task_id: "task", run_number: 1, attempt_count: 1, prior_attempts: "none" },
        }],
      }],
    }],
  };
}

test("historical passes remain explicitly not assessed", async ({ page }) => {
  await mockAllAPIs(page);
  await page.goto("/#/runs/run-001");
  await expect(page.getByTestId("assurance-not-assessed")).toContainText("not assessed");
  await page.getByRole("link", { name: "Inspect a separate supplied assurance report" }).click();
  await expect(page.getByRole("heading", { name: "Grader assurance", exact: true })).toBeVisible();
  await expect(page.getByText(/no assurance report selected/)).toBeVisible();
});

for (const state of ["passed", "failed", "not_assessed", "insufficient_evidence", "operational_error", "invalid"]) {
  test(`local supplied ${state} report preserves distinct claims and unavailable accounting`, async ({ page }) => {
    const requests: string[] = [];
    page.on("request", (request) => {
      if (request.method() !== "GET") requests.push(request.url());
    });
    await page.goto("/#/assurance");
    await page.getByLabel("Supplied assurance report").setInputFiles({
      name: "assurance.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(suppliedReport(state))),
    });
    await expect(page.getByRole("heading", { name: `Supplied assessment: ${state}`, exact: true })).toBeVisible();
    await expect(page.getByText(/Supplied observed credits: unavailable/)).toBeVisible();
    await expect(page.getByText(/Eligibility is not authenticated human identity/)).toBeVisible();
    await page.getByRole("link", { name: "task/state/eval/check", exact: true }).click();
    await page.getByText("bad (critical_bad): observed; agreement false", { exact: true }).click();
    await expect(page.getByText(/actual native pass: true/)).toBeVisible();
    await expect(page.getByText(/authored-output\/output/)).toBeVisible();
    await expect(page.getByText(/authored_finite_output; scenario/)).toBeVisible();
    expect(requests).toEqual([]);
    if (state === "failed") {
      await page.screenshot({ path: "../docs/images/assurance-report.png", fullPage: true });
    }
  });
}

test("reported zero is distinct from unavailable and invalid imports clear stale reports", async ({ page }) => {
  await page.goto("/#/assurance");
  const input = page.getByLabel("Supplied assurance report");
  await input.setInputFiles({ name: "zero.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(suppliedReport("failed", 0))) });
  await expect(page.getByText(/Supplied observed credits: 0.00/)).toBeVisible();
  for (const value of ["{", JSON.stringify({ kind: "waza.grader-assurance", schema_version: "99" }), JSON.stringify({
    ...suppliedReport(), calibration: { state: "not_assessed", reason: "missing accounting" },
  }), JSON.stringify({ ...suppliedReport(), unknown: true })]) {
    await input.setInputFiles({ name: "invalid.json", mimeType: "application/json", buffer: Buffer.from(value) });
    await expect(page.getByRole("alert")).toContainText("Cannot inspect");
    await expect(page.getByRole("heading", { name: /Supplied assessment/ })).toHaveCount(0);
  }
});

test("ambiguous raw JSON, invalid UTF8, aliases, depth and invalid claimed digests are rejected", async ({ page }) => {
  await page.goto("/#/assurance");
  const valid = JSON.stringify(suppliedReport());
  const invalid = [
    valid.replace('"state":"failed"', '"state":"failed","state":"passed"'),
    valid.replace('"state":"failed"', '"state":"failed","st\\u0061te":"passed"'),
    valid.replace('"state":"failed"', '"state":"failed","State":"passed"'),
    valid.replace('"schema_version":"1.0"', '"schema_version":"99"'),
    valid.replace('"labels_sha256":"' + "a".repeat(64) + '"', '"labels_sha256":"tampered"'),
    valid.replace('"state":"failed"', '"state":"claimed-trusted"'),
    valid.replace('"usage":null', '"usage":' + "[".repeat(66) + "0" + "]".repeat(66)),
    valid + " {}",
    valid.replace('"source_id":"synthetic"', '"source_id":"\\ud800"'),
  ].map((text) => Buffer.from(text));
  invalid.push(Buffer.concat([Buffer.from(valid), Buffer.from([0xff])]));
  for (const buffer of invalid) {
    await page.getByLabel("Supplied assurance report").setInputFiles({ name: "invalid.json", mimeType: "application/json", buffer });
    await expect(page.getByRole("alert")).toContainText("Cannot inspect");
    await expect(page.getByRole("heading", { name: /Supplied assessment/ })).toHaveCount(0);
  }
});

test("overflowing numeric claims anywhere reject and clear a displayed report", async ({ page }) => {
  await page.goto("/#/assurance");
  const valid = JSON.stringify(suppliedReport("failed", 0));
  for (const invalid of [
    valid.replace('"credits":0', '"credits":1e400'),
    valid.replace('"executions":0', '"executions":1e400'),
    valid.replace('"usage":null', '"usage":{"arbitrary":-1e400}'),
    valid.replace('"score":1', '"score":1,"details":{"arbitrary":1e400}'),
  ]) {
    const input = page.getByLabel("Supplied assurance report");
    await input.setInputFiles({ name: "valid.json", mimeType: "application/json", buffer: Buffer.from(valid) });
    await expect(page.getByRole("heading", { name: "Supplied assessment: failed", exact: true })).toBeVisible();
    await input.setInputFiles({ name: "overflow.json", mimeType: "application/json", buffer: Buffer.from(invalid) });
    await expect(page.getByRole("alert")).toContainText("Cannot inspect");
    await expect(page.getByRole("heading", { name: /Supplied assessment/ })).toHaveCount(0);
  }
});

function suppliedCalibratedReport() {
  const report = suppliedReport("operational_error");
  return {
    ...report, schema_version: "1.1", assessment_mode: "independent_authored_rubric_calibration",
    calibration: {
      ...report.calibration, state: "operational_error", reason: "synthetic_initialize_failure",
      protocol: "fixed_corpus_agreement_v1", model: "synthetic-model", max_judge_executions: 1,
      execution_ledger: [{
        case_id: "bad", task_id: "task", requirement_id: "state", check: { scope: "eval", grader: "check" },
        initialized: false, executed: false, callbacks: 0, requested_model: "synthetic-model",
        event_models: null, accounting_models: null, event_model_attribution_complete: false,
        accounting_model_attribution_complete: false, usage_source: "", usage_complete: false,
        state: "operational_error", reason: "synthetic_initialize_failure",
        diagnostics: [{ stage: "initialize", code: "start_failed" }], usage: null, credits: null,
      }],
    },
  };
}

test("calibrated claims require explicit version selection and expose unavailable lifecycle evidence", async ({ page }) => {
  const requests: string[] = [];
  page.on("request", (request) => { if (request.method() !== "GET") requests.push(request.url()); });
  await page.goto("/#/assurance");
  const input = page.getByLabel("Supplied assurance report");
  const report = suppliedCalibratedReport();
  await input.setInputFiles({ name: "calibrated.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(report)) });
  await expect(page.getByRole("alert")).toContainText("Cannot inspect");
  await page.getByRole("checkbox", { name: /Explicitly inspect calibrated report 1.1/ }).check();
  await expect(input).toHaveValue("");
  await input.setInputFiles({ name: "calibrated.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(report)) });
  await expect(page.getByText(/Report version: 1.1/)).toBeVisible();
  await page.getByText("bad/task/state: operational_error", { exact: true }).click();
  await expect(page.getByText(/Initialized: false; executed: false; native callbacks: 0/)).toBeVisible();
  await expect(page.getByText(/Received event models: unavailable/)).toBeVisible();
  await expect(page.getByText(/Accounting models: unavailable/)).toBeVisible();
  await expect(page.getByText(/Sanitized diagnostics: initialize\/start_failed/)).toBeVisible();
  await page.screenshot({ path: "../docs/images/assurance-calibrated-report.png", fullPage: true });
  await page.getByRole("checkbox", { name: /Explicitly inspect calibrated report 1.1/ }).uncheck();
  await expect(input).toHaveValue("");
  await expect(page.getByRole("heading", { name: /Supplied assessment/ })).toHaveCount(0);
  expect(requests).toEqual([]);
});

test("selected calibrated claims reject incomplete ledgers and do not reinterpret offline versions", async ({ page }) => {
  await page.goto("/#/assurance");
  await page.getByRole("checkbox", { name: /Explicitly inspect calibrated report 1.1/ }).check();
  const valid = JSON.stringify(suppliedCalibratedReport());
  const invalid = [
    valid.replace('"execution_ledger":[', '"unknown_ledger":['),
    valid.replace('"execution_ledger":[', '"execution_ledger":null,"unknown_ledger":['),
    valid.replace('"schema_version":"1.1"', '"schema_version":"1.0"'),
    valid.replace('"schema_version":"1.1"', '"schema_version":"1.2"'),
    valid.replace('"callbacks":0', '"callbacks":1'),
    valid.replace('"credits":null', '"credits":1e400'),
    valid.replace('"initialized":false', '"initialized":true,"initialized":false'),
    valid.replace('"event_model_attribution_complete":false', '"event_model_attribution_complete":true'),
  ];
  for (const text of invalid) {
    await page.getByLabel("Supplied assurance report").setInputFiles({
      name: "invalid-ledger.json", mimeType: "application/json", buffer: Buffer.from(text),
    });
    await expect(page.getByRole("alert")).toContainText("Cannot inspect");
    await expect(page.getByRole("heading", { name: /Supplied assessment/ })).toHaveCount(0);
  }
  const report = suppliedCalibratedReport();
  report.calibration.execution_ledger = [];
  await page.getByLabel("Supplied assurance report").setInputFiles({
    name: "empty-ledger.json", mimeType: "application/json", buffer: Buffer.from(JSON.stringify(report)),
  });
  await expect(page.getByText("No judge execution evidence supplied.")).toBeVisible();
});

test("actual offline producer outputs retain version boundaries and operational failures", async ({ page }) => {
  const directory = process.env.WAZA_CALIBRATION_REPORT_DIR;
  test.skip(!directory, "Requires actual deterministic Calibrate and Verify export gate.");
  if (!directory) return;
  await page.goto("/#/assurance");
  const input = page.getByLabel("Supplied assurance report");
  await input.setInputFiles(path.join(directory, "verify-control-1.0.json"));
  await expect(page.getByRole("heading", { name: "Supplied assessment: passed", exact: true })).toBeVisible();
  await expect(page.getByText(/Report version: 1.0/)).toBeVisible();
  await input.setInputFiles(path.join(directory, "positive-calibrated-1.1.json"));
  await expect(page.getByRole("alert")).toContainText("Cannot inspect");
  await page.getByRole("checkbox", { name: /Explicitly inspect calibrated report 1.1/ }).check();
  await input.setInputFiles(path.join(directory, "positive-calibrated-1.1.json"));
  await expect(page.getByRole("heading", { name: "Supplied assessment: passed", exact: true })).toBeVisible();
  await expect(page.getByText(/Judge executions: 4\/4/)).toBeVisible();
  await expect(page.getByText(/Supplied observed credits: 0.00/)).toBeVisible();
  await input.setInputFiles(path.join(directory, "operational-failure-calibrated-1.1.json"));
  await expect(page.getByRole("heading", { name: "Supplied assessment: operational_error", exact: true })).toBeVisible();
  await expect(page.getByText(/Supplied observed credits: 2.50/)).toBeVisible();
  await expect(page.getByRole("region", { name: "Supplied judge execution ledger" })).toBeVisible();
});
