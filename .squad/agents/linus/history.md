# History — Linus

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Go Architecture
- **Model directive:** Coding in Claude Opus 4.6 (user requirement)
- **Code structure:** Functional options pattern for configuration
- **Interfaces:** AgentEngine, Validator, Grader (extensible design)
- **Testing:** Unit tests in internal packages, integration tests for CLI

### Waza-specific
- Fixture isolation: temp workspace created per task, original fixtures never modified
- TestCase, BenchmarkSpec, EvaluationOutcome models
- ValidatorRegistry pattern for pluggable graders
- CLI flags: -v (verbose), -o (output), --context-dir (fixtures)

### Integration
- Copilot SDK integration (via AgentEngine interface)
- Web UI gets results from CLI JSON output
- Makefile for build/test/lint automation

### Web API Architecture
- API types in `internal/webapi/types.go` are decoupled from internal models (no direct imports)
- `outcomeToDetail()` in `store.go` maps `models.EvaluationOutcome` → API response types
- JSON uses camelCase consistently across the API surface
- TranscriptEvent mapping uses direct field access (not marshal/unmarshal) due to MarshalJSON snake_case mismatch

## Completed Work

### #237 — Expose transcript & session digest in web API (PR #242)
- **Date:** 2026-02-19
- **Branch:** `squad/237-api-transcript`
- **Files changed:** `internal/webapi/types.go`, `internal/webapi/store.go`, `internal/webapi/handlers_test.go`, `web/src/api/client.ts`
- **What:** Added `TranscriptEventResponse`, `SessionDigestResponse` API types; wired them into `TaskResult`; mapped from `RunResult` in `outcomeToDetail()`; added TS interfaces; added test

### #239 — Trajectory Diffing (PR #244)
- **Date:** 2026-02-19
- **Branch:** `squad/239-trajectory-diffing`
- **Files changed:** `web/src/components/TrajectoryDiff.tsx` (new), `web/src/components/TaskTrajectoryCompare.tsx` (new), `web/src/components/CompareView.tsx` (modified)
- **What:** Added trajectory diffing to CompareView — aligns ToolExecutionStart events by tool name+index, renders matched/changed/insertion/deletion with color coding and expandable JSON diffs. No backend changes needed.


## 📌 Team update (2026-02-20): Model policy overhaul

All code roles now use `claude-opus-4.6`. Docs/Scribe/diversity use `gemini-3-pro-preview`. Heavy code gen uses `gpt-5.2-codex`. Decided by Scott Boyer. See decisions.md for full details.

### #299 — Grader Weighting (PR pending)
- **Date:** 2026-02-20
- **Branch:** `squad/299-grader-weighting`
- **Files changed:** `internal/models/spec.go`, `internal/models/outcome.go`, `internal/orchestration/runner.go`, `internal/reporting/interpreter.go`, `internal/webapi/types.go`, `internal/webapi/store.go`, `internal/models/spec_test.go`, `internal/models/outcome_test.go`
- **What:** Added optional `weight` field to `GraderConfig` (default 1.0 via `EffectiveWeight()`). Added `ComputeWeightedRunScore()` to `RunResult`. Weighted composite score surfaces in `TestStats.AvgWeightedScore`, `OutcomeDigest.WeightedScore`, and the interpretation report. Web API `GraderResult` also carries weight. All existing eval.yaml files work unchanged — weight is optional and defaults to 1.0.
- **Key learning:** `ValidatorInline` (task-level graders) already had a `Weight` field before this change — only `GraderConfig` (spec-level) was missing it. The runner is the correct place to stamp weights onto `GraderResults` since graders themselves don't know their config weight.

### #314 — agentskills.io Spec Compliance Checks (PR #322)
- **Date:** 2026-02-20
- **Branch:** `squad/314-spec-compliance`
- **Files changed:** `cmd/waza/dev/spec.go` (new), `cmd/waza/dev/spec_test.go` (new), `cmd/waza/dev/display.go`, `cmd/waza/dev/display_test.go`, `cmd/waza/dev/score_test.go`, `cmd/waza/dev/loop_test.go`, `cmd/waza/cmd_check.go`
- **What:** Added `SpecScorer` with 8 formal agentskills.io spec checks (frontmatter, allowed-fields, name format, dir-match, description length, compatibility length, license recommendation, version recommendation). Integrated into both `waza dev` (inline in DisplayScore) and `waza check` (separate section, summary table column, readiness gate). 15 new test cases.
- **Key learning:** `makeSkill` test helper needed `FrontmatterRaw` field populated (was nil before) to properly test spec checks. Existing display/loop tests use exact string matching — any new output from `DisplayScore` requires updating all dependent test expected strings.

