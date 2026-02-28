# Session: 2026-02-26 — Performance Audit

**Requested by:** Shayne Boyer

## Summary

Turk (Go Performance Specialist) joined the team and conducted a dual-model performance audit of the waza Go codebase:
- **GPT-5.3-Codex pass:** 28 findings (3 P0, 9 P1, 16 P2)
- **Claude Opus 4.6 pass:** 23 findings (3 P0, 9 P1, 11 P2)
- **Coordinator synthesis:** 30 total unique findings
  - 19 overlapping between both models
  - 7 Codex-only findings
  - 4 Opus-only findings

## Top P0 Actions

1. Cache graders: Grader instances recreated per run; create once during test initialization, reuse across runs (runner.go:1167–1233)
2. Cache fixtures: Resource files read into memory on every run; cache content or pass file paths for direct copy (runner.go:1081–1144)
3. Fix O(N²) scan: Every sequential test iteration re-scans all previous outcomes; track `hadFailure` flag instead (runner.go:658–671)
4. Wire signal context: `runSingleModel` uses `context.Background()` with no signal propagation; wire `signal.NotifyContext` at runner startup (cmd_run.go:607)

## Additional Key Findings

- Inline script grader creates temp file per invocation; write once in constructor, reuse
- Program grader loads .waza.yaml on every construction; accept as parameter, load once
- FileStore recomputes summaries per API call; cache on load/reload
- Goldmark parser recreated per markdown file; create single instance, share it
- JSON transport allocates per message; use `json.Encoder` with buffer

## Impact

Fixes are multiplicative with benchmark scale (tasks × runs × graders). Correctness fixes (cancellation, lifecycle) prevent wasted work; I/O reductions deliver largest wall-clock gains.
