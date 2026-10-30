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

The target checks Git/Node and a working Python interpreter (preferring
`python3`, then `python`), fixes the locale/timezone, and
executes the fixed corpus **and** the existing package-owned
preservation suites, uncached. This intentionally reuses the tests and their
SDK fakes rather than copying them into a second suite. It includes all CLI
subpackages, every grader and the ownership areas below. Do not substitute
only the new `Compatibility` selector for the release gate.

The recovery lifecycle regressions include a forced readiness deadline and a
joined child safety kill; an incomplete invocation keeps its exit code
unavailable. Session inspection and finalization are serialized, including
native capture, but callers must stop command processes before finalization.
The SDK judge wire suite uses ordinary lifecycle budgets for admission and
arms short cleanup bounds only after the relevant phase is reached. Separate
delayed Start/Create controls require explicit deadline failure before any
`session.create` request. Sanitized phase/deadline and RPC-method diagnostics
are retained before count assertions; passing these controls does not identify
the cause of an earlier uninstrumented timeout.

The real MCP subprocess lifecycle test gives process creation a separate
30-second setup guard, then starts the unchanged 10-second MCP exchange/EOF
budget. Cancellation callbacks are joined when changing phases or cleaning up;
child `Wait` uses a separate three-second join bound. The setup guard detects
a late `Start` return but cannot interrupt a blocked OS process-creation syscall.
Fake-time controls cover delayed startup and both phase deadlines; the actual
MCP caller also exercises a partial-request deadline with stdin still open.
These controls do not establish why a particular host's startup was slow.

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
(a working `python3`, or `python` as fallback, and Node), and, for the browser gate, installed web dependencies and
Chromium. A fresh worktree must have `web/dist/index.html` and
`web/dist/favicon.svg` for Go embedding; create missing stubs only, never
overwrite existing assets. Missing prerequisites are failures, not evidence of
compatibility. Live integration tests remain opt-in and separate; this target
forces `ENABLE_COPILOT_TESTS=false`, even when the caller enabled live tests,
and does not use paid agent calls. `TestCompatibilityOfflineMakeTarget` checks
the actual recipe's subprocess environment and each missing prerequisite
without executing Go or an agent (POSIX hosts with Make installed).

The broader Go suite also includes
`internal/nativesnapshot: TestPinnedOriginalFixturesPreserveCheckoutBytes`.
This test requires Git to perform actual isolated index checkouts with
`core.autocrlf=true` and `false`; missing Git is an explicit infrastructure
failure, not a skipped compatibility check. This is not a runtime Git
dependency. The separate mandatory
`internal/releasepolicy: TestRetryPinnedOriginalJournalOracle` checks all 431
cases against retained exact source bytes without Git or historical ancestry.
Checkout tests do not certify Windows durable collection writers.

The native-snapshot original-capture oracle compiles its checksum-pinned source
overlay once per package test process. Only the executable is reused: every
capture launches a fresh process against the current fixture roots and writes
a separate output artifact. `TestG2OriginalOracleReusesOnlyCompilation` checks
changed prompt bytes and different roots without caching captured results.

## Fixed artifacts and repeatability

`internal/testdata/compatibility/v1/` owns the versioned evals, task, all-grader
configuration, results, snapshot, lock shape and dashboard API fixture.
Treat these as fixed inputs. Change an expectation only with an explicit
reviewed behavioral decision, not to make a failing test green.
The user-directed 2026-10-12 scenario admission correction is an explicit
exception: native YAML/JSON readers reject present null/non-string/blank
`scenario` fields, including previously accepted versionless/`1.x` nulls and
scalar coercions. A present identity requires a nonblank string and exact eval
`schemaVersion: "2.0"`. Remove the field to retain legacy behavior, or supply that
identity/version pair. Truly absent scenario fields and task/result defaults are
unchanged. `TestClassifyEvalSpecDeclaredScenarioSchemaParity` compares native
classify/parse/load/offline admission with the published schema for both formats.
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
v1 reader version (`1.4`); explicit same-major versions load, cross-major versions
require migration. New evaluations and regrades independently write result
version `1.7`; reading/re-saving historical inputs keeps their declared version
and never invents reliability fields. Snapshot versioning is independent of
result versioning.

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

The frozen `cmd/waza/testdata/compatibility-run-1.4.json` oracle was verified by executing
the CLI built from an isolated `git archive` of baseline `04e7b4c6` against
the fixed eval/task inputs. The current CLI process test compares each full raw
JSON artifact against `compatibility-run-1.7.json`: a separate expected writer
artifact differing only in the explicitly advanced result version. A regression
asserts that this is the only difference; neither schema versions nor reliability
fields are added to the normalization allowlist. Historical corpus files and the
original `1.4` and prior-writer `1.5` oracles are not rewritten. A separate two-run comparison still checks repeatability.
Mutation tests prove prompt, checkpoint, configuration, billing, score, output
and removed-field, null/empty, omitted/zero and cardinality drift cannot be
normalized away. Its volatile allowlist is `eval_id`, `timestamp`,
summary/run/grader durations, average task duration, workspace directory and
the mock session ID. Before replacing the session ID, it requires exactly one
task/run/session and proves the run digest and evaluation ledger reference the
same nonempty session. Both references then receive the same canonical ID.
Checkpoint verdicts, prompts, outputs, billing, configuration and scores remain
in the comparison, as do raw optional-field omission and null-versus-empty shapes.
Program-grader execution uses a native test executable on every platform to
cover pass/fail, stdin, workspace environment, stderr, missing executables and
pre-cancelled contexts; older shell-specific tests remain POSIX-only.

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

## Explicit native release-policy addition

Native Copilot collection is additive and requires explicit
`--collector native-copilot` selection on both `compare-plan` and
`compare-collect`. The mock collector remains the default, and ordinary
`compare`/`gate` behavior and exits are unchanged unless a release policy and
collection directory are explicitly selected.

Native planning writes a `waza.release-native-policy` version `1.0` envelope
containing the core policy protocol version `1.0`, native task profile version
`2.0`, source-capture digest and required core
`collection_profile: "copilot_sdk_single_turn_text_v1"` discriminator. Frozen
core-only readers reject both the envelope and the extracted core policy with
that unknown discriminator; stripping the wrapper does not make native policy
legacy-compatible. The public native 2.0 adapter bounds source, evaluator,
capture-wire, attempt-descriptor, request, path and tape sizes; those bounds do
not change existing private 1.0 bounds. Oversized native payloads/tapes fail
explicitly. Operationally incomplete native collection returns nonzero even
after core publication, with the cause in `native-stop.json`; an API collection
item's `error` can coexist with its attempts. Native collection can incur
provider usage and durable creation is limited to Linux/macOS. The
compatibility corpus remains offline and does not invoke this paid-capable
producer.

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
