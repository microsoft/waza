# Exact-unit checkpoint contract

Checkpoint schema **1.0 is an additive declaration contract under development
for #765**. Protected exact-unit operations are implemented in the unreleased
source; this is not yet a validated release recovery guarantee. The current
`executioncheckpoint` package provides strict codecs, declared compatibility
comparison, original-source context admission and ordinary immutable checkpoint
blob storage, retained-creator boundary publication and typed source
reads with original-completion stale rejection. The source-level
`runnercheckpoint` adapter connects these primitives to actual single-admission
submission and protected public CLI/API creation/recovery. Compiled-CLI
interruption, hosted browser and native-platform completion gates remain pending.
A separate read-only API/Lab inspector admits current blob metadata but does not
authorize recovery.
Do not treat stored declaration bytes or a passing codec as evidence that
interruption recovery works.

## Safe boundaries

The bounded [runner contract](RUNNER_CONTRACT.md) executes one captured inline
task/trial/attempt. The checkpoint profile names only these boundaries:

| Boundary | Required contents | Intended owning-caller behavior |
|---|---|---|
| `before-unit` | Original execution ID and reservation digest, creation time, compatibility metadata; no completion/manifest references | No completed work is claimed. The resume caller obtains a new distinct reservation before constructing an engine. A declaration does not allow reconstructing a lost creator capability. |
| `finalized-unit` | All preceding fields plus canonical original completion and manifest digests | The restore caller rereads and verifies the configured original source, complete original completion binding, final marker, every declared artifact and terminal result before skipping the unit. It preserves original source identity/attempt and failed-check verdicts. |

These are **not mid-runtime snapshots**. SDK sessions, active tools, multi-turn
responder state, grading-in-progress, filesystem workspaces, pending side effects
and historical result/snapshot files are not resumable under this profile.
A published error envelope alone does not prove a finalized source attempt.
When an independently authenticated completion and current finalized manifest
admit that original terminal, failures and cancellations remain their original
statuses with no new engine call. Finalizing error evidence is not successful
logical work; retry eligibility belongs to the separate lifecycle contract.
Completed grader failures must not be rerun or relabeled as a pass.

## Compatibility

`ForRequest` validates original request wire shape and negotiation, then reuses
`runneridentity.DeriveJSON`. Its `work` is the original logical trial identity:
run key, request version, deadline, complete captured eval/task source and
task/trial selection participate. Model, config, graders and captured tool/policy
declarations therefore cannot be changed invisibly. Source canonicalization
preserves omitted/null/empty/zero/false distinctions; source comments, mapping
order and LF/CRLF do not change that identity.

Request version/capability requirements and explicit paid-execution consent are
also hashed separately, because the shared logical identity does not include
all negotiation declarations. Capability/version list order is insignificant;
their values are not. Only an attempt ordinal may differ between source and
destination work. An owning caller must still apply current semantic admission,
bounds and attempt/ownership policy; shape validation and matching hashes do
not authorize execution.

Compatibility includes an explicit exact lowercase 40-character runner commit
revision, identity profile and separate request, terminal, manifest, attempt and
completion schema versions. The production caller must provide its actual
build revision; a caller-authored string is not a verified build attestation.
Unstamped development builds cannot supply a version fallback. A changed
revision, schema, logical work or policy fails closed with named differing
fields and instructions to preserve the checkpoint and start a distinct attempt.
There is no implicit cross-version migration or age-based expiry policy.

`CurrentRunnerRevision` reads the owning CLI's embedded Go build metadata without
reading ambient files or invoking Git. It requires the Waza CLI main module/path,
one exact Git revision and an explicit `vcs.modified: false`. Missing, duplicate,
dirty, foreign-host and non-Git metadata fail closed. The CLI recovery caller
uses this actual build value and does not accept `runner_revision` as a user flag.
Admission tests using authored build metadata are not execution evidence for
a released binary; production-CLI recovery tests remain required.

Verify the actual owning binary with `go version -m /path/to/waza`. A clean
working tree alone does not ensure Go recorded VCS metadata: the observed Go
1.26.4 compiler recognizes a `.git` directory but not the `.git` file in an
app-managed worktree. Such an unstamped binary is rejected before reservation.
Use an ordinary versioned-checkout/CI build that records the real revision and
clean flag; do not inject metadata or waive admission. Pre-frontend API/process
builds are not evidence of matched embedded-dashboard delivery.

The native engine also resolves ambient provider settings and `COPILOT_CLI_PATH`.
Declared configuration hashes do not pin those external runtimes or providers.
An owned native checkpoint caller must use the shared effective-runtime admission
guard before reserving or restoring work; matching declarations alone cannot
authorize native recovery. Ordinary runner and SDK session behavior is unchanged.

