# Team Decisions

> Shared brain — all agents read this before starting work.

<!-- Scribe merges decisions from .ai-team/decisions/inbox/ into this file. -->
### 2026-02-09: Proper git workflow required
**By:** Shayne Boyer (via Copilot)
**What:** All issues must follow: feature branch → commit → push → PR with `Closes #N` → @copilot review → address feedback → merge. No direct commits to main.
**Why:** User directive

### 2026-02-09: Sensei reference repo for E2 implementation
**By:** Squad (Coordinator)
**What:** The sensei engine (E2: #32-38) must adopt functionality from https://github.com/spboyer/sensei. Key patterns to port to Go:

**Scoring Algorithm:** Low (description < 150 chars OR no triggers) → Medium (description >= 150 chars AND trigger keywords) → Medium-High (USE FOR + DO NOT USE FOR) → High (Medium-High + routing clarity). Rule-based checks: name validation, description length, trigger/anti-trigger detection, routing clarity, MCP integration.

**Ralph Loop:** READ → SCORE → CHECK → SCAFFOLD → IMPROVE FRONTMATTER → IMPROVE TESTS → VERIFY → CHECK TOKENS → SUMMARY → PROMPT → REPEAT (max 5 iterations, target: Medium-High).

**Token Management:** count/check/suggest/compare with .token-limits.json config. SKILL.md: 500 soft / 5000 hard. References: 2000 each. --strict exits 1 on limit breach.
**Why:** User directive — spboyer/sensei is the reference implementation

### 2026-02-09: Monitor human engineer comments
**By:** Shayne Boyer (via Copilot)
**What:** Periodically check for comments from Charles (@chlowell) and Richard (@richardpark-msft) on open issues. Follow up on responses.
**Why:** User directive

### 2026-02-09: Run command test patterns
**By:** Linus (Backend Dev)
**Related:** #24
**What:** Each test that calls `cmd.Execute()` must reset package-level flag vars (`contextDir`, `outputPath`, `verbose`) at the top of the test body to prevent state leakage. If new flags are added to `newRunCommand()`, the reset block must be updated accordingly.
**Why:** Package-level Cobra flag bindings persist across test cases in the same process.

### 2026-02-11: PR #103 — azd extension packaging approved
**By:** Rusty (Lead)
**Related:** E7 (#62), PR #103
**What:** Wallace Breza's azd extension PR approved and merged. Adds `extension.yaml`, build scripts, `registry.json`, `version.txt`. Purely additive — no changes to existing Go code or CI. Follow-up: update registry URLs to `spboyer/waza` for production; add trailing newlines (POSIX nit); consider CI for automated extension builds.
**Why:** E7 (AZD Extension) P2 roadmap item — establishes foundation without disruption.

### 2026-02-11: Grader nil-session error handling contract
**By:** Rusty (Lead)
**Related:** PR #110
**What:** All graders depending on `SessionDigest` must return a zero-score `GraderResults` with `nil` error when session is nil — not `(nil, error)`. Matches `behavior_grader.go` pattern (lines 56-62). Returning an error would abort the entire run instead of gracefully recording a zero score.
**Why:** Runner distinguishes grader errors (may abort) from zero-score results (contribute to scoring). Affects `action_sequence_grader.go` and future session-dependent graders.

### 2026-02-11: PR #111 approved — tokens compare command
**By:** Rusty (Lead)
**What:** Approved Charles Lowell's `waza tokens compare` implementation. New `internal/git` package lives under `cmd/waza/tokens/internal/git/` — correctly scoped as a tokens-internal dependency. Command follows established Cobra factory patterns (`newCompareCmd()`). Closes #51 (E4: Token Management).
**Why:** Clean architecture, comprehensive tests, CI green. One non-blocking nit: `RefExists()` is dead code. No security concerns.

### 2026-02-12: azd-publish skill location convention
**By:** Wallace Breza (via Copilot)
**What:** The azd-publish skill is a repo-level skill (lives at `.github/skills/azd-publish/`), NOT part of the project eval skills under `skills/`. Repo-level automation skills go under `.github/skills/`, project eval skills go under `skills/`.
**Why:** User request — captured for team memory

### 2026-02-12: azd extension uses non-standard tag pattern
**By:** Linus (Backend Dev)
**Related:** PR #113, E7
**What:** The azd extension release pipeline uses tags of the form `azd-ext-microsoft-azd-waza_VERSION` (e.g., `azd-ext-microsoft-azd-waza_0.2.0`), not `vVERSION`. Any tooling or documentation that references version tags for the azd extension must use this pattern. The SKILL.md comparison link examples have been updated accordingly.
**Why:** The `azd-publish` skill's Step 5 instructions referenced `vX.Y.Z` tags which don't match the actual tag convention, leading to broken comparison links in changelogs.

### 2026-02-12: PR #115 review feedback addressed
**By:** Linus (Backend Dev)
**Related:** PR #115, E7 (#62)
**What:** Rebased `feat/metadata-capability` onto latest `main` (797f72c), resolved `.golangci.yml` conflict (kept v2 format with `version: "2"` header), added doc comments on `metadataSchemaVersion` and `extensionID` constants per Rusty's review. All 4 metadata tests pass. Force-pushed to `wbreza/feat/metadata-capability`.
**Why:** PR had merge conflicts after main advanced; review requested clarifying comments on constants.
### 2026-02-11: Sensei dev command heuristics & test discipline
**By:** Rusty (Lead)
**Related:** E2 (#32-35), PR #117
**What:** `waza dev` heuristic scoring rules (from `spboyer/sensei` reference):
- **Low** — description < 150 chars OR no trigger phrases
- **Medium** — description ≥ 150 chars AND trigger keywords (USE FOR, USE THIS SKILL, TRIGGERS, etc.)
- **Medium-High** — Medium + anti-trigger phrases (DO NOT USE FOR, NOT FOR, Don't use, Instead use)
- **High** — Medium-High + routing clarity (INVOKES, FOR SINGLE OPERATIONS, **WORKFLOW SKILL**, **UTILITY SKILL**, **ANALYSIS SKILL**)

Test discipline: use table-driven subtests for pattern detection, validate against real fixture loading (code-explainer=Low, waza=High), test exact terminal output (box-drawing, emoji width awareness, rune counting), mock scorer interface for loop testing.
**Why:** Clear, testable compliance framework enables future skill compliance automation across the team's codebase. Reference implementation pattern reduces drift across similar tools.

