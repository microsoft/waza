# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: dependency-recovery.spec.ts >> dependency recovery guide states public support and safety boundaries
- Location: e2e/dependency-recovery.spec.ts:3:1

# Error details

```
Error: expect(locator).toBeVisible() failed

Locator: locator('main a[href*=\'examples/scenarios/recovery/mcp/eval.yaml\']')
Expected: visible
Timeout: 5000ms
Error: element(s) not found

Call log:
  - Expect "toBeVisible" locator('main a[href*=\'examples/scenarios/recovery/mcp/eval.yaml\']') with timeout 5000ms
  - waiting for locator('main a[href*=\'examples/scenarios/recovery/mcp/eval.yaml\']')

```

```yaml
- link "Skip to content":
  - /url: "#_top"
- banner:
  - link "waza":
    - /url: /waza/
  - navigation:
    - link "Getting Started":
      - /url: /waza/getting-started/
    - link "Guides":
      - /url: /waza/guides/eval-yaml/
    - link "Reference":
      - /url: /waza/reference/cli/
    - link "About":
      - /url: /waza/about/
  - button "Search": Ctrl K
  - link "GitHub":
    - /url: https://github.com/microsoft/waza
  - text: Select theme
  - combobox "Select theme":
    - option "Dark"
    - option "Light"
    - option "Auto" [selected]
- navigation "Main":
  - list:
    - listitem:
      - link "Concepts and Learning Path":
        - /url: /waza/concepts/
    - listitem:
      - link "Quick Start":
        - /url: /waza/quick-start/
    - listitem:
      - link "Getting Started":
        - /url: /waza/getting-started/
    - listitem:
      - group:
        - text: Guides
        - list:
          - listitem:
            - link "Writing Eval Specs":
              - /url: /waza/guides/eval-yaml/
          - listitem:
            - link "Offline Eval Preflight":
              - /url: /waza/guides/preflight/
          - listitem:
            - link "Dependency Recovery":
              - /url: /waza/guides/dependency-recovery/
          - listitem:
            - link "Controlled comparisons":
              - /url: /waza/guides/controlled-comparisons/
          - listitem:
            - link "Preserving Existing Workflows":
              - /url: /waza/guides/compatibility/
          - listitem:
            - link "Evaluating Custom Agents":
              - /url: /waza/guides/custom-agents/
          - listitem:
            - link "Validators & Graders":
              - /url: /waza/guides/graders/
          - listitem:
            - link "Spec Verification":
              - /url: /waza/guides/spec-verify/
          - listitem:
            - link "Token Limits":
              - /url: /waza/guides/token-limits/
          - listitem:
            - link "Web Dashboard":
              - /url: /waza/guides/dashboard/
          - listitem:
            - link "Dashboard Lab (Prototype)":
              - /url: /waza/guides/dashboard-lab/
          - listitem:
            - link "Explore the Dashboard":
              - /url: /waza/guides/dashboard-explore/
          - listitem:
            - link "CI/CD Integration":
              - /url: /waza/guides/ci-cd/
          - listitem:
            - link "OpenTelemetry Tracing":
              - /url: /waza/guides/otel/
          - listitem:
            - link "Adversarial Harness":
              - /url: /waza/guides/adversarial/
          - listitem:
            - link "Snapshot & Replay":
              - /url: /waza/guides/snapshot-replay/
          - listitem:
            - link "Azure Storage":
              - /url: /waza/guides/azure-storage/
    - listitem:
      - group:
        - text: Reference
        - list:
          - listitem:
            - link "CLI Commands":
              - /url: /waza/reference/cli/
          - listitem:
            - link "Project Configuration":
              - /url: /waza/reference/waza-yaml/
          - listitem:
            - link "Statistical Fields":
              - /url: /waza/reference/statistical-fields/
          - listitem:
            - link "YAML Schema":
              - /url: /waza/reference/schema/
          - listitem:
            - link "Rubric Library":
              - /url: /waza/reference/rubrics/
          - listitem:
            - link "Schema Changes":
              - /url: /waza/reference/schema-changes/
          - listitem:
            - link "Releases":
              - /url: /waza/reference/releases/
    - listitem:
      - link "About":
        - /url: /waza/about/
- complementary:
  - navigation "On this page":
    - heading "On this page" [level=2]
    - list:
      - listitem:
        - link "Overview":
          - /url: "#_top"
      - listitem:
        - link "Run the examples":
          - /url: "#run-the-examples"
      - listitem:
        - link "Authoring boundaries":
          - /url: "#authoring-boundaries"
- main:
  - heading "Deterministic Dependency Recovery" [level=1]
  - paragraph:
    - text: Scenario evals can use finite MCP and CLI response sequences to test how an agent handles controlled dependency failures. These are
    - strong: authored mocked interactions
    - text: ", not replayed recordings or tests of a real CLI/server implementation."
  - heading "Run the examples" [level=2]
  - link "Section titled “Run the examples”":
    - /url: "#run-the-examples"
  - paragraph: The MCP suite covers a transient error followed by success, permanent error, permission denial, dependency-reported timeout, malformed application payload and unmatched arguments. The CLI suite covers a transient failure and one retry. Each task grades an exact outcome and a prohibited file side effect.
  - list:
    - listitem:
      - link "MCP eval and tasks":
        - /url: https://github.com/microsoft/waza/tree/main/examples/scenarios/recovery/mcp
    - listitem:
      - link "CLI eval and task":
        - /url: https://github.com/microsoft/waza/tree/main/examples/scenarios/recovery/cli
  - paragraph: "Run static validation without provider credentials:"
  - figure "Terminal window":
    - text: Terminal window
    - code: waza preflight examples/scenarios/recovery/mcp/eval.yaml waza preflight examples/scenarios/recovery/cli/eval.yaml
    - button "Copy to clipboard"
  - paragraph: "Execution invokes the configured Copilot SDK agent and therefore requires its normal runtime credentials:"
  - figure "Terminal window":
    - text: Terminal window
    - code: waza run examples/scenarios/recovery/mcp/eval.yaml waza run examples/scenarios/recovery/cli/eval.yaml
    - button "Copy to clipboard"
  - paragraph:
    - text: Scenario evals require a nonempty
    - code: scenario
    - text: and exact
    - code: "schemaVersion: \"2.0\""
    - text: . The finite
    - code: sequence
    - text: field belongs to a matching MCP or CLI response; every call consumes one step, in order, and an exhausted sequence fails closed. Sequences contain at most 100 steps; each optional
    - code: delay_ms
    - text: "is limited to 300,000. Sequence state is scoped to the task’s mock session/server instance. There is no automatic retry policy: the agent decides whether to call again, and the task graders decide whether its final behavior is acceptable."
  - heading "Authoring boundaries" [level=2]
  - link "Section titled “Authoring boundaries”":
    - /url: "#authoring-boundaries"
  - list:
    - listitem:
      - text: A transient sequence can return an error first and a normal value next. The attempt record preserves that earlier error even if a later call succeeds. CLI details are in
      - code: runs[].command_invocations
      - text: ; MCP errors/calls appear in the run transcript.
    - listitem: A permanent error, permission denial, malformed application response, or unmatched call can be graded as an honest failure. A malformed application payload is not a malformed JSON-RPC frame.
    - listitem:
      - text: An MCP
      - code: error
      - text: containing “timeout” is a dependency-reported error, not transport cancellation.
      - code: delay_ms
      - text: can model latency, but public MCP stdio does not promise per-request cancellation. Direct handler tests cover a canceled context; they do not certify process-pipe cancellation.
    - listitem:
      - text: Unmatched MCP calls and exhausted sequences return tool errors and never fall through to a live MCP service. A CLI mock intercepts only its declared executable name; undeclared executables may resolve through the host
      - code: PATH
      - text: . Task
      - code: "command_mocks: []"
      - text: disables inherited command mocks.
    - listitem: A mock overrides a same-named configured live MCP server. Other configured MCP servers remain live. Mocks do not restrict filesystem, process, remote or network access and are not a host sandbox.
  - paragraph:
    - text: Per-run
    - code: dependency_modes
    - text: in results schema
    - code: "1.7"
    - text: records declared MCP servers and effective CLI mocks as
    - code: live
    - text: or
    - code: mocked
    - text: . It reports configuration, not actual invocation or verified component behavior. No
    - code: recorded
    - text: mode is available. The dashboard shows these modes and captured mocked CLI attempts; its task detail is first-repetition evidence, not a complete repetition export.
  - paragraph: These fixtures exercise the agent’s decisions against controlled responses. Use separate opt-in integration tests when the real CLI/server behavior, authentication, or remote service contract is what you need to verify.
  - link "Previous Offline Eval Preflight":
    - /url: /waza/guides/preflight/
  - link "Next Controlled comparisons":
    - /url: /waza/guides/controlled-comparisons/
```

