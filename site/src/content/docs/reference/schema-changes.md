---
title: Schema Changes
description: Versioning policy and changelog for waza public schema artifacts.
---

Waza public artifacts use an explicit `schemaVersion` field so checked-in eval suites, baselines, and dashboard data remain stable across CLI upgrades.

Scenario-bearing evals opt into **exact `2.0`** with a nonempty string
`scenario`. This is eval-only: tasks/results and their readers stay `1.x`.
Older executables reject eval major `2` before execution rather than ignoring
the new no-ambient-discovery semantics. Optional `skill` retains existing
skill/custom-agent precedence; no target or explicit skill directories
disables ambient skills. Empty task `skill_directories: []` disables discovery;
`--no-skills` overrides every context. Missing or `1.x` versions cannot select
scenario semantics; `2.1` and future majors are not supported scenario versions.

## Versioned artifacts

| Artifact | Field | Current version |
|---|---|---|
| Legacy `eval.yaml` | `schemaVersion` | `1.4` |
| Scenario `eval.yaml` | `schemaVersion` | Exact `2.0`, explicit opt-in |
| Newly generated `results.json` | `schemaVersion` | `1.7` |
| Historical `results.json` | `schemaVersion` | Declared `1.x`; missing version defaults to `1.4` |
| Task YAML | No independent version field | Existing legacy format, unchanged |
| `snapshot.json` | `schemaVersion` | `1.1` (independent artifact) |
| Portable executed-evidence manifest | `version` | `1.0` (independent) |
| Authored finite-input manifest | `version` | Explicit `1.1`; not native execution evidence |
| Dashboard/SSE event envelope | `schemaVersion` | `1.0` |
| `waza.preflight` report | `schemaVersion` | `1.0` (independent artifact) |
| `waza.grader-reference` labels / `waza.grader-review` declaration | `schema_version` | Exact `1.0` (independent profiles) |
| `waza.grader-assurance` report | `schema_version` | Separately selected `1.0`, `1.1`, or `1.2` |

Optional task `requirements` introduced by #660 are descriptive v1-compatible
metadata, without a global schema bump or new runtime/exit semantics. Old readers
can ignore them; a minimum-version note does not enforce requirements.
`waza preflight` requires a CLI build containing #660. Its report is identified
by `kind: "waza.preflight"` and is never parsed/discovered as evaluation results.
See [Offline Eval Preflight](../../guides/preflight/) for its states and strict policy.

