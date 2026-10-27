# Public single-unit runner contract

The runner foundation is **unreleased**, tracked by [#754](https://github.com/microsoft/waza/issues/754).
It adds a scheduler-neutral execution boundary; it does not replace `waza run`,
`grade`, `replay`, their schemas, or their exit policies. It owns neither queues,
placement, distributed retries, leases, aggregation nor durable publication.

## Invocation

```bash
go build -o waza ./cmd/waza
./waza runner capabilities
./waza runner run < examples/runner/request.json > selected.runner.json
./waza runner validate-result < selected.runner.json
```

`runner run` reads **one** JSON document from stdin (maximum 4 MiB), and writes
**one** terminal JSON document to stdout. Logs and errors belong on stderr.
Terminal output is bounded to 16 MiB including its final newline; oversized
observations become a small infrastructure-failure envelope instead of an
artifact that the public validator or Lab cannot read.
Save stdout to a `*.runner.json` file to expose it to the Lab backend.
`validate-result` validates a saved envelope offline; it produces no output on
success. Neither command performs update checks or project discovery.

The synchronous equivalent is `POST /api/v1/runner/run`, exposed by `waza serve`
(also available as `waza dashboard`; `--lab` keeps the existing Lab entrypoint).
Send `Content-Type: application/json`; cross-origin execution is rejected.
Execution requires a `localhost` or literal loopback `Host` header; arbitrary DNS
aliases are rejected even with a matching Origin. The existing server binds to
loopback, not a remotely authenticated execution service.
`GET /api/v1/runner/capabilities` advertises the same versions/capabilities as the
CLI. The execution endpoint returns HTTP 200 with a terminal envelope for
application failures as well as success. Interpret `outcome`/`exit_code`, not HTTP
200, as the execution result. Transport disconnects can prevent delivery.

```bash
curl --fail-with-body --header 'Content-Type: application/json' \
  --data-binary @examples/runner/request.json http://127.0.0.1:3000/api/v1/runner/run
```

## Versions, negotiation and strict admission

These are separate contracts:

| Contract | Version | Definition |
|---|---|---|
| Bootstrap request schema | `1.0` | [`runner-request-1.0.schema.json`](../schemas/runner-request-1.0.schema.json) |
| Selected runner interface | `1.0` | Request `versions`, result `negotiation.version` |
| Terminal result schema | `1.0` | [`runner-result-1.0.schema.json`](../schemas/runner-result-1.0.schema.json) |
| Identity derivation | `sha256-json-v1` | Below; independent of eval/result loader defaults |

`schema_version` chooses the bootstrap request schema; `versions` offers runner
versions. Today only `1.0` is implemented. An offer `["2.0","1.0"]` explicitly
permits using `1.0`; `["2.0"]` does not. No unspecified or implicit fallback
occurs. Unknown bootstrap schemas are invalid requests.

Required capabilities must all be available. Unknown optional capabilities are
recorded in `unavailable_optional_capabilities`; they are not silently enabled.
A capability cannot be both required and optional. Advertised capabilities are
`exact-unit`, `identity.sha256-json-v1`, `inline.mock-text`, `inline.native-text`
and `terminal-result`. Negotiation completes before parsing sources, building
engines, launching processes, models or tools.

JSON admission rejects duplicate keys, invalid Unicode (including unpaired
surrogates), trailing documents, unknown fields, wrong types, nulls where a value
is required and out-of-range integers. Captured YAML source admission rejects
unknown fields, duplicate keys, aliases/anchors and unsupported floating-point
declarations. Source/schema validation is offline; external schema references
are not fetched.

New semantics must use a new advertised version or capability. Do not silently
change the meaning of `1.0`, rename an outcome, or reuse an identity derivation
name. Version sets, result schemas and identity algorithms evolve independently.
Consumers must explicitly offer the versions they can interpret.

## Exact source bundle and selection

The request contains one captured eval YAML string, one captured task YAML
string, and the declared shard key. It contains **no host input paths**.
The requested `selection` includes all of `shard`, `task`, `trial`, and `attempt`.
Trial and attempt are one-based integers bounded by the eval's declared
`trials_per_task` and `max_attempts` (omitted/zero `max_attempts` means one).
Disabled tasks, conflicting shard/task IDs, missing selectors and excessive
ordinals fail before execution.

The runner does not expand `eval.tasks`, CSVs, globs, filters or neighboring
tasks. `eval.tasks` remains captured identity-bearing source, not a discovery
instruction. One explicit attempt is invoked without a cache, baseline pass,
benchmark loop, automatic retries or adjacent trials. Repeating the request
can execute the **same** unit again; deterministic IDs do not imply exactly-once
execution or deduplication. Increment `attempt` explicitly for a new attempt.
`adjacent_work_selected: false` describes selection, not an external observation
of other processes.

The initial inline-text profiles intentionally reject semantics they cannot
capture faithfully:

- Single-turn mock or `copilot-sdk` execution, with ambient skills disabled,
  at least one native text/expected-output check, and non-colliding check names.
- No baseline, parallel suite execution, hooks, remote repositories, tools
  supplied by Waza, MCP/command mocks, responder, checkpoints, prompt judging,
  CSVs, authored metrics, requirements, context directories or instruction files.
- Resources must have unique, canonical POSIX-relative paths and nonempty
  inline content. No prompt-file or resource-file reads, absolute paths,
  backslashes, drive names or traversal. No implicitly resolved host paths enter identity derivation.
- Use scenario schema `2.0`, or explicitly disable all skills in a supported
  historical v1 eval. A skill name/path remains unsupported even if disabled.

Native execution additionally requires **both**
`allow_paid_execution: true` and required capability `inline.native-text`.
Set `config.executor: copilot-sdk` and the desired named model in the captured
eval. Credentials/runtime prerequisites remain the ordinary engine's
responsibility. This may incur usage and is never enabled by an optional
capability alone. The reference and deterministic tests use mock execution.
Container reference images below deliberately do not include a live SDK runtime.
This profile does not attest model/runtime versions or strong OS isolation.

## Deterministic identity

The frozen input/vector pair is
[`request.json`](../internal/runnercontract/testdata/request.json) and
[`identity.json`](../internal/runnercontract/testdata/identity.json). A separately
implemented Python SHA-256/JSON calculation produced the vector; Go tests compare
against the frozen bytes, including subprocess invocations. The normal Go CI
includes these tests on Linux and Windows; local Linux execution is not native
Windows evidence.

Sources are strict YAML mappings, converted into JSON-compatible values. Mapping
order, YAML comments, equivalent scalar quoting, and YAML LF/CRLF representation
are immaterial. Scalar content is not trimmed, case-folded or Unicode-normalized.
Omitted, null and empty source values remain distinct where admitted. Canonical
JSON is compact UTF-8, sorted object keys, signed-32-bit decimal integer encoding and Go
`encoding/json` string escaping (including HTML characters). Floats are rejected.

Let `D(namespace, value)` be lowercase hex
`SHA256(UTF8("sha256-json-v1") || NUL || UTF8(namespace) || NUL || canonicalJSON(value))`.
`E`/`T` below are the canonical eval/task mappings; IDs use the **unprefixed**
parent digest as input:

| Digest namespace | Canonical value |
|---|---|
| `run` | `[run_key, timeout_ms, E]` |
| `shard` | `[runDigest, selection.shard]` |
| `task` | `[shardDigest, selection.task, T]` |
| `trial` | `[taskDigest, selection.trial]` |
| `attempt` | `[trialDigest, selection.attempt]` |
| `input` | `[runDigest, shardDigest, taskDigest, trialDigest, attemptDigest, timeout_ms]` |

IDs prepend their namespace plus `-` to the corresponding digest.
`canonical_digest` is the unprefixed `input` digest. Capability offer ordering
and equivalent negotiation representations are not execution inputs. Consent
does not identify work; native consent is independently required by admission.
Changing run inputs changes descendants; changing only an attempt preserves its
parents. Changing task content preserves the run/shard parents. No timestamps,
session IDs, resolved workspace paths or ambient process/host information enter these logical IDs.
Any path-like text authored in the captured sources (including unused task
declarations) is literal identity-bearing input, never a discovered host location.
They bind declared source semantics, not an environment attestation or external
artifact verification.

## Terminal result and failure interpretation

| Outcome | Process exit | Meaning |
|---|---:|---|
| `success` | 0 | Selected attempt produced graded passing evidence |
| `evaluation_failure` | 1 | Selected attempt ran; one or more checks failed |
| `infrastructure_failure` | 2 | Setup/execution/shutdown/publication-channel failure |
| `canceled` | 3 | Cancellation observed, including supported graceful signals |
| `timeout` | 4 | Unit/eval/task deadline exceeded |
| `unsupported_capability` | 5 | No common runner version or required capability |
| `invalid_request` | 6 | Invalid source/schema/selection/profile/consent |

The discriminator `kind: "waza.runner-result"` prevents historical evaluation
readers from adopting these envelopes as empty passing evaluations.
The result carries requested/resolved selection, validation state, negotiated
versions and capability diagnostics, identities, canonical digest, terminal
outcome, exit code and `failure_boundary`. An unavailable selection, identity or
observation is omitted, never manufactured as a successful empty observation.
`observation` contains the existing run-result diagnostic/evidence structure;
it is not a historical `EvaluationOutcome` or a substitute for one. Its usage
fields remain unavailable when not observed; mock results do not prove live
execution or cost. `envelope_valid: true` means schema/consistency validation,
not independent verification of evidence.

One service return is terminalized before writing; there is no competing
completion/cancellation writer. Panic recovery, late cancellation, missing
observations and shutdown failures must not become success. The CLI writes one
document after validation. If stdout cannot be written, it exits 2 and reports
the delivery failure on stderr; it cannot promise a second successful envelope.
On Unix, SIGINT/SIGTERM request cancellation; Windows supports interrupt
cancellation. SIGKILL, process crashes before handler installation, machine loss,
and broken channels cannot produce an envelope. Absence of an envelope is
**unknown/infrastructure loss**, never success. Durable supervision/reconciliation
belongs to later releases, not this contract.

## Compatibility and migration

| Surface | Behavior |
|---|---|
| `waza run`, `grade`, `replay` | Unchanged local/batch commands and existing exit policies |
| Historical eval/result schemas | Existing readers/defaults and frozen compatibility corpus unchanged |
| `waza runner adapt` | Explicit one-task capture adapter for supported inline-text sources |
| Unsupported source semantics | Actionable error; keep using local command instead of lossy conversion |
| Historical run IDs | Not rewritten, re-derived or claimed to match new logical identities |

```bash
./waza runner adapt --eval path/to/eval.yaml --task path/to/task.yaml \
  --run-key qualification-42 --shard shard-a --task-id selected \
  --trial 1 --attempt 1 --timeout-ms 60000 > request.json
./waza runner run < request.json > unit.runner.json
```

The adapter reads only the two caller-named files and preserves their source
bytes. It does not silently remove skills/hooks, read prompt files, select CSV
rows or choose a task from a batch. It does not authorize paid execution.
Legacy batch commands are **not** rerouted into a single-unit contract: doing so
would lose discovery, retry, grading and output semantics. Wider #761 local
adapters need an independently reviewed mapping; this foundation does not claim
every historical workflow is representable.

## Bounded container reference

Requires a Linux host with Bash, `sha256sum`, Docker, Python 3, access to Go dependencies during
build, and built embedded dashboard assets. It uses no scheduler-specific code,
external artifact store or registry push:

```bash
bash examples/runner/build.sh
bash examples/runner/smoke.sh
```

The build pins Go `1.26.4` by default, consumes `go.mod`/`go.sum`, builds a static
binary, records its SHA-256 and builds from OCI `scratch` (no mutable base tag).
The local immutable `sha256:...` image ID is saved to
`.runner-reference/image-id`; smoke accepts only that ID, never a floating tag.
Changing compiler/source/assets changes the pin. Binary hash and image ID are
different identities. This is a locally built proof of concept, **not a published
release image** or a promise of reproducible image metadata across Docker builds.

The container is non-root, read-only and network-disabled, with bounded writable
workspace memory. Stdin is the local request; stdout is the only persistent
output, saved as `.runner-reference/output/unit.runner.json`. The smoke validates
the envelope via the public CLI and asserts the exact tuple/attempt and frozen
identity vector. `docker image inspect "$(cat .runner-reference/image-id)"` can
inspect the local pin. Virgil owns publishing/signing any future registry digest,
platform matrix, SBOM/provenance and update policy. Do not substitute an invented
registry digest or infer strong isolation from this reference.

## Dashboard and review handoff

The new backend endpoint `GET /api/v1/lab/runner-results` loads only
`*.runner.json` files from the configured results directory. It validates each
original wire document, returns `{schema_version:"1.0", results:[...]}`, and
reports reload errors rather than retaining stale success. Historical result
listing remains separate; no synthetic passing `EvaluationOutcome` is created
for rejected or canceled requests.

**Yen:** add a typed client in `web/src/api/runner.ts` and a dedicated surface in
`web/src/lab/DashboardLab.tsx` / `RunInspector.tsx`. Render the supplied negotiation,
requested/resolved tuple, selection validation, adjacent-work flag, identity
hierarchy/digest/version, terminal outcome/exit/failure boundary, diagnostics and
envelope validation directly. Do not infer success from observation status or
HTTP 200. This PR provides data, not the dashboard UI acceptance criteria.
**#761 and #762 require no dashboard UI change.**

**Rusty via coordinator:** review the new independent public boundary, inline-only
admission profile, explicit paid consent, capability evolution, canonical source
identity, shared orchestration seam, terminal loss limitations and separate Lab
artifact family. No implementation-owner self-approval is implied.

**Livingston/Saul:** review public navigation, compatibility guarantees and the
release distinction (unreleased foundation versus eventual complete v0.40.0).
**Virgil:** independently review the local image-ID proof and own publication if
the full #762 published-image acceptance criterion is pursued.

## Conformance selection

`make test-runner` selects every `Runner`-named unit, CLI/API, historical-source,
wire rejection, frozen identity, subprocess, signal and Lab artifact test.
`make test-compat` additionally selects the runner package alongside the existing
compatibility corpus. Normal `go test ./...` and native-platform CI discover the
new package automatically. No paid/live model run is a substitute for these
deterministic gates.
The existing required Linux build job also executes the local container
build/smoke scripts; Windows still runs the portable Go conformance suite.
