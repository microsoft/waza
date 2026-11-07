# Existing-workflow compatibility corpus

External definition conversion is separate from preservation of existing native
workflows. See [External evaluation interoperability](INTEROPERABILITY.md) for
the assertion-only AgentV preview, exact version pin, oracle evidence, rejected
lossy mappings and unsupported whole-suite semantics.

This is the offline preservation gate for #658 and the additive work under
#657. The contract is intended existing v1 behavior, not a new assurance
feature. The baseline is commit `04e7b4c6d593555834e71221227326c63274a921`;
the issue's earlier reference is `774df00a4f04fb5d193548a2cf3f45470fe285bb`.
Fixtures are synthetic, sanitized representations of historical wire formats,
not captures from a live account. No production defaults change.

## Logical execution attempt sidecars (bounded foundation)

The additive `execution-attempt-1.0.schema.json` profile and
`internal/executionattempt` codecs are declarations only. They do not alter
manifest v1, legacy result readers, CLI execution, lookup, billing, or Lab
behavior. The separate owned submission library described below has production
caller coverage; full delivery of #764 still requires public surfaces, broader
duplicate/concurrent-writer coverage, release/migration guidance, and final
validation. Do not treat pure codec tests as storage or runtime execution evidence.

The three frozen attempt, decision, and completion JSON fixtures retain their
recorded bytes without line-ending conversion through path-specific
`.gitattributes` rules. Their byte oracles are not normalized to hide checkout
changes; the codecs separately accept valid CRLF JSON whitespace. These rules
do not change legacy fixtures or wire decoding.

The source owner API in `internal/executionledger` now wraps the real foundation
namespace, without adding another persistence provider. `New(backend)` requires
an initialized supported backend. `Reserve(ctx, binding, parent)` generates a
fresh execution ID and returns an opaque `Creator` only after exclusive creation
and complete-byte readback. `Reservation(creator)` returns detached original
bytes/context; `ValidateCreator(ctx, creator)` compares them with current storage.
Zero, foreign, occupied, or ambiguous reservation results cannot grant authority.
IDs, stored records, and checkpoints never hydrate a creator.

`Complete(ctx, creator, manifestDigest, names...)` requires that retained creator
and actual finalized, verified artifacts; it retains the original binding and
canonical reservation hash. Identical completion retries require the same
creator. `ReadCompleted(ctx, source, names...)` verifies the actual reservation,
completion, manifest, objects, and final marker without granting authority.
`ReadCompletion(ctx, executionID)` separately returns the bounded canonical
completion declaration and its bytes/digest, not verified publication. Only a
missing completion record is absence; malformed records and storage failures are
errors. If a record exists, missing referenced manifests, artifacts, or final
markers do not erase its presence or authorize stale before-unit recovery.
Before-unit restore must reject an existing completion, verifying its actual
referenced source or failing closed. Concurrent completion after a read is not
excluded by a lease or cross-object transaction.
`Distinct(ctx, creator)` records the newly owned target; `Reuse(ctx, binding,
source, names...)` records only a verified compatible original source, with no
new execution ID or runtime call. Failed source outcomes and original native
ordinals remain unchanged. These are cooperating-writer API guarantees, not a
cross-object transaction, lease, tamperproof storage, or execution attestation.
The legacy runner and public submission/CLI/API/Lab integration remain separate.

`Ledger.Reserve` applies the shared `execution.ValidateOwnedRuntime` guard before
any namespace read or creation for new owned work or before-unit recovery.
Owned submission reapplies that same guard before executing native work.
The conservative profile rejects effective ambient BYOC providers and nonempty
`COPILOT_CLI_PATH` overrides; default embedded native execution remains eligible.
Mock mode ignores native overrides. This does not change legacy runner/SDK
behavior, hash or store credentials, or treat VCS revision as runtime attestation.
Finalized source-only restore performs no new engine call and preserves original
verified evidence; it is not a claim about the current native provider.

`runnerunit.Service.Submit(ctx, wrapperBytes, backend, hooks)` is an additive
embedding API, not yet a CLI/API/Lab route. Its strict
`execution-submission-1.0.schema.json` wrapper retains the original native request
and admits it once. Omitted wrapper policy selects distinct execution; explicit
null is invalid. Distinct submission reserves before factory construction,
records the decision, executes the admitted unit once, and publishes its original
terminal and category-scoped artifacts through the actual Store before completion.
`completed` means terminal evidence was finalized, not that grading passed.
Cancellation after reservation can finalize a canceled terminal with zero calls.
Publication or observer errors are submission errors, not successful completion;
partial immutable records are retained rather than reclaimed.

