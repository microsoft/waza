# Derived execution evidence declarations

Issue #768 introduces a **separate, opt-in 1.0 evidence sidecar**. The bounded
`internal/evidencestate` codec does not change existing result readers, verdicts,
billing, native bindings, or immutable manifest 1.0. The codec remains pure;
provider-backed publication, recorded-usage admission and read-only review are
separate opt-in paths. Neither declarations nor byte verification establish
release assurance acceptance.

## Categories and presence

Every document contains exactly one record for conversation, tool, grader,
reliability, usage, and provenance. No category defaults to present.

| State | Interpretation |
|---|---|
| `present` | Producer declares a complete observation; explicit `observed_count: 0` and `artifacts: []` represents observed present-empty. Positive counts require source artifact references. |
| `unavailable` | No observation was recorded. Count is absent, references are empty, reason is `not_recorded`. |
| `incomplete` | Capture is partial or a real manifest limit was exceeded. An optional count describes only recorded observations, not a complete total. |
| `redacted` | Withheld for privacy. No payload, count, or artifact references may be retained; reason is `privacy`. |
| `conflicting` | Source declarations disagree. References may retain admitted, nonrestricted evidence; there is no winning source or implicit pass. |

The source kind (`authored`, `mock`, `cached`, `live`, `unknown`) and context
(`declared`, `observed`, `unknown`) are **producer declarations**, not inferred
from execution mode, a digest, a configured responder, or message content.
Current manifest bindings are declared context. `copilot` mode alone does not
prove live execution. Pure fixtures using `observed` are authored controls, not
runtime observation evidence.

Conversation text must come from recorded user/assistant messages, not prompts,
tool events, output reconstruction, or inferred per-message simulated-user
attribution. Historical transcript readers can omit empty text; that does not
prove present-empty. Category-specific adapters must distinguish actual absence
before emitting this new explicit profile.

Grader verdicts remain `runnerterminal.Verdict` in the referenced terminal or
existing result artifacts. A passing recorded verdict is independent of missing,
redacted, or conflicting evidence. Usage remains `models.UsageStats`; recorded
zero credits and numeric precision are not missing data. Complete-credit
aggregation remains `models.AggregateUsageStats`, not a sidecar billing total.
Optional terminal observations may be absent legitimately; absence must not
be reconstructed into successful grader, reliability, or usage observations.

## Integrity and lineage

`Document.OriginManifest` references an **already finalized** origin manifest.
The evidence document is separately canonicalized and digested; publication is
always `derived`, never a new execution. Do not insert the document into its
containing origin manifest or rewrite origin artifacts. A later derived
publication must be explicitly distinguishable from execution evidence.

`Parse` validates original JSON using the embedded offline schema and native
invariants. It rejects duplicate/unknown keys, invalid UTF-8/surrogates, missing
or null required fields, unknown states, duplicate/missing categories,
inconsistent presence, self-lineage, and duplicate lineage links. `Encode`
sorts detached categories, artifact references, and lineage, preserving caller
input. It emits compact Go JSON with no trailing newline. `Digest` hashes those
canonical bytes using the actual immutable manifest hash helper.

Schema-only consumers also require exactly one of each category and reject
non-exceeded limit diagnostics. Native cross-field checks additionally reject
self-lineage and exact totals below measured lower bounds; run the codec before
making a declaration authoritative.

`VerifySources` accepts **bytes already obtained by the caller**, keyed by
manifest digest and origin artifact name. It checks canonical manifest bytes,
hashes, lengths, declared artifact membership, and the actual terminal envelope
against `Binding.TerminalBinding()`. Every ancestor manifest reference must have
matching canonical source bytes. Retry, resume, cache, and aggregation links
identify source manifests; they neither infer unique execution from legacy
`Identity.Attempt` nor replace #764's execution-attempt ownership.

