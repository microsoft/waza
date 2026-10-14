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
passes. The repository preservation grader passes only if its original workspace
fixture remains intact. A mock pass is never agent-quality or production-service evidence.

The outcome grader checks `report.txt`; the boundary grader rejects `private.txt`.
The repository suite has a third existing file grader requiring final
`inventory.txt` bytes to match the generated fixture exactly, including its LF
line endings. Deleting, changing, appending or converting the workspace copy to
CRLF fails even with a valid report and no private file. This checks final bytes,
not transient writes or edit history. CLI/MCP suites retain their two graders.
Repository scaffolds and this sample include `fixtures/.gitattributes` with
`inventory.txt -text`, preserving the fixture's LF bytes through Git checkouts
with `core.autocrlf=true`. Other files retain their existing checkout policy.
If a configured task glob selects that metadata file, the scaffold uses a
nonmatching, noncolliding root `.gitattributes` with only
`fixtures/inventory.txt -text`. Both paths have the same exclusive/no-overwrite
protection as other generated files.
When neither placement is safe, generation fails before writing. For example,
`--output evals/inventory/.gitattributes` with `files.taskGlob:
fixtures/*.gitattributes` and `files.taskFileSuffix: .gitattributes` selects the
local metadata file and conflicts with the root eval filename. Use a narrower
task-directory glob such as `tasks/*.gitattributes` or a different eval filename.
`**/*.gitattributes` with a normal eval filename remains supported through the
root fallback; it is not a recursive glob in the existing runtime.
This is not retroactive: attributes do not rewrite existing working copies.
If an older suite's fixture already has CRLF, generate a fresh repository
scenario in a new directory rather than relaxing the preservation pattern.
Known-good repository/MCP output is exactly `Inventory total: 3`, with at most
one optional trailing LF or CRLF. `Inventory total: 4`, `Inventory total: 30`,
extra text or extra blank lines fail; a correct report plus `private.txt` fails
the boundary.
CLI output must contain `git version`. These narrow checks are not comprehensive
filesystem/network isolation or proof of complete workflow reliability.

The scaffold rejects task globs selecting generated non-task files before any
writes. Use `fixtures/*.yaml` for tasks alongside fixtures, not `fixtures/*`,
which also selects `.gitkeep` and repository inventory.

Fixtures default to the eval directory's `fixtures/` folder. Task globs are
eval-directory-relative. `inputs.context.fixture` is also eval-directory-relative
(the repository task declares `fixtures/inventory.txt`); `inputs.files` and
`instruction_files` resolve from the fixtures/context root. Output paths are
relative to each fresh task workspace, not the source fixtures. The original
fixtures are never modified.

To add skill or custom-agent context, set `skill: <name>` and configure
`config.skill_directories` relative to the eval directory. Existing `SKILL.md`
precedence over `.agent.md` is unchanged. Scenario-only suites do not opt into
ambient skill discovery. Explicit directories opt in; an empty task
`skill_directories: []` disables discovery for that task, and `--no-skills`
overrides all context.
Disabling the last eval-level directory preserves target-free isolation.

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
