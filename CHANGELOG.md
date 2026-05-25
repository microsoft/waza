# Changelog

All notable changes to waza will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.34.0] - 2026-05-23

### Added

- **BYOK provider wiring** — Added bring-your-own-key provider support for configured model providers (#240)
- **`waza update` command** — Added an update command for upgrading local Waza installations (#288)
- **Skill injection opt-out** — Added an option to run evals without injecting the target skill body (#285, #292)
- **Forbidden skills grading** — `skill_invocation` graders can now assert that specific skills must not be invoked (#286, #291)
- **Per-trial usage reporting** — Results JSON now includes per-trial usage details for deeper run analysis (#277)
- **Agent-friendly GitHub templates** — Added issue and pull request templates tuned for agent-authored work (#293)

### Fixed

- **Tool approval handling** — Tool permission handling now uses the SDK approval kind (#240)
- **Signal cancellation** — `waza run` now respects cancellation signals more reliably (#279)
- **Sandbox prompt handling** — Empty sandbox prompts are guarded before execution (#273, #278)
- **Custom agent example schema** — Fixed the custom-agent eval example to match the supported schema (#282)
- **Binary release links** — Fixed binary release documentation links (#276, #284)
- **Agent path guidance** — Corrected AGENTS root path guidance (#267, #269)

### Changed

- **Run concurrency** — `waza run` now reuses a shared Copilot client and auto-sizes parallel workers when `--workers` is unset (#135, #221)
- **Documentation** — Updated integration testing, custom-agent eval, and OpenAI Evals model-graded YAML documentation (#281, #283, #14, #280)
- **Release workflow** — GitHub Pages deployment now runs after the release workflow (#265)

## [0.33.0] - 2026-05-21

Note: This release includes the changes previously prepared under 0.32.0, which was not published.

### Added

- **Configurable eval file naming** — `.waza.yaml` can now configure `files.evalFile`, `files.taskGlob`, and `files.taskFileSuffix`, with the new naming carried through scaffolding, workspace discovery, discovery mode, schemas, and docs while preserving the existing `eval.yaml` and `tasks/*.yaml` defaults (#254, closes #232)
- **Instruction files in eval runs** — Eval-level `config.instruction_files` and task-level `instruction_files` now copy files from the active context into task workspaces and append path-labeled contents to the Copilot system message (#248, closes #239)

### Fixed

- **Prompt graders use the execution engine** — Prompt graders now route judge turns through `CopilotEngine` instead of constructing a Copilot client directly, keeping grader execution aligned with engine configuration and preserving follow-up recovery behavior (#258, closes #54)
- **Prompt grader follow-up recovery** — Prompt grading now preserves collected grades when a follow-up turn fails after successful grader collection (#251)
- **Bundled Copilot CLI updated** — Embedded `copilot-cli` bundles are updated from 1.0.2 to 1.0.49 across supported platforms, with reproducible pinned bundle generation via `COPILOT_CLI_VERSION` (#260, closes #244)
- **Spec-aligned skill scaffolding** — `waza new skill` no longer asks for a nonstandard skill type or emits `type:` frontmatter, and the wizard now rejects early exits that omit required name or description fields (#261, closes #243)
- **`waza check` eval discovery** — Nested skills and separated evals are discovered consistently in multi-skill workspaces (#247, closes #238)
- **Skill body routing markers** — Compliance scoring now detects trigger, anti-trigger, and routing markers in `SKILL.md` body sections as well as frontmatter descriptions (#236, closes #223)

### Changed

- **Copilot SDK v0.3.0 migration** — Updated `github.com/github/copilot-sdk/go` to v0.3.0, migrated session event handling to typed payloads, and refreshed transcript, logging, web API, usage collection, suggestion trace, and test coverage for the new API (#255, closes #253)
- **Dashboard validation coverage** — Added coverage for dashboard lint and end-to-end validation (#249)
- **Install documentation** — Replaced unsupported `go install` guidance and clarified Windows/WSL install behavior (#246, closes #242; #245, closes #241)
- **Dependencies** — Bump devalue in /site, postcss in /web, and astro in /site (#237, #235, #234)

## [0.31.0] - 2026-04-28

### Added

- **Custom agent (`.agent.md`) eval support** — Discover `.agent.md` files alongside `SKILL.md`, parse agent-specific frontmatter (`tools`, `model`, `handoffs`, `mcp-servers`, `agents`), auto-inject `tool_constraint` grader from agent `tools:` field, complete worked example under `examples/custom-agent/`, and new "Evaluating Custom Agents" docs guide (#226, closes #225)

### Fixed

- **Mock engine echoes file content** — `_output_contains` expectations against file contents now work in CI without a real model. Mock response includes task metadata, file paths, and a 1KB content preview per resource (#228, closes #227)
- **`waza serve` no longer crashes when stdin isn't a terminal** — MCP stdio server only starts when `term.IsTerminal()` is true; piped input or background mode no longer kills the HTTP dashboard (#224)

### Changed

- **Vocabulary renames** — Internal types renamed: `BenchmarkSpec` → `EvalSpec`, `TestRunner` → `EvalRunner`. Not a breaking change for external consumers (types live in `internal/`) (#222)

### Documentation

- Cross-reference audit for recent renames + custom agent feature: added `.agent.md` coverage to quickstart, getting-started, GUIDE, TUTORIAL, examples README; updated mock engine descriptions in INTEGRATION-TESTING and eval-yaml guide (#230)

### Dependencies

- Bump postcss from 8.5.6 to 8.5.12 in /site (#229)

## [0.30.1] - 2026-04-22

### Documentation

- **Updated README with missing CLI commands** — Added documentation for recently-added CLI commands that were missing from the README (#220)

## [0.30.0] - 2026-04-22

### Added

- **`waza quality` command** — LLM-as-Judge skill quality scoring that evaluates skill output quality using a configurable judge model (#218)
- **Scope-reduction advisory check** — `waza check` now includes an advisory that flags skills with overly broad scope, helping authors tighten skill definitions (#219)

## [0.29.0] - 2026-04-22

### Added

- **`--keep-workspace` flag** — Preserve the temporary workspace after task execution for debugging agent output (#123, #217)
- **`--no-skills` flag and `disabled_skills` config** — Disable specific skills during evaluation to isolate behavior (#126, #216)
- **Non-blocking version update check** — CLI now checks for newer waza versions in the background without slowing startup (#104, #214)
- **Per-task `skill_directories`** — Specify different skill directories for individual tasks in eval YAML (#156, #215)

### Dependencies

- Bump astro and @astrojs/starlight in /site (#212)

## [0.28.0] - 2026-04-21

### Added

- **Follow-up prompts in eval YAML** — Tasks can now include pre-written follow-up prompts for multi-turn evaluation conversations (#189, #209)
- **`waza models` command** — List all available models supported by the configured engine (#208)
- **Early termination for trigger tests** — Trigger tests can now stop early once the target skill is invoked, reducing evaluation time (#207)

### Fixed

- **Stricter YAML validation** — Audited all YAML parsers; unknown fields in `TestCase` definitions are now properly rejected (#132, #206)
- **Test fixture assertion syntax** — Fixed invalid Python expression in a test fixture assertion (#197)
- **CI integration test stability** — CI integration tests now correctly handle expected eval failures when using the mock executor (#210)

### Documentation

- Added Quick Start guide to the documentation site (#205)

## [0.27.0] - 2026-04-21

### Added

- **`output_contains_any` expectation** — New expectation field that passes when the agent response contains any one of the specified strings (#203)
- **`max_response_time_ms` behavior rule** — Enforce maximum response time constraints on agent execution (#201)
- **Task prompt from file** — Task `prompt` field can now reference an external file path instead of inline text (#157, #200)
- **`tool_calls` grader** — New grader type that validates the specific tool calls an agent makes during execution (#187, #202)

### Fixed

- **Webserver test resilience** — Webserver tests now skip gracefully when frontend assets are not built (#204)

## [0.26.0] - 2026-04-21

### Changed

- **Timestamped output directories** — `run --output-dir` now groups result files by timestamp for cleaner organization (#153)
- **Improved debug logging** — Debug output is now more structured and useful for troubleshooting (#152)

### Fixed

- **`--discover` finds eval.yaml in nested layout** — Skill discovery now correctly locates `eval.yaml` files in `evals/{name}/` directories at the project root (#44)
- **Diff grader reads post-execution workspace** — The diff grader now reads files from the workspace after agent execution completes, not before (#165, #196)
- **Grader config validation** — Required grader configuration fields are now validated before evaluation starts (#195)
- **macOS install and trigger test count** — Fixed macOS binary installation and an off-by-one error in trigger test counting (#164, #184, #193)

### Documentation

- Added cache command reference, prompt mode documentation, and complete YAML schema reference (#198)
- Updated demo guide and added CI/CD integration guide (#112, #89, #194)

### Dependencies

- Bump defu from 6.1.4 to 6.1.6 in /site (#181)
- Bump vite from 6.4.1 to 6.4.2 in /site and /web (#182, #192)
- Bump go.opentelemetry.io/otel/sdk from 1.42.0 to 1.43.0 (#185)
- Bump astro from 5.17.3 to 5.18.1 in /site (#163)
- Bump picomatch from 4.0.3 to 4.0.4 in /site and /web (#159, #160)
- Bump smol-toml from 1.6.0 to 1.6.1 in /site (#158)

## [0.25.0] - 2026-04-21

### Added

- **Eval coverage grid generator** — New coverage output that visualizes which skills have eval coverage across grader types (#92)

### Fixed

- **SKILL.md injection and trigger fixture loading** — `waza run` now correctly injects SKILL.md content into the evaluation context, loads trigger test fixtures, and passes MCP server configuration to the engine (#191)

### Dependencies

- Bump h3 from 1.15.5 to 1.15.8 in /site (#144)

## [0.24.0] - 2026-03-25

### Changed

- **Strict YAML validation** — All YAML parsers now use `KnownFields(true)` to reject unknown fields, catching typos and misconfigurations early (#132, #133)
- **`max_workers` renamed to `workers`** — Config YAML key renamed for consistency across all config types (**breaking change**)
- **Unified token counting** — `waza check` and `waza tokens count` now share the same counting logic for consistent results (#146)

### Fixed

- **Typo in prompt grader** — Fixed "prmopt" → "prompt" in error message

### Dependencies

- Bump h3 from 1.15.8 to 1.15.9 in /site (#155)
- Bump github.com/buger/jsonparser from 1.1.1 to 1.1.2 (#149)

## [0.21.0] - 2026-03-12

### Added

- **`waza new task from-prompt` command** — Record Copilot sessions into task YAML files for eval creation (#110)
- **Trigger heuristic grader** — New grader type that scores based on trigger/anti-trigger matching heuristics (#90)
- **Eval scaffolding command** — `waza eval new` generates eval.yaml scaffolding for skills (#94)
- **Multi-trial flakiness detection** — Detect flaky evals across multiple trial runs (#103)
- **Snapshot auto-update workflow** — Diff grader can now auto-update snapshot files on mismatch (#95)
- **Per-file token budget configuration** — Configure token budgets per-file in `.waza.yaml` (#96)
- **Skill-aware thresholds** — `waza tokens compare` supports skill-specific threshold configuration (#93)
- **Sensei scoring parity** — WHEN triggers, spec-security, invalid level, and advisory checks 16-18 (#79)
- **CI/CD integration guide** — GitHub Actions and Azure DevOps integration documentation (#100)
- **FileWriter service** — Refactored `waza init` inventory with FileWriter abstraction (#63)

### Fixed

- **`waza suggest` deadlock** — `Execute()` now applies the request timeout before calling `Start()`, preventing goroutine deadlock (#43)
- **`ResourceFile.Content` type** — Changed from `string` to `[]byte` for proper binary file handling (#117)
- **`tokens compare` in subdirectory** — No longer shows all files as "added" when run from a subdirectory (#105)
- **`--output-dir` ignored** — Fixed `--output-dir` having no effect for single-skill runs (#109)
- **Web dashboard build order** — Build dashboard assets before Go compilation (#107)
- **Test file leak** — Fixed test that leaked files into the repo (#120)
- **Config schema defaults** — Aligned `config.schema.json` defaults with Go source of truth (#65)
- **Skill discovery path** — Discover skills under `.github/skills/` directory (#69)

### Changed

- Renamed `config` node `max_workers` to `workers` for consistency across all config types
  - This is a breaking change
- Custom YAML deserializers for config types (#106)
- Validate only known fields in YAML decoders. (#132)
- Token limits priority inverted to `.waza.yaml` first (#64)
- `@wbreza` added to CODEOWNERS (#111)
- Go 1.26+ noted in agent instruction files (#108)

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

[Unreleased]: https://github.com/microsoft/waza/compare/v0.34.0...HEAD
[0.34.0]: https://github.com/microsoft/waza/compare/v0.33.0...v0.34.0
[0.33.0]: https://github.com/microsoft/waza/compare/v0.31.0...v0.33.0
[0.31.0]: https://github.com/microsoft/waza/compare/v0.30.1...v0.31.0
[0.30.1]: https://github.com/microsoft/waza/compare/v0.30.0...v0.30.1
[0.30.0]: https://github.com/microsoft/waza/compare/v0.29.0...v0.30.0
[0.29.0]: https://github.com/microsoft/waza/compare/v0.28.0...v0.29.0
[0.28.0]: https://github.com/microsoft/waza/compare/v0.27.0...v0.28.0
[0.27.0]: https://github.com/microsoft/waza/compare/v0.26.0...v0.27.0
[0.26.0]: https://github.com/microsoft/waza/compare/v0.25.0...v0.26.0
[0.25.0]: https://github.com/microsoft/waza/compare/v0.23.0...v0.25.0
[0.24.0]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.23.0...azd-ext-microsoft-azd-waza_0.24.0
[0.21.0]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.20.0...azd-ext-microsoft-azd-waza_0.21.0
[0.9.0]: https://github.com/microsoft/waza/compare/v0.8.0...azd-ext-microsoft-azd-waza_0.20.0
[0.8.0]: https://github.com/microsoft/waza/compare/v0.4.0-alpha.1...v0.8.0
[0.4.0-alpha.1]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.3.0...v0.4.0-alpha.1
[0.3.0]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.2.1...azd-ext-microsoft-azd-waza_0.3.0
[0.2.1]: https://github.com/microsoft/waza/compare/azd-ext-microsoft-azd-waza_0.2.0...azd-ext-microsoft-azd-waza_0.2.1
[0.2.0]: https://github.com/microsoft/waza/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/microsoft/waza/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/microsoft/waza/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/microsoft/waza/releases/tag/v0.0.1
