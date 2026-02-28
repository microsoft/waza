# History — Basher

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Testing Strategy
- **Model directive:** Coding in Claude Opus 4.6 (same as production code)
- **Test types:** Go unit tests (*_test.go), integration tests, Playwright E2E
- **Fixture isolation:** Original fixtures never modified — tests work in temp workspace
- **Coverage goal:** Non-negotiable (Rusty's requirement)

### Waza-specific Tests
- TestCase execution scenarios
- BenchmarkSpec validation
- Validator registry functionality
- CLI flag handling
- Agent execution mocking

### CI/CD
- Branch protection requires tests to pass
- Go CI workflow in .github/workflows/go-ci.yml
- Test results tracked for quality assurance

### Playwright E2E (PR #241, Issue #208)
- **Config:** `web/playwright.config.ts` — Chromium, vite preview on port 4173, screenshots/video on failure
- **Test dir:** `web/e2e/` with `fixtures/mock-data.ts`, `helpers/api-mock.ts`, and spec files
- **Script:** `npm run test:e2e` (pre-builds with `npm run build`)
- **Route interception:** Must use regex patterns (not globs) to handle query strings — e.g. `/\/api\/runs(\?|$)/`
- **Tailwind v4 colors:** `getComputedStyle` returns `oklch()` not `rgb()` — assert lightness < 0.3 for dark theme
- **react-query retries:** Default 3 retries with backoff; error state tests need ~15s timeout
- **page.request vs page.evaluate:** `page.request.get()` bypasses route interception; use `page.evaluate(() => fetch(...))` instead
- **Hash routing:** App uses hash-based routing (`#/runs/:id`), not react-router. URLs in tests are like `/#/runs/run-001`
- **Previous branch was broken:** Old `squad/208-playwright-e2e` committed node_modules and diverged from main. Force-pushed clean version.

### Trajectory E2E Tests (PR #245, Issue #240)
- **Branch:** `squad/240-trajectory-e2e`
- **Spec files:** `trajectory.spec.ts` (9 tests), `trajectory-diff.spec.ts` (6 tests)
- **Mock data:** Extended `RUN_DETAIL` first task with `transcript` + `sessionDigest`; added `RUN_DETAIL_B` for diff tests
- **API mocks:** `run-002` now returns `RUN_DETAIL_B` for compare/diff test scenarios
- **Strict mode gotcha:** `getByText("3")` matches "38s" too — scope assertions to parent containers (`.filter({ hasText })` or `.locator()`)
- **Ambiguous tool names:** `read_file` appears in both digest card badges and timeline text — use `.locator("span").filter()` to target digest badges specifically
- **Tab testing pattern:** Navigate to `/#/runs/run-001`, click `getByRole("button", { name: "Trajectory" })`, then click task buttons
- **Compare flow:** Navigate to `/#/compare`, select runs via `page.locator("select").nth(0).selectOption("run-001")`, then click table rows
- **Total E2E count:** 33 tests (15 new + 18 existing), all passing on Chromium

### Screenshot Spec (Issue #251)
- **Spec file:** `web/e2e/screenshots.spec.ts` — 4 tests, chromium-only, 1280×720 viewport
- **Output dir:** `docs/images/` (with `.gitkeep`), screenshots: dashboard-overview.png, run-detail.png, compare.png, trends.png
- **Path gotcha:** Playwright resolves screenshot paths from config root (`web/`), NOT from the test file (`web/e2e/`). Use `../docs/images/` not `../../docs/images/`
- **Reuses existing mocks:** `mockAllAPIs` + existing mock-data.ts fixtures — no new mock data needed
- **Trends view:** Uses `/api/runs?sort=timestamp&order=asc` which is already handled by the runs list regex mock
- **Compare view setup:** Must select both runs via `selectOption` before screenshotting, otherwise you just get empty selectors
- **Total E2E count:** 37 tests (4 screenshot + 33 existing), all passing on Chromium

📌 Team update (2026-02-19): Screenshot conventions formalized (viewport, paths, naming, mock reuse) — decided by Basher (#251)


## 📌 Team update (2026-02-20): Model policy overhaul

All code roles now use `claude-opus-4.6`. Docs/Scribe/diversity use `gemini-3-pro-preview`. Heavy code gen uses `gpt-5.2-codex`. Decided by Scott Boyer. See decisions.md for full details.

### Batch Skill Processing (PR #317)
- **Branch:** `squad/317-batch-dev`
- **Feature:** `waza dev` now supports batch processing: multiple skill names, `--all`, and `--filter <level>`
- **Implementation:** Added `runDevBatch()` in `loop.go`, `DisplayBatchSummary()` in `display.go`, new flags in `root.go`
- **Reuses:** `internal/workspace` skill discovery (same as `waza check`)
- **Gotcha:** Untracked files from concurrent branches (e.g. `spec.go`) can cause build failures — always verify `git status` for stray files before testing
- **Test count:** 8 new batch tests in `batch_test.go`, all passing alongside existing 40+ dev tests
- **Pattern:** Batch summary table uses `batchSkillResult` struct to track before/after state per skill — emoji status indicators: ✅ unchanged, 📈 improved, ❌ error

### Judge Model Flag (PR #323, Issue #309)
- **Branch:** `squad/309-judge-model`
- **Feature:** `--judge-model` CLI flag for `waza run` — allows prompt graders to use a separate model from execution (LLM-as-judge pattern)
- **Implementation:** `JudgeModel` field in `models.Config`, CLI flag in `cmd_run.go`, `injectJudgeModel()` helper in `runner.go`
- **Threading pattern:** `runGraders()` checks `spec.Config.JudgeModel` and injects it into prompt grader params map before creating the grader — copies map to avoid mutating originals
- **Backward compatible:** Empty `JudgeModel` = no override, existing per-grader model or SDK default used
- **Gotcha:** Prior session on same branch had partially committed changes — always check `git log` for existing commits on branch before starting work
- **Test count:** 7 new tests (5 injectJudgeModel unit + 2 spec YAML deserialization), all passing

### SkillsBench Advisory Checks (PR #324, Issue #315)
- **Branch:** `squad/315-skillsbench-advisory`
- **Feature:** `AdvisoryScorer` added to `waza dev` scoring pipeline — 5 research-backed SkillsBench checks
- **Checks:** module-count (≥4 warns), complexity (>2500 tokens), negative-delta-risk (500-800 range), procedural-content (≥3 steps = positive), over-specificity (>50 code blocks)
- **Pattern:** New scorer type (`AdvisoryScorer`) follows existing `HeuristicScorer`/`SpecScorer` pattern but uses `AdvisoryResult` with `Advisory` structs (Check/Message/Kind) instead of `Issue` — "positive" kind is new for beneficial signals
- **Display:** `DisplayAdvisory()` integrated into `DisplayScore()` after spec compliance; uses ✅/⚠️/ℹ️ icons for positive/warning/info
- **Counting helpers:** `countModules` (## and ### headings), `countNumberedSteps` (regex), `countCodeBlocks` (``` fence pairs)
- **Test count:** 28 new tests in `advisory_test.go`, all passing alongside existing dev tests
