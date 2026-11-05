import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { nativeReleaseCollections, nativeScenarios } from "./fixtures/native-release-data";
import { parseCSV } from "./helpers/csv";

for (const phase of ["construction", "initialize", "execute", "grade", "shutdown"] as const) {
  for (const state of ["completed", "completed-empty-reason", "failed", "not_attempted", "cancelled",
    "missing", "completed-with-reason"] as const) {
    test(`accepted native lifecycle ${phase}: ${state}`, async ({ page }) => {
      const collections = await nativeReleaseCollections("accepted");
      const collection = collections[0];
      const attempts = collection?.decision.native?.attempts;
      const first = attempts?.[0];
      if (!collection || !attempts || !first) throw new Error("Producer fixture must contain native evidence.");
      expect(collection.decision.accepted).toBe(true);
      expect(attempts).toHaveLength(16);
      const original = structuredClone(first);
      const keys = structuredClone(attempts.map((attempt) => attempt.key));
      expect(first.lifecycle[phase]).toEqual({ state: "completed" });
      if (state === "missing") {
        delete first.lifecycle[phase];
      } else if (state === "completed-empty-reason") {
        first.lifecycle[phase] = { state: "completed", reason: "" };
      } else if (state === "completed-with-reason") {
        first.lifecycle[phase] = { state: "completed", reason: "Contradictory lifecycle failure." };
      } else if (state === "failed") {
        first.lifecycle[phase] = { state };
      } else if (state !== "completed") {
        first.lifecycle[phase] = { state, reason: "Lifecycle prerequisite did not complete." };
      }
      if (state !== "completed") expect(first).not.toEqual(original);
      expect(attempts.map((attempt) => attempt.key)).toEqual(keys);
      expect(first.status).toBe(original.status);
      expect(first.category).toBe(original.category);
      expect(Object.fromEntries(Object.entries(first.lifecycle).filter(([name]) => name !== phase))).toEqual(
        Object.fromEntries(Object.entries(original.lifecycle).filter(([name]) => name !== phase)),
      );
      for (const arm of ["baseline", "candidate"] as const) {
        expect(attempts.filter((attempt) => attempt.key.arm === arm)).toHaveLength(8);
        expect(collection.decision.accounting[arm]?.complete_attempts).toBe(8);
      }
      await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
      await page.goto("/#/release");
      if (state === "completed" || state === "completed-empty-reason") {
        await expect(page.getByRole("alert")).toHaveCount(0);
        await expect(page.getByText("Selected policy: PASSED", { exact: true })).toBeVisible();
        await expect(page.getByRole("region", { name: "Native attempt observations", exact: true }).locator("tbody tr")).toHaveCount(16);
        const downloadEvent = page.waitForEvent("download");
        await page.getByRole("button", { name: "Export decision JSON", exact: true }).click();
        const path = await (await downloadEvent).path();
        expect(path).not.toBeNull();
        expect(JSON.parse(await readFile(path!, "utf8"))).toEqual(collection);
      } else {
        await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
        await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toHaveCount(0);
      }
    });
  }
}