Optional `execution_source` reuses the actual `executionattempt.Source` contract
from #764 (`fc60a6d77037c72e2d0a3b9fe9fc101f1820437c`). It must name the
origin manifest and be paired with `attempt_artifact`, the name of an actual
attempt descriptor already declared in that manifest. `VerifySources` verifies
the descriptor's admitted bytes, canonical owner codec, execution ID, and
unchanged original binding. Missing/invalid/retagged descriptor bytes are not
verified references. Origins without such a descriptor omit both fields;
no execution ID is inferred from a legacy ordinal. This is source-reference
integration only, not a reservation, ownership ledger, duplicate decision,
retry graph verification, or finalization guarantee.

Successful source verification establishes **byte integrity only**. It does not
verify counts/category semantics, lineage relationships, finalized publication,
execution attestation, privacy authorization, or assurance acceptance. Parsing
does not verify sources at all. These distinctions must remain visible when
actual producers and read-only public surfaces integrate this codec.

## Bounds and privacy

Immutable manifest limits remain **128 artifacts, 64 MiB per object, and
256 MiB total**. An incomplete record can state `artifact_count_limit`,
`object_size_limit`, or `total_size_limit`, with `limit.observed` as an actual
measured lower bound strictly exceeding that limit. `limit.total` is omitted
when unknown and cannot be less than the observed bound. These diagnostics do
not admit oversize origin artifacts, silently truncate sources, or make the
capture complete. The sidecar itself is bounded to 1 MiB.

Redact or withhold restricted material **before persistence**. This profile
contains only closed metadata enums, counts, digest links, and artifact names,
not raw messages, tool output, or free-text diagnostics. Names themselves must
be privacy-admitted by the producer. Redacted/unavailable records have no
artifact references. A codec cannot sanitize an already persisted origin,
remove restricted source files, or authorize API/CSV/JSON/Lab exposure.

## Extension and migration boundary

The schema is `schemas/execution-evidence-1.0.schema.json`, embedded through
`schemas/execution-evidence.go`. New categories/states require a separately
reviewed version/profile, strict schema/native parity, presence and degraded
controls, actual producer/API/export tests, and documentation updates.

There is no automatic migration, new default assurance or legacy-result
reinterpretation. Existing artifacts remain unchanged. Missing sidecars do not
authorize reconstructed observations, invented zero credits or inferred unique
executions.

Full #768 remains open until all-category producers, retry/resume and actual
execution ownership, aggregation and degraded production controls are integrated.
Pure codec tests are not filesystem, cloud, runtime, end-to-end or native Windows
execution evidence.

## Opt-in persistence and read-only review

The integration adds these commands, using an **existing private store** and
an already finalized source manifest:

```bash
waza artifact evidence publish EVIDENCE.json --store /path/to/private/store
waza artifact evidence inspect EVIDENCE_SHA256 --store /path/to/private/store
```

The inherited `--azure-container-url` option selects the explicit Azure provider
instead of the local store. It does not sign in, provision resources or fall back
to local storage. Provider configuration is never read by pure codecs.

The derived document is stored at `blob-<evidence SHA-256>`, using the actual
manifest backend and content-addressed `Store.Put`. It is not inserted into its
origin, placed at a manifest key or assigned a manifest final marker. Publication
may leave an immutable sidecar blob if its subsequent read fails; no transaction,
rollback or cleanup guarantee is implied.

Publication admits each source once, retaining its current completed-read
observation and a detached canonical declaration. It then verifies the newly
persisted sidecar blob without reopening those sources. The publication receipt
describes that invocation's source capture, not an atomic post-write source
snapshot. A subsequent inspection or download performs fresh source admission.

The configured read-only API exposes:

| Endpoint | Result |
|---|---|
| `GET /api/v1/evidence/{digest}` | Current inspection receipt with admitted declaration metadata. |
| `GET /api/v1/evidence/{digest}/categories` | Same-pass category shape admission beside declaration metadata; no captured text/tool/grader bodies. |
| `GET /api/v1/evidence/{digest}/document` | Canonical sidecar JSON, only after successful current source admission. |
| `GET /api/v1/evidence/{digest}/csv` | Six category rows with exact decimal counts and empty cells for unavailable counts or unknown totals. |

