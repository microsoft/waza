# Derived execution evidence declarations

Issue #768 introduces a **separate, opt-in 1.0 evidence sidecar**. The bounded
`internal/evidencestate` codec does not change existing result readers, verdicts,
billing, native bindings, or immutable manifest 1.0. It is a pure declaration and
source-byte verification layer, not a producer, store, release assurance policy,
or dashboard feature.

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

No automatic migration, new default assurance, legacy-result reinterpretation,
store API, or public command is delivered by this pure slice. Full #768 remains
open pending committed #763 publisher/provider and final #764 ledger integration,
producer/retry/resume/aggregation integration, production category-removal,
redaction/corruption/conflict controls, and API/Lab/download/site/release/migration
delivery. Pure codec tests are not filesystem, cloud, runtime, end-to-end, or
native Windows execution evidence.
