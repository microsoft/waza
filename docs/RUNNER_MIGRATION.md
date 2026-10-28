# Runner 1.0 migration and adapter reference

The runner is an **unreleased, main-only** single-unit interface for v0.40.0.
It is additive: it does not replace local `waza run`, `grade`, `replay` or
historical artifact formats. No existing command is deprecated by this change.
Use runner 1.0 only when its one-task/one-trial/one-attempt semantics preserve
the behavior you intend; otherwise continue using the existing local command.

The frozen mapping oracle is
[`adapter-matrix.json`](../internal/runnercontract/testdata/local/adapter-matrix.json).
The table below reflects that fixture. It is not a promise that every historical
workflow can be converted.

## User migration guide

### Preserve a historical workflow

Keep the existing command and its established selection, retry, grading,
result-writing and exit behavior:

```bash
waza run eval.yaml --task selected --trials 3 --output results.json
```

This remains a historical multi-unit command; it is **not** translated into one
runner unit. Existing local runs can include multiple tasks, trials, attempts,
baseline or parallel work, caching, snapshots, hooks, and other features. Do
not substitute `waza runner run` when you need any of those semantics.

### Opt into one exact supported work unit

For compatible inline mock/text input, capture the exact source files and
selection as a request, then execute that request once:

```bash
waza runner adapt --eval eval.yaml --task tasks/selected.yaml \
  --run-key qualification-42 --shard shard-a --task-id selected \
  --trial 1 --attempt 1 --timeout-ms 60000 > request.json
waza runner run < request.json > selected.runner.json
waza runner validate-result < selected.runner.json
```

The example's task file must contain `id: selected`; the request shard and
selected shard must both be `shard-a`. Trial and attempt are one-based and must
be declared by the eval. `runner adapt` reads only those exact files, does no
discovery or execution, and preserves their source bytes. `runner run` has no
selection flags, performs no automatic retry and writes one terminal result.
Inspect `outcome`/`exit_code`; a nonzero process code is the result category,
not evidence that the result file is malformed.

If the adapter rejects a field or behavior, keep using `waza run` or redesign
the input explicitly for the supported profile. Do not silently drop hooks,
skills, prompt files, CSVs, requirements or other unsupported semantics to make
the request pass. For native execution, use the public request directly and
require both `allow_paid_execution: true` and `inline.native-text`; this can
incur model usage. The local adapter intentionally emits mock-only requests.

### Historical result files

`results.json` and the existing evaluation/result schemas continue through
their established commands and readers. A terminal runner result is a separate
`waza.runner-result` envelope, not a legacy `EvaluationOutcome` or aggregate
results file. Use `waza runner validate-result` only for `*.runner.json`
envelopes; use historical result tooling for ordinary run outputs. Historical
IDs are not migrated or recalculated into runner IDs.

## Compatibility matrix

### Execution commands and services

| Existing/new surface | Runner mapping | Compatibility action |
|---|---|---|
| `waza run` | Only an exact selected mock/text task, single trial and single attempt can pass through `adaptLocal` → inline execution. | Historical behavior remains unchanged. Use `runner adapt` only when its source profile preserves the intended behavior. |
| `waza runner adapt` | Captures caller-named eval and task files, then parses/admit-validates the runner request. | No discovery, rewriting, runtime construction or execution. Native adaptation is rejected; it emits no partial runnable request on failure. |
| `waza runner run` | Versioned request → admission → one inline attempt → terminal writer. | Writes a runner envelope, not historical results. One invocation never scans adjacent work or automatically retries. |
| `waza serve` / `waza dashboard` | `POST /api/v1/runner/run` calls the same runner service. The server persists terminal envelopes under `results/.runner` for Lab inspection where private publication is supported. | Existing server/dashboard behavior remains. On unsupported platforms publication fails explicitly as `infrastructure_failure` with `publication_failed`; execution evidence is retained in the returned envelope, but persistence is not claimed. |

### Historical inputs and artifacts

| Family | Preserved behavior / mapping |
|---|---|
| Eval YAML | Historical 1.x eval readers and supported minor versions remain. The captured 1.x runner text profile requires explicit `disabled_skills: [*]`. Scenario 2.0 has no ambient skill discovery. |
| Task YAML | Historical task YAML remains unversioned. Omitted/null `enabled` inherits the historical default of true; `false` is not executable. Omitted/null timeout inherits; explicit zero is rejected. `command_mocks: null` inherits, while `[]` is an explicit unsupported override. |
| Evaluation results | Historical 1.x `EvaluationOutcome` readers remain; current aggregate writers emit 1.7. Runner 1.0 envelopes are distinguished by `kind: "waza.runner-result"` and are not interpreted as evaluation results. |
| Snapshots and sidecars | Historical snapshot/evidence families remain accepted by their own readers. They are never converted into runner success. |
| Historical identifiers | Existing IDs are not rewritten, re-derived or claimed to match runner logical identities. |

