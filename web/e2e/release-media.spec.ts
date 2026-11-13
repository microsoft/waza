import { expect, test, type Browser, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, realpath, rename, rm, writeFile } from "node:fs/promises";
import { isAbsolute, join, relative, resolve, sep } from "node:path";
import lineage from "./fixtures/execution-lineage.json" with { type: "json" };
import checkpoints from "./fixtures/checkpoint-operations.json" with { type: "json" };

// Release-media drafts are written outside the repository (see docs/RELEASE-MEDIA.md);
// routine and docs-refresh runs never execute this file.
const out = process.env.WAZA_RELEASE_MEDIA_DIR ?? "";
test.skip(!out, "Set WAZA_RELEASE_MEDIA_DIR to an absolute path outside the repository.");
function outside(root: string, path: string) {
  const rel = relative(root, path);
  return rel === ".." || rel.startsWith(`..${sep}`) || isAbsolute(rel);
}
if (out && (!isAbsolute(out) || !outside(resolve(".."), out))) {
  throw new Error("WAZA_RELEASE_MEDIA_DIR must be an absolute path outside the repository.");
}
test.skip(({ browserName }) => browserName !== "chromium", "Release media is captured once, in Chromium.");
test.describe.configure({ mode: "serial" });

const landscape = { width: 1280, height: 800 };
const vertical = { width: 540, height: 960 };

interface Cue { start: number; end: number; text: string }

// Burns a caption bar into the recording and keeps the same text as a WebVTT cue
// that stays open until the next caption replaces it.
class Captions {
  private readonly cues: Cue[] = [];
  private readonly origin = Date.now();
  constructor(private readonly page: Page) {}

  async say(text: string, holdMs: number) {
    const start = (Date.now() - this.origin) / 1000;
    const previous = this.cues.at(-1);
    if (previous) previous.end = start;
    await this.page.evaluate((caption) => {
      let bar = document.getElementById("waza-release-media-caption");
      if (!bar) {
        bar = document.createElement("div");
        bar.id = "waza-release-media-caption";
        bar.setAttribute("aria-hidden", "true");
        Object.assign(bar.style, {
          position: "fixed", left: "16px", right: "16px", bottom: "48px", zIndex: "2147483647",
          padding: "14px 18px", borderRadius: "12px", background: "rgba(0,0,0,0.82)", color: "#fff",
          font: "600 22px/1.3 system-ui, sans-serif", textAlign: "center", pointerEvents: "none",
        });
        document.body.appendChild(bar);
      }
      bar.textContent = caption;
    }, text);
    await this.page.waitForTimeout(holdMs);
    this.cues.push({ start, end: (Date.now() - this.origin) / 1000, text });
  }

  vtt() {
    const stamp = (s: number) => new Date(s * 1000).toISOString().slice(11, 23);
    return `WEBVTT\n\n${this.cues.map((cue, i) => `${i + 1}\n${stamp(cue.start)} --> ${stamp(cue.end)}\n${cue.text}\n`).join("\n")}`;
  }

  transcript() {
    return `${this.cues.map((cue) => cue.text).join("\n")}\n`;
  }
}

async function scrollTo(page: Page, locator: ReturnType<Page["locator"]>) {
  await locator.evaluate((element) => element.scrollIntoView({ behavior: "smooth", block: "start" }));
  await page.waitForTimeout(600);
}

// Records one 9:16 clip and writes <name>.webm, <name>.vtt and <name>.txt.
async function clip(browser: Browser, name: string, journey: (page: Page, captions: Captions) => Promise<void>) {
  const context = await browser.newContext({ viewport: vertical, recordVideo: { dir: raw, size: vertical } });
  const page = await context.newPage();
  const captions = new Captions(page);
  await journey(page, captions);
  const video = page.video();
  await context.close();
  await rename(await video!.path(), join(out, `${name}.webm`));
  await writeFile(join(out, `${name}.vtt`), captions.vtt());
  await writeFile(join(out, `${name}.txt`), captions.transcript());
}

async function openRunner(page: Page) {
  await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
  await page.goto("/#/lab/runner");
  await expect(page.getByText("Experimental dashboard using your saved results.", { exact: false })).toBeVisible();
}

