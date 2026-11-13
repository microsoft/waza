import { expect, test, type Page } from "@playwright/test";
import rejected from "./fixtures/isolation-rejected.json" with { type: "json" };

// Captured from `waza isolation preflight` with a host declaration whose
// subprocess claim the conformance probe disproved.
type Report = typeof rejected;

function admitted(): Report {
  const report = structuredClone(rejected);
  report.decision = "admitted";
  report.dimensions = report.dimensions.map((entry) => {
    if (entry.dimension === "network") {
      return {
        ...entry, available: true, effective: "restricted", enforcement_source: "host-declaration",
        probes: [{ provider: "host-declaration", outcome: "enforced", detail: "no non-loopback network interface is up and connections to 1.1.1.1:443 failed; loopback is not covered" }],
        diagnostic: undefined,
      };
    }
    return { ...entry, requested: "unrestricted", effective: "unrestricted", probes: [], diagnostic: {
      code: "no_provider", message: `no available provider claims to enforce the ${entry.dimension} boundary`,
    } };
  }) as Report["dimensions"];
  return JSON.parse(JSON.stringify(report)) as Report;
}

function panel(page: Page) {
  return page.getByRole("region", { name: "Execution isolation" });
}

async function inspect(page: Page, body: unknown) {
  await panel(page).getByLabel("Isolation preflight report (JSON)").fill(typeof body === "string" ? body : JSON.stringify(body));
  await panel(page).getByRole("button", { name: "Inspect isolation report" }).click();
}

test.describe("Execution isolation in Lab", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/lab/runs", (route) => route.fulfill({ json: [] }));
    await page.goto("/#/lab/runner");
  });

  test("shows requested and effective isolation with unavailable diagnostics for a rejected preflight", async ({ page }) => {
    await inspect(page, rejected);
    const result = panel(page).getByRole("status", { name: "Isolation preflight result" });
    await expect(result).toContainText("Preflight rejected: execution must not start");
    const table = result.getByRole("table", { name: "Isolation capabilities" });
    await expect(table.getByRole("row", { name: /Subprocesses/ })).toContainText("UNAVAILABLE (rejected)");
    await expect(table.getByRole("row", { name: /Subprocesses/ })).toContainText("host-declaration: violated");
    await expect(table.getByRole("row", { name: /Environment and secrets/ })).toContainText("Unrestricted");
    await expect(result.getByRole("list", { name: "Isolation providers" })).toContainText("copilot-native-sandbox: absent");
    const diagnostics = result.getByRole("list", { name: "Isolation diagnostics" });
    await expect(diagnostics).toContainText("Subprocesses: probe_violated");
    await expect(diagnostics).toContainText("Network: probe_inconclusive");
    await expect(diagnostics).toContainText("Filesystem: no_provider");
    await expect(result).not.toContainText("Restricted (enforced)");
  });

  test("shows an admitted preflight with its enforcement source", async ({ page }) => {
    await inspect(page, admitted());
    const result = panel(page).getByRole("status", { name: "Isolation preflight result" });
    await expect(result).toContainText("Preflight admitted: execution may start");
    const network = result.getByRole("table", { name: "Isolation capabilities" }).getByRole("row", { name: /Network/ });
    await expect(network).toContainText("Restricted (enforced)");
    await expect(network).toContainText("host-declaration: enforced");
  });

  test("fails closed when a report claims a boundary without probe evidence", async ({ page }) => {
    const forged = admitted();
    const network = forged.dimensions.find((entry) => entry.dimension === "network")!;
    network.probes = [{ provider: "host-declaration", outcome: "inconclusive", detail: "refused" }];
    await inspect(page, forged);
    await expect(panel(page).getByRole("alert")).toContainText("shown as enforced without a passing conformance probe");
    await expect(panel(page).getByRole("status", { name: "Isolation preflight result" })).toHaveCount(0);
  });

  test("rejects duplicate keys and wrong document kinds", async ({ page }) => {
    await inspect(page, JSON.stringify(rejected).replace('"decision":"rejected"', '"decision":"admitted","decision":"rejected"'));
    await expect(panel(page).getByRole("alert")).toContainText("Ambiguous or malformed JSON.");
    await inspect(page, { ...rejected, kind: "waza.execution-isolation-request" });
    await expect(panel(page).getByRole("alert")).toContainText("Not a valid waza.execution-isolation-preflight 1.0 report.");
    await inspect(page, { ...admitted(), checked_at: "not-a-date" });
    await expect(panel(page).getByRole("alert")).toContainText("Not a valid waza.execution-isolation-preflight 1.0 report.");
    await expect(panel(page).getByRole("status", { name: "Isolation preflight result" })).toHaveCount(0);
  });
});