for (const status of ["passed", "failed"] as const) {
  for (const mutation of ["available-output", "empty-output", "unavailable-output", "nonempty-attempt-error",
    "empty-attempt-error", "absent-collection-error", "empty-collection-error", "absent-attempt-error"] as const) {
    test(`accepted native behavioral ${status} response admission: ${mutation}`, async ({ page }) => {
      const collections = await nativeReleaseCollections("accepted");
      const collection = collections[0];
      const attempts = collection?.decision.native?.attempts;
      const first = attempts?.[0];
      if (!collection || !attempts || !first) throw new Error("Producer fixture must contain native evidence.");
      expect(collection.decision.accepted).toBe(true);
      expect(attempts).toHaveLength(16);
      const keys = structuredClone(attempts.map((attempt) => attempt.key));
      if (status === "failed") {
        const source = await nativeReleaseCollections("retry");
        const failed = source[0]?.decision.native?.attempts?.[0];
        if (!failed) throw new Error("Producer fixture must contain failed behavioral evidence.");
        expect(failed.status).toBe("failed");
        expect(failed.category).toBe("behavioral");
        attempts[0] = { ...failed, key: first.key };
      }
      const attempt = attempts[0]!;
      const original = structuredClone(attempt);
      expect(attempt.error).toBe("");
      expect(attempt.output_availability).toBe("available");
      expect(attempt.output).not.toBe("");
      expect(collection.error).toBeUndefined();
      if (mutation === "empty-output") attempt.output = "";
      if (mutation === "unavailable-output") {
        attempt.output_availability = "unavailable";
        delete attempt.output;
      }
      if (mutation === "nonempty-attempt-error") attempt.error = "Execution failed but behavioral acceptance was retained.";
      if (mutation === "empty-attempt-error") attempt.error = "";
      if (mutation === "absent-collection-error") delete collection.error;
      if (mutation === "empty-collection-error") collection.error = "";
      if (mutation === "absent-attempt-error") expect(Reflect.deleteProperty(attempt, "error")).toBe(true);
      expect(attempts.map((entry) => entry.key)).toEqual(keys);
      expect(attempt.status).toBe(status);
      expect(attempt.category).toBe("behavioral");
      expect(attempt.lifecycle).toEqual(original.lifecycle);
      for (const arm of ["baseline", "candidate"] as const) {
        expect(attempts.filter((entry) => entry.key.arm === arm)).toHaveLength(8);
        expect(collection.decision.accounting[arm]?.complete_attempts).toBe(8);
      }
      await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
      await page.goto("/#/release");
      if (["unavailable-output", "nonempty-attempt-error", "absent-attempt-error"].includes(mutation)) {
        await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
        await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toHaveCount(0);
        await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toHaveCount(0);
      } else {
        await expect(page.getByRole("alert")).toHaveCount(0);
        await expect(page.getByText("Selected policy: PASSED", { exact: true })).toBeVisible();
        const observations = page.getByRole("region", { name: "Native attempt observations", exact: true });
        await expect(observations.locator("tbody tr")).toHaveCount(16);
        if (mutation === "empty-output") {
          const row = observations.locator("tbody tr").first();
          await row.locator("summary").filter({ hasText: /^Actual output/ }).click();
          await expect(row.locator("pre")).toHaveText("(empty output)");
        }
        const jsonEvent = page.waitForEvent("download");
        await page.getByRole("button", { name: "Export decision JSON", exact: true }).click();
        const jsonPath = await (await jsonEvent).path();
        expect(jsonPath).not.toBeNull();
        expect(JSON.parse(await readFile(jsonPath!, "utf8"))).toEqual(collection);
        const csvEvent = page.waitForEvent("download");
        await page.getByRole("button", { name: "Export native attempts CSV", exact: true }).click();
        const csvPath = await (await csvEvent).path();
        expect(csvPath).not.toBeNull();
        const [headers, ...rows] = parseCSV(await readFile(csvPath!, "utf8"));
        expect(headers).toBeDefined();
        if (!headers) throw new Error("CSV header missing.");
        expect(rows).toHaveLength(16);
        for (const [name, expected] of [["Output availability", "available"], ["Output present", "true"],
          ["Output", attempt.output], ["Error", ""], ["Status", status]] as const) {
          const column = headers.indexOf(name);
          expect(column).toBeGreaterThanOrEqual(0);
          expect(rows[0]?.[column]).toBe(expected);
        }
      }
    });
  }
}