async function routeLineage(page: Page) {
  await page.route("**/api/v1/lineage**", (route) =>
    route.fulfill({ body: JSON.stringify(lineage), contentType: "application/json" }));
}

async function loadLineage(page: Page) {
  const panel = page.getByRole("region", { name: "Execution attempt lineage" });
  await panel.getByRole("textbox", { name: "Logical run ID" }).fill(lineage.logical.run);
  await panel.getByRole("textbox", { name: "Logical work ID" }).fill(lineage.logical.work);
  await panel.getByRole("button", { name: "Load attempt lineage" }).click();
  const result = panel.getByRole("status", { name: "Loaded attempt lineage" });
  await expect(result).toContainText("3 attempts, 4 decisions");
  return { panel, result };
}

const resumed = checkpoints.reused;

function checkpointPanel(page: Page) {
  return page.getByRole("region", { name: "Checkpoint creation and recovery" });
}

async function routeCheckpoint(page: Page) {
  const result = JSON.parse(resumed.response) as { exit_code: number };
  await page.route("**/api/v1/runner/validate", (route) => route.fulfill({ json: resumed.admission }));
  await page.route("**/api/v1/checkpoints**", (route) => route.fulfill({
    status: resumed.http_status, body: resumed.response, contentType: "application/json",
    headers: { "X-Waza-Exit-Code": String(result.exit_code) },
  }));
}

async function resumeCheckpoint(page: Page) {
  const panel = checkpointPanel(page);
  await panel.getByRole("textbox", { name: "Captured checkpoint submission (JSON)" }).fill(JSON.stringify(resumed.request, null, 2));
  await panel.getByRole("textbox", { name: "Checkpoint digest to resume" }).fill(resumed.path.split("/")[4]!);
  await panel.getByRole("button", { name: "Resume checkpoint", exact: true }).click();
  const result = panel.getByLabel("Checkpoint operation result", { exact: true });
  await expect(result).toContainText("evaluation_failure");
  return { panel, result };
}

let raw = "";
let aggregate: { inputs: { manifest_sha256: string; evidence_sha256?: string }[]; canonical: string };

async function routeAggregate(page: Page) {
  await page.route("**/api/v1/aggregates", (route) =>
    route.fulfill({ status: 409, contentType: "application/json", body: aggregate.canonical }));
}

async function buildAggregate(page: Page) {
  const panel = page.getByRole("region", { name: "Standalone manifest aggregation" });
  await panel.getByRole("textbox").fill(aggregate.inputs.map((input) =>
    `${input.manifest_sha256}${input.evidence_sha256 ? ` ${input.evidence_sha256}` : ""}`).join("\n"));
  await panel.getByRole("button", { name: "Build aggregate", exact: true }).click();
  const result = panel.getByRole("status", { name: "Evaluated manifest aggregate" });
  await expect(result).toContainText("Aggregate admission: conflicting");
  return { panel, result };
}

test.beforeAll(async () => {
  test.setTimeout(180_000);
  await mkdir(out, { recursive: true });
  if (!outside(await realpath(resolve("..")), await realpath(out))) {
    throw new Error("WAZA_RELEASE_MEDIA_DIR resolves inside the repository.");
  }
  raw = await mkdtemp(join(out, ".raw-"));
  // The aggregate report is produced by the Go aggregator, as in aggregate-evidence.spec.ts.
  const fixture = join(raw, "aggregate-evidence.json");
  execFileSync("go", ["test", "-count=1", "-run", "^TestEvidenceStatesLabFixture$",
    "./internal/manifestaggregate", "-args", "-aggregate-evidence-fixture-output", fixture], {
    cwd: resolve(".."), timeout: 150_000,
  });
  aggregate = JSON.parse(await readFile(fixture, "utf8"));
});

test.afterAll(async () => {
  if (raw) await rm(raw, { recursive: true, force: true });
});

