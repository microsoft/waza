import { test, expect, type Page } from "@playwright/test";
import { inspectRunnerResult, parseRunnerResults, type RunnerOutcome } from "../src/api/runner";
import { runnerCollection, runnerResult } from "./fixtures/runner-data";
import identity from "../../internal/runnercontract/testdata/identity.json" with { type: "json" };

const outcomes: RunnerOutcome[] = ["success", "evaluation_failure", "infrastructure_failure",
  "canceled", "timeout", "unsupported_capability", "invalid_request"];

function contradictions(outcome: RunnerOutcome): unknown[] {
  const result = runnerResult(outcome);
  const partial = { ...result } as Partial<typeof result>;
  delete partial.outcome;
  const cases: unknown[] = [
    partial,
    { ...result, exit_code: (result.exit_code + 1) % outcomes.length },
    { ...result, failure_boundary: outcome === "success" ? "execution" : "none" },
  ];
  if (outcome !== "success") cases.push({ ...result, diagnostics: [] });
  if (result.selection_valid) {
    cases.push(
      { ...result, resolved: { ...result.resolved, trial: 1 } },
      { ...result, observation: { ...result.observation, attempts: 1 } },
      { ...result, observation: { ...result.observation, run_number: 1 } },
    );
  } else {
    cases.push(
      { ...result, observation: runnerResult().observation },
      { ...runnerResult(), outcome, exit_code: result.exit_code, failure_boundary: result.failure_boundary },
    );
  }
  if (outcome === "evaluation_failure") {
    for (const change of [
      { status: "passed" }, { status: "error" }, { status: "skipped" },
      { error_msg: "runtime failed, not grading" }, { validations: null }, { validations: {} },
      { validations: runnerResult().observation!.validations },
    ]) cases.push({ ...result, observation: { ...result.observation, ...change } });
    const absent = { ...result };
    delete absent.observation;
    cases.push(absent, { ...result, failure_boundary: "execution" });
  }
  return cases;
}

async function archive(page: Page, value: unknown = runnerCollection()) {
  await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
  await page.route("**/api/v1/lab/runner-results", (route) => route.fulfill({ json: value }));
}