## Contents and admission

The public schema is
[`execution-checkpoint-1.0.schema.json`](../schemas/execution-checkpoint-1.0.schema.json).
Original JSON is validated before typed decoding. Missing/null fields, unknown
members, duplicate keys (including escaped duplicates), invalid Unicode,
unsupported boundaries, extra documents and oversize payloads are rejected.
The maximum checkpoint payload is 1 MiB. Canonical bytes use typed field order,
compact Go JSON escaping and no trailing LF. Creation time must be canonical UTC
RFC3339, with nonzero year and optional nanosecond precision; redundant fractional
zeros and timezone aliases are not canonical. The timestamp records creation,
not ownership, expiry or proof that storage committed the record.

`ValidateContext` requires the independently retained original production
admission binding, not a binding inferred from checkpoint/sidecar bytes or the
new destination request. It compares the entire original identity and selection
(including source attempt ordinal), execution mode and grader identifiers with
the reservation. Coherently rehashed sidecars cannot substitute a retagged
source. This reference binding is still caller-supplied; the pure helper does
not authenticate its provenance.

Exact canonical reservation bytes are also bound to execution ID, digest and
logical work. At `finalized-unit`, completion bytes must also be
canonical and match their digest and source references. It reuses
`executionledger.ValidateContext` to admit the original reservation and manifest,
including original identity, selection, execution mode and grader identifiers.
Inputs are not modified and the source terminal must never be retagged to a
resuming attempt. **This is pure declaration admission, not content/finalization
verification.** The manifest provider and retained attempt owner remain the
authority for those separate checks.

No embedded request, credentials, transcript, session or workspace is added to
the checkpoint. Digests identify already-captured source/evidence; they do not
prove execution, runtime isolation, billing completeness or model quality.

## Migration and delivery status

Canonical declarations are stored as ordinary `blob-<sha256>` objects through
the existing `artifactmanifest.Store.Put`, not a checkpoint ledger namespace or
a disguised reservation/completion/decision record. `Load` consumes a
caller-known digest through bounded `Backend.Get` and independently verifies
hash, strict schema and canonical bytes. It does not normalize a noncanonical
stored record or adopt unrelated correctly hashed blobs. There is no mutable
pointer, checkpoint catalog or implicit completed-evidence final marker.
The owning caller must authenticate source ownership separately. Existing
provider no-overwrite, rooted-handle and partial-staging limitations remain;
checkpoint storage does not upgrade them into transactions or crash durability.
Publication errors retain the digest of possibly stored, unverified residue.
Cancellation after stored bytes is an error, not successful invocation completion,
and does not delete immutable evidence.

`ReadFinalized` compares current compatibility before provider access, then
uses the actual `Store.ReadCompleted` API to capture original terminal bytes
from the full current-content/final-marker verification pass. It admits those
bytes against the independently authenticated original binding and sidecars.
It never reconstructs an SDK session or runs an engine, and preserves the exact
original failure/cancellation status, identity and native ordinal. The helper
does not authenticate a caller-supplied binding or grant execution ownership.
The protected public adapter separately performs typed original-source admission
and capture/disclosure validation through the actual owned submission path.

`PublishFinalized` accepts only the actual retained `Ledger` creator. It compares
both the supplied original request's full identity **and its explicit selection**
with the retained reservation, then uses the typed ledger's current completion
and verified publication. Matching identity hashes do not repair an inconsistent
native selection. Original creator bytes are validated before publication and
again afterward; a late failure returns the stored digest with an error and
does not delete evidence. The owning runner must supply its original admitted
request and real build revision and join execution before this helper runs;
the helper does not independently authenticate caller-supplied negotiation
metadata or establish the lifecycle boundary.

`ReadStoredFinalized` uses `Ledger.ReadCompleted` to obtain the independently
stored original binding/reservation/completion rather than a binding inferred
from checkpoint fields or a destination ordinal. It validates checkpoint context
against those actual source records and returns their current verified
publication and original terminal bytes. These reads do not recreate a creator
capability or upgrade declared-context/content verification into runtime
attestation. The blob backend and typed ledger must select the same configured
source store in the public caller.

`PublishBefore` requires the same retained original creator and request binding,
and checks typed original completion state before and after blob publication.
The owning single-admission runner must call it before engine construction;
the helper alone cannot establish that lifecycle point. `ReadBefore` admits the
actual stored original reservation and current compatibility, never a creator.
Only the typed ledger's missing **completion key** permits a fresh child attempt.
A present completion must retain the original reservation and pass the actual
complete publication/content/final-marker reader. Verified completion makes the
old before checkpoint stale; corrupt, unfinalized, missing referenced content
and unknown storage state all block recovery rather than becoming absence.