Explicit reuse verifies finalized original context and any recorded report
against the terminal, including status and grader verdicts, before recording an
actionable decision. It makes no factory/runtime or observer call. The returned
caller identity is separate from unchanged source identity/ordinal. Outer
`engine_calls` counts only new calls; source report usage remains historical,
not new-operation billing. Missing reports/usage remain unavailable. These
checks establish source content and consistency, not runtime or billing
attestation. `SubmissionHooks.Reserved` receives the retained creator and a
detached reservation before the factory; `Terminal` receives a detached original
envelope after joined lifecycle. Hooks are embedding-host code, never request
JSON, and do not prove cancellation acknowledgment or process-wait completion.
`Admission` receives detached declared binding before reservation, permitting the
category owner's actual capture policy to reject identifiers before persistence.
`Artifacts` receives detached reservation, terminal, and rich-report bytes after
lifecycle return, and can return at most 16 named objects for the same original
manifest. Names, media types, per-object/total bounds, and duplicate names are
validated before blob writes; observer/publication failures cannot complete the
attempt. Content binding does not turn a sidecar declaration into attestation.
Its typed `ObservedReport` retains a detached original comparison binding and
actual observation presence, including unavailable versus explicitly empty
transcript/tool arrays, validation maps, and recorded zero usage. The legacy
raw-report omission behavior is unchanged; deserializing an omitted array never
proves observed emptiness or hydrates a creator.

Raw rich reports contain potentially restricted conversation/tool/path data and
are no longer persisted or returned by owned submission, including reuse.
They remain detached in-memory input to the category producer; legacy Run and
historical report readers are unchanged. Private storage and source integrity
do not constitute category capture/disclosure approval.

`PreparePublication` runs before the terminal observer and blob writes. Its
first-publication terminal projection may change only diagnostic messages and
observation error text; canonical comparison preserves every other native field,
verdict, code, ordinal, and counter. The prepared terminal is shared by the
observer and the original manifest; duplicate matching terminal bytes are not
republished. Raw `report.json` cannot be reintroduced as an extra artifact.
`ValidatePublication` receives a fresh snapshot of the independently retained
original invocation after preparation, before any terminal observer or writes.
It requires a preparation policy. The fixed `ownedsubmission.CaptureHooks`
facade regenerates category artifacts from that original typed report and
compares the complete inventory, media types, and bytes against the prepared
publication. Consistently mutating a callback report and its projected terminal
cannot substitute category observations. These are cooperating producer
comparisons, not SDK/process attestation or disclosure authorization.
The fixed facade rejects competing preparation/validation and extra-artifact
callbacks: an unvalidated renamed raw report cannot bypass its category inventory.
Additional lifecycle artifacts require a separately admitted producer contract;
arbitrary embedding artifact callbacks remain outside the protected facade.

The facade explicitly withholds conversation, tool, and reliability categories;
every nonempty native diagnostic/error message becomes `[REDACTED]` before first
publication, without changing codes, verdicts, counters, or original ordinals.
Restricted native identifiers fail before reservation. Reuse admits only
already-protected original terminal text and never sanitizes a finalized source.
`ReuseAdmission` can deny source disclosure before an actionable decision;
reuse never runs first-publication projection or rewrites original terminal
bytes. Public submit registration remains disabled until the actual evidence
privacy/category policy and protected transport are fully integrated. Public
integrations remain pending.

The additive `execution-submission-result-1.0.schema.json` and
`ParseSubmissionResult`/`EncodeSubmissionResult` validate original wire types,
phase requirements, caller/decision logical context, original reservation hash,
completion/source references, native terminal binding and new-call counts.
Reuse cannot relabel the original ordinal or count historical calls as new.
An optional historical report must agree with the original native verdicts and
status, but absence remains unavailable. These offline profile checks access no
provider, establish no runtime/billing attestation, and grant no capture or
disclosure rights. They do not modify any legacy reader/default.

Logical run is the existing `runneridentity.Identity.Run`; logical work is
`Identity.Trial`. An execution owner allocates a fresh opaque `exec-` identifier,
independent of the caller's native `Selection.Attempt`. Allocation uses 256 random
bits, not a uniqueness or reservation guarantee. The owner reserves the ID
conditionally before owned execution. Decision IDs use
a separate `decision-` namespace. Original bindings use the actual
`artifactmanifest.Binding`; declared context is not runtime attestation.

