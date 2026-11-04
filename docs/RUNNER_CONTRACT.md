# Runner contract: negotiation and exact captured work 1.0

The [v0.40.0 release](https://github.com/microsoft/waza/releases/tag/v0.40.0)
implements negotiation [#757](https://github.com/microsoft/waza/issues/757)
and bounded exact inline text execution [#758](https://github.com/microsoft/waza/issues/758),
with input-derived [logical identities](RUNNER_IDENTITIES.md) (#759). It is available
in standalone v0.40.0, not retroactively in an older binary.
The [documentation-site guide](../site/src/content/docs/guides/runner-contract.mdx)
is the canonical user invocation, API, compatibility and migration reference.

Exact-unit execution/shutdown deadlines and cancellation do not fix the broader
legacy multi-trial timeout issue [#865](https://github.com/microsoft/waza/issues/865),
addressed separately by the later timeout/persistence fix
[#870](https://github.com/microsoft/waza/pull/870). That fix is post-v0.40.0 source behavior,
not included in the published v0.40.0 artifacts.

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
`Report` is an internal invocation-local `waza.runner-unit` receipt, not a #760 terminal
envelope or historical evaluation artifact. Its optional `identity` is input
metadata, not archive publication, execution evidence or an envelope-validity claim.

The CLI `runner validate` / `runner run` and HTTP
`POST /api/v1/runner/{validate,run}` are public callers. Run uses
`Service.RunTerminal` to project the same invocation into the
[terminal envelope](RUNNER_TERMINAL.md); validation keeps the receipt.
Lab separates offline validation from the explicit **Run selected unit** action.
`exact-unit` and exactly one profile (`inline.mock-text` or
`inline.copilot-text`) must be required explicitly. Optional capabilities and
paid consent alone never select a profile. Copilot additionally requires
`config.executor: copilot-sdk`, an explicit nonblank model and
`allow_paid_execution: true`. Both profiles retain identical selection limits;
neither enables file, tool, skill, MCP, hook or multi-turn execution.

The exported factory retains its `EngineFactory` signature, but its result must
be a non-nil concrete `*execution.MockEngine` for mock input or
`*execution.CopilotEngine` for Copilot input. Runtime admission uses Go's
concrete type, not a model name, marker method, embedded mock, or mode claim.
Other engines and wrappers are rejected before `Initialize`, `Execute`, or
`Shutdown`; calling shutdown on a rejected engine could itself start native
work. This is an execution error with valid selection, zero calls, no observation
and no `execution_mode`, not a mock execution. Factory errors likewise do not
claim a runtime mode. The embedding host remains responsible for side effects
inside its own factory; this boundary never invokes a mismatched runtime.
Lifecycle fault injection is package-private test coverage, not an exported
runtime-admission exception.

Copilot admission also requires a private managed, force-stoppable client,
disabled command mocks, disabled ambient discovery and failure-retaining diagnostic mode. The default
factory uses the existing owned SDK builder, not the process-wide shared
client. `ExecuteInlineAttempt` applies the existing deny-all `ToolPolicy` to
the frozen request, so SDK tool filtering, permission checks and pre-tool
hooks agree. Skills remain explicitly disabled at the SDK boundary.
Session configuration explicitly sets `EnableConfigDiscovery`, `EnableFileHooks`
and `EnableOnDemandInstructionDiscovery` to false, rather than inheriting runtime
defaults; no explicit hook/plugin/instruction discovery roots are supplied.
It also skips custom instructions and embedding retrieval, disables the shared
session store and memory, and disables host git context. The configuration
executor remains `copilot-sdk`; the separate terminal execution mode is `copilot`.
The SDK also sends `skipCustomInstructions: true` in its post-create/resume
`session.options.update`; a rejected update fails execution before any prompt
is sent.
Ordinary execution retains its previous discovery defaults. Production CLI/HTTP
captures and an offline real-SDK wire control verify those settings, not actual
provider behavior or a host filesystem sandbox. The synthetic peer does not
reproduce or attest execution of ambient hooks.
Diagnostic failures, including incomplete usage and cleanup failures, propagate
to terminal failure instead of becoming successful partial evidence. The
terminal summary still does not export billing.
If final usage is unavailable after disconnect, isolated runner sessions do not
resume solely for shutdown-usage retrieval: that SDK fallback uses unrestricted
defaults. Missing usage remains explicit and fails terminalization rather than
being invented as zero. Ordinary execution retains its existing fallback.

Paid native HTTP execution is local-only: the immediate peer must be a loopback
IP, `Host` must be `localhost` or a literal loopback IP, and any browser `Origin`
must exactly match that request's HTTP(S) scheme and authority. Cross-site or
same-site (but not same-origin) Fetch Metadata is rejected. Untrusted forwarded
headers cannot authorize execution. Rejection returns HTTP 403 with a
`native_http_denied` terminal diagnostic, zero engine calls and no identity or
observation, before constructing a runtime. Offline validation/negotiation,
remote mock execution and existing remote read/static serving remain unchanged.
Use local `waza runner run` for native execution outside this HTTP boundary.
This is local-process trust, not authentication of OS-local users. Do not expose
the paid endpoint through a reverse proxy that rewrites remote peer, Host or
Origin into trusted local values; no proxy or remote-user authorization is
provided. `allow_paid_execution` is request consent, not server-owner authorization.

Request cancellation and the effective task/eval execution deadline interrupt
the private SDK transport with `ForceStop`; the separate five-second shutdown
budget also interrupts it on expiry. Each callback is joined before returning.
This handles RPCs that ignore their caller context but unblock when the owned
transport is killed. It is not a guarantee against a non-cooperating custom
transport whose `ForceStop` itself blocks, arbitrary filesystem hangs, SIGKILL
or host loss. Deterministic production-caller tests cover hanging startup,
send and stop, including send ignoring context, plus cancellation, task timeout,
denied tools and disconnect/delete failure. They are SDK-fake evidence on the
tested host, not live-provider or native Windows/Linux execution evidence.
Effective nonzero `first_event_timeout_seconds` is rejected before runtime
construction for this profile: its legacy child-context watchdog cannot bound
an uncooperative RPC. Use `timeout_seconds` or request `timeout_ms` instead.
A task override of zero explicitly disables an eval-level first-event setting.

### Attempt-only metric metadata

Both exact-unit profiles accept eval `metrics` as **uncomputed, identity-bound
metadata**. Nonempty declarations are validated offline against the canonical
eval schema's metric definition, before numeric decoding can round a threshold.
Numbers must be finite and must not underflow the native numeric range.
Malformed, wrong-type or out-of-range nonempty declarations reject before
runtime construction. For historical compatibility, explicit `metrics: null`
decodes as nil, like an omitted field, in the shared captured reader used by
mock execution and native snapshot reconstruction; exact-unit Copilot admission
still rejects explicit null. Existing empty arrays also remain accepted. The
local compatibility adapter independently rejects an explicit `metrics` field,
including null, rather than dropping unsupported metadata. This captured-reader
compatibility does not relax strict canonical eval-schema admission, which still
requires a nonempty metrics array.

Terminal `success` describes only the selected graded attempt. No aggregate
metric values, metric-threshold decisions or whole-evaluation success are
computed or claimed. Changing authored metric metadata changes the input
identity, not this scope. The local compatibility adapter still rejects
nonempty metrics rather than projecting uncomputed aggregate evidence.

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

| Issue | Implemented boundary and remaining scope |
|---|---|
| #758 | Captured inline mock/text and explicitly opted-in Copilot/text exact execution, CLI/API and Lab selection validation. Files, tools, skills and general native eval workflows remain unsupported. |
| #759 | Implemented original-source identity derivation, versioned metadata in CLI/API receipts and Lab lineage/validation JSON export. Negotiation alone derives no identity. |
| #760 | Implemented independent terminal schema, seven execution exit categories, shared CLI/HTTP terminal projection, cancellation and Lab/export. No archive or durable publication. |
| #761 | Explicit `runner adapt` and opt-in `run --runner-compat` preserve the bounded mock/text profile and historical result/exit contracts. Ordinary `waza run` and artifact readers retain their defaults. |
| #762 | Mock-only local scratch reference plus a published Linux AMD64 native reference, accepted by anonymous digest-pinned mock and bundled-native synthetic-provider production controls. See the [exact source, digest and verifier evidence](../site/src/content/docs/guides/runner-container.mdx#published-image-and-acceptance-evidence); this is not live-provider reliability or general native workflow support. |

The [local container quickstart](../site/src/content/docs/guides/runner-container.mdx)
and [maintainer procedure](../examples/runner/container/MAINTAINERS.md) use the
existing public CLI unchanged. No scheduler integration or Lab change is added.

Source #779 at frozen `4fdbaf62e9dcf4ded2055c1919544973bc42761d`
is source material, not approval. This layer adapts only its captured admission
and single-attempt seam; the separate #759 layer adds canonical identities.
The current additive Copilot/text layer uses the owned production lifecycle;
it adds no publication or archive code. Terminalization is independently integrated from
the focused #760 preparation. Source review findings about
empty/invalid graders are addressed by offline parameter validation. Open-pipe
stdin cancellation and HTTP cancellation during execution/partial upload have
production-path regressions. Server shutdown cancels request contexts and
awaits cleanup. Excluded persistence/frontend archive findings remain
unresolved by this layer, not silently waived.

All three HTTP POST routes install their five-second read deadline before media-type
validation, including missing/malformed/non-JSON media types. This also bounds
`net/http`'s unread-body drain before completing an early 415 on a partial
keep-alive upload. Completed negotiation/validation requests and fully read run
requests retain keep-alive. Run rejections before body admission are not drained:
their HTTP/1.1 connections close even if the client has sent the complete body,
so clients must reconnect. Incomplete timed-out uploads also close their
connection. Response writers and any future middleware
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

### Offline production-process controls

Run the bounded subprocess controls from the repository root:

```bash
NO_COLOR=1 go test -count=1 ./.github/scripts/testdata/runner-runtime
NO_COLOR=1 go test -count=1 -run '^TestRunnerNativeProductionProcess$' -v ./.github/scripts
```

The second command builds the real `cmd/waza` executable and a **synthetic**
stdio runtime peer, then invokes `waza runner run` with derivatives of the
current `examples/runner/copilot-unit.json`. An absolute `COPILOT_CLI_PATH`
selects the fixture; no embedded runtime or provider is used. Each invocation
gets a fresh working directory and an environment containing only test-owned
`HOME`, `COPILOT_HOME`, `TMPDIR`, `PATH`, the override, `NO_COLOR`, and the two
synthetic fixture controls. Credentials, provider settings and proxy variables
are not inherited. The SDK additionally injects its
`COPILOT_RUNTIME_PROCESS_FILE_LOGGING` flag; the fixture records environment key
names and asserts the complete inventory, including absence of deliberately
seeded ambient credential/provider sentinels.

The controls validate the actual terminal JSON against its schema and
input-derived identity, the selected task/trial/attempt, grader verdicts, and
the complete recorded RPC inventory. They cover successful execution, failed
checks, session errors, request timeout, startup rejection, incompatible runtime
protocol and unsupported capabilities. A selected ordinal of 2/2 still sends
one prompt to one session; it does not execute prior or neighboring work.
Unexpected fixture methods fail with JSON-RPC errors. Frame sizes and child
process waiting are bounded.

This is production CLI/SDK subprocess evidence with synthetic text, authentication
and usage responses, **not** native Copilot runtime, inference, billing,
credential-isolation or container-image assurance. The fixture itself makes no
network requests; it does not sandbox the production CLI. Unix host process
controls currently skip Windows. Container/runtime probes must additionally
execute on their actual target platform with networking disabled and no
credential mounts; host passes and cross-compilation alone do not prove that
the bundled runtime starts in an image. No image, release or tag is published
by these controls.

The [shared container controls](../examples/runner/container/MAINTAINERS.md)
also exercise these boundaries with runtime networking disabled. Their synthetic
target asserts all explicit ambient-context opt-outs and the required
`session.options.update`; rejecting that update must emit infrastructure failure
without sending a prompt. The unchanged default scratch reference is not
native-ready. A separate pinned Alpine no-Send smoke starts the actual bundle
and checks its local-provider lifecycle with zero provider requests, not inference.