test("screenshots: Lab opt-in boundaries, lineage, lifecycle, checkpoint resume and evidence", async ({ page }) => {
  await page.setViewportSize(landscape);
  await openRunner(page);
  await page.getByRole("button", { name: "What’s real?" }).click();
  const boundaries = page.getByRole("region", { name: "Prototype boundaries" });
  await expect(boundaries).toContainText("Real results. Explicit boundaries.");
  await page.screenshot({ path: join(out, "lab-opt-in-boundaries.png"), animations: "disabled" });
  await boundaries.getByRole("button", { name: "Close prototype information" }).click();

  await routeLineage(page);
  const { result: lineageResult } = await loadLineage(page);
  await lineageResult.getByRole("region", { name: "Ordered attempts" }).screenshot({
    path: join(out, "lineage-attempts-retries.png"), animations: "disabled" });
  await lineageResult.getByRole("region", { name: "Attempt lifecycle" }).screenshot({
    path: join(out, "attempt-lifecycle.png"), animations: "disabled" });
  await lineageResult.screenshot({ path: join(out, "lineage-full.png"), animations: "disabled" });

  await routeCheckpoint(page);
  const { result: checkpointResult } = await resumeCheckpoint(page);
  await checkpointResult.screenshot({ path: join(out, "checkpoint-resume.png"), animations: "disabled" });

  await routeAggregate(page);
  const { result: aggregateResult } = await buildAggregate(page);
  await aggregateResult.screenshot({ path: join(out, "aggregate-evidence-states.png"), animations: "disabled" });
});

test("video: retries, lineage and lifecycle", async ({ browser }) => {
  test.setTimeout(90_000);
  await clip(browser, "v0.41.0-lineage-lifecycle", async (page, captions) => {
    await openRunner(page);
    await captions.say("waza dashboard --lab: experimental views over your saved results", 3500);
    await routeLineage(page);
    const { panel, result } = await loadLineage(page);
    await scrollTo(page, panel);
    await captions.say("Waza v0.41.0: every retry is a new, recorded attempt", 4000);
    await scrollTo(page, result.getByRole("region", { name: "Ordered attempts" }));
    await captions.say("Attempts are ordered by parent, so you can trace each retry", 4500);
    await scrollTo(page, result.getByRole("list", { name: "Lineage decisions" }));
    await captions.say("Reuse decisions show when no new engine calls were made", 4500);
    const lifecycle = result.getByRole("region", { name: "Attempt lifecycle" });
    await scrollTo(page, lifecycle);
    await captions.say("Lifecycle: state, cancellation and deadline per attempt", 3500);
    await lifecycle.evaluate((element) => element.scrollTo({ left: element.scrollWidth, behavior: "smooth" }));
    await captions.say("Plus partial work and advisory retry eligibility", 3500);
    await captions.say("Sample data from a test fixture. Try it: waza dashboard --lab", 3500);
  });
});

test("video: checkpoint resume", async ({ browser }) => {
  test.setTimeout(90_000);
  await clip(browser, "v0.41.0-checkpoint-resume", async (page, captions) => {
    await openRunner(page);
    await routeCheckpoint(page);
    await scrollTo(page, checkpointPanel(page));
    await captions.say("Waza v0.41.0: checkpoint and resume interrupted evaluation work", 4000);
    const { result } = await resumeCheckpoint(page);
    await scrollTo(page, result);
    await captions.say("Resume checks compatibility before anything runs", 4500);
    await captions.say("A finished original is reused, even a failed one, so results stay honest", 5000);
    await page.mouse.wheel(0, 500);
    await captions.say("Zero new engine calls: the original outcome is preserved, not regraded", 4000);
    await captions.say("Sample data from a test fixture. Try it: waza dashboard --lab", 3500);
  });
});

test("video: evidence states in aggregates", async ({ browser }) => {
  test.setTimeout(90_000);
  await clip(browser, "v0.41.0-evidence-aggregation", async (page, captions) => {
    await openRunner(page);
    await routeAggregate(page);
    await scrollTo(page, page.getByRole("region", { name: "Standalone manifest aggregation" }));
    await captions.say("Waza v0.41.0: aggregate results across runs without hiding gaps", 4000);
    const { result } = await buildAggregate(page);
    await scrollTo(page, result);
    await captions.say("Conflicting sources are flagged in the admission result", 4000);
    await scrollTo(page, result.getByLabel("Evidence state legend"));
    await captions.say("Each category is present, unavailable, incomplete, redacted or conflicting", 5000);
    await scrollTo(page, result.getByRole("list", { name: "Evidence lineage" }).first());
    await captions.say("Resumed members link to their retained evidence lineage", 4500);
    await captions.say("Report from a generated test fixture. Try it: waza dashboard --lab", 3500);
  });
});