The original-completion check and subsequent child reservation are not a
transaction or a global exactly-once guarantee. A cooperating original writer
may complete after the check, and an already-completed child is not discovered
by probing the original parent alone. Distinct children do not reclaim the
original creator or overwrite records. Preserve source and child evidence;
do not infer global completed-work discovery or concurrent retry exclusion.

### Source-level owning caller adapter

`runnercheckpoint.Service` wraps the actual `runnerunit.Service.Submit` once.
It does not copy semantic admission, factory construction, execution or terminal
lifecycle. The host supplies the real `CurrentRunnerRevision` value; library
tests using authored revisions do not prove clean production build admission.

`Submit` creates a before checkpoint in the actual retained-creator `Reserved`
hook before factory construction. The embedding host's existing reservation
observer runs first; checkpoint publication then validates the original creator
and frozen request. The separate `Published` callback receives an actual stored
checkpoint digest and boundary, not a guessed extra-artifact or lifecycle profile.
The actual joined `Terminal` hook remains **before** manifest publication and
completion. Finalized checkpoint publication occurs only after actual `Submit`
returns its verified completion with the retained creator still in-process.
The adapter preserves the actual shared `Admission`, `ReuseAdmission`,
`PreparePublication` and `ValidatePublication` hooks: capture denial precedes
reservation; source disclosure denial precedes a reuse decision; first-publication policy runs before
terminal observers and writes. The committed `ownedsubmission.CaptureHooks`
facade composes original-category withholding with those observers and validates
prepared content against independently retained original observations. It rejects
competing publication policies and arbitrary additional artifact producers.
Actual adapter controls cover restricted binding rejection before reservation,
prepared-category corruption rejection before the terminal observer/completion,
and protected successful, failed and canceled original reuse with zero new calls.
Current owned submission does not publish or
return a raw rich report. Historical optional report admission does not grant
permission to disclose it or transform a reused original.
Publication/callback failures preserve available digest locators and partial
source records, return errors, and do not create success-shaped progress.

`Resume` admits current compatibility and actual original source records.
Before-unit recovery supplies a new distinct child with immutable `retry`
lineage to that exact original reservation, and rechecks the original completion
state in the real pre-factory callback. Finalized recovery uses actual explicit
source reuse, without callbacks, fresh IDs or engine construction. Original
terminal/report bytes, failed/canceled status and native ordinal are retained.
Omitted policy selects this boundary-specific operation; explicit conflicting
parent/source/mode declarations reject rather than being silently rewritten.
Ordinary submission omission still means distinct execution.

Successful finalized reuse reports one restored unit, zero remaining work and
the actual submission's zero new engine calls. A fresh child reports zero restored
units and, only after actual completed publication, zero remaining work. These
counts describe this selected exact unit, not global work or successful grading.
Rejected/unknown restore progress remains null. Digest fields on a failed
operation can locate unverified residue; their presence is not publication success.
Compatibility rejections expose only differing field names, never original
captured values or provider details. Public serialization and operation transport
remain gated on the shared owner privacy/capture policy; a library submission's
retained rich report is not automatically a public response. The protected
`SubmitCaptured`/`ResumeCaptured` paths compose actual original-category policy,
track actual publication/reuse approval, and produce a non-mutating public
projection. Unapproved original source/partial terminal/report bytes are omitted.
The output-only optional `source_withheld: true` records source withholding;
it is mutually exclusive with `source` and forbidden on successful/compatible
results. Incompatible results retain nonempty field-name-only differences even
when source metadata cannot be disclosed. False, null and wrong-type markers
reject, as does a marker plus source. The marker never grants admission or
ownership and is not accepted in submission input. Optional historical reports
are omitted only from public output, never rewritten in original storage.
The read-only source inspector below does not inherit these owning-caller results.

The additive
[`runner-checkpoint-result-1.0.schema.json`](../schemas/runner-checkpoint-result-1.0.schema.json)
and `ParseResult`/`EncodeResult` validate original wire types, explicit null
progress, actual submission status/exit context and exact original reuse or
fresh-child lineage declarations. They compose the actual submission-result
profile, without reading storage, starting a runtime or granting disclosure
rights. Changed boundary/source references, fabricated progress and coherent
source substitutions reject. Reader admission is not independently verified
recovery evidence; public transport must still apply the shared capture/privacy
policy before serializing any rich report.

## Protected public operations (unreleased v0.41 source)