### Commands that are not one runner unit

These surfaces keep their existing local behavior and have no semantics-preserving
single-unit mapping:

| Existing command(s) | Why it is not one runner unit |
|---|---|
| `run` batch modes | Multiple resolved tasks/trials/attempts, baseline, concurrency, CSV, caching, snapshots, execution-only modes, hooks, skills, files, dependency mocks and multi-turn orchestration remain historical behavior. Native local modes retain the legacy shared SDK lifecycle, not runner's request-owned cleanup/consent policy. |
| `dev` | Iterative/batch optimization; no exact single-unit mapping. |
| `compare`, `compare-collect`, `compare-plan`, `compare-assurance-plan` | Paired/multiple observations, controlled collection or offline comparison planning; not one selected attempt. |
| `assure` | Frozen multi-artifact assurance assessment; not one selected attempt. |
| `quality`, `suggest` | Quality judging or artifact authoring can load skill files and produce reports/writes; those side effects and semantics are outside the inline runner text profile. |
| `models` | SDK model-catalog discovery; not task execution. |
| `adversarial` | Multi-arm fault execution; not one task/trial/attempt. |
| `grade` | Offline regrading of historical evidence, not execution. |
| `replay` | Historical snapshot/artifact operation, not runner invocation. |
| `preflight` | Offline static validation, not execution. |
| `results` | Historical artifact inspection, not runner execution. |
| `spec` | Static schema tooling, not execution. |
| `migrate` | Historical file migration, not runner execution. |
| `init`, `new`, `get`, `update`, `tokens`, `gate`, `coverage`, `metadata`, `check`, `cache`, `session`, `mcp-mock`, `command-mock`, `registry` | Project setup, skill management, static inspection, historical policy/artifact tools or mock-service setup; not one runner unit. |

The complete machine-readable source of this mapping is the linked fixture;
the table groups commands for readability, but does not expand runner support
beyond that fixture.

## Preserved, deprecated and incompatible surfaces

| Status | Surface | Action |
|---|---|---|
| **Preserved** | Existing `waza run`, `grade`, `replay`, batch features, their exit policies and historical eval/task/result/snapshot readers. | Continue using the same commands and artifacts. No migration is required. |
| **Preserved** | Existing run IDs and aggregate result files. | Do not rewrite them as runner IDs or pass them to `runner validate-result`. |
| **Additive / opt-in** | `runner adapt`, `runner run`, `runner capabilities`, `runner validate-result`; `POST /api/v1/runner/run`. | Explicitly choose runner 1.0 when the contract's source and selection constraints fit. |
| **Incompatible mapping** | Any historical behavior the runner profile cannot represent, including discovery, batch or adjacent work, retries, hooks, ambient skills, remote/external inputs, unsupported graders/dependencies or multi-turn behavior. | Hard error before execution; retain the local command or explicitly refactor inputs. No lossy conversion is performed. |
| **Deprecated** | None in this revision. | No removal is implied. |

The local adapter hard-fails with an actionable diagnostic beginning
`cannot preserve local semantics in runner 1.0`; its guidance is to keep using
`waza run`, or select one task/trial/attempt, inline prompt/resources, disable
ambient skills and use builtin text checks before retrying `waza runner adapt`.
Direct request validation reports `invalid_request` with a diagnostic code,
message and failure boundary. Version/capability negotiation failures are
`unsupported_capability`, not silent downgrade.

## Developer mapping and evolution

The request bootstrap schema (`schema_version: "1.0"`), offered runner interface
(`versions`), result schema (`schema_version: "1.0"`) and identity derivation
(`sha256-json-v1`) are independent. Changing one does not implicitly version
the others. A semantics change must use a new runner version or capability;
do not reinterpret a `1.0` field, identity, outcome or capability in place.
The Lab archive API is another independent version axis: it now returns
paginated archive schema `1.1`, containing runner result schema `1.0`.
Consumers of the prior unpaginated archive shape must explicitly support the
1.1 entries and cursor; the server does not negotiate a 1.0 archive response.

Adapters must preserve source bytes, exact selection and historical behavior.
They must not discover additional inputs, create runtime clients during
adaptation, rewrite unsupported fields, fabricate missing evidence, or return
success after a lossy mapping. Unsupported mappings are hard errors before
engine construction. Additions must keep the frozen compatibility oracle and
historical reader corpus unchanged unless a reviewed compatibility change
explicitly says otherwise.

For implementation details, normalization, error categories, capability
negotiation, identity derivation and terminal boundaries see
[the public runner contract](RUNNER_CONTRACT.md). The reference source of truth
is [`adapter-matrix.json`](../internal/runnercontract/testdata/local/adapter-matrix.json).

**No Lab UI change is required for #761.** Compatibility adapters and their
failure behavior are validated through commands, schemas, fixtures and these
docs; #761 adds no separate runtime status or evidence surface.
