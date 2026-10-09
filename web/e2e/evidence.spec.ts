import { test, expect, type Page } from "@playwright/test";
import type { EvidenceRun } from "../src/api/client";
import { mockAllAPIs } from "./helpers/api-mock";
import { evidenceDetail, evidenceRun } from "./fixtures/evidence-data";

async function openEvidence(page: Page, runs?: EvidenceRun[]) {
  await mockAllAPIs(page);
  if (runs) {
    await page.route(/\/api\/runs\/run-001$/, (route) => route.fulfill({ json: evidenceDetail(runs) }));
  }
  await page.goto("/#/runs/run-001");
  await page.getByRole("button", { name: "Evidence", exact: true }).click();
  return page.getByTestId("evidence-view");
}

test("legacy results without evidence stay unassessed", async ({ page }) => {
  const view = await openEvidence(page);
  await expect(view.getByText(/Evidence unassessed:/)).toHaveCount(4);
  await expect(view).not.toContainText("Manifest 1.0");
  await expect(view).toContainText("not verified enforcement");
});

test("every trial retains partial mock evidence and cached source origin", async ({ page }) => {
  const requests: string[] = [];
  page.on("request", (request) => requests.push(new URL(request.url()).pathname));
  const view = await openEvidence(page, [evidenceRun(), evidenceRun(2), {
    runNumber: 3, attempts: 1, cached: false, assessment: "unassessed",
    message: "Historical absence is not success.",
  }]);
  await expect(view).toContainText("Run 1 · 2 attempts · recorded");
  await expect(view).toContainText("Run 2 · 2 attempts · recorded");
  await expect(view).toContainText("Run 3 · 1 attempts · unassessed");
  await expect(view.getByText(/cached source evidence/)).toHaveCount(2);
  await expect(view).toContainText("eval source-evaluation · task report-task · run 2 · attempt 2 · prior attempts not_preserved");
  await expect(view).toContainText("Actual execution mode: mock");
  await expect(view).toContainText("SDK: unavailable · Effective model: unavailable");
  await expect(view).toContainText("Requested no-skills: true");
  await expect(view).toContainText("Native skill control: mock_not_applicable");
  await expect(view).toContainText("not proof of SDK enforcement");
  await expect(view.getByRole("row", { name: /workspace-file\/report.txt/ }).first()).toContainText("complete");
  await expect(view.getByRole("row", { name: /^workspace unavailable/ }).first()).toContainText("partial");
  await expect(view).toContainText("Content SHA-256 (utf8)");
  await expect(view).toContainText("Source SHA-256 (json-v1)");
  await expect(view).toContainText("snapshot/toolEvents");
  await expect(view).toContainText("no source bytes");
  await expect(view).toContainText("normalization; this flag is not a secret-match count");
  await expect(view).toContainText("1 rule matches");
  await expect(view.getByRole("link")).toHaveCount(0);
  await expect(view.getByRole("button")).toHaveCount(0);
  expect(requests.filter((path) => /snapshot|report\.txt|upload|grade|replay/.test(path))).toEqual([]);
});

test("scoped checks and operational uncertainty do not invent a cause", async ({ page }) => {
  const view = await openEvidence(page, [evidenceRun()]);
  await expect(view).toContainText("task / report-check: grader_recorded_failure · operational_status_unavailable");
  await expect(view).toContainText("checkpoint / report-check / turn 2: unresolved · insufficient_evidence");
  await expect(view).toContainText("unknown / run_error");
  await expect(view).toContainText("no agent cause is inferred");
  await expect(view).toContainText("Evidence: tool-events/toolEvents/0 · eval source-evaluation");
});

test("unknown version values are unavailable rather than inferred", async ({ page }) => {
  const run = evidenceRun();
  run.manifest!.runtime.waza_version = null;
  run.manifest!.runtime.sdk_version = "unknown";
  run.manifest!.runtime.effective_model_version = "unknown";
  const view = await openEvidence(page, [run]);
  await expect(view).toContainText("waza: unavailable · SDK: unavailable · Effective model: unavailable");
});

