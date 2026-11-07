# Independent completed-manifest aggregation

This unreleased v0.41 source addition derives a new, separate
`waza.manifest-aggregate` **1.0** document. It does not rewrite historical
evaluation results or change their schema/defaults. The explicit expected set
is the scope of the report, not a discovered fleet inventory.

## Admission and deterministic policy

`waza aggregate DIGEST [DIGEST...] --store EXISTING_DIRECTORY` reads public
immutable publications without starting workers, models, grader subprocesses, loading
project configuration, or performing an update check. The selected local store
must already exist. Explicit `--azure-container-url` selects Azure instead of
`--store`, using the shared immutable provider factory and existing credentials.
There is no provider fallback, and aggregation does not initiate interactive
Azure login.

The aggregation core receives an already selected backend and constructs no
runtime or credentials. Explicit Azure CLI/API reads use the shared provider's
default credential chain, which reads credential configuration and may invoke
Azure CLI, Developer CLI or PowerShell authentication helpers. Process-free
provider authentication is not guaranteed by this source preview; local reads
do not construct that chain.
Static request/schema admission must not invoke the provider factory or
credential constructors. Runtime Azure credential failure is explicit
unavailability, never local fallback or verified empty evidence.

Each requested digest must identify canonical manifest 1.0 bytes with all
declared artifact bytes, native terminal/binding consistency, and the matching
`final-<digest>` marker. A declaration, successful historical receipt, or
syntactically valid digest is insufficient. Every invocation checks current
content. Finalization of a failed or cancelled execution remains possible;
finalization does not turn its terminal outcome into success.

Input digests are sorted bytewise and duplicates collapsed. Members retain
original logical run/work (`Identity.Run` / `Identity.Trial`), terminal outcomes,
and `runnerterminal.Verdict` values. Explicitly recorded empty verdict arrays
remain empty; absent terminal observations have no verdict field. No score
exists in terminal 1.0, so no score or threshold result is fabricated.

Distinct completed manifests naming the same logical work are all
`conflicting`, even if their outcomes agree. There is no last-writer, highest
attempt ordinal, timestamp, or digest-order winner. Choose the desired verified
source explicitly upstream rather than submitting competing attempts. This
rule does not claim #764 ledger ownership or verified duplicate reuse.

Compatibility compares manifest profile, identity version, logical run and
execution mode. Task/trial/attempt canonical digests differ legitimately; task
grader sets may differ. Unsupported profiles and unavailable sources remain
diagnostic members rather than being silently converted or dropped.
Incompatible run contexts retain their members without a cross-context verdict.
This is declaration consistency, not independent identity derivation or
execution attestation.

The report has no timestamp, host path, provider URL or arbitrary terminal error
text. Members and conflict lists are sorted; counts are keyed by actual member
state. Reordering and exact duplicates produce identical bytes for the same
stored content. Changed/missing bytes or failed storage are new observations,
not a promise that a mutable/corrupt provider will always return the old report.

## Interpret the report

`completion: complete` requires all requested unique sources to be admitted,
compatible, nonconflicting success/evaluation-failure terminal publications.
It is **membership completeness**, not evidence completeness or a passing
evaluation. `evaluation_failure` retains actual failing verdicts.
Selected sidecar/category admission failures and failed declared source context
also keep the operation incomplete. For example, every member may retain
`success` while one selected sidecar's `source_context.state` is `missing`:
the state counts still describe the original terminals, but the API returns
409 and the CLI exits 2. Do not infer whole-operation success from member counts
or from the sidecar's independent byte-verification state.

Missing manifests/artifacts, unfinalized sources, corruption, conflicts,
unsupported profiles, cancellation, timeout and infrastructure failure remain
explicit. Noncompleted members have no substituted verdicts. Inputs omitted
from the request cannot be inferred as missing: retain your scheduler's expected
digest list separately and supply it explicitly.

