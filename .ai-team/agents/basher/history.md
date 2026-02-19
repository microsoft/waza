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
