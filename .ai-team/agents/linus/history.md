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
- **PR #115 review fixes (metadata capability):** Rebased `feat/metadata-capability` onto main (797f72c), resolved `.golangci.yml` conflict keeping v2 format. Added doc comments on `metadataSchemaVersion` and `extensionID` per Rusty's review. The `--force-with-lease` push to a fork requires `git fetch <remote> <branch>` first if the remote tracking ref is stale. All 4 metadata tests pass (`TestMetadataCommand_OutputsValidJSON`, `TestMetadataCommand_ContainsExpectedCommands`, `TestMetadataCommand_FlagsPopulated`, `TestMetadataCommand_IsHidden`).
- **PR #113 review fixes (azd-ext-release-pipeline):** Three fixes: (1) Added trailing newline to `version.txt` for POSIX compliance — file had bare `0.2.0` with no `\n`. (2) Updated SKILL.md comparison link examples from `vX.Y.Z` tag pattern to `azd-ext-microsoft-azd-waza_X.Y.Z` to match the actual azd extension release tag convention. (3) Rebased onto `origin/main` — skipped already-applied golangci-lint commit (10b5f52) that was later reverted (35ba918, also skipped). Force-pushed to `wbreza` remote since the branch originates from wbreza's fork.
- **Fork PR push pattern:** When a PR comes from a fork, the branch lives on the fork remote (e.g., `wbreza`), not `origin`. Push with `--force-with-lease` to the fork remote after `git fetch <remote> <branch>` to update the tracking ref. The PR on origin updates automatically.
📌 Team update (2026-02-12): azd-publish skill location convention — repo-level skills go under `.github/skills/`, project eval skills go under `skills/`. — decided by Wallace Breza
📌 Team update (2026-02-12): PR #111 tokens compare command approved and merged. Closes #51 (E4). — decided by Rusty
<!-- Append new learnings below. -->