for (const attempts of [null, [], "partial" as const]) {
  test(`accepted native decision rejects missing observations ${JSON.stringify(attempts)}`, async ({ page }) => {
    const collections = await nativeReleaseCollections("accepted");
    const collection = collections[0];
    if (!collection?.decision.native) throw new Error("Producer fixture must contain native evidence.");
    expect(collection.decision.accepted).toBe(true);
    expect(collection.decision.accounting.baseline?.complete_attempts).toBe(8);
    expect(collection.decision.accounting.candidate?.complete_attempts).toBe(8);
    expect(collection.decision.native.attempts).toHaveLength(16);
    collection.decision.native.attempts = attempts === "partial"
      ? collection.decision.native.attempts!.slice(0, 1) : attempts;
    if (attempts === "partial") expect(collection.decision.native.attempts).toHaveLength(1);
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
    await page.goto("/#/release");
    await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
    await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toHaveCount(0);
  });

  test(`nonpassing native decision preserves missing observations ${JSON.stringify(attempts)}`, async ({ page }) => {
    const collections = await nativeReleaseCollections("passed");
    const collection = collections[0];
    if (!collection?.decision.native) throw new Error("Producer fixture must contain native evidence.");
    expect(collection.decision.accepted).toBe(false);
    expect(collection.decision.native.attempts).toHaveLength(2);
    collection.decision.native.attempts = attempts === "partial"
      ? collection.decision.native.attempts!.slice(0, 1) : attempts;
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
    await page.goto("/#/release");
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByText("Selected policy: NOT PASSED", { exact: true })).toBeVisible();
    if (attempts === "partial") {
      expect(collection.decision.native.attempts).toHaveLength(1);
      await expect(page.getByRole("region", { name: "Native attempt observations", exact: true }).locator("tbody tr")).toHaveCount(1);
      await expect(page.getByText("No native attempt observations available.", { exact: true })).toHaveCount(0);
    } else {
      await expect(page.getByText("No native attempt observations available.", { exact: true })).toBeVisible();
    }
    const downloadEvent = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export decision JSON", exact: true }).click();
    const download = await downloadEvent;
    const path = await download.path();
    expect(path).not.toBeNull();
    expect(JSON.parse(await readFile(path!, "utf8"))).toEqual(collection);
  });
}

for (const mutation of ["baseline-only", "candidate-only", "reassigned-arm", "duplicate", "unknown-arm"] as const) {
  test(`accepted native decision rejects invalid arm inventory ${mutation}`, async ({ page }) => {
    const collections = await nativeReleaseCollections("accepted");
    const collection = collections[0];
    const native = collection?.decision.native;
    const attempts = native?.attempts;
    const first = attempts?.[0];
    if (!collection || !native || !attempts || !first) throw new Error("Producer fixture must contain native evidence.");
    expect(collection.decision.accepted).toBe(true);
    expect(attempts).toHaveLength(16);
    expect(attempts.filter((attempt) => attempt.key.arm === "baseline")).toHaveLength(8);
    expect(attempts.filter((attempt) => attempt.key.arm === "candidate")).toHaveLength(8);
    const invalidAttempts = mutation === "baseline-only" || mutation === "candidate-only"
      ? attempts.filter((attempt) => attempt.key.arm === (mutation === "baseline-only" ? "baseline" : "candidate"))
      : mutation === "duplicate"
        ? attempts.map((attempt, index) => index === 1 ? first : attempt)
        : attempts.map((attempt, index) => index === 0
          ? { ...attempt, key: { ...attempt.key, arm: mutation === "unknown-arm" ? "unknown"
            : first.key.arm === "baseline" ? "candidate" : "baseline" } }
          : attempt);
    expect(invalidAttempts).not.toEqual(attempts);
    expect(invalidAttempts).toHaveLength(mutation.endsWith("-only") ? 8 : 16);
    const invalid = [{ ...collection, decision: { ...collection.decision, native: { ...native, attempts: invalidAttempts } } }];
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: invalid }));
    await page.goto("/#/release");
    await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
    await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toHaveCount(0);
  });
}