test.describe("Runner result contracts", () => {
  test("accepts the CLI-produced result and the frozen backend identity vector without mutation", () => {
    const result = runnerResult();
    const original = JSON.stringify(result);
    expect(result.identity).toEqual(identity);
    expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
    expect(JSON.stringify(result)).toBe(original);
  });

  for (const outcome of outcomes) {
    test(`validates ${outcome} without coercing missing or contradictory evidence`, () => {
      const result = runnerResult(outcome);
      const original = JSON.stringify(result);
      expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
      for (const key of ["kind", "schema_version", "outcome", "exit_code", "failure_boundary",
        "negotiation", "selection_valid", "adjacent_work_selected", "diagnostics", "envelope_valid"]) {
        const missing = { ...result } as unknown as Record<string, unknown>;
        delete missing[key];
        expect(inspectRunnerResult(missing).state, `${outcome}.${key}`).toBe("malformed");
      }
      for (const value of contradictions(outcome)) {
        expect(inspectRunnerResult(value).state, JSON.stringify(value)).toBe("malformed");
      }
      expect(JSON.stringify(result)).toBe(original);
    });
  }

  test("checks every outcome against every boundary without inventing evidence", () => {
    const allowed = {
      success: ["none"], evaluation_failure: ["grading"],
      infrastructure_failure: ["execution", "grading", "shutdown", "terminalization"],
      canceled: ["execution"], timeout: ["execution"],
      unsupported_capability: ["negotiation"], invalid_request: ["admission", "selection"],
    };
    for (const outcome of outcomes) {
      for (const failure_boundary of ["none", "admission", "negotiation", "selection",
        "execution", "grading", "shutdown", "terminalization"] as const) {
        const result = { ...runnerResult(outcome), failure_boundary };
        expect(inspectRunnerResult(result).state, `${outcome}/${failure_boundary}`)
          .toBe(allowed[outcome].includes(failure_boundary) ? "recorded" : "malformed");
      }
      if (outcome !== "success" && outcome !== "evaluation_failure") {
        const result = runnerResult(outcome);
        delete result.observation;
        expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
      }
    }
  });

  test("retains passing or failed observations when a later boundary fails, without inferring success", () => {
    for (const failure_boundary of ["shutdown", "terminalization"] as const) {
      for (const graded of ["success", "evaluation_failure"] as const) {
        const result = {
          ...runnerResult(graded), outcome: "infrastructure_failure" as const, exit_code: 2, failure_boundary,
          diagnostics: [{ code: "late_failure", message: "Failed after grading." }],
        };
        expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
      }
    }
    for (const outcome of ["canceled", "timeout"] as const) {
      const result = runnerResult(outcome);
      result.observation = runnerResult().observation;
      expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
      // Interruption can also occur before admission has derived any identity.
      result.selection_valid = false;
      delete result.requested;
      delete result.resolved;
      delete result.identity;
      delete result.observation;
      expect(inspectRunnerResult(result)).toEqual({ state: "recorded", result });
    }
    const terminalization = runnerResult("infrastructure_failure");
    terminalization.failure_boundary = "terminalization";
    terminalization.selection_valid = false;
    delete terminalization.requested;
    delete terminalization.resolved;
    delete terminalization.identity;
    delete terminalization.observation;
    expect(inspectRunnerResult(terminalization).state).toBe("recorded");
    terminalization.failure_boundary = "shutdown";
    expect(inspectRunnerResult(terminalization).state).toBe("malformed");
  });

  test("rejects every missing required envelope and nested contract field", () => {
    for (const key of ["kind", "schema_version", "outcome", "exit_code", "failure_boundary",
      "negotiation", "selection_valid", "adjacent_work_selected", "diagnostics", "envelope_valid",
      "requested", "resolved", "identity", "observation"]) {
      const result: Record<string, unknown> = { ...structuredClone(runnerResult()) };
      delete result[key];
      expect(inspectRunnerResult(result).state, key).toBe("malformed");
    }
    for (const parent of ["negotiation", "requested", "resolved", "identity"] as const) {
      const source = runnerResult()[parent]!;
      for (const key of Object.keys(source)) {
        if (parent === "negotiation" && key === "version") continue;
        const result = runnerResult();
        delete (result[parent] as unknown as Record<string, unknown>)[key];
        expect(inspectRunnerResult(result).state, `${parent}.${key}`).toBe("malformed");
      }
    }
  });

  test("rejects null, wrong types, inconsistent tuples, IDs, exits and incomplete passing observations", () => {
    const cases: unknown[] = [null, [], {}, "success"];
    const changes: Record<string, unknown>[] = [
      { outcome: "passed" }, { schema_version: null }, { exit_code: "0" }, { exit_code: 2 },
      { envelope_valid: false }, { adjacent_work_selected: true }, { selection_valid: false },
      { diagnostics: [null] }, { diagnostics: [{ code: "", message: "" }] },
      { requested: null }, { identity: null }, { observation: null },
      { negotiation: { ...runnerResult().negotiation, required_capabilities: null } },
      { negotiation: { ...runnerResult().negotiation, selected_capabilities: ["exact-unit", "exact-unit"] } },
      { requested: { ...runnerResult().requested, task: "another" } },
      { requested: { ...runnerResult().requested, trial: 0 } },
      { requested: { ...runnerResult().requested, attempt: 1.5 } },
      { identity: { ...identity, canonical_digest: "not-a-digest" } },
      { identity: { ...identity, run: identity.attempt } },
      { observation: { ...runnerResult().observation, run_number: 1 } },
      { observation: { ...runnerResult().observation, attempts: 1 } },
      { observation: { ...runnerResult().observation, status: "failed" } },
      { observation: { ...runnerResult().observation, error_msg: "execution failed" } },
      { observation: { ...runnerResult().observation, validations: null } },
      { observation: { ...runnerResult().observation, validations: {} } },
      { observation: { ...runnerResult().observation, validations: { check: { passed: false } } } },
      { observation: { ...runnerResult().observation, validations: { check: null } } },
      { unexpected: "extension" },
    ];
    cases.push(...changes.map((change) => ({ ...runnerResult(), ...change })));
    for (const value of cases) expect(inspectRunnerResult(value).state, JSON.stringify(value)).toBe("malformed");
  });

  test("distinguishes unsupported versions, optional unavailable fields and malformed collections", () => {
    expect(inspectRunnerResult({ ...runnerResult(), schema_version: "2.0" }).state).toBe("unsupported");
    const rejected = runnerResult("invalid_request");
    delete rejected.requested;
    delete rejected.negotiation.version;
    expect(inspectRunnerResult(rejected).state).toBe("recorded");
    for (const value of [null, [], {}, { results: [] }, { schema_version: "1.0", results: null }]) {
      expect(() => parseRunnerResults(value)).toThrow(/Malformed/);
    }
    expect(() => parseRunnerResults({ schema_version: "2.0", results: [] })).toThrow(/Unsupported/);
  });
});

