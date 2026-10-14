import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { once } from "node:events";
import { createServer } from "node:http";
import type { RunDetail, RunDetailWire, RunSummary } from "../src/api/client";
import { RUN_DETAIL, RUNS } from "./fixtures/mock-data";

const savedRuns: RunSummary[] = RUNS.map((run, index) => ({
  ...run,
  timestamp: `2024-01-0${3 - index}T12:00:00Z`,
  model: index === 0 ? "custom-model-v1" : run.model,
  spec: index < 2 ? "shared-eval" : run.spec,
  skill: index < 2 ? "custom-skill" : run.spec,
  source: index === 1 ? "azure-blob" : "local",
  repetitions: 3,
  aiCredits: index === 0 ? 2.500000001 : run.aiCredits,
}));

function detail(run: RunSummary): RunDetail {
  const candidate = run.id === "run-001";
  return {
    ...run,
    tasks: [
      {
        ...RUN_DETAIL.tasks[0],
        graderResults: RUN_DETAIL.tasks[0]!.graderResults.map((grader) => ({ ...grader })),
        id: "stable-task",
        name: candidate ? "Renamed task" : "Original name",
        weightedScore: candidate ? 0.8 : 0.5,
      },
      {
        name: candidate ? "Added task" : "Removed task",
        id: candidate ? "added" : "removed",
        outcome: "passed",
        score: 0.9,
        weightedScore: 0.9,
        duration: 1,
        graderResults: [],
      },
    ],
  };
}

async function mockResults(
  page: Page,
  runs = savedRuns,
  details: RunDetailWire[] = runs.map(detail),
) {
  const requests: string[] = [];
  await page.route("**/api/v1/lab/runs", (route) =>
    route.fulfill({ json: runs }),
  );
  await page.route(/\/api\/runs\/[^/]+$/, (route) => {
    const id = decodeURIComponent(
      new URL(route.request().url()).pathname.split("/").pop()!,
    );
    requests.push(id);
    const found = details.find((run) => run.id === id);
    return route.fulfill(
      found ? { json: found } : { status: 404, json: { error: "run missing" } },
    );
  });
  return requests;
}

async function mockEventSource(page: Page) {
  await page.addInitScript(() => {
    const sources: {
      url: string;
      closed: boolean;
      readyState: number;
      onopen?: (event: Event) => void;
      onerror?: (event: Event) => void;
      onmessage?: (event: MessageEvent) => void;
    }[] = [];
    Object.defineProperty(window, "EventSource", {
      value: class {
        static CONNECTING = 0;
        static OPEN = 1;
        static CLOSED = 2;
        url: string;
        closed = false;
        readyState = 0;
        onopen?: (event: Event) => void;
        onerror?: (event: Event) => void;
        onmessage?: (event: MessageEvent) => void;
        constructor(url: string) {
          this.url = url;
          sources.push(this);
        }
        close() {
          this.closed = true;
          this.readyState = 2;
        }
      },
    });
    Object.defineProperty(window, "labEmit", {
      value: (payload: unknown, kind = "message") => {
        const active = sources.at(-1);
        if (!active) return;
        if (kind === "open") {
          active.readyState = 1;
          active.onopen?.(new Event("open"));
        } else if (kind === "error" || kind === "closed") {
          active.readyState = kind === "closed" ? 2 : 0;
          active.onerror?.(new Event("error"));
        } else
          active?.onmessage?.(
            new MessageEvent("message", { data: JSON.stringify(payload) }),
          );
      },
    });
    Object.defineProperty(window, "labSources", {
      value: () =>
        sources.map((source) => ({ url: source.url, closed: source.closed })),
    });
  });
}

async function emit(page: Page, payload: unknown, kind?: string) {
  await page.evaluate(
    ({ payload, kind }) => {
      const emit = Reflect.get(window, "labEmit") as (
        payload: unknown,
        kind?: string,
      ) => void;
      emit(payload, kind);
    },
    { payload, kind },
  );
}

