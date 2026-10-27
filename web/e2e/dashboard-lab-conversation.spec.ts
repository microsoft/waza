import { expect, test, type Page } from "@playwright/test";
import type { RunDetail, TranscriptEvent } from "../src/api/client";
import { CONVERSATION_DETAIL, CONVERSATION_SUMMARY } from "./fixtures/conversation-data";

async function openConversation(page: Page, detail: RunDetail = CONVERSATION_DETAIL) {
  await page.route("**/api/v1/lab/runs", (route) =>
    route.fulfill({ json: [{
      ...CONVERSATION_SUMMARY,
      taskCount: detail.tasks.length,
      passCount: detail.tasks.filter((task) => task.outcome === "passed").length,
    }] }),
  );
  await page.route("**/api/runs/conversation", (route) =>
    route.fulfill({ json: detail }),
  );
  await page.goto("/#/lab/history");
  await page.getByRole("button", { name: "conversation-review", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("tab", { name: "Conversation", exact: true }).click();
  return dialog;
}

test.describe("Lab conversation", () => {
  test("reads full ordered messages through hundreds of events without expansion", async ({ page }) => {
    const recorded = CONVERSATION_DETAIL.tasks[0]!;
    const noise: TranscriptEvent[] = Array.from({ length: 300 }, (_, index) => ({
      type: "tool.execution_start", toolCallId: `noise-${index}`,
      toolName: "read_file", arguments: { path: "NOISE_NOT_A_MESSAGE" },
    }));
    const longMessage = `${"Full message content must remain readable. ".repeat(80)}\nFINAL_LINE_VISIBLE`;
    const transcript = [
      ...recorded.transcript!.slice(0, 1), ...noise,
      ...recorded.transcript!.slice(1),
      { type: "assistant.reasoning", content: "REASONING_NOT_A_MESSAGE" },
      { type: "system.message", content: "SYSTEM_NOT_A_MESSAGE" },
      { type: "assistant.message_delta", content: "STREAMING_FRAGMENT_NOT_A_MESSAGE" },
      { type: "assistant.message", content: longMessage },
    ];
    expect(transcript.length).toBeGreaterThan(300);
    const dialog = await openConversation(page, {
      ...CONVERSATION_DETAIL, tasks: [{ ...recorded, transcript }],
    });
    const messages = dialog.getByRole("list", { name: "Recorded conversation" }).getByRole("listitem");
    const expected = transcript.filter((event) =>
      event.type === "user.message" || event.type === "assistant.message",
    );
    expect(expected).toHaveLength(7);
    await expect(messages).toHaveCount(expected.length);
    for (const [index, event] of expected.entries()) {
      await expect(messages.nth(index).locator(".lab-message-content")).toHaveText(event.content!, { useInnerText: false });
      await expect(messages.nth(index).getByText(
        event.type === "user.message" ? "User" : "Agent under test", { exact: true },
      )).toBeVisible();
    }
    await expect(dialog).toContainText("Responder recorded 2 simulated-user follow-ups.");
    await expect(dialog).toContainText("Individual user messages do not record human or simulated provenance.");
    await expect(dialog).toContainText("Showing the first repetition in recorded order.");
    await expect(dialog).not.toContainText(/NOISE_NOT_A_MESSAGE|TOOL_OUTPUT_NOT_A_MESSAGE|STREAMING_FRAGMENT_NOT_A_MESSAGE|REASONING_NOT_A_MESSAGE|SYSTEM_NOT_A_MESSAGE/);
    await expect(dialog.getByRole("button", { name: /Show details/ })).toHaveCount(0);
    await expect(messages.last().locator(".lab-message-content")).toHaveCSS("white-space", "pre-wrap");
    const dimensions = await messages.last().locator(".lab-message-content").evaluate((element) => ({
      scroll: element.scrollHeight, client: element.clientHeight,
    }));
    expect(dimensions.scroll).toBeLessThanOrEqual(dimensions.client + 1);
    await dialog.getByRole("tab", { name: "Trajectory", exact: true }).click();
    await expect(dialog.getByRole("heading", { name: /Trajectory/ })).toBeVisible();
  });

  test("does not reconstruct a conversation from prompt or grader output", async ({ page }) => {
    const detail: RunDetail = {
      ...CONVERSATION_DETAIL,
      tasks: [
        { ...CONVERSATION_DETAIL.tasks[0]!, id: "absent", transcript: undefined, prompt: "PROMPT_IS_NOT_A_TRANSCRIPT" },
        { ...CONVERSATION_DETAIL.tasks[0]!, id: "empty", transcript: [] },
        { ...CONVERSATION_DETAIL.tasks[0]!, id: "tools", transcript: [{ type: "tool.execution_start", toolName: "read_file" }] },
      ],
    };
    const dialog = await openConversation(page, detail);
    const task = dialog.getByRole("combobox", { name: "Task", exact: true });
    for (const id of ["absent", "empty", "tools"]) {
      await task.selectOption(`id:${id}`);
      await expect(dialog).toContainText("No recorded conversation messages for this task.");
      await expect(dialog.getByRole("list", { name: "Recorded conversation" })).toHaveCount(0);
      await expect(dialog).not.toContainText("PROMPT_IS_NOT_A_TRANSCRIPT");
    }
  });

  test("does not infer text presence or provenance lost by the API", async ({ page }) => {
    const dialog = await openConversation(page);
    await dialog.getByRole("combobox", { name: "Task", exact: true }).selectOption("id:empty-messages");
    const messages = dialog.getByRole("list", { name: "Recorded conversation" }).getByRole("listitem");
    await expect(messages).toHaveCount(2);
    await expect(messages.nth(0).getByText("User", { exact: true })).toBeVisible();
    await expect(messages.nth(1).getByText("Agent under test", { exact: true })).toBeVisible();
    await expect(dialog.getByText("No message text recorded.", { exact: true })).toHaveCount(2);
    await expect(dialog).not.toContainText("Responder recorded");
  });

  test("preserves whitespace and renders message text without inventing pairings or HTML", async ({ page }) => {
    const dialog = await openConversation(page, {
      ...CONVERSATION_DETAIL,
      tasks: [{
        ...CONVERSATION_DETAIL.tasks[0]!, responder: undefined,
        transcript: [
          { type: "assistant.message", content: "Agent message without a recorded initial user." },
          { type: "assistant.message", content: "  \n" },
          { type: "user.message", content: "<script>unsafe()</script>" },
        ],
      }],
    });
    const messages = dialog.getByRole("list", { name: "Recorded conversation" }).getByRole("listitem");
    await expect(messages).toHaveCount(3);
    await expect(messages.nth(0)).toContainText("Agent under test");
    expect(await messages.nth(1).locator(".lab-message-content").textContent()).toBe("  \n");
    expect(await messages.nth(2).locator(".lab-message-content").textContent()).toBe("<script>unsafe()</script>");
    await expect(dialog.locator("script")).toHaveCount(0);
    await expect(dialog).not.toContainText("Responder recorded");
    await expect(dialog.getByText("User", { exact: true })).toHaveCount(1);
  });

  test("switches tasks and refreshes the mounted conversation without stale messages", async ({ page }) => {
    let detail: RunDetail = {
      ...CONVERSATION_DETAIL,
      tasks: [
        CONVERSATION_DETAIL.tasks[0]!,
        { ...CONVERSATION_DETAIL.tasks[0]!, id: "other", name: "Other task", transcript: [{ type: "user.message", content: "OTHER_TASK_MESSAGE" }] },
      ],
    };
    const dialog = await openConversation(page, detail);
    await page.route("**/api/runs/conversation", (route) => route.fulfill({ json: detail }));
    await expect(dialog).toContainText("Please review the deployment plan.");
    await dialog.getByRole("combobox", { name: "Task", exact: true }).selectOption("id:other");
    await expect(dialog).toContainText("OTHER_TASK_MESSAGE");
    await expect(dialog).not.toContainText("Please review the deployment plan.");
    detail = {
      ...detail,
      tasks: [{ ...detail.tasks[1]!, transcript: [{ type: "assistant.message", content: "REFRESHED_TASK_MESSAGE" }] }],
    };
    await page.getByRole("button", { name: "Refresh results", exact: true, includeHidden: true })
      .evaluate((element: HTMLButtonElement) => element.click());
    await expect(dialog).toContainText("REFRESHED_TASK_MESSAGE");
    await expect(dialog).not.toContainText("OTHER_TASK_MESSAGE");
    await expect(dialog.getByRole("tab", { name: "Conversation", exact: true })).toHaveAttribute("aria-selected", "true");
  });

  test("supports keyboard selection and wraps tabs and long text on narrow screens", async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 800 });
    const dialog = await openConversation(page, {
      ...CONVERSATION_DETAIL,
      tasks: [{
        ...CONVERSATION_DETAIL.tasks[0]!,
        transcript: [{ type: "user.message", content: "unbroken".repeat(150) }],
      }],
    });
    const conversation = dialog.getByRole("tab", { name: "Conversation", exact: true });
    await conversation.focus();
    await page.keyboard.press("Home");
    await expect(dialog.getByRole("tab", { name: "Grader evidence", exact: true })).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(conversation).toBeFocused();
    await expect(conversation).toHaveAttribute("aria-selected", "true");
    await expect(dialog.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", "lab-evidence-tab-conversation");
    await page.keyboard.press("ArrowRight");
    await expect(dialog.getByRole("tab", { name: "Trajectory", exact: true })).toBeFocused();
    await page.keyboard.press("ArrowLeft");
    await expect(conversation).toBeFocused();
    const overflow = await dialog.evaluate((element) => element.scrollWidth > element.clientWidth);
    expect(overflow).toBe(false);
    await expect(dialog.locator(".lab-tabs")).toHaveCSS("flex-wrap", "wrap");
    await page.keyboard.press("End");
    await expect(dialog.getByRole("tab", { name: "Recorded evidence", exact: true })).toBeFocused();
    await page.keyboard.press("ArrowRight");
    await expect(dialog.getByRole("tab", { name: "Grader evidence", exact: true })).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(dialog).not.toBeVisible();
    await expect(page.getByRole("button", { name: "conversation-review", exact: true })).toBeFocused();
  });

  test("never presents the synthetic demo as a recorded conversation", async ({ page }) => {
    await page.goto("/#/lab/demo");
    await page.getByRole("button", { name: "azure-deploy", exact: true }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("tab", { name: "Conversation", exact: true }).click();
    await expect(dialog).toContainText("The synthetic demo has no recorded conversation.");
    await expect(dialog.getByRole("list", { name: "Recorded conversation" })).toHaveCount(0);
  });
});
