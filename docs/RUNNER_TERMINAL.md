# Runner terminal foundation (preparation for #760)

This is developer preparation, **not a released CLI/API contract or completion
of #760**. It is intentionally independent of the in-progress execution and
identity integrations. No command, HTTP route, artifact archive, persistence
protocol, Lab view, scheduler retry policy, or release behavior changes here.

The implementation is `internal/runnerterminal`; the offline schema is
[`runner-terminal-1.0.schema.json`](../schemas/runner-terminal-1.0.schema.json).
[`envelopes.json`](../internal/runnerterminal/testdata/envelopes.json) contains
schema-checked examples of all seven outcomes. Those examples use **synthetic
identity strings and observations**: they are format controls, not evidence that
any request was admitted, derived, executed, or passed.

## Outcomes and exit categories

The `outcome` string is the exit category; `exit_code` is its stable numeric
mapping. Readers must reject a contradictory pair, not recompute a replacement.
These codes are reserved for the new runner boundary, not changes to existing
local-command exit behavior.

| Outcome | Exit | Required meaning |
| --- | --- | --- |
| `success` | 0 | Exact admitted work returned a complete `passed` observation; every declared grader has an explicitly passing result. |
| `evaluation_failure` | 1 | Work returned a complete `failed` observation, without an execution error, and at least one declared grader failed. |
| `infrastructure_failure` | 2 | An internal or transport operation failed at the reported boundary; it is not an evaluation verdict or proof of invalid input. |
| `cancelled` | 3 | Cancellation was observed at the reported failure boundary. |
| `timeout` | 4 | Deadline expiration was observed at the reported failure boundary. |
| `unsupported_capability` | 5 | Required capability or interface version was unavailable before admission/execution; the diagnostic distinguishes them. |
| `invalid_input` | 6 | Input syntax or exact semantic validation failed; not an unsupported capability, transport failure, or model failure. |

Unknown outcomes are invalid, never success. Every non-success state requires at
least one bounded diagnostic. Success and evaluation failure have boundary
`none`; all other states require a specific non-`none` boundary. State and
boundary legality is enforced in the schema, not just documentation.

`validation` records exact input validation (`not_checked`, `invalid`, `valid`).
Successful validation is not successful identity derivation or execution.
`engine_calls` records the number of attempted exact engine execution calls
(0 or 1), not model turns, construction calls, billing, or proof of isolation.
No admitted binding means no engine calls, identity, selection, or observation.

## Evidence and identities

`Binding` is caller-supplied comparison context: the exact resolved selection,
canonical identity JSON, and the complete set of admitted grader identifiers.
Pass it only after successful exact validation **and** successful identity
derivation, before constructing or calling a runtime. A derivation failure uses
boundary `identity`, `validation: valid`, zero engine calls, and no fabricated or
partial IDs. After successful derivation, retain that binding on later
startup/execution/shutdown errors, cancellation, and timeout.

The optional `identity` reserves the exact seven-field #759 representation:
`version`, `canonical_digest`, `run`, `shard`, `task`, `trial`, `attempt`.
The algorithm is `sha256-waza-c14n-v1`, with lowercase hexadecimal digests and
level-specific prefixes. **No second Go identity model or derivation algorithm
is defined here.** Preparation uses `json.RawMessage` because the canonical
package is not in this parent. Final integration must use the actual
`runneridentity.Identity` producer representation; no provisional type is an
authority over that package. Omit the entire object until derivation succeeds;
null, empty, partial, wrong-version, and unexpected-field objects are invalid.

`schema_version: "1.0"` belongs to the **terminal result**, independently of
request/interface versions and `identity.version`. Changing an identity
algorithm must not silently relabel old identities. New incompatible terminal
fields, meaning, or exit mappings require a new terminal schema version.
Version 1.0 rejects unknown fields, including nested fields and case aliases.

`Observation` is an explicit, bounded **terminal summary**, not a complete
`models.RunResult`, transcript, artifact manifest, billing record, or live
execution attestation. The eventual producer must project it from the actual
returned #758 observation and captured resolved selection, not from configured
mock text or a declared expected result. The current supported execution mode
is `mock`; native execution is not advertised by this foundation.

