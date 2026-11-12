# Derived execution evidence and protected capture

The `internal/evidencestate` package defines version 1.0 evidence declarations
separately from immutable manifest 1.0, native terminal outcomes, and historical
result readers. The public consumers are `waza aggregate`, `POST /api/v1/aggregates`
and the Lab aggregate panel ([manifest aggregation](MANIFEST_AGGREGATION.md)).
They report re-inspected category states per member; they never copy payloads.
Any new consumer must use the same capture policy before exposing execution data.

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
Capture uses recorded session-digest usage only when the run's top-level usage
is absent. An explicit top-level zero or incomplete record takes precedence.

## Interpreting missing data

Read every category independently. Evidence completeness is never a verdict:
a `success` outcome with redacted conversation is still a success, and a
failure with complete evidence is still a failure.

- Treat `unavailable`, `incomplete`, `redacted` and `conflicting` as "unknown"
  for that category. Do not count them as zero, empty, or passing.
- `present` with `observed_count: 0` is a recorded empty observation, which is
  different from `unavailable`.
- Consumers report the *verified* state, not the declared one. A sidecar that
  declares a state the original capture index does not record makes the member
  `conflicting`; its categories stay `unavailable` with
  `verification: declared-state-only`. A sidecar without the capture index (for
  example one that withholds provenance) stays unqualified the same way.
- Aggregated historical usage totals only verified `present` usage from
  non-cached executions. Any other usage state withholds the whole total.

## Retries, resumes and lineage

Retries and resumes append new records; they never overwrite an original
manifest, sidecar, reservation or completion. Two independent links exist:

- **Execution parent** (`parent` in an aggregate member) comes from the verified
  original reservation: `execution_id` plus `retry` or `branch`. The ledger
  rejects parents from different logical work, cycles and self-references.
- **Evidence lineage** (`lineage`) is the sidecar's declared list of source
  manifests with `retry`, `resume`, `cache` or `aggregation`. Each source must be
  a completed manifest; cache lineage excludes the member from usage totals.

Neither link selects a winner. An aggregate keeps the original attempt and its
outcome beside every retry, and counts each verified execution once.

## Privacy

Conversation and tool content are withheld by default. Withheld categories are
`redacted` with no payload, count, references or free text. A credential-shaped
value withholds the entire category. Aggregate reports carry only states,
sources, contexts, counts, reason codes and digests.

## Extending categories or states

The six categories and five states are a closed 1.0 vocabulary enforced by the
schema and parser. Adding a category, state, reason or lineage relationship
requires a new evidence schema version, an updated aggregate schema and Lab
legend, and a migration note. Unknown values are rejected, never mapped to the
nearest known state. Producers must emit every category explicitly; omission is
not a way to declare `unavailable`.

## Pure codecs and current source admission

`Parse` validates original JSON against the embedded offline schema and native
invariants. It rejects duplicate or unknown keys, invalid Unicode, null required
fields, missing or duplicate categories, and inconsistent state/count/reference
combinations. `Encode` sorts detached slices without mutating caller data and
emits compact canonical JSON. The evidence and capture schemas are independently
embedded in `schemas/`.
Capture encoding validates the original typed binding before JSON cloning, so
invalid UTF-8 cannot be silently replaced with a different identity.

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
Credential-pattern checks include nested freeform object keys as well as values;
a match withholds the entire category rather than retaining a renamed key.

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
join evidence. Runtime diagnostics, retry/resume policy and execution lifecycle
evidence are tracked by #764, #765 and #767.