for (const mutation of ["initialize", "operational-passed", "behavioral-incomplete", "behavioral-failed"] as const) {
  test(`accepted native terminal evidence admission without recomputing policy: ${mutation}`, async ({ page }) => {
    const collections = await nativeReleaseCollections("accepted");
    const collection = collections[0];
    const attempts = collection?.decision.native?.attempts;
    const first = attempts?.[0];
    if (!collection || !attempts || !first) throw new Error("Producer fixture must contain native evidence.");
    expect(collection.decision.accepted).toBe(true);
    expect(attempts).toHaveLength(16);
    const keys = structuredClone(attempts.map((attempt) => attempt.key));
    if (mutation === "initialize" || mutation === "behavioral-failed") {
      const source = await nativeReleaseCollections(mutation === "initialize" ? "initialize" : "retry");
      const replacement = source[0]?.decision.native?.attempts?.[0];
      if (!replacement) throw new Error("Producer fixture must contain replacement attempt evidence.");
      expect(replacement.category).toBe(mutation === "initialize" ? "operational" : "behavioral");
      expect(replacement.status).toBe(mutation === "initialize" ? "incomplete" : "failed");
      attempts[0] = { ...replacement, key: first.key };
    } else {
      attempts[0] = { ...first, category: mutation === "operational-passed" ? "operational" : "behavioral",
        status: mutation === "behavioral-incomplete" ? "incomplete" : "passed" };
    }
    expect(attempts[0]).not.toEqual(first);
    expect(attempts.map((attempt) => attempt.key)).toEqual(keys);
    for (const arm of ["baseline", "candidate"] as const) {
      expect(attempts.filter((attempt) => attempt.key.arm === arm)).toHaveLength(8);
      expect(collection.decision.accounting[arm]?.complete_attempts).toBe(8);
    }
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
    await page.goto("/#/release");
    if (mutation === "behavioral-failed") {
      await expect(page.getByRole("alert")).toHaveCount(0);
      await expect(page.getByText("Selected policy: PASSED", { exact: true })).toBeVisible();
      await expect(page.getByText("Status: failed", { exact: true })).toHaveCount(1);
      const downloadEvent = page.waitForEvent("download");
      await page.getByRole("button", { name: "Export decision JSON", exact: true }).click();
      const path = await (await downloadEvent).path();
      expect(path).not.toBeNull();
      expect(JSON.parse(await readFile(path!, "utf8"))).toEqual(collection);
    } else {
      await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
      await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toHaveCount(0);
      await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toHaveCount(0);
    }
  });
}

test("downloaded native CSV keeps bare CR output in its original attempt cell", async ({ page }) => {
  const collections = await nativeReleaseCollections("carriage-return");
  const collection = collections[0];
  const attempts = collection?.decision.native?.attempts;
  if (!collection || !attempts) throw new Error("Producer fixture must contain native attempts.");
  expect(attempts).toHaveLength(2);
  expect(attempts.map((attempt) => attempt.output)).toEqual(["hello\r=1+1", "hello\r=1+1"]);
  expect(collection.error).toBeUndefined();
  expect(collection.decision.operations.state).toBe("observed");
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
  await page.goto("/#/release");
  const downloadEvent = page.waitForEvent("download");
  await page.getByRole("button", { name: "Export native attempts CSV", exact: true }).click();
  const download = await downloadEvent;
  expect(download.suggestedFilename()).toBe("waza-native-attempts.csv");
  const path = await download.path();
  expect(path).not.toBeNull();
  const csv = await readFile(path!, "utf8");
  expect(csv.match(/,available,true,"hello\r=1\+1",/g)).toHaveLength(2);
  const [headers, ...rows] = parseCSV(csv);
  if (!headers) throw new Error("CSV header missing.");
  expect(rows).toHaveLength(2);
  for (const [index, attempt] of attempts.entries()) {
    const row = rows[index];
    expect(row).toHaveLength(headers.length);
    const value = (name: string) => {
      const column = headers.indexOf(name);
      expect(column).toBeGreaterThanOrEqual(0);
      return row?.[column];
    };
    expect(value("Arm")).toBe(attempt.key.arm);
    expect(value("Cluster")).toBe(attempt.key.cluster_id);
    expect(value("Task")).toBe(attempt.key.task_id);
    expect(value("Trial")).toBe(String(attempt.key.trial_ordinal));
    expect(value("Eval")).toBe(attempt.key.eval_id);
    expect(value("Attempt")).toBe(String(attempt.key.attempt_ordinal));
    expect(value("Output availability")).toBe("available");
    expect(value("Output present")).toBe("true");
    expect(value("Output")).toBe("hello\r=1+1");
  }
  const unquoted = csv.replaceAll('"hello\r=1+1"', "hello\r=1+1");
  expect(unquoted).not.toBe(csv);
  const [, ...splitRows] = parseCSV(unquoted);
  expect(splitRows).toHaveLength(4);
  expect(splitRows.filter((row) => row[0] === "=1+1")).toHaveLength(2);
});