The Lab runner page includes a separate evidence inspector. It displays declared
states and source kinds, recorded counts, measured lower bounds and lineage.
It does not fetch source payloads, expose credit estimates, rewrite recorded
verdicts or manufacture evidence in the synthetic demo. Failed refreshes and
edited digest inputs clear prior receipt data and download links.

JSON and CSV downloads recheck sources; an earlier receipt is not authorization
to reuse them. Source receipt artifact names and diagnostics are withheld from
this evidence response. Referenced names still require producer privacy
admission before their original persistence.

Source admission uses the actual #763 `Store.ReadCompleted` contract from
`fbb1a33fd543a51bad505fc1af4423825a6a04d3`. It captures requested artifact bytes
in the same full source verification pass, requiring a finalized marker and
current verified integrity. Usage admission consumes those captured bytes,
without another inspection or reopen. This is not an atomic cross-object
snapshot or execution attestation.

The reader returns no completed result on failure. Evidence receipts therefore
retain only admitted source receipts, withhold the document on failure and do
not perform a second inspection to invent failure-time publication metadata.
Missing final markers and missing source bytes both remain unavailable.

## Recorded usage admission

`ReadUsage` separates `DeclaredState` from its admitted `State` and returns
actual `models.UsageStats`, not a replacement billing representation. A positive
usage declaration must reference exactly one non-terminal `application/json`
artifact containing the native usage fields. Artifact names are caller-assigned
aliases; neither a name nor a terminal envelope supplies usage.

`InspectWithUsage(ctx, backend, digest)` returns the declaration `Inspection`,
`UsageEvidence` and an error using one source pass. Aggregation should use this
combined entrypoint rather than calling `Inspect` and then `ReadUsage` against
the same cumulative backend budget. A usage-shape error does not relabel valid
source bytes as corrupt or change the recorded declaration; the separate usage
result stays unavailable and contains no admitted `UsageStats`.

The opt-in `waza artifact evidence categories DIGEST` command exposes
`InspectCategories` results beside the declaration receipt using one source
pass. Missing capture indexes remain unqualified unavailable even when a
declaration says present-empty. It never emits captured message/tool/grader
payloads, and storage-close failures withdraw counts and usage. A successful
read exit is not category completeness or assurance acceptance.
Independent valid category observations remain visible when another category
has a semantic conflict: the command exits nonzero and adds a safe category
admission diagnostic. Such a conflict does not relabel hash-verified bytes as
corrupt or erase separately verified observations.

Original JSON field presence and types are validated before native decoding.
Missing or null required counts, negative values, duplicate fields, invalid
numeric types, overflow and unknown fields are rejected. Absent or null credits,
missing model attribution and missing per-model credits remain incomplete;
explicit recorded zero remains zero. Provider hosts must satisfy the native
host-only contract, without credentials, paths, queries or fragments.

The helper labels this result
`source-content-and-recorded-usage-shape-only`. It verifies saved shape and
completeness, not live execution, truthful producer provenance, privacy
authorization, unique-session ownership or new-evaluation billing. Aggregators
must require both `State == present` and non-nil `Usage`, preserve incomplete
inputs separately, and use existing complete-credit aggregation only after their
own source/ownership admission. Present-empty declarations contain no usage
artifact and do not produce a synthetic zero-valued `UsageStats`.

## Recorded category capture and admission

The separate `execution-evidence-capture-1.0.schema.json` index is produced
**before origin finalization**. It retains the actual native binding, six
category records and explicit empty payload proofs. It contains no origin
manifest digest or sidecar digest, and no nonempty message/tool/grader payload.
The separately digested derived document still references only the finalized
origin; neither artifact creates a digest cycle.

The public `/waza/schemas/execution-evidence-capture-1.0.schema.json` route
serves the same source embedded by the native codec. Publishing this schema
does not enable Submit or authorize source disclosure.

The committed `ownedsubmission.CaptureHooks` consumes the actual detached typed
`SubmissionSnapshot.ObservedReport` from that one invocation and validates
prepared content against a fresh independently retained original snapshot.
It explicitly withholds conversation/tool/reliability, publishes the capture
index and canonical owner `execution-attempt.json` in that same source manifest,
and never persists or returns the raw report by default. A callback's mutable
report/terminal pair cannot authenticate original observations; raw report JSON
never hydrates creator authority. Reuse admission checks original disclosure
before deciding and never sanitizes or retags finalized source bytes.
The separately derived sidecar may then reference that finalized source without
another execution or source publication. Public Submit selection remains
blocked; this embedding-only cooperating facade is not runtime attestation.
Competing arbitrary artifact/publication callbacks are rejected.