test.describe("Real dashboard lab", () => {
  test("keeps candidate selection available for singleton suites and filters baselines", async ({
    page,
  }) => {
    const singleton = { ...savedRuns[2]!, timestamp: "2024-01-04T12:00:00Z" };
    await mockResults(page, [singleton, ...savedRuns.slice(0, 2)]);
    await page.goto("/#/lab/compare");
    const candidate = page.getByRole("combobox", { name: "Candidate run" });
    const baseline = page.getByRole("combobox", { name: "Baseline run" });
    await expect(candidate).toHaveValue(singleton.id);
    await expect(baseline).toBeDisabled();
    await expect(page.getByText("Two runs from the same eval are needed")).toBeVisible();
    await candidate.selectOption("run-001");
    await expect(baseline).toBeEnabled();
    await expect(baseline.locator("option")).toHaveCount(1);
    await expect(baseline).toHaveValue("run-002");
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await candidate.selectOption(singleton.id);
    await expect(baseline).toBeDisabled();
    await expect(page.locator(".lab-matrix")).toHaveCount(0);
    await candidate.selectOption("run-002");
    await expect(baseline).toHaveValue("run-001");
    await expect(baseline.locator("option")).toHaveCount(1);
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
  });

  test("manual refresh updates same-ID same-timestamp comparison and preserves failed refresh evidence", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    const regraded = detail(savedRuns[0]!);
    regraded.tasks[0]!.weightedScore = 0.6;
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ json: regraded }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(0);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText("Previously loaded results");
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ json: savedRuns }),
    );
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ status: 503 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText("Previously loaded comparison");
    await expect(page.getByText("+10.0 pp", { exact: true })).toHaveCount(2);
  });

  test("summary polling refreshes shared comparison and inspector details once per active run", async ({
    page,
  }) => {
    await page.clock.install();
    const localRuns = savedRuns.map((run) => ({ ...run, source: "local" }));
    const requests = await mockResults(page, localRuns);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refreshes every 15 seconds");
    const inspectorResponse = page.waitForResponse("**/api/runs/run-001");
    await page.getByRole("button", { name: "Renamed task", exact: true }).click();
    await inspectorResponse;
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    const initialRequests = requests.length;
    const regraded = detail(savedRuns[0]!);
    regraded.tasks[0]!.weightedScore = 0.4;
    regraded.tasks[0]!.graderResults[0]!.message = "Regraded recorded evidence";
    await page.route("**/api/runs/run-001", (route) => {
      requests.push("run-001");
      return route.fulfill({ json: regraded });
    });
    await page.clock.fastForward(15_000);
    await expect(dialog.getByText("Regraded recorded evidence")).toBeVisible();
    await expect(dialog.getByText(/Weighted score 40.0%/)).toBeVisible();
    expect(requests.length).toBe(initialRequests + 2);
    expect(requests.slice(initialRequests).sort()).toEqual(["run-001", "run-002"]);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.clock.fastForward(15_000);
    await expect(page.locator(".lab-root [role='alert']")).toContainText("Previously loaded results");
    await expect(dialog.getByText("Regraded recorded evidence")).toBeVisible();
    expect(requests.length).toBe(initialRequests + 2);
  });

  test("remote archives do not poll or refetch mounted details until explicit refresh", async ({
    page,
  }) => {
    await page.clock.install();
    const remoteRuns = savedRuns.map((run) => ({ ...run, source: "azure-blob" }));
    let listings = 0;
    const requests = await mockResults(page, remoteRuns);
    // Keep the counting route newest without altering the result payload.
    await page.route("**/api/v1/lab/runs", (route) => {
      listings++;
      return route.fulfill({ json: remoteRuns });
    });
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText(/remote archive: refresh on demand/)).toBeVisible();
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh on demand");
    const initialDetails = requests.length;
    const initialListings = listings;
    expect(initialListings).toBeGreaterThan(0);
    await page.clock.fastForward(60_000);
    await page.evaluate(() => {
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event("offline"));
      window.dispatchEvent(new Event("online"));
    });
    await page.waitForTimeout(100);
    expect(listings).toBe(initialListings);
    expect(requests.length).toBe(initialDetails);
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect.poll(() => listings).toBe(initialListings + 1);
    await expect.poll(() => requests.length).toBe(initialDetails + 2);
  });

  test("filters multiple evaluation suites of one skill independently", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, skill: "shared-skill", spec: "suite-one" },
      { ...savedRuns[1]!, skill: "shared-skill", spec: "suite-two" },
      { ...savedRuns[2]!, skill: "other-skill", spec: "scenario-suite" },
    ]);
    await page.goto("/#/lab/history");
    const skill = page.getByRole("combobox", { name: "Filter skill" });
    const evaluation = page.getByRole("combobox", { name: "Filter eval" });
    await skill.selectOption("shared-skill");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await evaluation.selectOption("suite-one");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await evaluation.selectOption("suite-two");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await expect(page.locator("tbody")).toContainText("azure-blob");
    await evaluation.selectOption("");
    await expect(page.locator("tbody tr")).toHaveCount(2);
    await page.getByRole("combobox", { name: "Filter model" }).selectOption("custom-model-v1");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await evaluation.selectOption("suite-one");
    await page.getByRole("button", { name: "Reset filters" }).click();
    await expect(evaluation).toHaveValue("");
    await expect(skill).toHaveValue("all");
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await evaluation.selectOption("scenario-suite");
    await expect(page.getByRole("checkbox", { name: "Select run-003", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
  });

  test("eval filters distinguish overlapping skill names, absent skills and an eval named all", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, skill: "shared-identity", spec: "first-eval" },
      { ...savedRuns[1]!, skill: "first-eval", spec: "shared-identity" },
      { ...savedRuns[2]!, skill: undefined, spec: "eval-without-skill" },
      { ...savedRuns[0]!, id: "run-004", skill: "sentinel-skill", spec: "all" },
    ]);
    await page.goto("/#/lab/history");
    const skill = page.getByRole("combobox", { name: "Filter skill" });
    const evaluation = page.getByRole("combobox", { name: "Filter eval" });
    await evaluation.selectOption("shared-identity");
    await expect(page.getByRole("checkbox", { name: "Select run-002", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await skill.selectOption("shared-identity");
    await expect(page.locator("tbody tr")).toHaveCount(0);
    await evaluation.selectOption("");
    await expect(page.getByRole("checkbox", { name: "Select run-001", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.getByRole("button", { name: "Reset filters" }).click();
    await evaluation.selectOption("eval-without-skill");
    await expect(page.getByRole("checkbox", { name: "Select run-003", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await evaluation.selectOption("all");
    await expect(page.getByRole("checkbox", { name: "Select run-004", exact: true })).toBeVisible();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.getByRole("link", { name: "Reports", exact: true }).click();
    await expect(page.locator(".lab-report-filters")).toContainText("Eval: all");
    const download = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const path = await (await download).path();
    const report = JSON.parse(await readFile(path!, "utf8"));
    expect(report.filters.evalId).toBe("all");
    expect(report.runs.map((run: RunDetail) => run.id)).toEqual(["run-004"]);
    expect(report.runs[0].spec).toBe("all");
  });

  test("sidebar waits for archive evidence before reporting a refresh policy", async ({
    page,
  }) => {
    const remote = { ...savedRuns[0]!, source: "azure-blob" };
    await mockResults(page, [remote]);
    let release!: () => void;
    const ready = new Promise<void>((resolve) => { release = resolve; });
    await page.route("**/api/v1/lab/runs", async (route) => {
      await ready;
      return route.fulfill({ json: [remote] });
    });
    try {
      await page.goto("/#/lab/history");
      await expect(page.locator(".lab-local-indicator small")).toHaveText("Loading refresh policy...");
      await expect(page.locator(".lab-local-indicator")).not.toContainText("Refreshes every 15 seconds");
      release();
      await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh on demand");
    } finally {
      release();
    }
  });

  test("inspector tabs implement roving focus and labelled keyboard selection", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    const graders = dialog.getByRole("tab", { name: "Grader evidence" });
    const trajectory = dialog.getByRole("tab", { name: "Trajectory", exact: true });
    const configuration = dialog.getByRole("tab", { name: "Configuration" });
    await expect(graders).toHaveAttribute("tabindex", "0");
    await expect(trajectory).toHaveAttribute("tabindex", "-1");
    await expect(configuration).toHaveAttribute("tabindex", "-1");
    await graders.focus();
    for (const [key, selected, name] of [
      ["ArrowRight", trajectory, "Trajectory"],
      ["End", configuration, "Configuration"],
      ["ArrowRight", graders, "Grader evidence"],
      ["ArrowLeft", configuration, "Configuration"],
      ["Home", graders, "Grader evidence"],
    ] as const) {
      await page.keyboard.press(key);
      await expect(selected).toBeFocused();
      await expect(selected).toHaveAttribute("aria-selected", "true");
      await expect(dialog.getByRole("tab", { selected: true })).toHaveCount(1);
      await expect(dialog.locator('[role="tab"][tabindex="0"]')).toHaveCount(1);
      const id = await selected.getAttribute("id");
      await expect(dialog.getByRole("tabpanel", { name, exact: true })).toHaveAttribute("aria-labelledby", id!);
      await expect(selected).toHaveAttribute("aria-controls", "lab-evidence-panel");
    }
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  });

  for (const nullable of ["tasks", "graderResults"] as const) {
    test(`nullable ${nullable} remain empty evidence in inspector and JSON reports`, async ({
      page,
    }) => {
      const summary = {
        ...savedRuns[0]!,
        weightedScore: undefined,
        taskCount: nullable === "tasks" ? 0 : 1,
        passCount: nullable === "tasks" ? 0 : 1,
      };
      const wire: RunDetailWire = {
        ...detail(summary),
        weightedScore: undefined,
        tasks: nullable === "tasks" ? null : [{
          ...detail(summary).tasks[0]!,
          weightedScore: undefined,
          graderResults: null,
        }],
      };
      await mockResults(page, [summary], [wire]);
      await page.goto("/#/lab/history");
      await page.getByRole("button", { name: "custom-skill", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await expect(dialog.locator(".lab-grader")).toHaveCount(0);
      if (nullable === "tasks") {
        await expect(dialog.getByText("No recorded tasks in this result.")).toBeVisible();
        await expect(dialog.getByRole("combobox", { name: "Task", exact: true })).toBeDisabled();
      } else {
        await expect(dialog.getByText(/Weighted score Unavailable/)).toBeVisible();
      }
      await page.keyboard.press("Escape");
      await page.getByRole("link", { name: "Reports" }).click();
      const download = page.waitForEvent("download");
      await page.getByRole("button", { name: "Download JSON" }).click();
      const path = await (await download).path();
      const report = JSON.parse(await readFile(path!, "utf8"));
      expect(report.runs[0].weightedScore).toBeUndefined();
      if (nullable === "tasks") {
        expect(report.runs[0].tasks).toEqual([]);
      } else {
        expect(report.runs[0].tasks[0].graderResults).toEqual([]);
        expect(report.runs[0].tasks[0].weightedScore).toBeUndefined();
      }
    });
  }

  test("light lab preserves readable recorded content, arguments and results", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/history");
    await page.getByRole("button", { name: "custom-skill", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    await page.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await dialog.getByRole("button", { name: "Events", exact: true }).click();
    await dialog.getByRole("button", { name: "Show details", exact: true }).nth(0).click();
    await dialog.getByRole("button", { name: "Show details", exact: true }).nth(0).click();
    await dialog.getByRole("button", { name: "Arguments", exact: true }).first().click();
    await dialog.getByRole("button", { name: "Result", exact: true }).first().click();
    const blocks = dialog.locator(".lab-recorded-trajectory pre");
    await expect(blocks).toHaveCount(3);
    await expect(blocks.filter({ hasText: "I'll analyze" })).toHaveCount(1);
    await expect(blocks.filter({ hasText: "fibonacci.py" })).toHaveCount(1);
    await expect(blocks.filter({ hasText: "def fib" })).toHaveCount(1);
    const contrasts = await blocks.evaluateAll((elements) => {
      const luminance = (color: string) => {
        const canvas = document.createElement("canvas");
        canvas.width = canvas.height = 1;
        const context = canvas.getContext("2d", { willReadFrequently: true })!;
        context.fillStyle = color;
        context.fillRect(0, 0, 1, 1);
        const channels = Array.from(context.getImageData(0, 0, 1, 1).data).slice(0, 3).map((value) => {
          const v = value / 255;
          return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
        });
        return channels[0]! * 0.2126 + channels[1]! * 0.7152 + channels[2]! * 0.0722;
      };
      return elements.map((element) => {
        const style = getComputedStyle(element);
        const text = getComputedStyle(element.querySelector("code") ?? element);
        const a = luminance(style.backgroundColor);
        const b = luminance(text.color);
        return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
      });
    });
    contrasts.forEach((contrast) => expect(contrast).toBeGreaterThan(4.5));
    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(dialog.locator("pre")).toHaveCSS("background-color", "rgb(246, 248, 251)");
    await expect(dialog.locator("pre")).toHaveCSS("color", "rgb(36, 49, 70)");
    await expect(dialog.locator("pre")).toContainText("Explain how the Fibonacci");
  });

  test("permanently closed activity streams require manual reconnect and release prior resources", async ({
    page,
  }) => {
    await mockEventSource(page);
    await mockResults(page);
    await page.goto("/#/lab/current");
    await expect(page.getByRole("heading", { name: "Run activity", exact: true })).toBeVisible();
    await emit(page, {
      runId: "run-001",
      sequence: 1,
      type: "run_started",
      timestamp: "2024-01-03T12:00:00Z",
      data: { totalTasks: 4 },
    });
    await emit(page, null, "closed");
    await expect(page.locator(".lab-status").filter({ hasText: /^Error$/ })).toBeVisible();
    await expect(page.getByRole("alert")).toContainText("Use Reconnect stream");
    await expect(page.getByRole("alert")).not.toContainText("will reconnect");
    await expect(page.locator(".lab-stream > div")).toHaveCount(1);
    await page.getByRole("button", { name: "Reconnect stream" }).click();
    await expect(page.locator(".lab-stream > div")).toHaveCount(0);
    const sources = await page.evaluate(
      () => (Reflect.get(window, "labSources") as () => { closed: boolean }[])(),
    );
    expect(sources.length).toBeGreaterThan(1);
    expect(sources.at(-2)?.closed).toBe(true);
    expect(sources.at(-1)?.closed).toBe(false);
    await emit(page, null, "open");
    await expect(page.getByText("Connected", { exact: true })).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
  });

  test("uses UTC calendar dates consistently for chart selection and history", async ({
    page,
  }) => {
    await mockResults(page, [
      { ...savedRuns[0]!, timestamp: "2024-01-03T23:30:00-05:00" },
      savedRuns[1]!,
    ]);
    await page.goto("/#/lab");
    await page.getByRole("button", { name: "Jan 4", exact: true }).click();
    await expect(
      page.locator(".lab-metrics").getByText("1", { exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("04:30");
  });

  test("preserves reported zero scores and credits instead of treating them as unavailable", async ({
    page,
  }) => {
    await mockResults(page, [
      {
        ...savedRuns[0]!,
        taskCount: 1,
        passCount: 0,
        outcome: "failed",
        weightedScore: 0,
        aiCredits: 0,
      },
    ]);
    await page.goto("/#/lab");
    await expect(
      page.locator(".lab-metrics").getByText("0.0%", { exact: true }),
    ).toHaveCount(2);
    await expect(
      page.locator(".lab-metrics").getByText("0.00", { exact: true }),
    ).toBeVisible();
    await expect(page.locator(".lab-metrics")).not.toContainText("Unavailable");
  });

  test("uses authoritative counts and arbitrary models, without loading every trace", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab");
    await expect(page.getByText("SAVED WAZA RESULTS")).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("3", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("75.0%", { exact: true }),
    ).toBeVisible();
    await expect(
      page.locator(".lab-metrics").getByText("7.250000001", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Filter to custom-model-v1" }),
    ).toBeVisible();
    expect(requests).toEqual([]);
    await page
      .getByRole("button", { name: "Filter to custom-model-v1" })
      .click();
    await page.getByRole("link", { name: "Run history" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("38s");
    await page.getByRole("button", { name: "Reset filters" }).click();
    await page
      .getByRole("combobox", { name: "Filter outcome" })
      .selectOption("failed");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await expect(page.locator("tbody")).toContainText("azure-blob");
  });

  test("shows loading, errors and retries without falling back to synthetic data", async ({
    page,
  }) => {
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 503, json: { error: "unavailable" } }),
    );
    await page.goto("/#/lab");
    await expect(page.getByRole("alert")).toContainText("503");
    await expect(page.getByRole("alert")).toContainText(
      "No synthetic fallback",
    );
    await expect(page.locator(".lab-local-indicator small")).toHaveText("Refresh policy unavailable");
    await expect(
      page.locator(".lab-metrics").getByText("84", { exact: true }),
    ).toHaveCount(0);
    await mockResults(page);
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByText("3 saved runs")).toBeVisible();
  });

  test("refresh discovers new files and preserves stale data on refresh failure", async ({
    page,
  }) => {
    await mockResults(page, savedRuns.slice(0, 1));
    await page.goto("/#/lab/history");
    await expect(page.locator("tbody tr")).toHaveCount(1);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ json: savedRuns }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.locator("tbody tr")).toHaveCount(3);
    await page.route("**/api/v1/lab/runs", (route) =>
      route.fulfill({ status: 500 }),
    );
    await page.getByRole("button", { name: "Refresh results" }).click();
    await expect(page.getByRole("alert")).toContainText(
      "Previously loaded results",
    );
    await expect(page.locator("tbody tr")).toHaveCount(3);
  });

  test("handles empty archives and reports unavailable catalog checks honestly", async ({
    page,
  }) => {
    await mockResults(page, []);
    await page.goto("/#/lab/history");
    await expect(
      page.getByRole("heading", { name: "No runs match this view" }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Current runs" }).click();
    await expect(
      page.getByRole("heading", { name: "No saved runs in this scope" }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Skills & evals" }).click();
    await expect(
      page.getByText("No saved suites in this scope."),
    ).toBeVisible();
    await expect(
      page.getByText(/Readiness, token budgets, requirement coverage/),
    ).toBeVisible();
  });

  test("loads real evidence lazily with first-repetition provenance and actual trajectory", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab/history");
    await page
      .getByRole("button", { name: "custom-skill", exact: true })
      .first()
      .click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByText("Output file found")).toBeVisible();
    await expect(
      dialog.getByText("SAVED RESULT EVIDENCE", { exact: false }),
    ).toBeVisible();
    await expect(
      dialog.getByText(/describe the first repetition/),
    ).toBeVisible();
    expect(requests).toEqual(["run-001"]);
    await page.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await expect(dialog).toContainText("Rate limit exceeded");
    await expect(dialog).not.toContainText("Illustrative task trace");
    await page.getByRole("tab", { name: "Configuration" }).click();
    await expect(dialog.locator("pre")).toContainText(
      "Explain how the Fibonacci",
    );
    await expect(dialog.locator("pre")).not.toContainText(
      "trigger_skill_routing",
    );
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
  });

  test("reports detail errors explicitly and retries", async ({ page }) => {
    await mockResults(page, savedRuns, []);
    await page.goto("/#/lab/history");
    await page
      .getByRole("button", { name: "custom-skill", exact: true })
      .first()
      .click();
    await expect(page.getByRole("dialog").getByRole("alert")).toContainText(
      "404",
    );
    await page.route("**/api/runs/run-001", (route) =>
      route.fulfill({ json: detail(savedRuns[0]!) }),
    );
    await page.getByRole("button", { name: "Retry evidence" }).click();
    await expect(
      page.getByRole("dialog").getByText("Output file found"),
    ).toBeVisible();
  });

  test("aligns stable IDs across renamed tasks and does not score added tasks as zero", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("+30.0 pp", { exact: true })).toHaveCount(2);
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await expect(page.locator(".lab-matrix tbody tr")).toHaveCount(3);
    await expect(
      page.getByText("Coverage change", { exact: true }),
    ).toHaveCount(2);
    await page.getByRole("button", { name: "Renamed task" }).click();
    await expect(
      page.getByRole("combobox", { name: "Task", exact: true }),
    ).toHaveValue("id:stable-task");
  });

  for (const legacy of [false, true]) {
    test(`duplicate ${legacy ? "legacy names" : "task IDs"} are not silently paired`, async ({
      page,
    }) => {
      const duplicate = savedRuns.slice(0, 2).map((run) => {
        const result = detail(run);
        result.tasks = [0, 1].map(() => ({
          ...result.tasks[0]!,
          id: legacy ? undefined : "duplicate",
          name: "Same name",
        }));
        return result;
      });
      await mockResults(page, savedRuns.slice(0, 2), duplicate);
      await page.goto("/#/lab/compare");
      await expect(page.getByText("0 comparable tasks")).toBeVisible();
      await expect(page.locator(".lab-matrix tbody tr")).toHaveCount(4);
    });
  }

  test("unique legacy names align, while absent scores stay unavailable", async ({
    page,
  }) => {
    const details = savedRuns.slice(0, 2).map((run) => {
      const result = detail(run);
      result.tasks = [
        { ...result.tasks[0]!, id: undefined, name: "Unique legacy name" },
      ];
      return result;
    });
    await mockResults(page, savedRuns.slice(0, 2), details);
    await page.goto("/#/lab/compare");
    await expect(page.getByText("1 comparable tasks")).toBeVisible();
    await page.route("**/api/runs/run-002", (route) =>
      route.fulfill({
        json: {
          ...details[1],
          tasks: [{ ...details[1]!.tasks[0], weightedScore: undefined }],
        },
      }),
    );
    await page.reload();
    await expect(page.getByText("0 comparable tasks")).toBeVisible();
    await expect(page.getByText("Score / identity unavailable")).toBeVisible();
  });

  test("exports real details and exact credits, leaving unknown CSV cells empty", async ({
    page,
  }) => {
    const requests = await mockResults(page);
    await page.goto("/#/lab/reports");
    await expect(page.getByText("3 saved runs contain")).toBeVisible();
    const jsonDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download JSON" }).click();
    const jsonPath = await (await jsonDownload).path();
    const report = JSON.parse(await readFile(jsonPath!, "utf8"));
    expect(report.synthetic).toBe(false);
    expect(report.summary.tasks).toBe(12);
    expect(report.summary.creditCoverage).toBe(2);
    expect(report.runs).toHaveLength(3);
    expect(report.runs[0].tasks[0].id).toBe("stable-task");
    expect(report.runs[0].aiCredits).toBe(2.500000001);
    expect(requests.sort()).toEqual(["run-001", "run-002", "run-003"]);
    const csvDownload = page.waitForEvent("download");
    await page.getByRole("button", { name: "Download CSV" }).click();
    const csvPath = await (await csvDownload).path();
    const csv = await readFile(csvPath!, "utf8");
    expect(csv).toContain("2.500000001");
    expect(csv.split("\n").find((row) => row.includes("run-003"))).toContain(
      ",,9800,",
    );
    expect(csv.split("\n").find((row) => row.includes("run-001"))).toContain(
      ",4,100.00,92.00,12400,",
    );
  });

  test("does not download a partial report after a detail request fails", async ({
    page,
  }) => {
    await mockResults(page, savedRuns, [detail(savedRuns[0]!)]);
    const downloads: string[] = [];
    page.on("download", (download) =>
      downloads.push(download.suggestedFilename()),
    );
    await page.goto("/#/lab/reports");
    await page.getByRole("button", { name: "Download JSON" }).click();
    await expect(
      page.getByText("Report failed; no partial report downloaded.", {
        exact: false,
      }),
    ).toBeVisible();
    expect(downloads).toEqual([]);
    await expect(
      page.getByRole("button", { name: "Download JSON" }),
    ).toBeEnabled();
  });

  test("activity deduplicates events, surfaces reconnects and closes at terminal replay", async ({
    page,
  }) => {
    await mockEventSource(page);
    await mockResults(page);
    await page.goto("/#/lab/current");
    await expect(
      page.getByRole("heading", { name: "Run activity", exact: true }),
    ).toBeVisible();
    await emit(page, null, "open");
    const event = {
      runId: "run-001",
      sequence: 1,
      type: "run_started",
      timestamp: "2024-01-03T12:00:00Z",
      data: { totalTasks: 4 },
    };
    await emit(page, event);
    await emit(page, event);
    await expect(page.locator(".lab-stream > div")).toHaveCount(1);
    await emit(page, null, "error");
    await expect(page.getByRole("alert")).toContainText("Last-Event-ID");
    await emit(page, null, "open");
    await emit(page, {
      ...event,
      sequence: 2,
      type: "run_failed",
      data: { completedTasks: 2, totalTasks: 4 },
    });
    await expect(page.getByText("Run failed (replayed)")).toBeVisible();
    await emit(page, null, "closed");
    await expect(page.getByText("Run failed (replayed)")).toBeVisible();
    expect(
      await page.evaluate(
        () =>
          (
            Reflect.get(window, "labSources") as () => { closed: boolean }[]
          )().at(-1)?.closed,
      ),
    ).toBe(true);
    await page
      .getByRole("combobox", { name: "Activity run" })
      .selectOption("run-002");
    await expect(page.locator(".lab-stream > div")).toHaveCount(0);
    await emit(page, { ...event, runId: "wrong-run" });
    await expect(page.getByRole("alert")).toContainText("invalid run event");
    await page.getByRole("button", { name: "Reconnect stream" }).click();
    await emit(page, {
      ...event,
      runId: "run-002",
      data: { totalTasks: "invalid" },
    });
    await expect(page.getByRole("alert")).toContainText("invalid run event");
  });

  test("returns to the classic dashboard without lab style leakage", async ({
    page,
  }) => {
    await mockResults(page);
    await page.goto("/#/lab");
    await expect(page.getByText("3 saved runs")).toBeVisible();
    await page.getByRole("link", { name: "Original dashboard" }).click();
    await expect(page.locator(".lab-root")).toHaveCount(0);
    expect(
      await page
        .locator("html")
        .evaluate((element) => getComputedStyle(element).colorScheme),
    ).toBe("dark");
  });

  test("native EventSource resumes via Last-Event-ID and terminates without reconnecting again", async ({
    page,
  }) => {
    await mockResults(page);
    const headers: string[] = [];
    const endpoint = "**/api/v1/runs/run-001/events";
    const server = createServer((request, response) => {
      const cursor = request.headers["last-event-id"];
      headers.push(Array.isArray(cursor) ? cursor.join(",") : (cursor ?? ""));
      const sequence = cursor === "1" ? 2 : 1;
      const event = {
        runId: "run-001",
        sequence,
        type: sequence === 1 ? "run_started" : "run_completed",
        timestamp: "2024-01-03T12:00:00Z",
        data: { totalTasks: 4, completedTasks: sequence === 1 ? 0 : 4 },
      };
      response.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-store",
        "Access-Control-Allow-Origin": "*",
        Connection: "close",
      });
      response.end(`retry: 50\nid: ${sequence}\ndata: ${JSON.stringify(event)}\n\n`);
    });
    try {
      const ready = once(server, "listening");
      server.listen(0, "127.0.0.1");
      await ready;
      const address = server.address();
      if (!address || typeof address === "string") throw new Error("SSE fixture did not bind a TCP port");
      // Observe the wire header rather than the interception snapshot.
      await page.route(endpoint, (route) => route.continue({
        url: `http://127.0.0.1:${address.port}/api/v1/runs/run-001/events`,
      }));
      await page.goto("/#/lab/current");
      await expect(page.getByText("Run completed (replayed)")).toBeVisible();
      await expect(page.getByText("4 / 4 stored tasks replayed")).toBeVisible();
      await expect(page.locator(".lab-stream > div")).toHaveCount(2);
      expect(headers).toEqual(["", "1"]);
      // Multiple advertised retry periods expose an incorrectly retained source.
      await page.waitForTimeout(200);
      expect(headers).toHaveLength(2);
      await page.getByRole("link", { name: "Run history" }).click();
      await page.waitForTimeout(200);
      expect(headers).toHaveLength(2);
    } finally {
      try {
        await page.unroute(endpoint);
      } finally {
        server.closeAllConnections();
        if (server.listening) {
          await new Promise<void>((resolve, reject) =>
            server.close((error) => error ? reject(error) : resolve()),
          );
        }
      }
    }
  });
});
