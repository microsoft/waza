# Runner contract: negotiation and exact captured work 1.0

This source preview of [v0.40.0](https://github.com/microsoft/waza/issues/754)
implements negotiation [#757](https://github.com/microsoft/waza/issues/757)
and bounded exact mock/text execution [#758](https://github.com/microsoft/waza/issues/758),
with input-derived [logical identities](RUNNER_IDENTITIES.md) (#759). It is available
when built from this source, not retroactively in a published older binary.
The [documentation-site guide](../site/src/content/docs/guides/runner-contract.mdx)
is the canonical user invocation, API, compatibility and migration reference.

## Developer boundary

`internal/runnercontract.Negotiate` is the shared CLI/HTTP implementation. It
strictly parses UTF-8 JSON with duplicate-key and Unicode checks, validates
against embedded offline schemas, then negotiates version and capabilities.
It imports no model, engine, orchestration, subprocess, project configuration,
or network client package. Semantic execution admission lives separately in
`internal/runnerunit`, which imports the runtime/orchestration packages.

The compact negotiation request and complete execution request are separate
schemas. The latter preserves the pinned #779 source request's field names,
types, required presence and bounds; its shape is accepted for negotiation,
but its captured YAML is never interpreted. Partial execution forms are invalid.
Shape validity is not semantic admission of the selected unit.
Presence of any complete-only field selects the complete schema, even when
that field is null or another required field is missing. Diagnostics therefore
identify the invalid complete-request field instead of falling back to compact
unknown-field errors. Only negotiation fields are decoded after shape validation;
schema-valid integer spellings such as `timeout_ms: 6e4` and `trial: 2.0`
negotiate identically to `60000` and `2`, without execution-value conversion.

The negotiation response is **not** a terminal execution envelope. No work was
selected or executed, no identity was derived, and no observation or outcome is
fabricated. Capability acceptance alone is not evidence that a provider, tool,
skill, model, input or credential is usable. Future execution must invoke the
same negotiation before any runtime construction, then perform its own exact
selection and admission checks. `runnerunit` invokes this shared negotiation
first; negotiation-only endpoints keep their original offline behavior.

## Exact-unit execution boundary

`runnerunit.Validate([]byte) Report` and
`runnerunit.NewService(EngineFactory).Run(context.Context, []byte) Report`
share strict captured-source parsing and exact selector resolution. They
consume the original `runnercontract.Request` without changing its pinned schema.
After schema validation, `Parse` converts the three integer fields exactly:
`timeout_ms: 6e4`, `selection.trial: 2.0`, and `selection.attempt: 20e-1`
become Go integers `60000`, `2`, and `2`. No floating-point conversion is used.
Fractional numbers, schema-bound violations, nulls and wrong types remain
invalid; conversion also checks the destination Go `int` range. Negotiation
still checks shape only, without interpreting captured input.
`Report` is an invocation-local `waza.runner-unit` receipt, not a #760 terminal
envelope or historical evaluation artifact. Its optional `identity` is input
metadata, not archive publication, execution evidence or an envelope-validity claim.

The CLI `runner validate` / `runner run` and HTTP
`POST /api/v1/runner/{validate,run}` are public callers. Lab calls only validation,
not execution. `exact-unit` and `inline.mock-text` must be required explicitly;
the only admitted runtime is mock. No native startup is reachable even with paid
consent. This bounded profile avoids importing the source draft's demonstrated
native cleanup/startup defects; adding native capability requires its own
request-owned cancellation/cleanup implementation and regression evidence.

The exported factory retains its `EngineFactory` signature, but its result must
be a non-nil concrete `*execution.MockEngine`. Runtime admission uses Go's
concrete type, not a model name, marker method, embedded mock, or mode claim.
Other engines and wrappers are rejected before `Initialize`, `Execute`, or
`Shutdown`; calling shutdown on a rejected engine could itself start native
work. This is an execution error with valid selection, zero calls, no observation
and no `execution_mode`, not a mock execution. Factory errors likewise do not
claim a runtime mode. The embedding host remains responsible for side effects
inside its own factory; this boundary never invokes a returned non-mock runtime.
Lifecycle fault injection is package-private test coverage, not an exported
runtime-admission exception.

The single declared candidate is `input.task`; shard labels and task ID must
match, and trial/attempt must lie within the eval's declared bounds. Eval `tasks`
patterns are inert declarations in this profile, never discovery sources.
Disabled tasks, ambiguous source documents and unsupported behavior fail before
runtime construction. The strict captured parser retains original-wire
`inputs.prompt` presence separately from the decoded message. Shared admission
requires that presence: omitted `inputs`, `inputs: {}`, and prompt-file-only
tasks cannot become zero-value empty messages. An explicitly authored
`inputs: {prompt: ""}` remains valid. Null/wrong-type prompts and duplicate
keys retain their strict-source rejection. Private Go bookkeeping fields are
not accepted as captured YAML declarations. This does not tighten the ordinary
local task loaders. Text graders use offline `graders.ValidateConfig` for
both eval and task scopes, including empty/invalid-regex checks.

`orchestration.PrepareInlineInput` resolves and freezes the request before the
runtime factory. After this exact source admission, `runneridentity.DeriveJSON`
uses the original request bytes and authored sources, not defaulted models or
re-encoded JSON. The 4 MiB wire limit applies to those original bytes; internal
HTML escaping must not reject an otherwise admitted request.
Derivation errors reject with `invalid_identity`, zero calls and no identity.
Successful derivation is retained even if runtime admission, cancellation,
execution or shutdown later fails. `ExecuteInlineAttempt` reuses the current
`executeAttemptWithRequest` seam, not `RunBenchmark`, task loading, `runTest`,
cache or retry loops. A request-local engine wrapper limits actual Execute calls
to one and reports their observed count. The caller initializes a fresh engine,
passes cancellation/deadlines, and awaits shutdown even on initialization or
execution failure. Explicit repeats execute again in independent workspaces.
Legacy local execution and controlled-comparison admission remain unchanged.

The schema documents receipt shape; it is not a strict terminal evidence
admission schema. Nested observations are emitted directly from the attempt,
not decoded/repaired by an archive reader. The Lab validation client accepts
only zero-call `ready`/`rejected` reports and checks tuple equality. The additive
optional `identity` object has an exact seven-string shape and independent
algorithm version. Older receipts may omit it; null, partial, malformed,
unknown-version and rejected-with-identity reports fail the schema guard.
The guard validates metadata shape, not independent hash reconstruction.

## Layer allocation

| Issue | Interface to extend later |
|---|---|
| #758 | Implemented captured inline mock/text exact-unit admission and execution, CLI/API and Lab selection validation. Native execution remains unavailable. |
| #759 | Implemented original-source identity derivation, versioned metadata in CLI/API receipts and Lab lineage/validation JSON export. Negotiation alone derives no identity. |
| #760 | Add independent terminal result schema, execution exit mapping, bounded/cancelable runtime lifecycle and output/archive publication. Negotiation responses must not be stored as execution results. |
| #761 | Add explicit local compatibility adapters and historical regression evidence; this layer does not reroute `waza run` or artifact readers. |
| #762 | Add pinned container proof/publication only after execution is implemented and independently reviewed. |

Source #779 at frozen `4fdbaf62e9dcf4ded2055c1919544973bc42761d`
is source material, not approval. This layer adapts only its captured admission
and single-attempt seam; the separate #759 layer adds canonical identities.
It imports no local adapter, native lifecycle,
terminalization, publication or archive code. Source review findings about
empty/invalid graders are addressed by offline parameter validation. Open-pipe
stdin cancellation and HTTP cancellation during execution/partial upload have
production-path regressions. Server shutdown cancels request contexts and
awaits cleanup. Excluded persistence/terminal/frontend archive findings remain
unresolved by this layer, not silently waived.

All three HTTP POST routes install their five-second read deadline before media-type
validation, including missing/malformed/non-JSON media types. This also bounds
`net/http`'s unread-body drain before completing an early 415 on a partial
keep-alive upload. Completed requests retain keep-alive; incomplete timed-out
uploads close their connection. Response writers and any future middleware
must support `http.ResponseController.SetReadDeadline` (or expose the underlying
writer through `Unwrap`); otherwise admission fails closed with HTTP 500.
The exact-unit routes retain their separate request-cancellation and execution
lifecycle handling; the dashboard server's `ReadHeaderTimeout` alone does not
bound request bodies.

## Conformance and evolution

`internal/runnercontract/testdata/negotiation.json` records supported, downgraded,
unsupported and conflicting combinations. `testdata/request.json` preserves the
pinned source request as a wire compatibility fixture; the original schema is
retained unchanged. The public command/API parity test covers future execution
capability rejection. Exact-unit conformance checks observed engine calls
through CLI and HTTP, covering rejected input, selected ordinal alternatives,
failed checks and repeated dispatch. Browser tests forward real requests to a
freshly built Go server and run the compiled CLI, rather than inventing
successful responses a backend cannot emit.
The CLI and real HTTP route also inject native-like engines and wrappers around
real mocks, proving all lifecycle counters remain zero and no mock observation
or mode is emitted. Service tests additionally reject a concrete Copilot engine
and typed-nil mock without startup; default and factory-created concrete mocks
remain valid. Separate package-private lifecycle tests retain deterministic
initialization, execution, shutdown, timeout and cancellation failure controls.
The complete-request CLI/HTTP matrix sends deliberate LF and CRLF input bytes,
including invalid, alternative-valid numeric, duplicate-key and Unicode cases.
Its exact-count mutations use the selected line ending and assert that the
missing payment-permission field is absent before either public path runs;
neither input re-encoding nor checkout newline policy substitutes for this test.

Additive implementations may advertise new capability names for the same
compatible runner version. A breaking request or capability semantic change
requires a new version/name, not reinterpretation of an existing one. Version
offers are exact, case-sensitive strings, not semver ranges; the server chooses
its highest implemented mutual version, independently of client ordering.
Only 1.0 is implemented. Envelope `schema_version` is independent of offered
runner versions: unsupported envelope shapes fail schema validation rather
than being decoded as another profile. Unknown fields fail closed.