The summary retains the observed selection, `run_number`, local `attempts`,
status, explicit per-grader verdicts, and an error when status is `error`.
Logical selection attempt 3 can legitimately have local `attempts: 1`: no
automatic retries are introduced. The observed selection must match the admitted
selection on **every outcome**, and `run_number` must equal the selected trial.
Completed observations must contain exactly the admitted grader set, with no
duplicates or unknown results. An explicit empty declared set with an explicit
empty observed set is valid; an unavailable (`nil`) declared set is not.

Success requires actual completed execution evidence even with zero declared
graders. Failed observations require an actual failing grader. Error
observations may retain a partial set but require an error message. A later
shutdown failure may retain an already-passed observation while the terminal
outcome is infrastructure failure/cancelled/timeout; it must not rewrite the
observation into a different recorded result or turn the terminal outcome green.

`Validate(data, binding)` checks original JSON with the shared strict parser,
the embedded offline schema, then contextual consistency. Duplicate keys,
malformed Unicode, extra documents, invalid field presence/types, precision-
losing numeric admission, and inconsistent outcomes are rejected. Parsing
never constructs a runtime, reads project configuration, or loads external
schemas. Schema validity alone does **not** prove identity derivation, admission,
actual observation, or correspondence to absent original input. The supplied
binding is the producer's trust boundary; final input-aware readers must verify
it from the actual admitted request.

## Bounds and the one-shot writer

`Encode(envelope, binding)` checks typed bounds and UTF-8 before JSON encoding,
then runs the same wire/context checks as `Validate`. It returns one compact
JSON record ending in LF. It never silently truncates diagnostics, removes
observations, invents scores or IDs, or substitutes a passing result.

| Limit | Value |
| --- | --- |
| Input envelope bytes, including whitespace | 1 MiB |
| Diagnostics | 16 |
| Diagnostic message / observed error | 4096 UTF-8 bytes |
| Diagnostic code | 64 UTF-8 bytes, lowercase identifier syntax |
| Selection and grader identifiers | 256 UTF-8 bytes |
| Observed and declared graders | 128 |
| Canonical identity JSON | 2048 bytes |

JSON Schema string lengths are character counts; the Go checks additionally
enforce these byte limits. The final producer must detect evidence it cannot
represent **before** calling the writer and explicitly select an appropriate
bounded failure envelope with an actionable diagnostic. That is not permission
to silently drop oversized evidence from an otherwise successful outcome. This
preparation intentionally has no oversized-success fallback or archive subsystem.

`NewEmitter(writer).Emit(envelope, binding)` owns **one emission attempt per
invocation**. Create exactly one emitter and never copy or replace it for that
invocation. It validates/encodes before output. Concurrent or subsequent calls
return `ErrAlreadyAttempted`, including conflicting outcomes; a second call is
not silently accepted as the first result. Concurrent rejections do not wait
for an already-blocked writer.

For a valid prepared envelope and a fully successful writer, the primitive
writes one JSON line in one `Write` call and returns the mapped exit.
Validation/encoding failure writes nothing and consumes the attempt. Short
writes and write errors return exit 2 with the wrapped cause; **no second
envelope, retry, or success-shaped fallback** is emitted, even if a partial or
complete record has already reached the channel. The output transport failed,
so those bytes cannot prove successful delivery.

A successful local `Write` proves neither consumer receipt nor flush/close
success, atomic publication, or durability. The caller owns transport deadlines,
flush/close, and cancellation; this primitive cannot interrupt an arbitrary
blocked `io.Writer`. It does not recover programmer panics. SIGKILL, host loss,
or an unwritable/broken output channel cannot guarantee terminal output because
the process may be unable to execute or deliver any finalization code.

This follows the #750/#749 distinction: finishing one classification/invocation
does not authorize swallowing conflicts that already occurred. A recorded
decision is not itself proof that downstream delivery succeeded.

## Integration still required

The final dependency-ordered #760 layer must use the actual pushed #758/#759
parents. It must project the actual `Report`/`models.RunResult` and canonical
identity, and route CLI/process/API writers through one terminal decision.
Define and test cancellation/deadline/error precedence in the real lifecycle;
do not infer it from a generic error string.

Acceptance still needs interruptible blocked stdin, partial HTTP input, runtime
startup/shutdown and response delivery; deterministic process/signal tests;
input-aware identity verification and preserved evidence; production-to-Lab
outcome/exit/boundary/validation diagnostics; documentation-site examples and
navigation; migration/release notes; current-head full gates and independent
review. Helper/race/schema examples here do not close those criteria or claim
native Linux/Windows or public-runner process coverage.
