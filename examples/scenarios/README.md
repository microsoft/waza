# Scenario-first examples

These suites need no placeholder `SKILL.md`. Run with a Waza build that supports
**exact eval schema `2.0`**. Tasks and results retain schema `1.x`, and older
executables reject the new eval major before execution.

| Example | Agent runtime | Dependency behavior |
|---------|---------------|---------------------|
| `repository/eval.yaml` | Real Copilot SDK | Sanitized local inventory fixture, no external repository |
| `cli/eval.yaml` | Real Copilot SDK | Real installed `git --version`, no command mocks or network |
| `mcp/eval.yaml` | Real Copilot SDK | Harness-only mocked inventory MCP server; not production MCP evidence |

From the repository root:

```bash
waza run examples/scenarios/repository/eval.yaml
waza run examples/scenarios/cli/eval.yaml
waza run examples/scenarios/mcp/eval.yaml
```

Real runs require the normal Copilot runtime/authentication and can incur model
costs. They are opt-in, not part of deterministic validation. For an offline
**harness-only** smoke check, copy a suite and change `config.executor` to `mock`.
The mock engine does not execute prompts or create requested files: the outcome
grader should fail for missing `report.txt`, while the absent-private-file check
passes. A mock pass is never agent-quality or production-service evidence.

The outcome grader checks `report.txt`; the boundary grader rejects `private.txt`.
Known-good repository/MCP output contains `Inventory total: 3`; `Inventory total:
4` deliberately fails, and a correct report plus `private.txt` fails the boundary.
CLI output must contain `git version`. These narrow checks are not comprehensive
filesystem/network isolation or proof of complete workflow reliability.

Fixtures default to the eval directory's `fixtures/` folder. Task globs are
eval-directory-relative; input and instruction paths retain context-root
resolution. Output paths are relative to each fresh task workspace, not the
source fixtures. The original fixtures are never modified.

To add skill or custom-agent context, set `skill: <name>` and configure
`config.skill_directories` relative to the eval directory. Existing `SKILL.md`
precedence over `.agent.md` is unchanged. Scenario-only suites do not opt into
ambient skill discovery. Explicit directories opt in; an empty task
`skill_directories: []` disables discovery for that task, and `--no-skills`
overrides all context.

Scenario `2.0` currently bypasses result caching with an explicit notice,
including when `--cache` is requested. External MCP/CLI state, grader resources,
and SDK-discovered context are not fully fingerprinted. Every scenario task
therefore executes again; results are not represented as fresh attempts based
on stale cached evidence. Legacy `1.x` cache keys, defaults and billing behavior
are unchanged.

Generate another suite with:

```bash
waza new eval inventory --scenario --template repository
waza new eval cli-check --scenario --template cli
waza new eval mcp-check --scenario --template mcp
```