# Test source

```ts
  1  | import { test, expect } from "@playwright/test";
  2  | 
  3  | test("dependency recovery guide states public support and safety boundaries", async ({ page }) => {
  4  |   await page.goto("/waza/guides/dependency-recovery/");
  5  |   await expect(page.getByRole("heading", { level: 1 })).toHaveText("Deterministic Dependency Recovery");
  6  |   await expect(page.locator("main")).toContainText("authored mocked interactions");
  7  |   await expect(page.locator("main")).toContainText("does not promise per-request cancellation");
  8  |   await expect(page.locator("main")).toContainText("undeclared executables may resolve through the host PATH");
  9  |   await expect(page.locator("main")).toContainText("not actual invocation or verified component behavior");
  10 |   await expect(page.locator("main")).toContainText("prohibited file side effect");
> 11 |   await expect(page.locator("main a[href*='examples/scenarios/recovery/mcp/eval.yaml']")).toBeVisible();
     |                                                                                           ^ Error: expect(locator).toBeVisible() failed
  12 |   await expect(page.locator("main a[href*='examples/scenarios/recovery/cli/eval.yaml']")).toBeVisible();
  13 | });
  14 | 
  15 | test("reference pages link to the recovery guide under the site base path", async ({ page }) => {
  16 |   for (const reference of ["cli", "schema"]) {
  17 |     await page.goto(`/waza/reference/${reference}/`);
  18 |     await page.locator("main").getByRole("link", { name: "Dependency Recovery", exact: true }).click();
  19 |     await expect(page.getByRole("heading", { level: 1 })).toHaveText("Deterministic Dependency Recovery");
  20 |   }
  21 |   await page.locator("main").getByRole("link", { name: "compatibility corpus", exact: true }).click();
  22 |   await expect(page).toHaveURL(/\/waza\/guides\/compatibility\/$/);
  23 | });
  24 | 
```