### #308 — Statistical Confidence Intervals (PR #323)
- **Date:** 2026-02-20
- **Branch:** `squad/308-statistical-ci`
- **Files changed:** `internal/statistics/bootstrap.go` (new), `internal/statistics/bootstrap_test.go` (new), `internal/models/outcome.go`, `internal/orchestration/runner.go`
- **What:** New `internal/statistics/` package with `BootstrapCI` (10k resamples, percentile method), `IsSignificant` (CI doesn't cross zero), and `NormalizedGain` (Hake 1998). Wired bootstrap CI into `computeTestStats` (per-task, when ≥2 runs) and `buildOutcome` (digest-level `StatisticalSummary`). Also populated previously-empty `TestStats` fields: `ScoreVariance`, `CI95Lo`, `CI95Hi`, `Flaky`. 11 test cases covering edge cases, determinism, and CI properties. Fully backward compatible via `omitempty`/pointer types.
- **Key learning:** `TestStats` already had `CI95Lo`/`CI95Hi`/`ScoreVariance`/`Flaky` fields defined but never populated — they were placeholders from initial model design. The `internal/metrics` package already had a normal-approximation `ConfidenceInterval95` function; the new bootstrap approach is more robust for small samples and non-normal distributions. Using `BootstrapCIWithSeed` for deterministic tests is essential — non-seeded bootstrap CIs are non-deterministic and will cause flaky tests.

### #311 — Skill Profile Static Analysis (PR #325)
- **Date:** 2026-02-20
- **Branch:** `squad/311-skill-profile`
- **Files changed:** `cmd/waza/tokens/profile.go` (new), `cmd/waza/tokens/profile_test.go` (new), `cmd/waza/tokens/testdata/profile/SKILL.md` (new), `cmd/waza/tokens/root.go`, `README.md`, `site/src/content/docs/reference/cli.mdx`
- **What:** Added `waza tokens profile` subcommand for structural analysis of SKILL.md files. Reports token count, section count (## and deeper), code block count, numbered workflow steps, detail level classification (minimal/standard/detailed), and warnings (no steps, >2500 tokens, <3 sections). Supports JSON output and configurable tokenizer. 25 tests.
- **Key learning:** The tokens subcommand pattern is well-established — each subcommand (`check`, `compare`, `count`, `suggest`, now `profile`) gets its own file, uses shared `findMarkdownFiles` and `countTokens` helpers from `helpers.go`, and registers in `root.go`. The `findSkillFiles` filter (SKILL.md only) was needed since `findMarkdownFiles` returns all .md/.mdx files. The `mockCounter` test helper pattern (implementing `tokens.Counter` interface) is clean for testing analysis functions without BPE overhead.

### #286 — MCP Server (PR #364)
- **Date:** 2026-02-21
- **Branch:** `squad/286-mcp-server`
- **Files changed:** `internal/mcp/server.go` (new), `internal/mcp/tools.go` (new), `internal/mcp/stdio.go` (new), `internal/mcp/server_test.go` (new), `cmd/waza/cmd_serve.go`, `.copilot/mcp.json` (new)
- **What:** Added MCP (Model Context Protocol) server that runs on stdio alongside the HTTP dashboard in `waza serve`. Exposes 10 tools mapped from existing JSON-RPC handlers and webapi store. Thin adapter pattern — MCP server delegates to `internal/jsonrpc/` handlers via `MethodRegistry.Lookup`. New tools: `waza_results_summary`, `waza_results_runs` (from `webapi.FileStore`), `waza_skill_check` (lightweight readiness check). MCP is always on — no flag. 10 tests.
- **Key learning:** MCP protocol is essentially JSON-RPC 2.0 with specific methods (`initialize`, `tools/list`, `tools/call`) and a content-block response format for tool results. The thin adapter pattern works well — reusing `jsonrpc.Transport` for stdio and `jsonrpc.Handler` functions for tool dispatch avoids duplicating logic. The `tools/call` response wraps results as `{content:[{type:"text",text:"<json>"}]}` which means all tool results must be serialized to JSON text. Notifications (no `id` field) must not receive responses per both JSON-RPC 2.0 and MCP spec.

### #287 — `waza suggest` command (PR pending)
- **Date:** 2026-02-21
- **Branch:** `squad/287-suggest-command`
- **Files changed:** `cmd/waza/cmd_suggest.go` (new), `cmd/waza/cmd_suggest_test.go` (new), `internal/suggest/prompt.go` (new), `internal/suggest/suggest.go` (new), `internal/suggest/suggest_test.go` (new), `cmd/waza/root.go`, `README.md`, `site/src/content/docs/reference/cli.mdx`
- **What:** Added `waza suggest <skill-path>` for LLM-driven eval generation. Command supports `--model`, `--dry-run` (default), `--apply`, `--output-dir`, and `--format yaml|json`. New `internal/suggest` package builds prompt context from SKILL.md + grader types + eval schema summary + example eval, parses structured YAML responses, validates generated `eval_yaml`, and writes `eval.yaml`/task/fixture files when applying.
- **Key learning:** A robust parser needs to handle both structured wrapper YAML (`eval_yaml` + files) and fenced YAML blocks from models. Validating generated `eval_yaml` against `models.BenchmarkSpec.Validate()` catches malformed model output early before writing files.
