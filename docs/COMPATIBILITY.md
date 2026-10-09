# Existing-workflow compatibility corpus

This is the offline preservation gate for #658 and the additive work under
#657. The contract is intended existing v1 behavior, not a new assurance
feature. The baseline is commit `04e7b4c6d593555834e71221227326c63274a921`;
the issue's earlier reference is `774df00a4f04fb5d193548a2cf3f45470fe285bb`.
Fixtures are synthetic, sanitized representations of historical wire formats,
not captures from a live account. No production defaults change.

## Integration commands

From the repository root, after the normal embedded dashboard assets exist:

```bash
NO_COLOR=1 make test-compat
```

The target checks Git/Node/Python availability, fixes the locale/timezone, and
executes the fixed corpus **and** the existing package-owned
preservation suites, uncached. This intentionally reuses the tests and their
SDK fakes rather than copying them into a second suite. It includes all CLI
subpackages, every grader and the ownership areas below. Do not substitute
only the new `Compatibility` selector for the release gate.

For quick iteration on the new fixtures:

```bash
NO_COLOR=1 go test ./cmd/waza ./internal/models ./internal/graders ./internal/orchestration ./internal/snapshot ./internal/registry ./internal/webapi -run Compatibility -count=1
```

For the dashboard, build first, then run the historical fixture tests:

```bash
cd web
npm run build
npx playwright test e2e/compatibility.spec.ts --project=chromium
```

These commands need Go 1.26+, Git, the existing script-grader runtimes
(Python and Node), and, for the browser gate, installed web dependencies and
Chromium. A fresh worktree must have `web/dist/index.html` and
`web/dist/favicon.svg` for Go embedding; create missing stubs only, never
overwrite existing assets. Missing prerequisites are failures, not evidence of
compatibility. Live integration tests remain opt-in and separate; this target
does not set their enabling environment variables or use paid agent calls.

## Fixed artifacts and repeatability

`internal/testdata/compatibility/v1/` owns the versioned evals, task, all-grader
configuration, results, snapshot, lock shape and dashboard API fixture.
Treat these as fixed inputs. Change an expectation only with an explicit
reviewed behavioral decision, not to make a failing test green.
Remote-lock setup exercises both LF and CRLF eval inputs and asserts that
the inline grader was replaced before testing lock expansion and tampering.

The version matrix derives versionless, 1.0, 1.1, 1.4, forward same-major 1.99,
cross-major 2.0 and malformed headers from the fixed files. Unknown additive
fields and invalid known-field types are tested separately. Assertions check
loaded semantics (defaults, golden flags, typed grader parameters, output and
usage), not merely successful JSON/YAML parsing.

Task YAML has no independent `schemaVersion` member or version negotiation.
Its current loader warns and ignores unknown fields while rejecting invalid
known-field types/constraints. Do not infer a task major-version policy from
the eval/result policy. Eval/result versionless inputs default to the current
v1 version; explicit same-major versions load, cross-major versions require
migration. Snapshot versioning is independent of result versioning.

Run the full corpus twice, without Go test caching:

```bash
NO_COLOR=1 make test-compat
NO_COLOR=1 make test-compat
```

`TestCompatibilityScenariosRepeatable` also grades each of the nine recorded
cases twice and compares the typed verdict projection: grader identity, kind,
pass/fail and numeric score. Only grader elapsed duration and diagnostic
workspace paths are outside that projection. The inputs (output, tool
arguments and workspace file bytes) are fixed, not normalized.
`TestCompatibilitySnapshotReferences` compares the entire fixed round-trip
snapshot and the result's eval/task/run identities, events, output and
verdicts; it proves changed tool arguments diverge. No identity fields are
stripped from those assertions.

The CLI process test additionally produces two full result artifacts and
compares their typed content. Its volatile allowlist is `eval_id`, `timestamp`,
summary/run/grader durations, average task duration, workspace directory and
the mock session ID. Before replacing the session ID, it requires exactly one
task/run/session and proves the run digest and evaluation ledger reference the
same nonempty session. Both references then receive the same canonical ID.
Checkpoint verdicts, prompts, outputs, billing, configuration and scores remain
in the comparison.

## Capability inventory and package ownership

Ownership here means the package where a dependent implementer should add a
regression test, not a new organizational assignment.

