# Team Decisions

## 2026-02-19: User directive — preserve .squad/ state

**By:** Shayne Boyer (via Copilot)

**What:** .squad/ directory and its contents must be maintained and preserved across all work. Never gitignore it. It should be committed on feature branches. The Squad Protected Branch Guard CI is the enforcement mechanism that prevents it from reaching main/preview — that's the correct design. All agents and workflows must respect this.

**Why:** User request — captured for team memory. The worktree-local strategy depends on .squad/ being tracked on feature branches so state flows through git merge. Gitignoring it would break squad state propagation.

## 2026-02-18: Model selection directive (updated)

**By:** Shayne Boyer (via Copilot)

**What:** All coding work must use Claude Opus 4.6 (premium). All code reviews must use GPT-5.3-Codex. This supersedes and consolidates the earlier review-only directive from 2026-02-18.

**Why:** User request — captured for team memory. User explicitly stated "make sure we are coding in opus 4.6 high and reviewing in Codex 5.3" and requested this be persisted so it doesn't need repeating.

## 2026-02-18: Web UI model assignments

**By:** Shayne Boyer (via Copilot)

**What:** For Web UI (#14) implementation: coding in Claude Opus 4.6 (premium), checks/reviews in GPT-5.3-Codex, design work in Gemini Pro 3 Preview

**Why:** User request — optimizing model selection per task type for this epic

## 2026-02-18: Dashboard design — DevEx colors, no gradients

**By:** Shayne Boyer (via Copilot)

**What:** Dashboard theme should use colors/styling close to the DevEx Token Efficiency Benchmarks dashboard. No fancy gradients — keep it clean and functional.

**Why:** User preference — captured for design consistency

## 2026-02-19: Screenshot spec conventions

**By:** Basher (Tester / QA)
**Issue:** #251

**What:** Screenshot tests live in `web/e2e/screenshots.spec.ts` and output to `docs/images/`. Conventions:
- Viewport: 1280×720, chromium only (no firefox — screenshots must be pixel-consistent)
- Paths: Use `../docs/images/` (relative to Playwright config root `web/`), NOT relative to the test file
- Mock data: Reuse `mockAllAPIs` and existing fixtures — no screenshot-specific mock data
- Views requiring interaction: Set up state (select options, expand rows) before capturing
- Naming: kebab-case matching the view name: `dashboard-overview.png`, `run-detail.png`, `compare.png`, `trends.png`

**Why:** Reproducible screenshots from mock data mean docs images stay consistent regardless of when/where they're generated. Running `npx playwright test e2e/screenshots.spec.ts --project=chromium` regenerates all four images deterministically.

## 2026-02-19: Documentation Maintenance Routing (Issue #256)

**By:** Saul (Documentation Lead)

**Status:** Implemented

**What:** Established Saul (Documentation Lead) as the documentation quality gate. Added two new PR review rules:
- **Doc-review gate** (Rule 9): Saul reviews PRs touching CLI code (`cmd/waza/`, `internal/`, `web/src/`) for documentation impact
- **Doc-consistency gate** (Rule 10): Saul reviews PRs touching documentation files for style consistency and accuracy

Added Documentation Impact Matrix mapping code paths to required doc updates, showing which doc files must be checked when specific code changes.

**Why:** **Problem:** Documentation was reactive rather than proactive. Code changes happened without corresponding doc updates. Screenshots became stale. Examples diverged from actual behavior. No clear responsibility for doc freshness.

**Solution:** Make documentation review a first-class routing rule, like code review. Saul owns ongoing doc-freshness verification across all PRs. The Impact Matrix provides clear guidance on what needs checking for each code path.

**Scope:**
- **routing.md:** Added Rules 9–10 and Documentation Impact Matrix
- **charter.md:** Added doc-freshness reviews to "What I Own" and PR monitoring to "How I Work"
- **AGENTS.md:** Added Documentation Maintenance section with tables for "When to Update Docs" and screenshot regeneration steps
- **history.md:** Recorded doc-freshness reviews as a key learning

**Impact:** All code PRs (`cmd/waza/`, `internal/`, `web/src/`) now automatically routed to Saul for doc-impact review. All doc PRs (`docs/`, `README.md`, `DEMO-SCRIPT.md`) routed to Saul for consistency check. Clear accountability: Saul owns the matrix and updates it as new paths are discovered. Screenshot maintenance can be automated via Playwright tests.

## 2026-02-19: --tokenizer flag should be available on all token commands

**By:** Rusty (Lead / Architect)  
**PR:** #260  
**Date:** 2026-02-19

**What:** The `--tokenizer` flag is currently only on `waza tokens count`. The `check`, `compare`, and `suggest` commands hardcode `TokenizerDefault`. For consistency, all token commands should accept `--tokenizer` so users can choose between BPE and estimate across the board.

**Why:** If a user needs the fast estimate for CI (where speed matters more than precision), they should be able to use it from any token command — not just `count`. The current design forces BPE on `check` and `compare` with no escape hatch.

**Status:** Follow-up work, not blocking PR #260.

## 2026-02-20: Unified Release Trigger & Version Single Source-of-Truth

**By:** Rusty (Lead / Architect)
**Date:** 2026-02-20
**Status:** PROPOSED
**Impact:** Release process, artifact consistency, extension users

**What:** Unify the release process under a single `release.yml` workflow triggered by `v*.*.*` Git tags. Retire `go-release.yml` and `azd-ext-release.yml` once stable. Pre-flight validation ensures `version.txt == tag`. Version sync runs before builds, not after.

**Why:** Current two-workflow approach causes version desync (extension.yaml lags CLI), stale registry.json, dual tag schemes, and no validation. Tag-driven approach is Git-native, immutable, auditable.

**See Also:** Issue #223, `.squad/agents/rusty/history.md`

## 2026-02-20: Model assignment overhaul — quality-first policy

**By:** Scott Boyer (via Copilot)
**Date:** 2026-02-20
**Status:** APPROVED

**What:** Full model reassignment — cost is not a constraint, optimize for quality/speed per role:
1. Rusty (Lead) → `claude-opus-4.6` — always premium, no downgrade for triage
2. Linus (Backend Dev) → `claude-opus-4.6` — highest SWE-bench (81%), best debugging
3. Basher (Frontend Dev) → `claude-opus-4.6` — same quality advantage for components
4. Livingston (Tester) → `claude-opus-4.6` — best logical reasoning for edge cases
5. Saul (Documentation Lead) → `gemini-3-pro-preview` — 1M context, good for large docs
6. Scribe (Session Logger) → `gemini-3-pro-preview` — mechanical ops, Gemini handles fine
7. Diversity reviews → `gemini-3-pro-preview` — different provider = different perspective
8. Heavy code gen (500+ lines) → `gpt-5.2-codex` — 3.8× faster, 400K context

**Why:** User directive: "Cost is not an issue — optimize for best/fastest per role." Benchmarks consulted: SWE-bench Verified (Feb 2026). Claude Opus 4.6 leads at 81%, GPT-5.2 Codex wins speed, Gemini 3 Pro wins context window + provider diversity.

**Supersedes:** "Model selection directive (updated)" from 2026-02-18 and "Web UI model assignments" from 2026-02-18.

## 2026-02-21: User directive — MCP always on

**By:** Shayne Boyer (via Copilot)

**What:** MCP server always launches with `waza serve` — no --mcp flag needed. It's always on, supporting all features.

**Why:** User request — simplify the CLI surface, MCP is a core feature not an opt-in

## 2026-02-21: User directive — Waza skill should orchestrate workflows

**By:** Shayne Boyer (via Copilot)

**What:** The waza interactive skill (#288) should support scenarios and orchestrate user workflows — not just be a reference doc. It should guide users through creating evals, running them, interpreting results, comparing models, etc.

**Why:** User request — the skill needs to be a real workflow partner, not a tool catalog

## 2026-02-21: User directive — Use Mermaid for diagrams

**By:** Shayne Boyer (via Copilot)

**What:** Use Mermaid for all markdown diagrams in documentation and design docs — no ASCII art diagrams

**Why:** User request — captured for team memory

## 2026-02-20: Grader Weighting Design

**By:** Linus (Backend Dev)
**Date:** 2026-02-20
**Issue:** #299

### What

Added optional `weight` field to grader configs for weighted composite scoring. Key design choices:

1. **Weight lives on config, not on the grader interface.** Graders don't know their own weight — the runner stamps it onto `GraderResults` after grading. This keeps grader implementations simple and weight-unaware.

2. **Default weight is 1.0** (via `EffectiveWeight()`). Zero and negative values are treated as 1.0. This means all existing eval.yaml files produce identical results — no migration needed.

3. **Weighted score is additive, not a replacement.** `AggregateScore` (unweighted) is preserved. `WeightedScore` is a new parallel field. The interpretation report only shows weighted score when it differs from unweighted.

4. **Weight flows through the full pipeline:** `GraderConfig.Weight` → `GraderResults.Weight` → `RunResult.ComputeWeightedRunScore()` → `TestStats.AvgWeightedScore` → `OutcomeDigest.WeightedScore`. Web API also carries weight per grader result.

### Why

Weighted scoring lets users express that some graders matter more than others (e.g., correctness 3× more important than style). Without breaking existing pass/fail semantics.

### Impact

- `internal/models/` — new fields on `GraderConfig`, `GraderResults`, `TestStats`, `OutcomeDigest`
- `internal/orchestration/runner.go` — weight stamping in `runGraders`, weighted stats in `computeTestStats`/`buildOutcome`
- `internal/reporting/` — conditional weighted score display
- `internal/webapi/` — weight exposed in API responses
- JSON schema unchanged (eval.yaml schema is separate from waza-config.schema.json)

## 2026-02-20: SpecScorer as separate scorer from HeuristicScorer

**By:** Linus (Backend Developer)
**Date:** 2026-02-20
**PR:** #322
**Issue:** #314

**What:** The agentskills.io spec compliance checks are implemented as a separate `SpecScorer` type rather than extending `HeuristicScorer`. Both run independently — `HeuristicScorer` handles heuristic quality scoring (triggers, anti-triggers, routing clarity) while `SpecScorer` handles formal spec validation (field presence, naming rules, length limits).

**Why:** The two scorers have different concerns: `HeuristicScorer` is about quality/adherence level (Low→High), while `SpecScorer` is about pass/fail conformance to the agentskills.io specification. Keeping them separate means each can evolve independently. The spec may change without affecting heuristic scoring, and vice versa.

**Impact:** Both `waza dev` and `waza check` now run both scorers. Any new spec checks should be added to `SpecScorer` in `cmd/waza/dev/spec.go`. The `SpecResult.Passed()` method only considers errors (not warnings) — warnings like missing license/version don't block readiness.

## 2026-02-21: Releases page pattern

**By:** Saul (Documentation Lead)
**Date:** 2026-02-21
**Issue:** #383
**PR:** #384

**What:** Created a releases reference page at `site/src/content/docs/reference/releases.mdx` that shows the current release (v0.8.0) with changelog highlights, download table, install commands, and azd extension info. Older releases link out to GitHub Releases rather than duplicating content.

**Why:** The docs site should be a self-contained starting point for users downloading waza. Having binaries, install commands, and changelog highlights in one place reduces friction. Linking to GitHub Releases for history avoids maintaining two changelog surfaces.

**Pattern for future releases:** When cutting a new version, update the releases.mdx page — change the version number, update the changelog highlights, and update download URLs. The CHANGELOG.md remains the source of truth; the releases page is a curated summary of the latest.

## 2026-02-26: Performance Audit — 30 findings from dual-model assessment

**By:** Turk (Go Performance Specialist)
**Requested by:** Shayne Boyer
**Date:** 2026-02-26
**Scope:** runner, cmd_run, execution, jsonrpc, webapi, tokens, graders, links
**Status:** INFORMATIONAL + PRIORITIZED REMEDIATION

### What

Full independent performance audit of waza Go codebase across two models:
- **GPT-5.3-Codex pass:** 28 findings (3 P0, 9 P1, 16 P2)
- **Claude Opus 4.6 pass:** 23 findings (3 P0, 9 P1, 11 P2)
- **Coordinator synthesis:** 30 total unique findings (19 overlapping, 7 Codex-only, 4 Opus-only)

### Critical P0 Findings

1. **O(N²) stop-on-error scan** (runner.go:658–671): Sequential test iteration re-scans all previous outcomes. **Fix:** Track `hadFailure` flag instead.
2. **Grader instances recreated per run** (runner.go:1167–1233): `runGraders()` calls `graders.Create()` for every grader on every run. 10 tasks × 3 runs × 4 graders = 120 factory calls, each loading `.waza.yaml`. **Fix:** Create instances once during test init, reuse.
3. **Resource files read into memory per run** (runner.go:1081–1144): `loadResources()` reads fixture files as strings on every run. 1MB × 10 tests × 3 runs = 30MB redundant read. **Fix:** Cache across runs or use direct file copy.
4. **No signal propagation on long-running evals** (cmd_run.go:607): `runSingleModel` uses `context.Background()` — Ctrl+C never reaches engine/graders. **Fix:** Wire `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`.

### Important P1 Findings

- Inline script grader creates temp file per invocation; write once in constructor
- Program grader loads `.waza.yaml` on every construction; accept timeout as parameter
- FileStore recomputes summaries on every API call; cache on load
- Goldmark parser recreated per markdown file; use single shared instance
- JSON transport allocates per message; use `json.Encoder` with buffer
- BPE tokenizer regex applied char-by-char with repeated substring slicing
- HTTP response bodies not drained, blocking connection reuse

### Nice-to-Have P2 Findings (11 total)

Cache mutex should be RWMutex, token limits reads files twice, session runs never evicted, spinner uses `time.After` in select loop, repeated JSON marshaling for cache keys, context ignored in `Shutdown()`, and others.

### Remediation Priority

1. **Concurrency/cancellation first:** Fix lifecycle leaks, add `ctx.Done()` checks before semaphore acquisition, wire signal context
2. **High-frequency I/O second:** Remove repeated config loads, reduce lock-held disk I/O, revisit workspace lifecycle
3. **CPU/allocation micro-optimizations third:** Tokenizer hot loops, pretty JSON on machine paths, HTTP link checker reuse

### Why

Top issues are multiplicative with benchmark scale (tasks × runs × graders). Correctness-oriented fixes prevent wasted work and leaks. Grader/store I/O reductions deliver largest immediate wall-clock gains. Micro-optimizations follow after control-path fixes.

### Session Log

See `.squad/log/2026-02-26-performance-audit.md` for detailed session notes.
