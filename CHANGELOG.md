# Changelog

All notable changes to waza will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **`waza suggest` deadlock** — `Execute()` now applies the request timeout before calling `Start()`, so an unresponsive copilot CLI no longer causes a fatal goroutine deadlock. Previously, `Start()` ran with an unbounded context and deadlocked when the JSON-RPC ping never received a response. The fix also ensures the command exits with a non-zero exit code on failure rather than exit 0.

## [0.9.0] - 2026-02-23

### Added

- **A/B baseline testing** — `--baseline` flag runs each task with and without skill, computes weighted improvement scores across quality, tokens, turns, time, and task completion (#307)
- **Pairwise LLM judging** — `pairwise` mode on `prompt` grader with position-swap bias mitigation. Three modes: pairwise, independent, both. Magnitude scoring from much-better to much-worse (#310)
- **Tool constraint grader** — New `tool_constraint` grader type with `expect_tools`, `reject_tools`, `max_turns`, `max_tokens` constraints. Validates agent tool usage behavior (#391)
- **Auto skill discovery** — `--discover` flag walks directory trees for SKILL.md + eval.yaml pairs. `--strict` mode fails if any skill lacks eval coverage (#392)
- **Releases page** — New docs site page at `reference/releases` with platform download links, install commands, and azd extension info (#383)

### Fixed

- **Lint warnings** — Resolved errcheck (webserver) and ineffassign (utils) lint warnings

### Changed

- **Competitive research** — Added OpenAI Evals analysis (`docs/research/waza-vs-openai-evals.md`), skill-validator analysis (`docs/research/waza-vs-skill-validator.md`), and eval registry design doc (`docs/research/waza-eval-registry-design.md`)
- **Mermaid diagrams** — Converted remaining ASCII diagrams to Mermaid across all markdown files. Added Mermaid directive to AGENTS.md

## [0.8.0] - 2026-02-21

### Added

- **MCP Server** — `waza serve` now includes an always-on MCP server with 10 tools (eval.list, eval.get, eval.validate, eval.run, task.list, run.status, run.cancel, results.summary, results.runs, skill.check) via stdio transport (#286)
- **`waza suggest` command** — LLM-powered eval suggestions: reads SKILL.md, proposes test cases, graders, and fixtures. Flags: `--model`, `--dry-run`, `--apply`, `--output-dir`, `--format` (#287)
- **Interactive workflow skill** — `skills/waza-interactive/SKILL.md` with 5 workflow scenarios for conversational eval orchestration (#288)
- **Grader weighting** — `weight` field on grader configs, `ComputeWeightedRunScore` method, dashboard weighted scores column (#299)
- **Statistical confidence intervals** — Bootstrap CI with 10K resamples, 95% confidence, normalized gain. Dashboard CI bands and significance badges (#308)
- **Judge model support** — `--judge-model` flag and `judge_model` config for separate LLM-as-judge model (#309)
- **Spec compliance checks** — 8 agentskills.io compliance checks in `waza check` and `waza dev` (#314)
- **SkillsBench advisory** — 5 advisory checks (module-count, complexity, negative-delta, procedural, over-specificity) (#315)
- **MCP integration scoring** — 4 MCP integration checks in `waza dev` (#316)
- **Batch skill processing** — `waza dev` processes multiple skills in one run (#317)
- **Token compare --strict** — Budget enforcement mode for `waza tokens compare` (#318)
- **Scaffold trigger tests** — Auto-generate trigger test YAML from SKILL.md frontmatter (#319)
- **Skill profile** — `waza tokens profile` for static analysis of skill token distribution (#311)
- **JUnit XML reporter** — `--format junit` output for CI integration (#312)
- **Template Variables** — New `internal/template` package with `Render()` for Go text/template syntax in hooks and commands. System variables: `JobID`, `TaskName`, `Iteration`, `Attempt`, `Timestamp`. User variables via `vars` map (#186)
- **GroupBy Results** — New `group_by` config field to organize results by dimension (e.g., model). CLI shows grouped output, JSON includes `GroupStats` with name/passed/total/avg_score (#188)
- **Custom Input Variables** — New `inputs` section in eval.yaml for defining key-value pairs available as `{{.Vars.key}}` throughout evaluation. Accessible in hooks, task templates, and grader configs (#189)
- **CSV Dataset Support** — New `tasks_from` field to generate tasks from CSV files. Each row becomes a task with columns accessible as `{{.Vars.column}}`. Optional `range: [start, end]` for row filtering. First row treated as headers (#187)
- **Retry/Attempts** — Add `max_attempts` config field for retrying failed task executions within each trial (#191)
- **Lifecycle Hooks** — Add `hooks` section with `before_run`/`after_run`/`before_task`/`after_task` lifecycle points (#191)
- **`prompt` grader (LLM-as-judge)** — LLM-based evaluation with rubrics, tool-based grading, and session management modes (#177, closes #104)
  - Two modes: `clean` (fresh context) and `continue_session` (resumes test session)
  - Tool-based grading: `set_waza_grade_pass` and `set_waza_grade_fail` tools for LLM graders
  - Separate judge model configuration: run evaluation with a different model than the executor
  - Pre-built rubric templates adapted from Azure ML evaluators
- **`trigger_tests.yaml` auto-discovery** — measure prompt trigger accuracy for skills (#166, closes #36)
  - New `internal/trigger/` package for trigger testing
  - Automatically discovered alongside `eval.yaml`
  - Confidence weighting: `high` (weight 1.0) and `medium` (weight 0.5) for borderline cases
  - `trigger_accuracy` metric with configurable cutoff threshold
  - Metrics: accuracy, precision, recall, F1, error count
- **`diff` grader** — new grader type for workspace file comparison with snapshot matching and contains-line fragment checks (#158)
- **Azure ML evaluation rubrics** — 8 pre-built rubric YAMLs in `examples/rubrics/` adapted from Azure ML evaluators (#160, #161):
  - Tool call rubrics: `tool_call_accuracy`, `tool_selection`, `tool_input_accuracy`, `tool_output_utilization`
  - Task evaluation rubrics: `task_completion`, `task_adherence`, `intent_resolution`, `response_completeness`
- **MockEngine WorkspaceDir support** — test infrastructure for graders that need workspace access (#159)

### Changed

- **Dashboard** — Aspire-style trajectory waterfall, weighted scores column, CI bands with significance indicators, judge model badge (#303, #330, #331, #332)
- **Docs site** — Dashboard explore page with 14+ screenshots, light/dark mode, navbar polish (#357, #358, #360)

### Fixed

- **install.sh macOS checksum** — added `shasum -a 256` fallback for macOS (which lacks `sha256sum`) (#163)
- Dashboard compare-runs screenshot now shows 2 runs selected with full comparison
- GitHub icon alignment and search bar width on docs site

## [0.4.0-alpha.1] - 2026-02-17

### Added

- **Go cross-platform release pipeline** — `go-release.yml` workflow builds binaries for linux/darwin/windows on amd64 and arm64 (#155)
- **`install.sh` installer** — one-line binary install with checksum verification: `curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash`
- **`skill_invocation` grader** — validates orchestration workflows by checking which skills were invoked (#146)
- **`required_skills` preflight validation** — verifies skill dependencies before evaluation (#147)
- **Multi-model `--model` flag** — run evaluations across multiple models in a single command (#39)
- **`waza check` command** — skill submission readiness checks (#151)
- **Evaluation result caching** — incremental testing with cache invalidation (#150)
- **GitHub PR comment reporter** — post eval results as PR comments (#140)
- **Skills CI integration** — GitHub Actions workflow for microsoft/skills (#141)

### Fixed

- **Engine shutdown leak** — `runSingleModel()` now calls `engine.Shutdown(context.Background())` via defer after engine creation (#153, #154)

### Changed

- **Python release deprecated** — the Python release workflow is no longer maintained; Go binaries are the official distribution
- **First Go binary release** — v0.4.0-alpha.1 is the first release distributed as pre-built binaries

## [0.3.0] - 2026-02-13

### Added

- Grader showcase examples demonstrating all grader types (#134)
- Reusable GitHub Actions workflow for waza evaluations (#132)
- Documentation for prompt and action_sequence grader types (#133)
- Documentation for `waza dev` command and compliance scoring (#131)
- Auto-loading of skills for testing (#129)
- Debug logging support (`--debug` flag) (#130)

### Fixed

- Always output test run errors to help debug failures (#128)
- Include cwd as a skill folder when running waza (workspace fix)

### Changed

- Exit codes for CI/CD integration: 0=success, 1=test failure, 2=config error (#135)
- Reordered azd-publish skill workflow steps (#127)
- Auto-merge bot registry PRs in release workflow

## [0.2.1] - 2026-02-12

### Added

- `waza dev` command for interactive skill development and testing (#117)
- Prerelease input to azd publish workflow
- CHANGELOG.md as release notes source for azd extension releases
- `waza generate --skill <name>` - Filter to specific skill when using `--repo` or `--scan`

### Fixed

- Fixed azd extensions documentation link
- Corrected `azd ext source add` command syntax
- Branch release PR from origin/main to avoid workflow permission error (#121)

### Changed

- Removed path filters from Go CI to unblock non-code PRs
- Removed auto-merge from azd publish PR workflow
- Added azd extension installation instructions to README

## [0.2.0] - 2026-02-02

### Added

- **Skill Discovery** (#3)
  - `waza generate --repo <org/repo>` - Scan GitHub repos for SKILL.md files
  - `waza generate --scan` - Scan local directory for skills
  - `waza generate --all` - Generate evals for all discovered skills (CI-friendly)
  - Interactive skill selection with checkboxes when not using `--all`

- **GitHub Issue Creation** (#3)
  - Post-run prompt to create GitHub issues with eval results
  - Options: create for failed tasks only, all tasks, or none
  - Issues include results table, failed task details, and suggestions
  - `--no-issues` flag to skip prompts (CI-friendly)

- **New Modules**
  - `waza/scanner.py` - Skill discovery from GitHub repos and local directories
  - `waza/issues.py` - GitHub issue creation and formatting

### Changed

- Improved documentation with new feature guides
- Added skill discovery section to DEMO-SCRIPT.md
- Updated TUTORIAL.md with discovery and issue creation steps

## [0.1.0] - 2026-02-02

### Changed

- **Renamed project from `skill-eval` to `waza`** (技 - Japanese for "technique/skill")
  - New CLI command: `waza` (previously `skill-eval`)
  - New package name: `waza` (previously `skill_eval`)
  - Repository renamed to `waza`
- Bumped version to 0.1.0 to mark the rename milestone

### Migration

If you were using `skill-eval`, update your scripts:

```bash
# Old
skill-eval run ./eval.yaml
pip install skill-eval

# New
waza run ./eval.yaml
pip install waza
```

## [0.0.2] - 2026-02-01

### Added

- `--suggestions-file` option to save improvement suggestions to markdown file
- Improved progress display with step-by-step status (tool counts, activity indicators)
- Copilot SDK usage guide in AGENTS.md

### Fixed

- Fixed Copilot SDK import (`from copilot import CopilotClient` not `copilot_sdk`)
- Fixed Windows glob pattern in release workflow
- Fixed linting issues across codebase (import sorting, exception chaining, etc.)
- Clarified fixture isolation between tasks (each task gets fresh temp workspace)

## [0.0.1] - 2026-02-01

### Added

- **CLI Commands**
  - `waza run` - Run evaluation suites against skills
  - `waza generate` - Auto-generate evals from SKILL.md files
  - `waza init` - Initialize new eval suites interactively
  - `waza report` - Generate reports from results

- **Eval Generation**
  - Pattern-based generation from SKILL.md files
  - LLM-assisted generation with `--assist` flag for better tasks/fixtures
  - Support for multiple models (Claude, GPT-4, etc.)

- **Executors**
  - Mock executor for testing without LLM calls
  - Copilot SDK executor for real integration testing

- **Graders**
  - Code graders with Python assertions
  - Regex graders for pattern matching
  - LLM graders for semantic evaluation

- **Features**
  - Real-time progress display with conversation streaming (`-v`)
  - Transcript logging (`--log`)
  - Project context support (`--context-dir`)
  - LLM-powered improvement suggestions (`--suggestions`)

- **Documentation**
  - Comprehensive README with examples
  - Tutorial guide
  - Grader reference
  - Demo script for walkthroughs

### Fixed

- Grader eval context now includes `str`, `int`, `bool`, etc.
- Transcript normalization for proper tool call detection
- YAML escaping for regex patterns with backslashes
- Progress bar now shows 100% on completion

[Unreleased]: https://github.com/microsoft/waza/compare/v0.8.0...HEAD
[0.8.0]: https://github.com/microsoft/waza/compare/v0.4.0-alpha.1...v0.8.0
[0.4.0-alpha.1]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.3.0...v0.4.0-alpha.1
[0.3.0]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.2.1...azd-ext-microsoft-azd-waza_0.3.0
[0.2.1]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.2.0...azd-ext-microsoft-azd-waza_0.2.1
[0.2.0]: https://github.com/microsoft/waza/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/microsoft/waza/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/microsoft/waza/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/microsoft/waza/releases/tag/v0.0.1
