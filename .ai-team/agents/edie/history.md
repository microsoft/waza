# Edie — History

## 2025-02-21: Fix compare runs screenshot

**Task:** Update the compare-runs screenshot in `docs/images/explore/` to show 2 runs actually selected.

**What I did:**
- Updated `web/e2e/screenshots.spec.ts` compare test to save to the correct path (`../docs/images/explore/compare-runs.png` instead of `../docs/images/compare.png`)
- Added `Metrics Comparison` visibility assertion for render stability
- Increased viewport height to 900px so the full compare view (run cards, metrics, pass rate bars, per-task table) is captured
- Ran the spec — screenshot now clearly shows both runs selected with all comparison data visible

**Learnings:**
- The screenshot spec output paths were out of sync with the mdx references — the mdx uses `/waza/images/explore/compare-runs.png` but the spec was writing to `../docs/images/compare.png`
- The mock data in `web/e2e/fixtures/mock-data.ts` has `run-001` (code-explainer/gpt-4o) and `run-002` (skill-checker/claude-sonnet-4) which provide good visual contrast for comparison screenshots
- Playwright `page.setViewportSize()` mid-test is useful for screenshots that need more vertical space than the default 720px
