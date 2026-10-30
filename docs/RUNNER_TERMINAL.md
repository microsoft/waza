# Runner terminal envelope 1.0

This source-build preview implements #760 over the exact mock/text runner.
`waza runner run` and `POST /api/v1/runner/run` share
`runnerunit.Service.RunTerminal`: one admission, one execution and synchronous
cleanup, then one schema-validated output attempt. Negotiation and offline
validation retain their separate receipts and do not claim execution.
The [site guide](../site/src/content/docs/guides/runner-terminal.mdx) is the
canonical user, HTTP, exit-policy and migration reference.

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
canonical typed identity, and the complete set of admitted grader identifiers.
Pass it only after successful exact validation **and** successful identity
derivation, before constructing or calling a runtime. A derivation failure uses
boundary `identity`, `validation: valid`, zero engine calls, and no fabricated or
partial IDs. After successful derivation, retain that binding on later
startup/execution/shutdown errors, cancellation, and timeout.

The optional `identity` uses the exact seven-field #759 representation:
`version`, `canonical_digest`, `run`, `shard`, `task`, `trial`, `attempt`.
The algorithm is `sha256-waza-c14n-v1`, with lowercase hexadecimal digests and
level-specific prefixes. **No second Go identity model or derivation algorithm
is defined here.** Both envelope and binding use `*runneridentity.Identity`
from the admitted invocation directly. Omit the entire object until derivation succeeds;
null, empty, partial, wrong-version, and unexpected-field objects are invalid.

`schema_version: "1.0"` belongs to the **terminal result**, independently of
request/interface versions and `identity.version`. Changing an identity
algorithm must not silently relabel old identities. New incompatible terminal
fields, meaning, or exit mappings require a new terminal schema version.
Version 1.0 rejects unknown fields, including nested fields and case aliases.

`Observation` is an explicit, bounded **terminal summary**, not a complete
`models.RunResult`, transcript, artifact manifest, billing record, or live
execution attestation. The producer projects it from the actual
returned #758 observation and captured resolved selection, not from configured
mock text or a declared expected result. The current supported execution mode
is `mock`; native execution is not advertised by this foundation.

The summary retains the admitted selection, observed `run_number`, selected `attempts`,
status, explicit per-grader verdicts, and an error when status is `error`.
The production `RunResult.Attempts` carries the selected attempt ordinal, not
the number of automatic retries. Its value must equal `selection.attempt`;
`run_number` must equal `selection.trial`. No retries are introduced.
Completed observations must contain exactly the admitted grader set (eval graders,
task graders and applicable `_output_*` expectations), with no
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
| Each canonical identity field | 292 UTF-8 bytes (schema further constrains shape) |

JSON Schema string lengths are character counts; the Go checks additionally
enforce these byte limits. The production projection detects evidence it cannot
represent **before** calling the writer and explicitly selects an appropriate
bounded failure envelope with an actionable diagnostic. That is not permission
to silently drop oversized evidence from an otherwise successful outcome. This
implementation has no oversized-success fallback or archive subsystem.
Unrepresentable observations produce `infrastructure_failure` at `terminalization`,
with `unrepresentable_evidence`, the original outcome/boundary, retained admitted
identity/selection and observed call count. No score, output or billing is invented.
Large rejection lists retain the first 15 diagnostics plus an explicit remaining
count; overlong rejection messages are marked as truncated. The original rejection
outcome remains unchanged. This is diagnostic preview, never passing evidence.
`NewFailure` similarly marks overlong messages as truncated and invalid UTF-8
replacement explicitly, retaining the actual failure outcome and boundary.
This keeps even oversized CLI flag and host error diagnostics writable; it does
not permit truncating identities, observations or grader bindings.

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

## Production lifecycle and ownership

The internal `Report` retains an unserialized binding and typed error cause.
Projection never derives identities again or guesses cancellation from strings.
A typed deadline wins over cancellation; either wins over a joined runtime
error. A later shutdown error changes the terminal boundary to `shutdown` while
retaining any completed observation and earlier error cause. Input rejection
precedes runtime construction. The host-owned factory is synchronous, not a
sandbox: hosts must return promptly and own any work they start; the service
does not launch detached constructor goroutines or preempt arbitrary host code.
The concrete-mock admission guard remains unchanged.

On Unix, the CLI does not change flags or close inherited stdin/stdout: duplicated
descriptors share an open file description, including `O_NONBLOCK`. It performs
one asynchronous read or one prepared asynchronous terminal-write attempt so
SIGINT/SIGTERM and the five-second output bound can win without changing a
parent's or peer's descriptor behavior. The top-level CLI exits after that
decision, so a blocked inherited I/O attempt cannot survive terminalization or
produce a fallback record. Supplied pollable files are never closed; their
deadline is reset after the read. In-process embedders supplying arbitrary
`io.Reader` or `io.Writer` implementations must provide prompt/cancellation-aware
I/O: Go cannot interrupt arbitrary synchronous code safely.
Windows relies on Go's pipe I/O cancellation; native execution evidence must be
obtained from the required Windows CI, not inferred from Unix signal tests.
Descriptors without deadline support and arbitrary embedding writers do not
have a bounded-output guarantee.

HTTP body admission installs a five-second deadline before media-type validation,
failing closed if read deadlines are unavailable, and supports context interruption. A body
deadline also cancels net/http's request context; the actual read error and
whether cancellation interrupted it before the deadline distinguish timeout
from cancellation. The hook is joined before response delivery. Output gets a
fresh five-second write deadline. Short writes/flush failures are logged and
never trigger a second response. Rejections before body admission close the HTTP/1.1 connection without draining
the unadmitted body, even if the client already sent it completely; clients
must reconnect. Fully read requests retain keep-alive, including admitted
terminal failures. A late write-deadline setup failure becomes an
output infrastructure failure while retaining the admitted identity and actual
observation. HTTP success and `X-Waza-Exit-Code` describe
the encoded decision, not consumer receipt or durable publication.

Lab validates original JSON and the terminal schema, compares identity/selection
with offline admission for the same captured request, and checks observed
tuple/ordinal/duplicate-verdict consistency. The producer validates the complete
grader set; the browser does not independently reconstruct YAML or hashes.
Edits, fresh requests and errors clear previous success. Downloads contain the
actual producer envelope. Neither a validation receipt nor a terminal summary
is a historical `EvaluationOutcome`, transcript, archive or billing record.
Successful offline admission does not imply the subsequent HTTP upload was
admitted. A real transport rejection without identity/selection/observation or
engine calls remains visible and exportable as unadmitted failure, never as
identity-bound execution evidence.
