# Runner contract: negotiation 1.0

This is the first, negotiation-only layer of [v0.40.0](https://github.com/microsoft/waza/issues/754),
implementing [#757](https://github.com/microsoft/waza/issues/757). It is available
when built from this source, not retroactively in a published older binary.
The [documentation-site guide](../site/src/content/docs/guides/runner-contract.mdx)
is the canonical user invocation, API, compatibility and migration reference.

## Developer boundary

`internal/runnercontract.Negotiate` is the shared CLI/HTTP implementation. It
strictly parses UTF-8 JSON with duplicate-key and Unicode checks, validates
against embedded offline schemas, then negotiates version and capabilities.
It imports no model, engine, orchestration, subprocess, project configuration,
or network client package. No runtime factory exists in this layer.

The compact negotiation request and complete execution request are separate
schemas. The latter preserves the pinned #779 source request's field names,
types, required presence and bounds; its shape is accepted for negotiation,
but its captured YAML is never interpreted. Partial execution forms are invalid.
Shape validity is not semantic admission of the selected unit.

The negotiation response is **not** a terminal execution envelope. No work was
selected or executed, no identity was derived, and no observation or outcome is
fabricated. Capability acceptance alone is not evidence that a provider, tool,
skill, model, input or credential is usable. Future execution must invoke the
same negotiation before any runtime construction, then perform its own exact
selection and admission checks.

## Layer allocation

| Issue | Interface to extend later |
|---|---|
| #758 | Add `runner run` and exact-unit admission; consume the existing complete `Request` shape and shared negotiation first. Advertise `exact-unit` only after implementation. |
| #759 | Add deterministic identity derivation and record its independent version. Do not equate negotiation with identity verification. |
| #760 | Add independent terminal result schema, execution exit mapping, bounded/cancelable runtime lifecycle and output/archive publication. Negotiation responses must not be stored as execution results. |
| #761 | Add explicit local compatibility adapters and historical regression evidence; this layer does not reroute `waza run` or artifact readers. |
| #762 | Add pinned container proof/publication only after execution is implemented and independently reviewed. |

All eleven unresolved source findings in #779 belong to those later runtime,
terminal/archive or frontend-result layers: six HTTP/stdin/publication or
numeric/archive-read issues, two tuple/terminal consistency issues, and three
quarantine/reconciliation races. This layer imports none of that execution,
result, persistence or archive code. Negotiation stdin cancellation is tested
with an open pipe; HTTP body admission is deadline-bounded. This is not a claim
that the later findings have been fixed.

## Conformance and evolution

`internal/runnercontract/testdata/negotiation.json` records supported, downgraded,
unsupported and conflicting combinations. `testdata/request.json` preserves the
pinned source request as a wire compatibility fixture; the original schema is
retained unchanged. The public command/API parity test covers future execution
capability rejection. Browser tests forward real requests to a freshly built Go
server, rather than inventing responses a backend cannot emit.

Additive implementations may advertise new capability names for the same
compatible runner version. A breaking request or capability semantic change
requires a new version/name, not reinterpretation of an existing one. Version
offers are exact, case-sensitive strings, not semver ranges; the server chooses
its highest implemented mutual version, independently of client ordering.
Only 1.0 is implemented. Envelope `schema_version` is independent of offered
runner versions: unsupported envelope shapes fail schema validation rather
than being decoded as another profile. Unknown fields fail closed.
