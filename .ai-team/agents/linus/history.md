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