for (const scenario of nativeScenarios) {
  test(`producer native ${scenario} remains a non-passing collection with exact attempt evidence`, async ({ page }) => {
    const collections = await nativeReleaseCollections(scenario);
    const attempts = collections[0]?.decision.native?.attempts;
    expect(attempts?.length).toBe(scenario === "retry" ? 4 : scenario.endsWith("budget") ? 1 : 2);
    if (!attempts?.length) throw new Error("Producer fixture must contain native attempt evidence.");
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
    await page.goto("/#/release");
    await expect(page.getByText("Selected policy: NOT PASSED", { exact: true })).toBeVisible();
    const collectionError = collections[0]?.error;
    if (["initialize", "execute", "shutdown", "disconnect", "empty-output", "unknown-usage"].includes(scenario)) {
      expect(collectionError).toBe("native collection has a retained failure");
    }
    if (collectionError) {
      await expect(page.getByRole("alert")).toHaveText(collectionError);
      await expect(page.getByRole("button", { name: "Export decision JSON", exact: true })).toBeVisible();
      await expect(page.getByRole("button", { name: "Export native attempts CSV", exact: true })).toBeVisible();
    } else {
      await expect(page.getByRole("alert")).toHaveCount(0);
    }
    const evidence = page.getByRole("region", { name: "Native collection evidence", exact: true });
    await expect(evidence.getByText("copilot_sdk_single_turn_text / 1.0", { exact: true })).toBeVisible();
    await expect(evidence.getByText("Declared client tool policy: deny all. This is not server isolation.", { exact: true })).toBeVisible();
    await expect(evidence.getByText(/unknown required usage blocks new starts.*Asynchronous overshoot is possible/)).toBeVisible();
    await expect(evidence.getByText("unsupported", { exact: true })).toBeVisible();
    await expect(evidence.getByText("unavailable", { exact: true })).toHaveCount(2);
    const rows = evidence.locator("tbody tr");
    await expect(rows).toHaveCount(attempts.length);
    for (const [index, attempt] of attempts.entries()) {
      const row = rows.nth(index);
      await expect(row.getByRole("rowheader")).toContainText(
        `${attempt.key.arm} / ${attempt.key.cluster_id} / ${attempt.key.task_id} / trial ${attempt.key.trial_ordinal} / attempt ${attempt.key.attempt_ordinal}`,
      );
      await expect(row.getByRole("rowheader")).toContainText(`Eval: ${attempt.key.eval_id}`);
      await expect(row.getByText(`Status: ${attempt.status}`, { exact: true })).toBeVisible();
      await expect(row.getByText(`Category: ${attempt.category}`, { exact: true })).toBeVisible();
      await expect(row.getByText(`Output: ${attempt.output_availability}`, { exact: true })).toBeVisible();
      await expect(row.getByText(`Usage: ${attempt.usage_complete ? "complete" : "incomplete / unavailable"}`, { exact: true })).toBeVisible();
      await expect(row.getByText(`Received event models: ${attempt.event_models?.length ? attempt.event_models.join(", ") : "unavailable"}`, { exact: true })).toBeVisible();
      await expect(row.getByText(`Accounting models: ${attempt.accounting_models?.length ? attempt.accounting_models.join(", ") : "unavailable"}`, { exact: true })).toBeVisible();
      await expect(row.locator("summary").filter({ hasText: /^Actual output/ })).toHaveCount(attempt.output === undefined ? 0 : 1);
      if (attempt.error) await expect(row.getByText(`Error: ${attempt.error}`, { exact: true })).toBeVisible();
    }
    if (scenario === "retry") {
      await expect(page.getByText("First-attempt passes: 0; retry-policy passes: 1; recovered: 1.", { exact: true })).toHaveCount(2);
      await expect(page.getByText("First-attempt rate: 0.0%; retry-policy rate: 100.0%.", { exact: true })).toHaveCount(2);
      await expect(page.getByText(/golden first attempt failed \(recovery cannot erase it\)/)).toHaveCount(2);
    }
    if (scenario === "unknown-usage") {
      await expect(page.getByText(/ai_credits : unavailable \(unknown\)/)).toHaveCount(2);
      await expect(page.getByText(/ai_credits : 0/)).toHaveCount(0);
    }
    if (scenario === "unknown-budget") {
      const startedArm = attempts[0]?.key.arm;
      if (!startedArm) throw new Error("Budget fixture must identify the started arm.");
      const unstartedArm = startedArm === "baseline" ? "candidate" : "baseline";
      expect(collections[0]?.decision.accounting[startedArm]?.started_attempts).toBe(1);
      expect(collections[0]?.decision.accounting[unstartedArm]?.started_attempts).toBe(0);
      const started = page.getByRole("heading", { name: startedArm, exact: true }).locator("..");
      const unstarted = page.getByRole("heading", { name: unstartedArm, exact: true }).locator("..");
      await expect(started.getByText(/ai_credits : unavailable \(unknown\)/)).toBeVisible();
      await expect(started.getByText(/ai_credits : 0/)).toHaveCount(0);
      await expect(unstarted.getByText("ai_credits : 0 (final_complete_attributable)", { exact: true })).toBeVisible();
      await expect(page.getByText(
        "admitting next attempt: required prior input_tokens usage is unavailable; no further native attempt may start",
        { exact: true },
      )).toBeVisible();
    }
    if (scenario === "reached-budget") {
      await expect(page.getByText(
        "admitting next attempt: observed input_tokens budget is reached/exceeded; no further native attempt may start (in-flight overshoot is possible)",
        { exact: true },
      )).toBeVisible();
    }
  });
}

