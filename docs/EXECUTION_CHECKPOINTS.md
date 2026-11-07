# Exact-unit checkpoint contract

Checkpoint schema **1.0 is an additive declaration contract under development
for #765**, not an operational resume feature. The current
`executioncheckpoint` package provides strict codecs, declared compatibility
comparison and original-source context admission only. It does not reserve
attempts, create checkpoints in storage, restore work, or provide a checkpoint
CLI/API/Lab view. Do not treat a passing codec or a declared boundary as evidence
that interruption recovery works.

## Safe boundaries

The bounded [runner contract](RUNNER_CONTRACT.md) executes one captured inline
task/trial/attempt. The checkpoint profile names only these boundaries:

| Boundary | Required contents | Intended owning-caller behavior |
|---|---|---|
| `before-unit` | Original execution ID and reservation digest, creation time, compatibility metadata; no completion/manifest references | No completed work is claimed. A future resume caller must obtain a new distinct reservation before constructing an engine. A declaration does not allow reconstructing a lost creator capability. |
| `finalized-unit` | All preceding fields plus canonical original completion and manifest digests | A future restore caller must reread and verify source ownership, the complete original completion binding, final marker, every declared artifact and the terminal result before skipping the unit. Preserve the original source identity/attempt and failed-check verdicts. |

These are **not mid-runtime snapshots**. SDK sessions, active tools, multi-turn
responder state, grading-in-progress, filesystem workspaces, pending side effects
and historical result/snapshot files are not resumable under this profile.
A failed or interrupted runtime is not a completed unit merely because its
error envelope was published. Completed grader failures must remain failures,
not be rerun or relabeled as a pass.

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

This schema has its own version; it does not change native task YAML, eval
schema, historical `EvaluationOutcome`, snapshots, terminal 1.0 or SDK session
resume behavior. No existing artifact is automatically adopted as a checkpoint.
Keep existing commands/readers for unsupported workflows.

The first contract layer has codec and compatibility tests and context tests
using actual production mock runner terminal output, including passed and
failed-check controls. Those tests **do not prove kill-and-resume, persistence
or exactly-once work**. Durable creation/restore, failed-restore process tests,
user commands, connected site examples and derived Lab availability/progress
remain required before #765 is complete or a release can advertise recovery.