| Preserved family | Owner and executable coverage |
|---|---|
| Artifact versions, output shape, default injection/routing/executor/trials | `internal/models`: `TestCompatibilityArtifactVersions`, `TestCompatibilityTaskContract`; `internal/validation`: schema and documentation-example suites |
| All 13 grader families | `internal/graders`: `TestCompatibilityGraderInventory` constructs every kind in `models.AllGraderKinds()`; each `*_grader_test.go` covers actual pass/fail/error behavior |
| Text, file, code, program, JSON schema, prompt/rubric, behavior, action sequence, skill invocation, trigger, diff, tool constraint, tool calls | `internal/testdata/compatibility/v1/graders.yaml`; inventory equality fails when a kind is added without a fixture; prompt/quality execution uses existing fake engines, not a live judge |
| CLI run/grade/compare/gate/check/replay and exit policies | `cmd/waza`: `TestCompatibilityCLIProcessExits` builds and invokes the actual binary; existing `cmd_run`, `cmd_grade`, `cmd_compare`, `cmd_gate`, `cmd_check`, `cmd_replay` suites retain flag/output/error coverage |
| Skill discovery/injection, task overrides, disabled/required skills, custom-agent lifecycle | `internal/execution`: skill injection, engine shutdown and tool-policy suites; `internal/skill`: skill/agent parsing; `internal/orchestration`: skill discovery and agent grader suites; `cmd/waza`: new/check/workspace command suites |
| Token budgets and quality checks | `cmd/waza/tokens`, `internal/checks`, `internal/scoring`, `internal/quality`, `cmd/waza/cmd_quality_test.go`: strict/warning policy, JSON shape, fractional scores, fake judge errors |
| Static multi-turn/checkpoints and responder decisions | `internal/orchestration/runner_orchestration_test.go`: `TestExecuteRun_Checkpoints_*`; `responder_loop_test.go`: reply/stop, abstain/error, cap exhaustion; `internal/responder` decision parsing |
| CLI mock inheritance, populated/empty task replacement | `internal/orchestration`: `TestCompatibilityMockRequestOverrides` checks the actual execution request and eval-relative fixture base |
| Configured unmatched CLI calls, response exit codes, counts, isolation | `internal/commandmock/session_test.go`, `command_test.go`; `internal/execution` mock hook/session suites; `internal/orchestration`: `TestCommandMockFinalizationErrorFailsRun` |
| MCP mocks, exact/schema/regex arguments, unmatched/unknown calls | `internal/mcpmock`: `TestServerToolsCallMatchesExactSchemaAndRegex`, `TestServerUnknownAndUnmatchedCallsReturnMCPErrorResult`; MCP example asserts recorded argument correctness |
| Golden failures and default gate policy | `cmd/waza`: process exit 2 takes precedence over regression exit 1; `cmd_gate_test.go` covers once-golden handling, new/removed task policy and opt-out |
| Cache versus current execution billing, retries, auxiliary sessions | `internal/orchestration/session_accounting_test.go`: `TestBenchmarkCacheExcludesHistoricalUsageOnReusedEngine`, `TestRetryAccountingIncludesDiscardedAttemptAndItsGraders`, responder and cumulative multi-turn tests |
| Missing usage/credit attribution, reported zero, RPC/shutdown fallback | `internal/models` credit aggregation; `internal/execution` usage collector/scope/RPC suites; `internal/webapi`: historical fallback versus current ledger, omission versus zero |
| Remote grader locks and trust | `internal/models`: `TestCompatibilityLockIntegrity`; `internal/registry`: `TestCompatibilityRemoteLockTampering` resolves a local Git module, rejects content changes and missing pinned commit offline; existing program-grader trust and missing-lock tests |
| Snapshots, replay/bisect, redaction and fixture hashes | `internal/snapshot`: fixed references/round trip plus existing capture/hash/redaction/compare suites; `cmd/waza`: process replay consistency failures and existing bisect tests |
| Legacy dashboard loading | `internal/webapi`: `TestCompatibilityHistoricalDashboard` exercises file ingestion and HTTP detail responses; its exact legacy API fixture is shared with `web/e2e/compatibility.spec.ts` |
| Historical dashboard empty states | Playwright: omitted prompt/transcript/model attribution/judge, unavailable credits, cached diagnostics and numeric zero; existing credit specs retain CSV/trend/comparison coverage |
| Other existing feature families | The target retains CLI authoring, registry, adversarial, trigger, workspace and recommendation-facing command suites; `go test ./...` remains the required broader gate |

## Stable exit meanings

| Command | Existing contract |
|---|---|
| `run` | 0 success, 1 failed task validation, 2 configuration/runtime error |
| `gate` | 0 pass, 1 regression/task-set policy, 2 golden failure, 3 configuration error |
| `grade` | Emits JSON `passed: false` for a failed verdict but currently exits 0; consumers must inspect the verdict. This is reporting, not the CI gate. |
| `replay` | 0 consistency pass, 1 inconsistency/divergence; configuration errors use the normal error exit |

## Three sanitized workflows

See [the scenario guide](../examples/compatibility/README.md).
Each of `examples/compatibility/{mcp,cli,repository}/` contains a task and
three recorded cases: known-good, deliberately-bad and alternative-valid.
Alternate JSON ordering/extra metadata, prose and report metadata can pass;
wrong MCP arguments, a wrong CLI command and changed repository source fail.
They are graded recorded outcomes, **not** agent executions.

The `mock` executor is a wiring/defaults smoke test. It does not call MCP,
launch declarative CLI mocks or prove model quality. Declarative command
mocks require `copilot-sdk`, and their offline tests use the existing SDK
fakes/session helpers. No mock result establishes real-world assurance.

## Known bugs and boundaries

- #656 (skill injection missing the resolved base directory) remains an
  independent reliability fix. No corpus assertion requires that directory
  to be absent; injection tests preserve discovery/body selection only.
- Duplicate task IDs and unmatched globs belong to authoring/preflight
  diagnostics. The corpus does not assert that duplicate IDs are successful
  or turn loader quirks into a new policy. Existing run filtering/empty-task
  and grade-empty-task tests remain in the gate.
- Offline replay currently checks stored consistency; it does not recreate
  a model response or establish real-world reproducibility. Live replay,
  assurance policies and new roadmap features are not claimed as shipped.
- No UI implementation changes are made, so existing dashboard screenshots
  remain valid. Browser tests exercise historical states without replacing
  documentation images.

Every dependent change must run the corpus and document intentional opt-in
behavior separately. An additive feature must not rewrite these fixtures to
silently redefine old command, grader, skill or billing semantics.
