import { test, expect } from "@playwright/test";
import { RUNS } from "./fixtures/mock-data";

const liveEvents = [
  {
    schemaVersion: "1.0",
    sequence: 1,
    runId: "run-001",
    type: "run_started",
    data: { totalTasks: 1, spec: "code-explainer", model: "gpt-4o" },
    timestamp: "2026-06-30T13:00:00Z",
  },
  {
    schemaVersion: "1.0",
    sequence: 2,
    runId: "run-001",
    type: "task_started",
    data: { taskName: "explain-fibonacci", totalTasks: 1 },
    timestamp: "2026-06-30T13:00:01Z",
  },
  {
    schemaVersion: "1.0",
    sequence: 3,
    runId: "run-001",
    type: "step_executed",
    data: {
      taskName: "explain-fibonacci",
      graderName: "output-exists",
      graderType: "code",
      passed: true,
      score: 1,
      message: "Output file found",
    },
    timestamp: "2026-06-30T13:00:02Z",
  },
  {
    schemaVersion: "1.0",
    sequence: 4,
    runId: "run-001",
    type: "task_completed",
    data: {
      taskName: "explain-fibonacci",
      outcome: "passed",
      score: 1,
      totalTasks: 1,
      completedTasks: 1,
    },
    timestamp: "2026-06-30T13:00:03Z",
  },
  {
    schemaVersion: "1.0",
    sequence: 5,
    runId: "run-001",
    type: "run_completed",
    data: {
      outcome: "passed",
      totalTasks: 1,
      passCount: 1,
      failCount: 0,
      tokens: 12400,
      cost: 1.24,
    },
    timestamp: "2026-06-30T13:00:04Z",
  },
];

declare global {
  interface Window {
    __eventSourceUrls: string[];
  }
}

test.describe("Live View", () => {
  test("renders progress from run SSE events", async ({ page }) => {
    await page.route(/\/api\/runs(\?|$)/, (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(RUNS.slice(0, 1)),
      }),
    );

    await page.addInitScript((events) => {
      window.__eventSourceUrls = [];

      class MockEventSource {
        static readonly CONNECTING = 0;
        static readonly OPEN = 1;
        static readonly CLOSED = 2;

        readonly url: string;
        readyState = MockEventSource.CONNECTING;
        onopen: ((event: Event) => void) | null = null;
        onmessage: ((event: MessageEvent<string>) => void) | null = null;
        onerror: ((event: Event) => void) | null = null;

        constructor(url: string) {
          this.url = url;
          window.__eventSourceUrls.push(url);
          setTimeout(() => {
            this.readyState = MockEventSource.OPEN;
            this.onopen?.(new Event("open"));
            for (const event of events) {
              this.onmessage?.(
                new MessageEvent("message", {
                  data: JSON.stringify(event),
                  lastEventId: String(event.sequence),
                }),
              );
            }
          }, 0);
        }

        close() {
          this.readyState = MockEventSource.CLOSED;
        }

        addEventListener() {}
        removeEventListener() {}
        dispatchEvent() {
          return true;
        }
      }

      window.EventSource = MockEventSource as unknown as typeof EventSource;
    }, liveEvents);

    await page.goto("/#/live");

    await expect
      .poll(() => page.evaluate(() => window.__eventSourceUrls[0] ?? ""))
      .toBe("/api/v1/runs/run-001/events");
    await expect(page.getByRole("heading", { name: "Live" })).toBeVisible();
    await expect(page.getByText("run_completed")).toBeVisible();
    await expect(page.getByText("Run complete — 1/1 passed")).toBeVisible();
    await expect(page.getByText("step_executed")).toBeVisible();
    await expect(page.getByText("output-exists [code]: ✓ Output file found")).toBeVisible();
    await expect(page.getByText("12.4K")).toBeVisible();
  });
});