test("overall operational errors remain distinct from a passing check", async ({ page }) => {
  const run = evidenceRun();
  run.explanations![0].checks[0] = {
    check: { scope: "eval", grader: "content" },
    observation: "grader_recorded_pass",
    category: "operational_error",
    message: "The run recorded an operational error; a check result alone cannot establish requirement satisfaction.",
  };
  run.manifest!.diagnostics![0].category = "timeout";
  const view = await openEvidence(page, [run]);
  await expect(view).toContainText("grader_recorded_pass · operational_error");
  await expect(view).toContainText("timeout / run_error");
  await expect(view).not.toContainText("agent caused");
});

for (const [native, noSkills] of [
  ["unknown", null], ["sdk_default", false], ["requested_sdk_disable", true],
] as const) {
  test(`requested native skill control ${native} is not enforcement`, async ({ page }) => {
    const run = evidenceRun();
    run.manifest!.runtime.native_skill_control = native;
    run.manifest!.runtime.no_skills = noSkills;
    const view = await openEvidence(page, [run]);
    await expect(view).toContainText(`Native skill control: ${native}`);
    await expect(view).toContainText(`Requested no-skills: ${noSkills === null ? "unknown" : String(noSkills)}`);
    await expect(view).toContainText("Verified enforcement: unknown");
  });
}

for (const invalid of ["api-invalid", "unknown-version", "unknown-metadata", "wrong-origin", "missing-content", "malformed-runtime", "unknown-encoding", "duplicate-artifact", "absent-complete"] as const) {
  test(`${invalid} metadata is withheld without rendering payload or references`, async ({ page }) => {
    const run = evidenceRun();
    run.manifest!.origin.eval_id = "DO-NOT-DISPLAY-ORIGIN";
    run.explanations![0].requirement_id = "DO-NOT-DISPLAY-REQUIREMENT";
    if (invalid === "api-invalid") run.assessment = "invalid";
    if (invalid === "unknown-version") run.manifest!.version = "2.0";
    if (invalid === "unknown-metadata") Object.assign(run.manifest!.runtime, { unexpected: "DO-NOT-DISPLAY-METADATA" });
    if (invalid === "wrong-origin") run.manifest!.origin.run_number = 10;
    if (invalid === "missing-content") delete run.manifest!.artifacts[0].content_digest;
    if (invalid === "malformed-runtime") Object.assign(run.manifest!, { runtime: null });
    if (invalid === "unknown-encoding") run.manifest!.artifacts[0].content_digest!.encoding = "unrecognized";
    if (invalid === "duplicate-artifact") run.manifest!.artifacts.push(run.manifest!.artifacts[0]);
    if (invalid === "absent-complete") run.manifest!.artifacts[3].completeness = "complete";
    const view = await openEvidence(page, [run]);
    await expect(view).toContainText("Evidence metadata withheld");
    await expect(view).not.toContainText("DO-NOT-DISPLAY");
    await expect(view.getByRole("table")).toHaveCount(0);
  });
}

test("server-withheld metadata never appears as recorded success", async ({ page }) => {
  const view = await openEvidence(page, [{
    runNumber: 1, attempts: 1, cached: false, assessment: "invalid",
    message: "Evidence identity or metadata is invalid; do not use it for state reconstruction.",
  }]);
  await expect(view).toContainText("1 attempts · invalid");
  await expect(view).toContainText("do not use it for state reconstruction");
  await expect(view).not.toContainText("Manifest");
});

test("regraded original capture digest and unavailable links stay explicit", async ({ page }) => {
  const run = evidenceRun();
  run.manifest!.source_manifest_sha256 = "e".repeat(64);
  run.manifest!.artifacts.push({
    id: "validations", kind: "grader_results", availability: "unavailable",
    completeness: "unknown", reason: "Original observations do not apply to new declarations.",
  });
  const view = await openEvidence(page, [run]);
  await expect(view).toContainText(`Original capture SHA-256: ${"e".repeat(64)}`);
  await expect(view).toContainText("Original observations do not apply to new declarations.");
});