`CaptureReport(report, withheld)` consumes one fresh invocation-local
`runnerunit.Report`. `Report.Terminal()` projects that same report and returns
detached comparison binding values without another execution or creator
authority. A guarded mock report is labeled with a declared mock source;
copilot mode does not become live attestation. Provenance records the original
declared binding, not an observed environment or finalization claim.

Nonempty conversation, tool and grader payloads are stored in independent
`application/json` objects named `execution-evidence-{category}.json`. Usage
remains a standalone native `UsageStats` object. Reliability references the
original native terminal. The index's provenance record references
`execution-evidence-capture.json` and that terminal; category counts are counts
of recorded items, not numbers of supporting objects. Only the capture index
name is reserved by admission; other payload names remain aliases and never
qualify a category by themselves.

Nil withholding uses the conservative conversation/tool-withheld default.
Explicit reliability withholding, or a secret-triggered reliability redaction,
replaces **all nonempty** native diagnostic messages and observation error text
with `[REDACTED]` before first publication, not only regex-matched substrings.
Codes, context, outcome, verdicts, counters and empty error text are unchanged.
Restricted public facades must explicitly withhold reliability; the nil policy
does not itself authorize arbitrary SDK text disclosure.
Explicit `[]` opts into captured content subject to the shipped snapshot
secret policy. A matching restricted value withholds the **entire category**:
no payload bytes, count or reference enter immutable storage. Fixed native
token-counter field names are not mistaken for credentials; freeform tool
arguments/results and grader details retain sensitive-key checks. Operators
must additionally withhold restricted categories: regex filtering is not
comprehensive privacy detection or disclosure authorization.

`ValidateCaptureBinding` must run **before reservation or any persistence**.
Known native identity digests are opaque hashes; restricted task/grader/shard
identifiers are rejected. First-publication capture may sanitize native
diagnostic free text while preserving outcome, exit policy and counters;
reliability is explicitly redacted when this occurs. Never apply this
transformation to a finalized or reused terminal.

`PreparedCapture.Artifacts()` returns detached native descriptors and prepared
bytes, with no runtime/storage work. `PersistOwnedCapture` requires the actual
`executionledger.Ledger` and retained `Creator`, compares original reservation
binding, stages into one native origin, finalizes it, records completion, and
publishes the separate sidecar. Its lifecycle has distinct verification phases;
it does not promise a single read across the entire write lifecycle, an atomic
snapshot, transaction or rollback. Storage/completion/semantic failures remain
errors and can leave already-persisted artifacts.

`InspectCategories(ctx, backend, digest)` returns declaration inspection,
six `CategoryEvidence` results and an error. It uses **one current source
pass** and never exports captured text/tool/grader payloads. It compares index,
sidecar and original binding by category key; validates original payload types
and counts; and checks native grader IDs/verdicts without replacing them.
Missing/null message content and missing tool completion remain incomplete,
not empty text or false-success defaults. Explicit recorded `[]`/`{}` proofs
are required for present-empty. Missing index means unqualified declarations;
failed whole-source admission withholds every category. A byte-verified source
can still have conflicting/invalid semantic evidence.

Oversize category objects are withheld, never truncated, with a measured bound
strictly exceeding the real 64 MiB limit. Publication-total degradation uses
actual candidate byte totals, including the actual reservation object, against
256 MiB. Unparsed oversize payload counts remain unknown. Recorded degradation
does not verify discarded bytes or turn incomplete evidence into acceptance.

These are internal producer/read contracts with actual owner-backed mock
regressions, not completed public submission, retry/resume, aggregation, Lab or
release integration. Existing CLI/API/Lab surfaces above still display
declarations; they must consume semantic admission before claiming categories
qualified. Full #768 remains open.