test.describe("Saved Lab runner results", () => {
  test("is opt-in, separate from ordinary history and never requested or synthesized in demo mode", async ({ page }) => {
    let requests = 0;
    await archive(page);
    await page.route("**/api/v1/lab/runner-results", (route) => {
      requests++;
      return route.fulfill({ json: runnerCollection() });
    });
    await page.goto("/#/lab/demo/history");
    await expect(page.getByRole("heading", { name: "Run history", exact: true })).toBeVisible();
    await page.getByRole("link", { name: "Runner results", exact: true }).click();
    await expect(page.getByRole("status")).toContainText("unavailable in the synthetic demo");
    expect(requests).toBe(0);
    await page.getByRole("link", { name: "Open saved runner results" }).click();
    await expect(page.getByRole("article")).toHaveCount(1);
    expect(requests).toBe(1);
    await page.getByRole("link", { name: "Run history", exact: true }).click();
    await expect(page.getByRole("table").first().getByRole("row")).toHaveCount(1);
    await expect(page.getByRole("main")).not.toContainText("run-736b");
  });

  test("renders supplied negotiation, tuples, validation, identity and linked diagnostic evidence", async ({ page }) => {
    const result = runnerResult("timeout");
    result.negotiation.requested_versions = ["2.0", "1.0"];
    result.negotiation.optional_capabilities = ["future"];
    result.negotiation.unavailable_optional_capabilities = ["future"];
    await archive(page, runnerCollection([result]));
    await page.goto("/#/lab/runner");
    const article = page.getByRole("article");
    await expect(page.getByText("Runner results refresh on demand", { exact: true })).toBeVisible();
    await expect(article).toContainText("timeout");
    await expect(article.getByRole("region", { name: "Runner / API negotiation" })).toContainText("2.0");
    for (const values of [result.negotiation.available_capabilities, result.negotiation.selected_capabilities]) {
      for (const value of values) await expect(article.getByText(value, { exact: true }).first()).toBeVisible();
    }
    await expect(article).toContainText("future");
    await expect(article).toContainText("false — no adjacent work selected");
    for (const heading of ["Requested tuple", "Resolved tuple"]) {
      const tuple = article.locator("div", { has: page.getByRole("heading", { name: heading, exact: true }) }).last();
      await expect(tuple).toContainText("shard-a");
      await expect(tuple).toContainText("selected");
      await expect(tuple.locator("dd")).toHaveText(["shard-a", "selected", "2", "2"]);
    }
    await expect(article.getByRole("list", { name: "Logical identity lineage" }).getByRole("listitem")).toHaveCount(5);
    for (const value of Object.values(identity)) await expect(article.getByText(value, { exact: true })).toBeVisible();
    const diagnosticLink = article.getByRole("link", { name: "Diagnostics (1)" });
    await diagnosticLink.focus();
    await page.keyboard.press("Enter");
    await expect(article.getByRole("region", { name: "Linked diagnostics for result 1" })).toBeFocused();
    await expect(page).toHaveURL(/#\/lab\/runner$/);
    await expect(article).toContainText("Recorded timeout diagnostic.");
  });

  for (const [exit, outcome] of outcomes.entries()) {
    test(`displays terminal ${outcome} and exit ${exit} without deriving it from HTTP 200 or passed observation`, async ({ page }) => {
      const result = runnerResult(outcome);
      await archive(page, runnerCollection([result]));
      await page.goto("/#/lab/runner");
      const article = page.getByRole("article");
      await expect(article.getByRole("heading", { name: `Result 1: ${outcome}`, exact: true })).toBeVisible();
      await expect(article.locator("dt", { hasText: "Stable exit category" }).locator("+ dd")).toHaveText(String(exit));
      await expect(article.locator("dt", { hasText: "Failure boundary" }).locator("+ dd")).toHaveText(result.failure_boundary);
      if (outcome !== "success") await expect(article.getByRole("heading", { name: "Result 1: success", exact: true })).toHaveCount(0);
      if (!result.selection_valid) {
        await expect(article).toContainText("Unavailable — no tuple recorded.");
        await expect(article).toContainText("Unavailable — no identity derived.");
        await expect(article.locator("dt", { hasText: "Pre-execution validation" }).locator("+ dd")).toHaveText("false — not validated");
      }
      if (outcome === "unsupported_capability") {
        await expect(article).toContainText("Required capability future-required is unavailable.");
      }
    });

    test(`withholds missing and contradictory ${outcome} rows instead of reinterpreting them`, async ({ page }) => {
      const invalid = contradictions(outcome);
      await archive(page, runnerCollection([...invalid, runnerResult(outcome)]));
      await page.goto("/#/lab/runner");
      await expect(page.getByRole("alert")).toHaveCount(invalid.length);
      await expect(page.getByRole("heading", { name: /: Malformed envelope$/ })).toHaveCount(invalid.length);
      await expect(page.getByRole("heading", {
        name: `Result ${invalid.length + 1}: ${outcome}`, exact: true,
      })).toBeVisible();
      if (outcome !== "success") {
        await expect(page.getByRole("heading", { name: /: success$/ })).toHaveCount(0);
      }
    });
  }

  test("shutdown and publication failures display retained passing evidence as failures", async ({ page }) => {
    const results = (["shutdown", "terminalization"] as const).map((failure_boundary) => ({
      ...runnerResult(), outcome: "infrastructure_failure", exit_code: 2, failure_boundary,
      diagnostics: [{ code: "late_failure", message: "Failed after passing checks." }],
    }));
    await archive(page, runnerCollection(results));
    await page.goto("/#/lab/runner");
    await expect(page.getByRole("heading", { name: /: infrastructure_failure$/ })).toHaveCount(2);
    await expect(page.getByRole("heading", { name: /: success$/ })).toHaveCount(0);
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("loading and empty results are explicit, not success", async ({ page }) => {
    let release!: () => void;
    const waiting = new Promise<void>((resolve) => { release = resolve; });
    await archive(page);
    await page.route("**/api/v1/lab/runner-results", async (route) => {
      await waiting;
      return route.fulfill({ json: runnerCollection([]) });
    });
    await page.goto("/#/lab/runner");
    await expect(page.getByRole("status")).toHaveText("Loading saved runner envelopes...");
    await expect(page.getByRole("button", { name: "Loading runner results..." })).toBeDisabled();
    await expect(page.getByRole("article")).toHaveCount(0);
    release();
    await expect(page.getByRole("heading", { name: "No saved runner results" })).toBeVisible();
    await expect(page.getByRole("main")).toContainText("unassessed, not success");
  });

  test("failed refresh hides prior success, retains transport error and supports keyboard retry", async ({ page }) => {
    await archive(page);
    await page.goto("/#/lab/runner");
    await expect(page.getByRole("article")).toHaveCount(1);
    await page.route("**/api/v1/lab/runner-results", (route) => route.fulfill({ status: 500, json: { error: "bad artifact" } }));
    await page.getByRole("button", { name: "Refresh runner results" }).click();
    await expect(page.getByRole("alert")).toContainText("500");
    await expect(page.getByRole("article")).toHaveCount(0);
    await expect(page.getByRole("alert")).toContainText("No cached success or synthetic fallback");
    await archive(page, runnerCollection([runnerResult("canceled")]));
    await page.getByRole("button", { name: "Refresh runner results" }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("heading", { name: "Result 1: canceled" })).toBeVisible();
  });

  for (const [name, value] of [
    ["unsupported API", { schema_version: "2.0", results: [] }],
    ["missing fields", {}],
    ["null results", { schema_version: "1.0", results: null }],
  ] as const) {
    test(`${name} is unavailable, not an empty archive`, async ({ page }) => {
      await archive(page, value);
      await page.goto("/#/lab/runner");
      await expect(page.getByRole("alert")).toBeVisible();
      await expect(page.getByRole("heading", { name: "No saved runner results" })).toHaveCount(0);
      await expect(page.getByRole("article")).toHaveCount(0);
    });
  }

  for (const body of ["not json", '{"schema_version":"1.0","results":[],"results":[]}',
    '{"schema_version":"1.0","results":[{"outcome":"success","outcome":"timeout"}]}']) {
    test(`rejects invalid or ambiguous transport JSON: ${body.slice(0, 40)}`, async ({ page }) => {
      await archive(page);
      await page.route("**/api/v1/lab/runner-results", (route) => route.fulfill({ body, contentType: "application/json" }));
      await page.goto("/#/lab/runner");
      await expect(page.getByRole("alert")).toContainText("invalid or ambiguous JSON");
    });
  }

  test("malformed and unsupported rows are withheld without hiding valid siblings", async ({ page }) => {
    const malformed = runnerResult() as Partial<ReturnType<typeof runnerResult>>;
    delete malformed.outcome;
    await archive(page, runnerCollection([null, malformed, { ...runnerResult(), schema_version: "2.0" }, runnerResult("timeout")]));
    await page.goto("/#/lab/runner");
    await expect(page.getByRole("article")).toHaveCount(4);
    await expect(page.getByRole("alert")).toHaveCount(3);
    await expect(page.getByRole("heading", { name: "Result 4: timeout" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Result 3: Unsupported envelope" })).toBeVisible();
    await expect(page.getByRole("heading", { name: "Result 2: Malformed envelope" })).toBeVisible();
    await expect(page.getByRole("main")).not.toContainText("Result 2: success");
  });

  test("absent rejected fields remain unavailable and diagnostics render as text, not markup", async ({ page }) => {
    const result = runnerResult("invalid_request");
    delete result.requested;
    delete result.negotiation.version;
    result.diagnostics = [{ code: "<a href='https://invalid.test'>code</a>", message: "<script>alert('unsafe')</script>" }];
    await archive(page, runnerCollection([result]));
    await page.goto("/#/lab/runner");
    const article = page.getByRole("article");
    await expect(article).toContainText("Unavailable — no version selected");
    await expect(article.getByText("Unavailable — no tuple recorded.", { exact: true })).toHaveCount(2);
    await expect(article).toContainText("<script>alert('unsafe')</script>");
    await expect(article.locator("script")).toHaveCount(0);
    await expect(article.getByRole("link")).toHaveCount(1);
  });

  test("keyboard navigation, skip link, history and narrow rendering preserve semantics", async ({ page }) => {
    await archive(page);
    await page.goto("/#/lab/history");
    const runnerLink = page.getByRole("link", { name: "Runner results", exact: true });
    await runnerLink.focus();
    await page.keyboard.press("Enter");
    await expect(runnerLink).toHaveAttribute("aria-current", "page");
    await expect(page.getByRole("heading", { level: 1, name: "Runner results" })).toBeVisible();
    await page.getByRole("link", { name: "Skip to dashboard content" }).focus();
    await page.keyboard.press("Enter");
    await expect(page.getByRole("main")).toBeFocused();
    await page.goBack();
    await expect(page.getByRole("heading", { level: 1, name: "Run history" })).toBeVisible();
    await page.goForward();
    await expect(page.getByRole("heading", { level: 1, name: "Runner results" })).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByRole("list", { name: "Logical identity lineage" })).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  });
});
