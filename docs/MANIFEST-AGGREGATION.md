# Independent completed-manifest aggregation

This unreleased v0.41 source addition derives a new, separate
`waza.manifest-aggregate` **1.0** document. It does not rewrite historical
evaluation results or change their schema/defaults. The explicit expected set
is the scope of the report, not a discovered fleet inventory.

## Admission and deterministic policy

`waza aggregate DIGEST [DIGEST...] --store EXISTING_DIRECTORY` reads public
immutable publications without starting workers, models, subprocesses, loading
project configuration, or performing an update check. The selected local store
must already exist. There is no provider fallback or implicit login.

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

Until that integration is admitted, usage, credits, numeric scores and evidence
assurance are unavailable; zero or computed estimates are not substituted.
Future usage integration must reuse `models.AggregateUsageStats`, preserve
recorded zero/precision and original source usage, and exclude reused/cached
historical execution usage from new-operation billing. A historical observed
credit value and the cost of this read-only aggregation are distinct scopes.

The aggregate is metadata only. Source links point to existing manifest
inspection, not unrestricted payload downloads. Keep original source privacy
rules; aggregation cannot retroactively redact an already published source.

## Bounds and storage guarantees

There are at most 128 submitted digests **before deduplication** and 16 KiB of
API request JSON. The cumulative read budget is 256 MiB, covering manifest,
artifact and marker verification plus any selected rereads. Failed reads consume
their full requested limit because actual discarded bytes may be unknown.
Successful reads refund unused reservations. This is an admitted read budget,
not a resident-memory, whole-store snapshot or provider-bandwidth guarantee.
Report emission is capped at 16 MiB. Per-manifest/object native bounds still
apply. Exceeding a bound is explicit; sources are never silently truncated.

Local roots and content handles retain the underlying immutable store guarantees.
There is no atomic cross-object snapshot, new distributed transaction, mutable
result directory, scheduler, or exactly-once claim.

## API and Lab

`POST /api/v1/aggregates` accepts only `{"manifests":[DIGEST,...]}`. The server
selects `.waza-artifacts` below its results directory, never a request-supplied
path/URL. Original-wire validation precedes reads and provider construction.
HTTP 200 carries complete membership (including recorded evaluation failure),
409 carries an incomplete/conflicting set, and 400 rejects invalid input.
Unavailable stores/report emission return 503. The initial API supports the
explicit local immutable store; configured Azure aggregate reads are rejected
without substituting local results.

The Lab's Runner contract section displays member state counts, composition,
conflicts, omissions, compatibility diagnostics and original manifest links.
Downloads preserve that report. Input edits and failed reloads clear prior
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

Full #766 remains open until actual committed #768 category persistence and
producer admission are integrated, all category/source/usage controls pass,
shared provider reads are integrated, and the required Go/compatibility,
dashboard/site browser, screenshot and independent review gates complete.
Pure codecs, authored controls, mock producer integration and build success
alone do not prove live agent quality, native Windows behavior or publication.