Attempt records retain their original bindings and optional `retry` or `branch`
parent links. Complete lineage validation rejects missing/cross-work parents,
cycles, and repeated execution IDs; input order is irrelevant and siblings are
incomparable. Multiple independent roots for the same logical work are valid,
including matching native attempt ordinals. This graph check is not an append
operation, global ordering, lock, overwrite guard, or exactly-once mechanism.

Duplicate decision declarations require an explicit policy. The submission
wrapper defaults to distinct execution to preserve legacy behavior, but the
decision codec never supplies an omitted policy. `distinct` policy/outcome requires a new execution ID and no
source. Explicit `reuse` policy requires a source execution ID and original
manifest digest, with `reused`, `unfinished`, or `conflict` outcome and no new
execution ID. Even a `reused` declaration is not evidence verification: a provider
must check immutable finalized source bytes and context before recording that
decision. Unfinished and digest-only sources must never become verified success.
Reusing failures must retain the original failure, native ordinal, terminal bytes,
and historical billing attribution; it must not invent a new execution or billing.

The strict offline codecs reject missing required fields, explicit null, invalid
wire types, duplicate/aliased keys, malformed Unicode, and invalid numeric tokens.
Optional parent/source members may be omitted where allowed but never null or
empty objects. Explicit empty grader arrays remain valid and distinct from null.
Canonical encoding preserves original ordinals, sorts grader IDs without mutating
caller slices, and uses compact Go JSON field order/escaping with no trailing LF.
The bounded graph accepts at most 1,024 records; each sidecar is at most 1 MiB.

The separate `execution-completion-1.0.schema.json` profile names the original
execution ID, canonical reservation-byte digest, and manifest digest.
`internal/executionledger.ValidateContext` admits these references only when
whole canonical bytes match both digests and the manifest retains the exact
original native identity, selection/ordinal, execution mode, and grader set.
This is pure context/hash admission, not source-artifact or final-marker
verification. The shared provider must independently verify the finalized
manifest and artifact bytes before a retained creator capability can publish a
completion or any caller can record verified reuse. No persistence provider,
capability, reservation, or recovery behavior is supplied by this codec.
`ValidateReuseContext` additionally compares a reused decision to its original
source reservation, completion reference, and manifest. The caller may declare a
different native attempt ordinal and canonical input digest while retaining the
same logical work, shard/task/trial, execution mode, and grader set. The source
execution ID identifies the original execution, not a new distinct target, and
its completion/native binding is never rewritten. Pure context admission still
requires real provider content/final-marker verification before actionable reuse.

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

Ordinary Linux, Windows, and preview validation run the broad Go suite with
`-count=1` under the rejecting runtime policy. Linux and preview also execute
`make test-compat-inventory`, which checks the corpus prerequisites and prints
the exact package inventory subsumed by that broad run; it does not execute the
same packages a second time.
Workflow regression controls execute the production test-selection shell with a
recording Go substitute and compare its arguments with actual package discovery.
They require the whole suite (excluding only the separately guarded native shard
package on Linux), uncached race/atomic coverage, and no narrowing test selectors.
They also execute each production runtime-marker check with an absent and a
present marker, requiring success only when no forbidden launch was recorded.
Required guard, inventory, test, marker, and integration steps cannot tolerate
failure via `continue-on-error`; mutation controls check every Linux, Windows,
and preview occurrence, allowing only omission or explicit `false`.

Ordinary and `--runner-compat` benchmark summaries use the command's configured
output writer and propagate write failures, including short writes, as output
errors (exit 2), before saving the requested result file. The summary text is
unchanged. Direct `runSingleModel` callers without a Cobra command retain stdout
output and the same write-error checks.

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
version `1.9`; reading/re-saving historical inputs keeps their declared version
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
JSON artifact against `compatibility-run-1.9.json`: a separate expected writer
artifact differing only in the explicitly advanced result version. A regression
asserts that this is the only difference; neither schema versions nor reliability
fields are added to the normalization allowlist. Historical corpus files and the
original `1.4`, prior-writer `1.5`, `1.6`, `1.7` and `1.8` oracles are not rewritten. A separate two-run comparison still checks repeatability.
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
