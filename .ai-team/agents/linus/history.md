# Project Context

- **Owner:** Shayne Boyer (spboyer@live.com)
- **Project:** Waza — Go CLI for evaluating AI agent skills (scaffolding, compliance scoring, cross-model testing)
- **Project:** Waza — Go CLI for evaluating AI agent skills
- **Stack:** Go, Cobra CLI, Copilot SDK, YAML specs
- **Created:** 2026-02-09

## Learnings

<!-- Append new learnings below. Each entry is something lasting about the project. -->
- **Transcript logging (#31):** Added `--transcript-dir` flag and `internal/transcript` package. `TaskTranscript` model lives in `internal/models/transcript.go`. The runner calls `saveTranscript()` after each test in both sequential and concurrent paths. Filename pattern: `{sanitized-name}-{timestamp}.json`. The transcript package is self-contained with `Write()` and `BuildTaskTranscript()` helpers — reusable for any future per-task file output.
- **Branch hygiene:** The repo has many local branches tracking different features. Always verify `git branch --show-current` before committing — `gh pr create` can silently switch branches.
- **Verbose mode (#30):** Enhanced `verboseProgressListener` with 3 new event types: `EventAgentPrompt`, `EventAgentResponse`, `EventGraderResult`. The runner emits these only when `r.verbose` is true to avoid overhead in normal mode. Grader feedback only shows for failed validators. Tests use `captureOutput()` helper (pipe stdout) in `verbose_test.go`.

### 2026-02-09 — #24 waza run command verification & tests

- **`cmd/waza/cmd_run.go`** — Full implementation of `waza run` using Cobra. Uses package-level vars (`contextDir`, `outputPath`, `verbose`) for flag binding. Tests must reset these between runs to avoid cross-test contamination.
- **`cmd/waza/cmd_run_test.go`** — 10 tests covering arg validation, flag parsing (long + short), error paths (missing file, invalid YAML, unknown engine), mock engine integration (normal, verbose, JSON output, context-dir), and root command wiring. Coverage: 72.7%.
- **Pattern: `newRunCommand()` factory** — Each Cobra command is built via a factory function, making it testable without the full CLI harness. Call `cmd.SetArgs(...)` + `cmd.Execute()` in tests.
- **Mock engine (`execution.NewMockEngine`)** — Returns deterministic responses. Useful for testing the full pipeline without a real Copilot SDK connection. Spec YAML with `executor: mock` triggers it.
- **Fixture isolation** — The runner resolves `--context-dir` relative to CWD and task glob patterns relative to the spec file directory. Tests use `t.TempDir()` for full isolation.
- **Shared workspace hazard** — Multiple agents may work concurrently in this repo. Always verify `git branch --show-current` before committing; stash operations can carry changes across branches.
- **Nil session pattern (#105 review fix):** Graders that depend on `SessionDigest` must return a graceful zero-score `GraderResults` (not an error) when `session == nil`. The runner treats grader errors differently from zero-score results — errors may abort the run while zero-scores are recorded as failed validations. Follow `behavior_grader.go` as the canonical pattern. Fixed in `action_sequence_grader.go` per PR #110 review from Rusty.
📌 Team update (2026-02-11): Grader nil-session error handling contract — all graders depending on SessionDigest must return zero-score GraderResults with nil error when session is nil, not (nil, error). — decided by Rusty

📌 Team update (2026-02-11): PR #103 azd extension packaging approved and merged. Registry URLs need update to spboyer/waza for production. — decided by Rusty
<!-- Append new learnings below. -->
