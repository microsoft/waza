---
title: Schema Changes
description: Versioning policy and changelog for waza public schema artifacts.
---

Waza public artifacts use an explicit `schemaVersion` field so checked-in eval suites, baselines, and dashboard data remain stable across CLI upgrades.

## Versioned artifacts

| Artifact | Field | Current version |
|---|---|---|
| `eval.yaml` | `schemaVersion` | `1.1` |
| `results.json` | `schemaVersion` | `1.1` |
| `snapshot.json` | `schemaVersion` | Not yet emitted |
| Dashboard/SSE event envelope | `schemaVersion` | `1.1` |

## Policy

Schema versions use `MAJOR.MINOR` format with no patch component.

- **MINOR** changes are backward-compatible additions, usually optional fields. Readers accept same-major artifacts and warn when they see unknown fields.
- **MAJOR** changes are breaking. Readers refuse artifacts from a different major version and point to `waza migrate <file>`.
- Missing `schemaVersion` is interpreted as the current schema version (currently `1.1`). Same-major minor differences are accepted and any unknown fields are warned about; cross-major mismatches are rejected.
- New artifacts should emit the current `schemaVersion` (currently `1.1`). The version is automatically populated by the writer; you only need to set it manually when authoring fixtures or schema-pinned test data.

## Migration command

Use `waza migrate <file>` when a reader reports an incompatible major version.

```bash
waza migrate eval.yaml
waza migrate results.json
```

For schema `1.0`, the command is a no-op because there is no prior major version to migrate from.

## Changelog

### 1.1

- Added optional `checkpoints[]` to task YAML for per-turn graders (`after_turn`, `graders`, `on_failure`). Backward-compatible: 1.0 task files load unchanged.
- Added optional `checkpoints[]` to `results.json` task results, recording per-turn grader outcomes.
- Added optional `runs[].tool_events[]` array to `results.json` (issue #366). Each entry captures one tool call with `turn`, `sequence`, `tool_call_id`, `tool_name`, `args`, `result`, `success`, `error`, and `duration_ms`. The legacy `session_digest.tool_calls` field is preserved.
- Bumped `results.json` `schemaVersion` default to `1.1`. Readers accept `1.0` and `1.1` interchangeably (same major).

### 1.0

- Added `schemaVersion` to `eval.yaml`.
- Added `schemaVersion` to `results.json`.
- Added `schemaVersion` to the dashboard SSE event envelope type.
- Established same-major unknown-field warnings and cross-major reader errors.
- Added the `waza migrate <file>` command stub for future major migrations.