The CLI accepts one `waza.execution-submission` wrapper on stdin:
`waza runner checkpoint create --store DIRECTORY` or
`waza runner checkpoint resume CHECKPOINT_SHA256 --store DIRECTORY`.
The explicit `--azure-container-url` alternative uses the same actual provider
factory and existing credentials, without automatic login. There is no
user-supplied runner revision; public operations read the owning CLI's clean
embedded VCS metadata. Stdout is one strictly validated protected operation
result. Stderr announces `checkpoint BOUNDARY SHA256` as each stored boundary
is published, allowing the caller to retain a before-unit reference if interrupted.
Those references are locators, not completed execution evidence.

The dashboard's literal-loopback, same-origin JSON API provides
`POST /api/v1/checkpoints` and
`POST /api/v1/checkpoints/{digest}/resume`. Both use the configured original
immutable store, the same protected adapter, and the shared bounded transport.
The wrapper budget is the native 4 MiB request budget plus 8192 bytes. Unsupported
active providers reject rather than falling back to local storage. Delivery is
one write/flush with an exit-code header, not a claim of consumer receipt.
The HTTP response is not a checkpoint discovery stream: an interruption before
delivery may leave a blob whose digest the caller never received. No pointer,
catalog or ownership-rehydration API is introduced.

The Lab **Create or resume an exact-unit checkpoint** panel executes explicit
actions and displays actual operation compatibility/progress, original terminal
status/native ordinal and new-versus-original engine calls. Source withholding
is explicit. A separate current blob read displays newly published boundary/time
as declaration-only metadata, not replacement execution evidence. Input edits,
failed calls and cancellation clear prior result/metadata; the synthetic demo
does not run these operations. JSON download preserves the actual protected
response bytes. See the [operational guide](../site/src/content/docs/guides/runner-checkpoints.mdx).

Focused Unix process controls run actual `Submit`/adapter in a separate test
process, observe its stored checkpoint reference, kill that exact process and
verify `SIGKILL` before opening the store under a fresh owner. Before recovery
creates a new child; finalized recovery constructs no engine. A removed original
final marker then blocks restoration without rerunning work. These tests use an
authored revision in a test binary: they are not public-CLI interruption,
clean-build revision, Windows or browser evidence.

### Read-only source inspection

The current-source dashboard registers `GET /api/v1/checkpoints/{digest}` and
the Lab **Runner contract → Inspect a stored checkpoint declaration** panel.
They accept a caller-known full lowercase SHA-256 digest, not a host path or
provider URL. The same configured immutable provider as manifest inspection is
used; unsupported active providers are rejected rather than replaced by local
results. No checkpoint catalog or mutable pointer is introduced.

Each request rereads and admits the current bounded blob hash, schema and exact
canonical bytes. **Blob verified (declaration only)** means only that admission.
Boundary, creation time, original execution reference and runner revision remain
declarations. Source ownership, current input compatibility, restored units and
remaining work are unavailable; null progress must not be replaced with zero
or derived from a boundary name or completion/manifest digest.

Malformed caller digests return HTTP 400 before opening storage. Missing blobs
return 404, corrupt/noncanonical stored declarations return 409, and storage or
close failures return 503 when no earlier source error applies. Provider details
remain in operator logs. Inspection failures and digest edits clear previous Lab
metadata; the synthetic demo never substitutes a checkpoint. This view does not
create checkpoints, write completion records, finalize evidence or resume work.

This schema has its own version; it does not change native task YAML, eval
schema, historical `EvaluationOutcome`, snapshots, terminal 1.0 or SDK session
resume behavior. No existing artifact is automatically adopted as a checkpoint.
Keep existing commands/readers for unsupported workflows.

The source layer has codec and compatibility tests, actual immutable-provider
blob persistence/concurrency/corruption/failure tests, and context tests
using actual production mock runner terminal output, including passed and
failed-check controls. Typed-owner tests also cover foreign/zero creators,
coherent source selection/identity mismatch, late creator failure and current
source corruption. Separate production submission and protected CLI/HTTP tests
cover single admission, fresh before-unit retry parents, original failed/canceled
finalized reuse, unavailable/corrupt source and capture denial. The current-source
Lab is connected to those public operations; its new browser cases still require
execution against the exact changed head.

Full compiled-CLI interruption and failed-restore process evidence, native-platform
checks, hosted dashboard/site validation and matching generated assets remain
required before #765 is complete or a release can advertise recovery. Library
process controls are not public-CLI evidence; no check establishes global
exactly-once work. The ordinary CLI inherits the process-only diagnostic writer's
100 ms cumulative stderr budget through the root command. Standalone/embedding
commands with custom stderr writers do not inherit that protection; they must
provide their own bounded delivery. A timed-out process diagnostic write may
finish late until process exit; no locator delivery success or joined write is
claimed.