CLI exit codes are 0 for complete compatible membership with no recorded
evaluation failure, 1 for complete membership containing an evaluation failure,
and 2 for incomplete/conflicting/incompatible input or I/O failure. None is an
assurance/release decision. The JSON report is emitted even for admitted
incomplete sets; invalid requests or an inaccessible store may fail before a
report can be produced. Do not treat an output/write/close failure as success.

## Evidence, usage and privacy boundary

Manifest/terminal 1.0 does not contain complete conversation, tool, reliability,
usage or provenance category observations. Passing terminal verdicts do not
make those categories present. The #768 pure sidecar codec verifies declarations
and referenced bytes, not category-specific semantics. Aggregation must use the
actual committed producer/admission contract before asserting category counts,
source provenance or complete usage.

Optional `--evidence MANIFEST_SHA256=EVIDENCE_SHA256` selects explicit derived
sidecars; repeat it up to 128 times before deduplication. Declared origins must
belong to the expected set before any provider is opened. Sorted, distinct pairs
each use one actual #768 `InspectCategories` pass. The stored origin must match;
all distinct competing sidecars are reported, with no declaration/usage winner.
Every lineage/current-source read shares the original cumulative budget.
Origin and sidecar verification are separate current passes, not a snapshot.
Observed failures remain withheld even when another pass verified the origin.

`declared_categories` and `declaration_state_counts` retain the six declarations.
The separate `categories` and `category_state_counts` report native category
admission against the original terminal, canonical capture index and
category-keyed payloads. Observed counts use exact decimal int64 text.
Native `present` states require an observed count; removing a count is invalid,
not an alternative representation of empty or unavailable evidence.
Unindexed flat sidecars remain byte-verified declarations but have unavailable
native categories, no admitted counts and no recorded usage. This intentionally
strengthens the **unreleased** optional-evidence admission; manifest-only output
and historical formats remain unchanged. Competing sidecars contribute to
neither tally, and semantic failures retain byte receipts without inventing
successful category evidence.

Explicit native `UsageStats` shape admission may retain original recorded usage,
including incomplete credits, for review. Present-empty declarations never
create zero usage. This verifies source content and recorded shape, not privacy
authorization, retained ownership, live execution or billing attestation.
Scores, assurance and usage totals remain withheld.
The separate `source_context` view verifies an explicitly supplied execution
source against current reservation/completion records and the original manifest
attempt bytes. The descriptor must equal the namespace reservation **byte for
byte**, not merely name the same execution ID or binding. Both the sealed
namespace reader and Store use the same admitted-read counter before I/O.
This additional current-source pass is explicitly **not a snapshot** of the
earlier category pass. A missing, corrupt, mismatched or budget-denied declared
source makes the aggregate incomplete through CLI/API exit/status handling;
native shape observations remain separately labeled. Without a source
declaration, context remains unavailable and no namespace is opened.
Even verified context establishes only content and declared-context consistency:
it does not hydrate Creator authority, prove live execution or authorize billing.
Future usage integration must reuse `models.AggregateUsageStats`, preserve
recorded zero/precision and original source usage, and exclude reused/cached
historical execution usage from new-operation billing. A historical observed
credit value and the cost of this read-only aggregation are distinct scopes.

The aggregate exposes safe declarations, category admission and recorded usage, not arbitrary
payload text or artifact names. Source links point to existing manifest/sidecar
inspection, not unrestricted payload downloads. Keep original source privacy
rules; aggregation cannot retroactively redact an already published source.

## Bounds and storage guarantees

Recorded grader identifiers use UTF-8 byte ordering, not locale-dependent or
JavaScript UTF-16 ordering. Native emission and Lab admission preserve the same
ordering and reject duplicate verdict identifiers or contradictory outcomes.