test("accepted native producer collection displays its complete matched evidence without recomputing policy", async ({ page }) => {
  const collections = await nativeReleaseCollections("accepted");
  const decision = collections[0]?.decision;
  expect(decision?.accepted).toBe(true);
  expect(decision?.planned_clusters).toBe(8);
  expect(decision?.native?.attempts).toHaveLength(16);
  expect(decision?.statistics.state).toBe("noninferiority");
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
  await page.goto("/#/release");
  await expect(page.getByText("Selected policy: PASSED", { exact: true })).toBeVisible();
  await expect(page.getByText("Selected policy: NOT PASSED", { exact: true })).toHaveCount(0);
  const observations = page.getByRole("region", { name: "Native attempt observations", exact: true });
  await expect(observations.locator("tbody tr")).toHaveCount(16);
  await expect(observations.getByText("Status: passed", { exact: true })).toHaveCount(16);
  await expect(observations.getByText("Usage: complete", { exact: true })).toHaveCount(16);
  await expect(page.getByText("Trials: 8/8 started, 8 complete. Attempts: 8 started, 8 complete.", { exact: true })).toHaveCount(2);
  await expect(page.getByText("First-attempt passes: 8; retry-policy passes: 8; recovered: 0.", { exact: true })).toHaveCount(2);
  await expect(page.getByText("First-attempt rate: 100.0%; retry-policy rate: 100.0%.", { exact: true })).toHaveCount(2);
  await expect(page.getByText("ai_credits : 0 (final_complete_attributable)", { exact: true })).toHaveCount(2);
  await expect(page.getByText(/^Fixed-suite estimate .*8 planned clusters; retries are not iid samples\.$/)).toBeVisible();
});

