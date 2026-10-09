# Recorded compatibility examples

These three sanitized workflows preserve existing grader semantics without a
live model, credentials or a real external service. Each directory has
`task.yaml` and `cases.json`; each case records final output, tool events and/or
workspace file contents plus its known verdict.

| Workflow | Known-good | Deliberately-bad | Alternative-valid |
|---|---|---|---|
| [MCP lookup](mcp/task.yaml) | Correct item lookup arguments and JSON state | Claims the right answer after looking up the wrong item | Reordered JSON with extra source metadata |
| [CLI inspection](cli/task.yaml) | Read-only status command and ready answer | Calls configure instead of status | Different wording and extra call description |
| [Repository report](repository/task.yaml) | Required report with original source preserved | Changes original source | Adds report metadata without changing the source |

Run all nine cases, including the two-iteration verdict comparison:

```bash
NO_COLOR=1 go test ./internal/graders -run TestCompatibilityScenariosRepeatable -count=1
```

The test materializes repository file bytes in fresh temporary workspaces and
uses the production grader factory for all scenarios. It adapts recorded tool
events into the same digest/event inputs graders receive; it never launches
`compat-cli` or contacts `catalog_lookup`.

These are **recorded-output grading examples**, not runnable eval suites or
claims that a mock agent performed the workflow. Running a generic mock engine
against the tasks does not reproduce their recorded calls or files. A real
skill eval must provide an actual skill, appropriate executor, MCP/CLI fixtures
and isolated resources. See [the preservation inventory](../../docs/COMPATIBILITY.md)
for the full gate, existing mock transport tests and known limitations.