There are at most 128 submitted digests and 128 submitted evidence pairs
**before deduplication**, independently of the 32 KiB API JSON ceiling.
The maximum compact 128+128 selection fits that ceiling; padding and overlong
wire input still count toward it. The cumulative read budget is 256 MiB, covering
manifest, artifact, marker, sidecar, lineage and namespace reservation/completion
verification. It deliberately
does not promise to fit 128 individually maximum-sized publications.
The actual `Store.ReadCompleted` retains
selected original terminal bytes in the same all-artifact verification pass;
there is no separate check/reopen shortcut. Failed reads consume
their full requested limit plus its excess-detection byte because actual
discarded bytes may be unknown.
Successful reads refund unused reservations. This is an admitted read budget,
not a resident-memory, whole-store snapshot or provider-bandwidth guarantee.
Report emission is capped at 16 MiB. Per-manifest/object native bounds still
apply. Exceeding a bound is explicit; sources are never silently truncated.

Local roots and content handles retain the underlying immutable store guarantees.
There is no atomic cross-object snapshot, new distributed transaction, mutable
result directory, scheduler, or exactly-once claim.

## API and Lab

`POST /api/v1/aggregates` accepts `{"manifests":[DIGEST,...]}` with an optional
`"evidence":[{"manifest_sha256":DIGEST,"evidence_sha256":SIDECAR},...]`.
Omission preserves manifest-only output; `[]` explicitly selects no sidecars
and emits unavailable evidence membership. `null`, nonarrays and unknown fields
are rejected. The server
selects `.waza-artifacts` below its results directory, never a request-supplied
path/URL. Original-wire validation precedes reads and provider construction.
HTTP 200 carries complete membership (including recorded evaluation failure),
409 carries an incomplete/conflicting set, and 400 rejects invalid input.
Unavailable stores/report emission return 503. The API honors the server's
explicitly configured Azure store through the same
provider factory, without substituting local results on a provider error.

The Lab's Runner contract section displays member state counts, composition,
conflicts, omissions, compatibility diagnostics and original manifest links.
JSON downloads preserve the original response bytes. CSV (`--format csv`,
API `?format=csv`, or Lab download) retains exact native integer counts, absent
credits as blank cells, and recorded zero as zero. Lab rendering and CSV use
preserved integer tokens rather than rounded JavaScript numbers, including
9007199254740993 and MaxInt64. Lab CSV derives from its captured response without
rereading sources; separate CLI/API invocations are separate observations.
Optional-evidence CSV appends separate native category and declaration JSON
columns plus `source_context_json`; manifest-only CSV keeps its existing header
and bytes. An unread/unselected sidecar cannot supply a source-context claim.
The aggregate's native usage fragment intentionally supports int64 counts;
it does not change assurance profiles' separate JavaScript-safe integer bounds.
Input edits and failed reloads clear prior
success; the synthetic demo cannot claim live aggregation.

## Migration and remaining release gates

The profile is `schemas/manifest-aggregate-1.0.schema.json`. It is independent
of artifact manifest, runner terminal, execution-evidence and legacy result
versions. Historical `results.json` is not a completed-manifest substitute and
is not automatically migrated. Publish original compatible runner terminal
bytes with their actual binding using the immutable publication workflow.
Do not reconstruct missing terminal evidence from aggregate statistics.

Unknown future profiles are not implicitly upgraded; adopt a separately
reviewed aggregate reader/profile. Frozen historical result compatibility
continues to use the existing compatibility corpus.

The actual read-only ledger adapter and same-manifest producer descriptor
bridge are integrated into the bounded source-context layer. Full #766 remains
open until the owning public producers/consumers are aligned on the combined
source, all category/source/usage controls and required Go/compatibility,
dashboard/site browser, screenshot and independent review gates complete,
and the user/developer documentation is published with the tested release.
Pure codecs, authored controls, mock producer integration and build success
alone do not prove live agent quality, native Windows behavior or publication.

The shared `ReadCompleted` and `OpenBackend` integration uses the actual #763
commit `fbb1a33fd543a51bad505fc1af4423825a6a04d3`. Focused local controls do not
replace final current-head provider, native-platform or release validation.