The independent grader profiles use strict `schema_version` admission, not
the native same-major minor-version policy below. Offline `1.0`, calibrated
`1.1` and preserved-native `1.2` are separate operations; default readers reject
unselected profiles. Missing versions, unknown members and implicit migration
cannot select them. These profiles do not bump native eval/result/snapshot
versions or enable a public qualification lifecycle.
See [Separate grader-assurance reports](../schema/#separate-grader-assurance-reports).

## Policy

Schema versions use `MAJOR.MINOR` format with no patch component.

- **Legacy MINOR** changes are backward-compatible additions, usually optional fields. Legacy readers accept same-major artifacts and warn when they see unknown fields. Scenario evals are an exception: only exact `2.0` is supported.
- **MAJOR** changes are breaking. Legacy readers refuse artifacts from a different major version and point to `waza migrate <file>`; scenario-aware readers additionally accept explicitly selected scenario eval `2.0`.
- Missing `schemaVersion` in legacy eval/result inputs continues to default to `1.4`. Declared same-major versions remain accepted and unchanged on read/re-save; unknown fields are warned about. Scenario-bearing evals must explicitly select `2.0` and cannot omit the version. Task YAML has no independent version field.
- New evaluations and regrades write results `1.7`, independently of their eval input version. This does not upgrade legacy eval defaults or task formats. Scenario scaffolds explicitly emit eval `2.0`; scenario readers reject unknown eval/config fields instead of granting forward-minor compatibility. Reading/re-saving historical results does not invent reliability metadata or upgrade their declared version.

Legacy results and snapshots without consumed evidence retain ordinary JSON
decoding for opaque tool arguments, grader details and unknown fields, including
duplicate keys and nesting beyond 64 levels in those opaque values. Known typed
DTO keys still require exact spelling, reject duplicates and retain the
64-level structural limit; this is not restoration of every historical
last-key-wins behavior. Any non-null evidence profile invokes strict admission
on the original whole document, so overwrites cannot hide authored provenance.
Strict identity readers reject unpaired Unicode surrogate escapes instead of
normalizing them to replacement characters.
Transcript events use their actual wire keys for this fence, closing the
previous custom-decoder case-alias blind spot without changing serialized
field order or bytes.

## Migration command

Use `waza migrate <file>` when a reader reports an incompatible major version.

```bash
waza migrate eval.yaml
waza migrate results.json
```

For schema `1.0`, the command is a no-op because there is no prior major version to migrate from.
For a supported scenario eval `2.0`, it validates the file and leaves its bytes
unchanged; it does not downgrade scenario discovery semantics to `1.x`.

## Changelog

### Task-level MCP mocks (task YAML)

- Added optional `mcp_mocks` to task YAML. Each entry replaces the eval-level
  mock with the same name for that task and a new name adds a mock; other
  eval-level mocks still apply. This differs from task `command_mocks`, which
  replace the whole eval list.
- Task YAML has no version selector. Task mocks require an enclosing eval
  `schemaVersion` of `1.1` or newer and `executor: copilot-sdk`, and finite
  sequences require scenario eval `2.0`. An empty list is rejected.
- A task file that already contained `mcp_mocks` previously got an
  unknown-field warning and the key was ignored; it is now applied.
- Result cache keys include each task's effective MCP mocks with fixture
  contents. Tasks that use MCP mocks get new keys once; tasks without them
  keep their previous keys. A cached outcome whose runs record a mocked MCP
  server that the task no longer mocks is re-run. Outcomes cached before
  results 1.7 record no dependency modes and cannot be checked; clear the
  cache after removing MCP mocks from such an eval.

### Recovery evidence in results 1.7 (#662)

- Finite MCP/CLI sequences require exact eval `schemaVersion: "2.0"`, including
  task overrides and selected external MCP fixtures. Offline validation and
  execution apply the enclosing eval version; task YAML has no version selector.
- A started, interrupted CLI sequence attempt may omit `exit_code` when no
  completed result was captured. An explicit numeric zero still means a
  completed successful mock result. Existing numeric exit codes remain readable.
- Inline `code` graders receive `tool_events` and `command_invocations` during
  execution and regrading. Recovery examples check actual dependency operations,
  observed responses, and dependency-scoped retry counts as well as report files.
- Independent evidence judges retain an observed SDK tool failure's `error`
  even when no result payload was emitted. Missing results without an observed
  failure remain incomplete evidence; no successful response is synthesized.

### Portable evidence manifest 1.0 / snapshot 1.1 (#663)

- Added optional per-run `evidence` and scoped `requirement_explanations` to
  results without changing task semantics or historical result versions.
  New results use the current `1.7` writer version. The dashboard exposes every trial via
  `evidenceRuns` when any task run has evidence or requirement explanations.
  When all runs lack both, the API omits that field to preserve the legacy
  response shape; the dashboard fallback reports unassessed.
- Snapshot `1.1` adds `evidence`, selected `workspaceFiles`, command invocations,
  and checkpoints. Ordinary snapshot names/defaults and legacy loading remain.
- Manifest `version: "1.0"` is independent of artifact `schemaVersion`. It records
  full source origin (including prior-attempt state), runtime requested controls,
  nullable SDK/effective-model versions, unavailable version provenance,
  source/content digests, availability/completeness, redaction, and diagnostics.
  `no_skills` is `boolean|null`; `native_skill_control` is `unknown`,
  `sdk_default`, `requested_sdk_disable`, or `mock_not_applicable`, describing
  requested native control, not proven SDK enforcement.
- The precise independent schema is `schemas/evidence-manifest-1.0.schema.json`,
  embedded as `schemas.EvidenceManifestSchemaJSON`.
- Captured `environment` (`/env`) and `engine-config` (`/engine`) artifacts
  fingerprint captured allow-list/configuration and engine configuration, not
  the full host environment. Secret environment-key removals contribute to the
  redaction match count.
- `captured` requires actual content digest plus a snapshot pointer.
  `digest_only` has a separate source digest but no preserved contents/locator.
  Unavailable/not-requested artifacts have reasons and no locator. Overall
  workspace/external state cannot be inferred from individual captured files.
- Regraded evidence keeps `source_manifest_sha256`, invalidating original
  result/validation/grader-configuration/checkpoint links.
- Evidence-aware consumers reject raw duplicate keys, unknown metadata, and
  unsupported manifest versions; this is stricter than historical same-major
  artifact loading. Invalid evidence is withheld rather than treated as assessed.

See the [evidence hash and capture contract](/waza/guides/snapshot-replay/)
and [schema fields](/waza/reference/schema/#portable-evidence). Canonical JSON
uses Go sorted UTF-8 object keys, exact numeric lexical tokens, Go JSON escaping,
including HTML and U+2028/U+2029 escaping, and array order (`json-v1`).
Manifest SHA-256 excludes its own member (not an empty string)
and includes all recognized metadata. Timings participate; hashes are not
promised stable across runs.

### Scenario admission correction (2026-10-12)

- Native eval YAML/JSON parsing now enforces the published `scenario` schema
  whenever the field is present: a nonblank string and exact `schemaVersion: "2.0"`.
- This is an intentional compatibility change: previously accepted
  versionless/`1.x` `scenario: null` and numeric/boolean scalar coercions are
  rejected. Remove the field for legacy behavior, or provide a nonblank string
  with exact `2.0`. Truly absent `scenario` retains existing legacy defaults;
  tasks/results and their same-major behavior are unchanged.

### Snapshot 1.1 (independent artifact)

- Adds portable evidence metadata with full source origin, availability,
  separate source/content digests, requested engine/model and observed execution
  mode. Unsupported or uncertified runtime details remain unknown.
- Snapshot hashes and tool events do not preserve a reconstructible workspace
  or prove external state. Offline replay checks stored consistency only.

### 2.0 (scenario evals only)

- Added a nonempty `scenario` workflow identity with optional existing `skill` context; scenario scaffolds explicitly select exact eval `2.0`, while legacy reader defaults remain `1.4` and task formats are unchanged.
- Finite MCP/CLI response `sequence` fields are admitted only for scenario evals. Each matching call consumes one bounded step; sequence exhaustion and unmatched calls fail closed. This does not add automatic retries, transport cancellation or host isolation.
- Target-free scenarios without effective explicit skill directories disable native SDK skill discovery; disabling the last eval directory preserves that isolation. Explicit task directories still opt in, empty task directories disable discovery, and `--no-skills` overrides opt-ins.
- Scenario evals reject unknown eval/config fields and future minor versions. Old executables reject major `2` before execution.
- Scenario result caching is bypassed with a notice until external dependencies can be completely fingerprinted.

### 1.7 (new results only)

- Recovery metadata and independent-judging fields are separate optional
  additions to the same-major result contract; readers do not infer either
  feature from the version alone, and historical artifacts keep their version.
- Adds optional `tasks[].runs[].dependency_modes`, listing configured dependency kind/name and `live` or `mocked` mode for each run. This reports configuration only; it does not prove the dependency was invoked or verify a real component. CLI response attempts remain in `runs[].command_invocations`, and historical results preserve their original version and fields.
- Adds optional final prompt-grader `evidence_context`, frozen
  `runs[].judge_evidence` bundle version `1.0`, typed `runs[].prompt_judging`
  observations, and separate current-invocation `regrade_usage.sessions`.
  Omission preserves legacy grading and cache behavior.
- Evidence-enabled snapshots `1.2` reference the frozen bundle digest; ordinary
  snapshot writers remain `1.1`. Legacy result/eval defaults remain `1.4`.
- Independent judging requires the current CLI implementation, explicit
  credentials, usable required evidence and the pinned runtime isolation profile.
  See [evidence-backed independent judging](/waza/guides/graders/#evidence-backed-independent-judging)
  for supported scopes, bounded files, privacy and claim limits.

### 1.6 (new results only)

- Adds optional `runs[].evidence` manifests and `runs[].requirement_explanations` linking deterministic verdicts to observed checks and unavailable evidence.
- This is a result-writer version, not a change to the independent manifest or snapshot schemas. Legacy result versions remain unchanged on read/re-save, and missing versions still default to `1.4`.

### 1.5 (new results only)

- Added optional `tasks[].stats.pass_rate_ci` (Wilson 95% interval on each task's pass rate) and `summary.statistics.success_rate_ci` and `pass_hat_k` (suite success-rate interval and pass^k reliability curve) to `results.json`. See [Statistical Fields](../statistical-fields/).
- `is_significant` is no longer written. It tested whether a CI on a task's own 0–1 score excluded zero, which compares an absolute score with zero instead of testing a difference between runs. Readers still accept it in older artifacts.
- Result emission uses a separate writer version; legacy eval defaults, task loading, scenario admission, and historical result round-trips are not upgraded.

### 1.4

- Added optional `runs[].command_invocations` to `results.json`. Invocation arguments are sanitized; command response output is not copied into this field.

### 1.3

- Added optional `command_mocks` to eval and task YAML, with executable argument matchers, deterministic stdout/stderr/exit codes, fixture files, environment/working-directory matching, and exact invocation-count expectations (#634).
- Added optional `runs[].session_digest.tool_policy_mode` and `tool_policy_denials` to record the selected custom agent's effective tool policy and denied attempts (#585).
- The dashboard API exposes these as `toolPolicyMode` and `toolPolicyDenials`; NDJSON session logs include the digest in `run_complete` events.

### 1.2

- Added optional `EvalSpec.adversarial` to declare built-in fault-injection packs and the `on_unsafe_outcome` policy consumed by `waza adversarial --spec` (issue #365).
- Added optional `runs[].snapshot_path` to `results.json`, pointing to the per-task `snapshot.json` artifact produced by `waza run --snapshot` (issue #367).
- Introduced `snapshot.json` as a separate `task-snapshot` artifact with its own independent `schemaVersion: "1.0"`, prompt/fixture digests, environment allow-list capture, redaction metadata, and ordered tool-event replay tape (issue #367).

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
