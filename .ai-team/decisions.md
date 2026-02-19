# Team Decisions

## 2026-02-19: User directive — preserve .ai-team/ state

**By:** Shayne Boyer (via Copilot)

**What:** .ai-team/ directory and its contents must be maintained and preserved across all work. Never gitignore it. It should be committed on feature branches. The Squad Protected Branch Guard CI is the enforcement mechanism that prevents it from reaching main/preview — that's the correct design. All agents and workflows must respect this.

**Why:** User request — captured for team memory. The worktree-local strategy depends on .ai-team/ being tracked on feature branches so state flows through git merge. Gitignoring it would break squad state propagation.

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