test("native output and lifecycle details support keyboard access and mobile scrolling", async ({ page }) => {
  const collections = await nativeReleaseCollections("retry");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
  await page.goto("/#/release");
  const observations = page.getByRole("region", { name: "Native attempt observations", exact: true });
  await observations.focus();
  await expect(observations).toBeFocused();
  const row = observations.locator("tbody tr").nth(1);
  const output = row.locator("summary").filter({ hasText: /^Actual output/ });
  await output.focus();
  await page.keyboard.press("Enter");
  await expect(row.locator("pre")).toHaveText("hello");
  const lifecycle = row.locator("summary").filter({ hasText: /^Lifecycle/ });
  await lifecycle.focus();
  await page.keyboard.press("Enter");
  await expect(row.locator("dt")).toHaveText(["construction", "execute", "grade", "initialize", "shutdown"]);
  await expect(row.locator("dd")).toHaveText(["completed", "completed", "completed", "completed", "completed"]);
  await page.keyboard.press("Enter");
  await expect(row.locator("dd").first()).not.toBeVisible();
});

test("native failed lifecycle retains its reason and unattempted prerequisites", async ({ page }) => {
  await page.route(/\/api\/release-collections$/, async (route) =>
    route.fulfill({ json: await nativeReleaseCollections("initialize") }));
  await page.goto("/#/release");
  const row = page.getByRole("region", { name: "Native attempt observations", exact: true }).locator("tbody tr").first();
  await row.locator("summary").filter({ hasText: /^Lifecycle/ }).click();
  await expect(row.locator("dt").filter({ hasText: /^initialize$/ }).locator("..").locator("dd")).toHaveText(
    "failed: copilot diagnostic: initialize/start_failed",
  );
  await expect(row.locator("dt").filter({ hasText: /^execute$/ }).locator("..").locator("dd")).toHaveText(
    "not_attempted: Prior lifecycle prerequisite did not complete.",
  );
});

