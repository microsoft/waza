# Derived execution evidence and protected capture

The opt-in `internal/evidencestate` package defines version 1.0 evidence
declarations separately from immutable manifest 1.0, native terminal outcomes,
and historical result readers. This library recovery does not add CLI commands,
API routes, dashboard views, or release acceptance. Those consumers must use the
same capture policy before exposing execution data.

## Categories and presence

Every document contains exactly one record for conversation, tool, grader,
reliability, usage, and provenance. No category defaults to present.

| State | Meaning |
|---|---|
| `present` | Declared complete observation. Explicit `observed_count: 0` with empty references means observed present-empty, not missing data. |
| `unavailable` | No recorded observation; no count or references. |
| `incomplete` | Partial capture or a measured exceeded storage limit; not a complete total. |
| `redacted` | Withheld for privacy; no payload, count, or references. |
| `conflicting` | Inconsistent observations; no implicit winning source or passing verdict. |

Source kind (`authored`, `mock`, `cached`, `live`, `unknown`) and context
(`declared`, `observed`, `unknown`) are declarations, not execution attestation.
Copilot configuration alone does not establish live execution. A passing grader
verdict is independent of evidence completeness.

Recorded usage remains `models.UsageStats`. Explicit zero is preserved;
unavailable or incomplete credit evidence must not become an authoritative zero
or partial total. `ReadUsage` and `InspectWithUsage` admit actual recorded fields,
not inferred token prices. Consumers must require present, complete usage before
including it in authoritative aggregation.

## Pure codecs and current source admission

`Parse` validates original JSON against the embedded offline schema and native
invariants. It rejects duplicate or unknown keys, invalid Unicode, null required
fields, missing or duplicate categories, and inconsistent state/count/reference
combinations. `Encode` sorts detached slices without mutating caller data and
emits compact canonical JSON. The evidence and capture schemas are independently
embedded in `schemas/`.

`VerifySources` checks caller-provided manifest and artifact bytes, including
canonical encoding, digests, lengths, membership, terminal binding, and any
referenced execution-attempt descriptor. This establishes byte integrity only:
it does not prove execution, privacy authorization, category semantics, or a
valid retry/resume relationship.

`Publish` admits already finalized source publications through the configured
backend and stores a separate content-addressed derived blob. It never rewrites
the origin or grants creator authority. Its receipt describes that invocation's
source capture, not an atomic post-write source snapshot. `Inspect` performs
fresh source admission; old successful receipts are not reusable authorization.

`InspectCategories` also checks the producer capture index and recorded payload
shapes. A declaration without that index remains explicitly unqualified.
Category failures do not turn a byte-integrity receipt into semantic success.
Neither declaration parsing nor successful source admission establishes an
assurance verdict.

## Capture before persistence

`CaptureReport` projects one invocation's original report without running an
engine again. Nil withholding uses the conservative conversation/tool default;
an explicit empty list permits recorded content subject to the existing secret
filter. Regex filtering is not comprehensive disclosure authorization: a host
must additionally withhold restricted categories.

`ownedsubmission.CaptureHooks` supplies the conservative public-submission
policy: conversation, tool, and free-text reliability data are withheld before
publication. Reliability withholding covers both terminal diagnostics and the
observation error, not only recognized credential strings. Native outcome,
selection, identity, counters, and exit policy remain unchanged.

The hooks compose with admission and invocation observers but reject competing
publication/artifact policies. Admission checks binding identifiers before
reservation. Preparation requires the retained creator and exact canonical
reservation bytes. Validation independently recomputes approved terminal and
artifact bytes from the detached original observation, rejecting changed policy
output. An `execution-attempt.json` artifact retains the actual reserved source.

Reuse separately checks original-source disclosure. Unsafe original diagnostic
text is rejected without rewriting or republishing an immutable execution.
Public callers must still project **every** response, including incompatible
resume responses containing a source before a capture hook runs. These hooks
alone do not make an unrestricted native result safe for public exposure.

`Report.Terminal()` exposes the existing terminal projection with detached
identity/selection and binding comparison data. It does not expose a creator,
reconstruct an execution, change report JSON, or make another engine call.

## Limits, failures, and completion

Manifest bounds remain 128 artifacts, 64 MiB per object, and 256 MiB total.
The sidecar is bounded to 1 MiB. Measured degradation records a lower bound
strictly exceeding the relevant limit; an exact total is absent when unknown.
It does not authorize oversized origin artifacts or silent truncation.

`PersistOwnedCapture` requires an actual retained creator, validates the capture
against its reserved binding, and publishes admitted objects, the manifest,
finalization, execution completion, and then a separate derived sidecar. It
propagates partial publication, completion, and post-publication inspection
failures. There is no whole-operation transaction or rollback guarantee.

This package does not sanitize an already persisted origin, infer unique
executions from legacy attempt ordinals, or establish cancellation/force/process
join evidence. Runtime diagnostics, retry/resume policy, public CLI/API/Lab
integration, and execution-aware aggregation remain separately accountable
deliverables for #764, #765, #766, #767, and #768.
