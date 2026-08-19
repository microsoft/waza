# Custom Agent Eval Example

This example demonstrates how to evaluate a **custom agent** (`.agent.md`) using waza.

## What's in this example

| File | Purpose |
|------|---------|
| `security-reviewer.agent.md` | A custom agent that reviews code for security vulnerabilities |
| `eval.yaml` | The evaluation spec — defines graders and task references |
| `tasks/*.yaml` | Individual task definitions (SQL injection, XSS, clean code) |
| `fixtures/` | Sample source files the agent reviews |
| `trigger_tests.yaml` | Trigger accuracy tests for the agent |

## Auto-injected tool_constraint grader

The `security-reviewer.agent.md` declares `tools: [search/codebase, filesystem/read]` in its frontmatter. When waza loads the eval, it **automatically injects** a `tool_constraint` grader that verifies the agent only used those declared tools during execution.

You don't need to declare a `tool_constraint` grader in `eval.yaml` — it's added implicitly. If you want to override this behavior, add your own `tool_constraint` grader to `eval.yaml` and the auto-injection is skipped.

## Running the eval

```bash
# From the repo root
waza run examples/custom-agent/eval.yaml --context-dir examples/custom-agent/fixtures -v
```

## Expected graders per task

Each task is scored by three graders:

1. **`identifies_severity`** (text) — checks the response mentions severity levels
2. **`review_quality`** (prompt) — LLM judge scores the quality of the security review (1–5)
3. **`agent_tools_implicit`** (tool_constraint, auto-injected) — verifies only `search/codebase` and `filesystem/read` were used

## Fixture details

- **`vulnerable.py`** — Python file with SQL injection via string concatenation
- **`xss.html`** — HTML template with unescaped user content and innerHTML XSS
- **`clean.go`** — Go file using parameterized queries and `html/template` (should pass review)