for (const scenario of ["accepted", "carriage-return", ...nativeScenarios] as const) {
  test(`native ${scenario} JSON and CSV downloads preserve exact policy, attempts and usage`, async ({ page }) => {
    const collections = await nativeReleaseCollections(scenario);
    const collection = collections[0];
    if (!collection?.decision.native?.attempts?.length) throw new Error("Producer fixture must contain native attempts.");
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({ json: collections }));
    await page.goto("/#/release");
    if (collection.error) await expect(page.getByRole("alert")).toHaveText(collection.error);
    const jsonEvent = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export decision JSON", exact: true }).click();
    const json = await jsonEvent;
    expect(json.suggestedFilename()).toBe("waza-release-decision.json");
    const jsonPath = await json.path();
    expect(jsonPath).not.toBeNull();
    expect(JSON.parse(await readFile(jsonPath!, "utf8"))).toEqual(collection);
    const csvEvent = page.waitForEvent("download");
    await page.getByRole("button", { name: "Export native attempts CSV", exact: true }).click();
    const csv = await csvEvent;
    expect(csv.suggestedFilename()).toBe("waza-native-attempts.csv");
    const csvPath = await csv.path();
    expect(csvPath).not.toBeNull();
    const [headers, ...rows] = parseCSV(await readFile(csvPath!, "utf8"));
    expect(headers).toBeDefined();
    if (!headers) throw new Error("CSV header missing.");
    expect(rows).toHaveLength(collection.decision.native.attempts.length);
    for (const [index, attempt] of collection.decision.native.attempts.entries()) {
      const row = rows[index];
      expect(row).toHaveLength(headers.length);
      const value = (name: string) => {
        const column = headers.indexOf(name);
        expect(column).toBeGreaterThanOrEqual(0);
        return row?.[column];
      };
      expect(value("Arm")).toBe(attempt.key.arm);
      expect(value("Selected policy accepted")).toBe(String(collection.decision.accepted));
      expect(value("Cluster")).toBe(attempt.key.cluster_id);
      expect(value("Task")).toBe(attempt.key.task_id);
      expect(value("Trial")).toBe(String(attempt.key.trial_ordinal));
      expect(value("Eval")).toBe(attempt.key.eval_id);
      expect(value("Attempt")).toBe(String(attempt.key.attempt_ordinal));
      expect(value("Status")).toBe(attempt.status);
      expect(value("Category")).toBe(attempt.category);
      expect(value("Error")).toBe(attempt.error);
      expect(value("Output availability")).toBe(attempt.output_availability);
      expect(value("Output present")).toBe(String(attempt.output !== undefined));
      expect(value("Output")).toBe(attempt.output ?? "");
      expect(value("Usage complete")).toBe(String(attempt.usage_complete));
      expect(JSON.parse(value("Arm usage JSON") ?? "")).toEqual(collection.decision.usage[attempt.key.arm]);
      expect(JSON.parse(value("Lifecycle JSON") ?? "")).toEqual(attempt.lifecycle);
      expect(JSON.parse(value("Event models JSON") ?? "")).toEqual(attempt.event_models);
      expect(JSON.parse(value("Accounting models JSON") ?? "")).toEqual(attempt.accounting_models);
      expect(value("Client tool policy")).toBe(collection.decision.native.client_tool_policy);
      expect(value("Budget contract")).toBe(collection.decision.native.budget_contract);
    }
  });
}

for (const malformed of ["version", "profile", "usage_complete", "models", "output_missing", "output_unavailable",
  "lifecycle", "ordinal", "duplicate"]) {
  test(`native admission rejects mutated producer ${malformed}`, async ({ page }) => {
    const collections = await nativeReleaseCollections("accepted");
    const collection = collections[0];
    const native = collection?.decision.native;
    const first = native?.attempts?.[0];
    if (!collection || !native || !first) throw new Error("Producer fixture must contain native attempts.");
    const attempt: Record<string, unknown> = { ...first };
    const badNative: Record<string, unknown> = { ...native, attempts: [attempt] };
    if (malformed === "version") badNative.version = "2.0";
    if (malformed === "profile") badNative.profile = "unknown_profile";
    if (malformed === "usage_complete") attempt.usage_complete = "true";
    if (malformed === "models") attempt.event_models = [1];
    if (malformed === "output_missing") delete attempt.output;
    if (malformed === "output_unavailable") attempt.output_availability = "unavailable";
    if (malformed === "lifecycle") attempt.lifecycle = { initialize: { state: false } };
    if (malformed === "ordinal") attempt.key = { ...first.key, attempt_ordinal: 1.5 };
    if (malformed === "duplicate") badNative.attempts = [first, first];
    await page.route(/\/api\/release-collections$/, (route) => route.fulfill({
      json: [{ ...collection, decision: { ...collection.decision, native: badNative } }],
    }));
    await page.goto("/#/release");
    await expect(page.getByRole("alert")).toHaveText("Invalid release decision view; not passed.");
    await expect(page.getByRole("table")).toHaveCount(0);
    await expect(page.getByText("Selected policy: PASSED", { exact: true })).toHaveCount(0);
  });
}
