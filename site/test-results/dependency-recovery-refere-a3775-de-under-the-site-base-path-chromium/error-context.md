# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: dependency-recovery.spec.ts >> reference pages link to the recovery guide under the site base path
- Location: e2e/dependency-recovery.spec.ts:15:1

# Error details

```
Test timeout of 30000ms exceeded.
```

```
Error: locator.click: Test timeout of 30000ms exceeded.
Call log:
  - waiting for locator('main').getByRole('link', { name: 'compatibility corpus', exact: true })

```

# Page snapshot

```yaml
- generic [active] [ref=f3e1]:
  - link "Skip to content" [ref=f3e2] [cursor=pointer]:
    - /url: "#_top"
  - generic [ref=f3e3]:
    - banner [ref=f3e4]:
      - generic [ref=f3e5]:
        - link "waza" [ref=f3e7] [cursor=pointer]:
          - /url: /waza/
        - navigation [ref=f3e9]:
          - link "Getting Started" [ref=f3e10] [cursor=pointer]:
            - /url: /waza/getting-started/
          - link "Guides" [ref=f3e11] [cursor=pointer]:
            - /url: /waza/guides/eval-yaml/
          - link "Reference" [ref=f3e12] [cursor=pointer]:
            - /url: /waza/reference/cli/
          - link "About" [ref=f3e13] [cursor=pointer]:
            - /url: /waza/about/
        - button "Search" [ref=f3e16] [cursor=pointer]:
          - generic [aria-hidden] [ref=f3e19]: Search
          - generic [ref=f3e20]:
            - generic [ref=f3e21]: Ctrl
            - generic [ref=f3e22]: K
        - generic [ref=f3e23]:
          - link "GitHub" [ref=f3e25] [cursor=pointer]:
            - /url: https://github.com/microsoft/waza
          - generic [ref=f3e30]:
            - generic [ref=f3e31]: Select theme
            - combobox "Select theme" [ref=f3e32] [cursor=pointer]:
              - option "Dark"
              - option "Light"
              - option "Auto" [selected]
    - navigation "Main":
      - list [ref=f3e36]:
        - listitem [ref=f3e37]:
          - link "Concepts and Learning Path" [ref=f3e38] [cursor=pointer]:
            - /url: /waza/concepts/
        - listitem [ref=f3e39]:
          - link "Quick Start" [ref=f3e40] [cursor=pointer]:
            - /url: /waza/quick-start/
        - listitem [ref=f3e41]:
          - link "Getting Started" [ref=f3e42] [cursor=pointer]:
            - /url: /waza/getting-started/
        - listitem [ref=f3e43]:
          - group [ref=f3e44]:
            - generic "Guides" [ref=f3e45] [cursor=pointer]
            - list [ref=f3e49]:
              - listitem [ref=f3e50]:
                - link "Writing Eval Specs" [ref=f3e51] [cursor=pointer]:
                  - /url: /waza/guides/eval-yaml/
              - listitem [ref=f3e52]:
                - link "Offline Eval Preflight" [ref=f3e53] [cursor=pointer]:
                  - /url: /waza/guides/preflight/
              - listitem [ref=f3e54]:
                - link "Dependency Recovery" [ref=f3e55] [cursor=pointer]:
                  - /url: /waza/guides/dependency-recovery/
              - listitem [ref=f3e56]:
                - link "Controlled comparisons" [ref=f3e57] [cursor=pointer]:
                  - /url: /waza/guides/controlled-comparisons/
              - listitem [ref=f3e58]:
                - link "Preserving Existing Workflows" [ref=f3e59] [cursor=pointer]:
                  - /url: /waza/guides/compatibility/
              - listitem [ref=f3e60]:
                - link "Evaluating Custom Agents" [ref=f3e61] [cursor=pointer]:
                  - /url: /waza/guides/custom-agents/
              - listitem [ref=f3e62]:
                - link "Validators & Graders" [ref=f3e63] [cursor=pointer]:
                  - /url: /waza/guides/graders/
              - listitem [ref=f3e64]:
                - link "Spec Verification" [ref=f3e65] [cursor=pointer]:
                  - /url: /waza/guides/spec-verify/
              - listitem [ref=f3e66]:
                - link "Token Limits" [ref=f3e67] [cursor=pointer]:
                  - /url: /waza/guides/token-limits/
              - listitem [ref=f3e68]:
                - link "Web Dashboard" [ref=f3e69] [cursor=pointer]:
                  - /url: /waza/guides/dashboard/
              - listitem [ref=f3e70]:
                - link "Dashboard Lab (Prototype)" [ref=f3e71] [cursor=pointer]:
                  - /url: /waza/guides/dashboard-lab/
              - listitem [ref=f3e72]:
                - link "Explore the Dashboard" [ref=f3e73] [cursor=pointer]:
                  - /url: /waza/guides/dashboard-explore/
              - listitem [ref=f3e74]:
                - link "CI/CD Integration" [ref=f3e75] [cursor=pointer]:
                  - /url: /waza/guides/ci-cd/
              - listitem [ref=f3e76]:
                - link "OpenTelemetry Tracing" [ref=f3e77] [cursor=pointer]:
                  - /url: /waza/guides/otel/
              - listitem [ref=f3e78]:
                - link "Adversarial Harness" [ref=f3e79] [cursor=pointer]:
                  - /url: /waza/guides/adversarial/
              - listitem [ref=f3e80]:
                - link "Snapshot & Replay" [ref=f3e81] [cursor=pointer]:
                  - /url: /waza/guides/snapshot-replay/
              - listitem [ref=f3e82]:
                - link "Azure Storage" [ref=f3e83] [cursor=pointer]:
                  - /url: /waza/guides/azure-storage/
        - listitem [ref=f3e84]:
          - group [ref=f3e85]:
            - generic "Reference" [ref=f3e86] [cursor=pointer]
            - list [ref=f3e90]:
              - listitem [ref=f3e91]:
                - link "CLI Commands" [ref=f3e92] [cursor=pointer]:
                  - /url: /waza/reference/cli/
              - listitem [ref=f3e93]:
                - link "Project Configuration" [ref=f3e94] [cursor=pointer]:
                  - /url: /waza/reference/waza-yaml/
              - listitem [ref=f3e95]:
                - link "Statistical Fields" [ref=f3e96] [cursor=pointer]:
                  - /url: /waza/reference/statistical-fields/
              - listitem [ref=f3e97]:
                - link "YAML Schema" [ref=f3e98] [cursor=pointer]:
                  - /url: /waza/reference/schema/
              - listitem [ref=f3e99]:
                - link "Rubric Library" [ref=f3e100] [cursor=pointer]:
                  - /url: /waza/reference/rubrics/
              - listitem [ref=f3e101]:
                - link "Schema Changes" [ref=f3e102] [cursor=pointer]:
                  - /url: /waza/reference/schema-changes/
              - listitem [ref=f3e103]:
                - link "Releases" [ref=f3e104] [cursor=pointer]:
                  - /url: /waza/reference/releases/
        - listitem [ref=f3e105]:
          - link "About" [ref=f3e106] [cursor=pointer]:
            - /url: /waza/about/
    - generic [ref=f3e108]:
      - complementary [ref=f3e109]:
        - navigation [ref=f3e114]:
          - heading "On this page" [level=2] [ref=f3e115]
          - list [ref=f3e116]:
            - listitem [ref=f3e117]:
              - link "Overview" [ref=f3e118] [cursor=pointer]:
                - /url: "#_top"
            - listitem [ref=f3e119]:
              - link "Run the examples" [ref=f3e120] [cursor=pointer]:
                - /url: "#run-the-examples"
            - listitem [ref=f3e121]:
              - link "Authoring boundaries" [ref=f3e122] [cursor=pointer]:
                - /url: "#authoring-boundaries"
      - main [ref=f3e124]:
        - heading "Deterministic Dependency Recovery" [level=1] [ref=f3e127]
        - generic [ref=f3e129]:
          - generic [ref=f3e130]:
            - paragraph [ref=f3e131]:
              - text: Scenario evals can use finite MCP and CLI response sequences to test how an agent handles controlled dependency failures. These are
              - strong [ref=f3e132]: authored mocked interactions
              - text: ", not replayed recordings or tests of a real CLI/server implementation."
            - generic [ref=f3e133]:
              - heading "Run the examples" [level=2] [ref=f3e134]
              - link "Section titled “Run the examples”" [ref=f3e135] [cursor=pointer]:
                - /url: "#run-the-examples"
            - paragraph [ref=f3e140]: The MCP suite covers a transient error followed by success, permanent error, permission denial, dependency-reported timeout, malformed application payload and unmatched arguments. The CLI suite covers a transient failure and one retry. Each task grades an exact outcome and a prohibited file side effect.
            - list [ref=f3e141]:
              - listitem [ref=f3e142]:
                - link "MCP eval and tasks" [ref=f3e143] [cursor=pointer]:
                  - /url: https://github.com/microsoft/waza/tree/main/examples/scenarios/recovery/mcp
              - listitem [ref=f3e144]:
                - link "CLI eval and task" [ref=f3e145] [cursor=pointer]:
                  - /url: https://github.com/microsoft/waza/tree/main/examples/scenarios/recovery/cli
            - paragraph [ref=f3e146]: "Run static validation without provider credentials:"
            - figure "Terminal window" [ref=f3e148]:
              - code [ref=f3e153]:
                - generic [ref=f3e154]: waza preflight examples/scenarios/recovery/mcp/eval.yaml
                - generic [ref=f3e156]: waza preflight examples/scenarios/recovery/cli/eval.yaml
              - button "Copy to clipboard" [ref=f3e159] [cursor=pointer]
            - paragraph [ref=f3e161]: "Execution invokes the configured Copilot SDK agent and therefore requires its normal runtime credentials:"
            - figure "Terminal window" [ref=f3e163]:
              - code [ref=f3e168]:
                - generic [ref=f3e169]: waza run examples/scenarios/recovery/mcp/eval.yaml
                - generic [ref=f3e171]: waza run examples/scenarios/recovery/cli/eval.yaml
              - button "Copy to clipboard" [ref=f3e174] [cursor=pointer]
            - paragraph [ref=f3e176]:
              - text: Scenario evals require a nonempty
              - code [ref=f3e177]: scenario
              - text: and exact
              - code [ref=f3e178]: "schemaVersion: \"2.0\""
              - text: . The finite
              - code [ref=f3e179]: sequence
              - text: field belongs to a matching MCP or CLI response; every call consumes one step, in order, and an exhausted sequence fails closed. Sequences contain at most 100 steps; each optional
              - code [ref=f3e180]: delay_ms
              - text: "is limited to 300,000. Sequence state is scoped to the task’s mock session/server instance. There is no automatic retry policy: the agent decides whether to call again, and the task graders decide whether its final behavior is acceptable."
            - generic [ref=f3e181]:
              - heading "Authoring boundaries" [level=2] [ref=f3e182]
              - link "Section titled “Authoring boundaries”" [ref=f3e183] [cursor=pointer]:
                - /url: "#authoring-boundaries"
            - list [ref=f3e188]:
              - listitem [ref=f3e189]:
                - text: A transient sequence can return an error first and a normal value next. The attempt record preserves that earlier error even if a later call succeeds. CLI details are in
                - code [ref=f3e190]: runs[].command_invocations
                - text: ; MCP errors/calls appear in the run transcript.
              - listitem [ref=f3e191]: A permanent error, permission denial, malformed application response, or unmatched call can be graded as an honest failure. A malformed application payload is not a malformed JSON-RPC frame.
              - listitem [ref=f3e192]:
                - text: An MCP
                - code [ref=f3e193]: error
                - text: containing “timeout” is a dependency-reported error, not transport cancellation.
                - code [ref=f3e194]: delay_ms
                - text: can model latency, but public MCP stdio does not promise per-request cancellation. Direct handler tests cover a canceled context; they do not certify process-pipe cancellation.
              - listitem [ref=f3e195]:
                - text: Unmatched MCP calls and exhausted sequences return tool errors and never fall through to a live MCP service. A CLI mock intercepts only its declared executable name; undeclared executables may resolve through the host
                - code [ref=f3e196]: PATH
                - text: . Task
                - code [ref=f3e197]: "command_mocks: []"
                - text: disables inherited command mocks.
              - listitem [ref=f3e198]: A mock overrides a same-named configured live MCP server. Other configured MCP servers remain live. Mocks do not restrict filesystem, process, remote or network access and are not a host sandbox.
            - paragraph [ref=f3e199]:
              - text: Per-run
              - code [ref=f3e200]: dependency_modes
              - text: in results schema
              - code [ref=f3e201]: "1.7"
              - text: records declared MCP servers and effective CLI mocks as
              - code [ref=f3e202]: live
              - text: or
              - code [ref=f3e203]: mocked
              - text: . It reports configuration, not actual invocation or verified component behavior. No
              - code [ref=f3e204]: recorded
              - text: mode is available. The dashboard shows these modes and captured mocked CLI attempts; its task detail is first-repetition evidence, not a complete repetition export.
            - paragraph [ref=f3e205]: These fixtures exercise the agent’s decisions against controlled responses. Use separate opt-in integration tests when the real CLI/server behavior, authentication, or remote service contract is what you need to verify.
          - generic [ref=f3e207]:
            - link [ref=f3e208] [cursor=pointer]:
              - /url: /waza/guides/preflight/
              - generic [ref=f3e211]: PreviousOffline Eval Preflight
            - link [ref=f3e212] [cursor=pointer]:
              - /url: /waza/guides/controlled-comparisons/
              - generic [ref=f3e215]: NextControlled comparisons
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
  11 |   await expect(page.locator("main a[href*='examples/scenarios/recovery/mcp/eval.yaml']")).toBeVisible();
  12 |   await expect(page.locator("main a[href*='examples/scenarios/recovery/cli/eval.yaml']")).toBeVisible();
  13 | });
  14 | 
  15 | test("reference pages link to the recovery guide under the site base path", async ({ page }) => {
  16 |   for (const reference of ["cli", "schema"]) {
  17 |     await page.goto(`/waza/reference/${reference}/`);
  18 |     await page.locator("main").getByRole("link", { name: "Dependency Recovery", exact: true }).click();
  19 |     await expect(page.getByRole("heading", { level: 1 })).toHaveText("Deterministic Dependency Recovery");
  20 |   }
> 21 |   await page.locator("main").getByRole("link", { name: "compatibility corpus", exact: true }).click();
     |                                                                                               ^ Error: locator.click: Test timeout of 30000ms exceeded.
  22 |   await expect(page).toHaveURL(/\/waza\/guides\/compatibility\/$/);
  23 | });
  24 | 
```