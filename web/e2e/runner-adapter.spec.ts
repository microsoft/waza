import { expect, test, type Page } from "@playwright/test";
import fixture from "./fixtures/runner-adapter.json" with { type: "json" };

function panel(page: Page) {
  return page.getByRole("region", { name: "External adapter dispatch" });
}

async function translate(page: Page, descriptor = fixture.descriptor) {
  await panel(page).getByRole("textbox", { name: "Work descriptor JSON" }).fill(descriptor);
  await panel(page).getByRole("button", { name: "Translate descriptor" }).click();
}

test.describe("External adapter workflow in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("translates, dispatches once through the public route and shows the returned manifest", async ({ page }) => {
    const translateBodies: string[] = [];
    const submissions: string[] = [];
    await page.route("**/api/v1/adapter/translate", (route) => {
      translateBodies.push(route.request().postData() ?? "");
      return route.fulfill({ json: fixture.translation });
    });
    await page.route("**/api/v1/submissions", (route) => {
      submissions.push(route.request().postData() ?? "");
      return route.fulfill({ body: JSON.stringify(fixture.reply), contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" } });
    });
    await translate(page);
    expect(translateBodies).toEqual([fixture.descriptor]);
    const translation = panel(page).getByRole("status", { name: "Adapter translation" });
    await expect(translation).toContainText("Translated submit for example-queue/item-0001");
    const call = translation.getByRole("definition");
    await expect(call.first()).toHaveText("waza runner submit");
    await expect(translation).toContainText("POST /api/v1/submissions");
    await expect(translation).toContainText("shard-a / selected, trial 2, attempt 2");
    await expect(translation).toContainText("never retries");

    await translation.getByRole("button", { name: "Dispatch through public contract" }).click();
    const result = panel(page).getByRole("status", { name: "Adapter result" });
    await expect(result).toContainText("Terminal (returned manifest) (exit 0)");
    await expect(result).toContainText(fixture.reply.completion.manifest_digest);
    await expect(result).toContainText("success");
    await expect(result).toContainText("Not configured (optional host hook)");
    expect(submissions).toEqual([fixture.translation.call.input]);
  });

  test("rejects scheduler fields without offering dispatch", async ({ page }) => {
    const rejected = {
      kind: "waza.adapter-translation", schema_version: "1.0", adapter: fixture.translation.adapter, status: "rejected",
      descriptor_sha256: "0".repeat(64),
      diagnostics: [{ code: "descriptor_field_unsupported", message: "work descriptor field \"priority\" is not part of the public contract" }],
    };
    let dispatched = false;
    await page.route("**/api/v1/adapter/translate", (route) => route.fulfill({ status: 422, json: rejected }));
    await page.route("**/api/v1/submissions", (route) => { dispatched = true; return route.abort(); });
    await translate(page, fixture.descriptor.replace("\"work_id\"", "\"priority\": 5, \"work_id\""));
    const translation = panel(page).getByRole("status", { name: "Adapter translation" });
    await expect(translation).toContainText("Descriptor rejected; nothing will be dispatched");
    await expect(translation.getByRole("list", { name: "Translation diagnostics" })).toContainText("descriptor_field_unsupported");
    await expect(translation.getByRole("button", { name: "Dispatch through public contract" })).toHaveCount(0);
    expect(dispatched).toBe(false);
  });

  test("reports a contradictory public reply as a contract error", async ({ page }) => {
    await page.route("**/api/v1/adapter/translate", (route) => route.fulfill({ json: fixture.translation }));
    await page.route("**/api/v1/submissions", (route) => route.fulfill({
      body: JSON.stringify(fixture.reply), contentType: "application/json", headers: { "X-Waza-Exit-Code": "4" },
    }));
    await translate(page);
    await panel(page).getByRole("button", { name: "Dispatch through public contract" }).click();
    const result = panel(page).getByRole("status", { name: "Adapter result" });
    await expect(result).toContainText("Contract error (reply unusable) (exit 2)");
    await expect(result.getByRole("list", { name: "Result diagnostics" })).toContainText("contradicts");
  });

  test("rejects an oversized public reply without retaining it", async ({ page }) => {
    await page.route("**/api/v1/adapter/translate", (route) => route.fulfill({ json: fixture.translation }));
    let calls = 0;
    await page.route("**/api/v1/submissions", (route) => {
      calls += 1;
      return route.fulfill({ body: " ".repeat((32 << 20) + 1), contentType: "application/json", headers: { "X-Waza-Exit-Code": "0" } });
    });
    await translate(page);
    await panel(page).getByRole("button", { name: "Dispatch through public contract" }).click();
    const result = panel(page).getByRole("status", { name: "Adapter result" });
    await expect(result).toContainText("Contract error (reply unusable) (exit 2)");
    await expect(result.getByRole("list", { name: "Result diagnostics" })).toContainText("exceeds");
    expect(calls).toBe(1);
  });

  test("treats a lineage reply with a nonzero exit or invalid body as a contract error", async ({ page }) => {
    const logical = { run: `run-${"a".repeat(64)}`, work: `trial-${"b".repeat(64)}` };
    const statusTranslation = {
      ...fixture.translation, operation: "status", summary: undefined,
      call: { command: ["runner", "lineage", "--run", logical.run, "--work", logical.work],
        http: { method: "GET", path: `/api/v1/lineage?run=${logical.run}&work=${logical.work}` } },
    };
    await page.route("**/api/v1/adapter/translate", (route) => route.fulfill({ json: statusTranslation }));
    const replies = [
      { exit: "2", message: "exited 2", body: { kind: "waza.execution-lineage", schema_version: "1.0", logical, attempts: [], decisions: [], diagnostics: [] } },
      { exit: "0", message: "published schema", body: { kind: "waza.execution-lineage", schema_version: "1.0", logical } },
    ];
    for (const reply of replies) {
      await page.unroute("**/api/v1/lineage**");
      await page.route("**/api/v1/lineage**", (route) => route.fulfill({
        body: JSON.stringify(reply.body), contentType: "application/json", headers: { "X-Waza-Exit-Code": reply.exit },
      }));
      await translate(page, JSON.stringify({ kind: "waza.adapter-work-descriptor", schema_version: "1.0", work_id: "w", operation: "status", logical }));
      await panel(page).getByRole("button", { name: "Dispatch through public contract" }).click();
      const result = panel(page).getByRole("status", { name: "Adapter result" });
      await expect(result).toContainText("Contract error (reply unusable) (exit 2)");
      await expect(result.getByRole("list", { name: "Result diagnostics" })).toContainText(reply.message);
    }
  });
});
