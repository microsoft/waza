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
200, as the execution result. Body acquisition is bounded to 4 MiB plus one
overflow-detection byte and 30 seconds; cancellation interrupts the read.
Erroring/canceled body reads before a complete request is available return a
transport error without inventing a runner envelope. A detected size overflow
is a recorded `invalid_request` admission rejection without draining the remaining
body; header rejections likewise never drain an unfinished transport. After
admission, even a canceled/disconnected caller converges on one persisted
terminal envelope when the process and private storage remain writable.
Delivery itself is not promised.
Oversized terminal evidence is omitted with bounded original diagnostics,
original outcome/boundary, and an observation digest/encoded size when available;
publication failure is always a bounded schema-valid infrastructure result.

The CLI and HTTP endpoint use the same request parser, negotiation, selection,
execution service, result schema and terminal classification. The CLI writes one
JSON envelope to stdout and returns its `exit_code` as the process exit status.
The HTTP route returns status 200 for application-level outcomes, including
invalid requests and unsupported capabilities. Transport, origin/host checks,
body-read failures and client disconnects remain HTTP/channel concerns; they do
not turn a missing response into a successful run. `runner run` accepts no
selection flags or positional arguments: selection belongs in the JSON request.

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

### Request fields and selector validation

The request schema is the authoritative wire contract. Its required top-level
fields are `schema_version`, `versions`, `required_capabilities`,
`optional_capabilities`, `run_key`, `selection`, `input`, `timeout_ms` and
`allow_paid_execution`; `input` requires captured `shard`, `eval` and `task`
strings. `schema_version`, runner interface version, result schema version and
identity algorithm are separate version axes. Keys (`run_key`, shard and task
IDs) begin with an ASCII letter or digit and have at most 128 characters.
`timeout_ms` is 1–86,400,000. The entire request is limited to 4 MiB.

| Selection field | Required exact match/range |
|---|---|
| `selection.shard` | Must match `input.shard` and the one declared shard |
| `selection.task` | Must match the authored task YAML `id` |
| `selection.trial` | One-based and no greater than the eval's declared `trials_per_task` |
| `selection.attempt` | One-based and no greater than `max_attempts`; omitted or zero `max_attempts` means one attempt |

Trial and attempt values are JSON integers in the schema's positive signed
32-bit range, then checked against the captured eval's effective limits.
Selection is validated before execution. A disabled selected task, shard/task
conflict, excessive ordinal or unsupported source profile is rejected; there is
no "best match", first task, glob expansion, fallback selector or adjacent task.
The source bundle is captured inline and contains no runtime host paths.

Common diagnostics are stable examples of the current validation boundary;
schema-library wording inside a request-schema error can include the failing
field/path. The result always includes a machine-readable diagnostic `code` and
human-readable `message`.

| Condition | Result | Current diagnostic code/message |
|---|---|---|
| Malformed, duplicate-key, oversized, unknown-field or schema-invalid request | `invalid_request`, exit 6, `admission` | `invalid_request`; parser/schema detail in `message` |
| No offered `1.0` version | `unsupported_capability`, exit 5, `negotiation` | `incompatible_runner`: `no supported runner version; offer 1.0` |
| Required capability is unavailable | `unsupported_capability`, exit 5, `negotiation` | `incompatible_runner`: `required capability "<name>" is unavailable; available: ...` |
| Same capability is both required and optional | `unsupported_capability`, exit 5, `negotiation` | `incompatible_runner`: `capability "<name>" cannot be both required and optional` |
| Request shard/task disagrees with captured source or selector | `invalid_request`, exit 6, `selection` | `invalid_selection`: `selection conflicts with declared input shard/task` |
| Trial or attempt is outside the eval declaration | `invalid_request`, exit 6, `selection` | `invalid_selection`: `selection exceeds declared trials_per_task/max_attempts` |
| Selected task is disabled | `invalid_request`, exit 6, `selection` | `invalid_selection`: `selected task is disabled; explicitly enable it before dispatch` |
| Native execution lacks explicit paid consent or required native capability | `invalid_request`, exit 6, `selection` | `invalid_selection`: `native execution requires allow_paid_execution: true and required capability inline.native-text; this may incur model usage` |
| Unsupported local semantics or inputs | `invalid_request`, exit 6, `selection` | `invalid_selection`; profile detail directs the caller to retain `waza run` |

Use the exact task source file and its authored ID when a local workflow has
multiple similarly named tasks. `runner adapt --eval` and `--task` take exact
file paths (not globs); the separate `--task-id` identifies the authored task.
Correct the captured eval/task or keep the historical command if its meaning
depends on discovery, CSV rows, batch execution, retries, hooks, external files
or other behavior that runner 1.0 cannot preserve. Do not resolve ambiguity by
removing unsupported fields or selecting a nearby task.

### CLI and API examples

Create a request without executing it, then execute that request once:

```bash
./waza runner adapt --eval eval.yaml --task tasks/selected.yaml \
  --run-key qualification-42 --shard shard-a --task-id selected \
  --trial 1 --attempt 1 --timeout-ms 60000 > request.json
if ./waza runner run < request.json > selected.runner.json; then
  status=0
else
  status=$?
fi
./waza runner validate-result < selected.runner.json
printf 'runner exit category: %s\n' "$status"
```

The `if` form also works with shell `set -e` and captures a nonzero execution
category without skipping envelope validation. `runner validate-result` is
offline and silent on success. Its exit status validates the envelope; it does
not replace the runner's execution exit category.

```bash
./waza runner capabilities
# {"schema_version":"1.0","versions":["1.0"],"result_versions":["1.0"],"identity_version":"sha256-json-v1","capabilities":["exact-unit","identity.sha256-json-v1","inline.mock-text","inline.native-text","terminal-result"]}

curl --fail-with-body --header 'Content-Type: application/json' \
  --data-binary @request.json http://127.0.0.1:3000/api/v1/runner/run
```

The matching capability API is `GET /api/v1/runner/capabilities`. The `waza
serve`/`waza dashboard` process must already be running. Do not treat HTTP 200
as a pass; read `outcome` and `exit_code`.

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

The public envelope is `kind: "waza.runner-result"` and
`schema_version: "1.0"`; the complete strict schema is
[`runner-result-1.0.schema.json`](../schemas/runner-result-1.0.schema.json).
It includes outcome, exit code, failure boundary, negotiation, selection
validation, `adjacent_work_selected: false`, diagnostics and
`envelope_valid: true`. Depending on how far admission progressed it can also
include requested/resolved selection, versioned identity and the execution
observation. `observation` is the existing run-result diagnostic/evidence
structure, not a historical aggregate result envelope.

The following are terminal-field examples, not standalone complete JSON
documents; use the linked schema for all required envelope fields:

```json
{"outcome":"success","exit_code":0,"failure_boundary":"none"}
{"outcome":"evaluation_failure","exit_code":1,"failure_boundary":"grading"}
{"outcome":"infrastructure_failure","exit_code":2,"failure_boundary":"execution"}
{"outcome":"canceled","exit_code":3,"failure_boundary":"execution"}
{"outcome":"timeout","exit_code":4,"failure_boundary":"execution"}
{"outcome":"unsupported_capability","exit_code":5,"failure_boundary":"negotiation"}
{"outcome":"invalid_request","exit_code":6,"failure_boundary":"selection"}
```

`evaluation_failure` requires an exact selected observation with at least one
failed check and no execution error. `success` requires the same selection
binding, nonempty validation evidence, all checks passing, and no execution
error. A partial, missing, contradictory or ungraded observation is not a
success; it is classified as infrastructure failure. Typical diagnostic codes
are `evaluation_failed`, `execution_error`, `missing_observation`,
`incomplete_observation`, `canceled`, `deadline_exceeded`, `incompatible_runner`,
`invalid_request`, `invalid_selection`, `publication_failed` and
`invalid_envelope`.

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

Exactly-once applies to terminalization within a live process: all recoverable
paths converge on one validated result writer, and the CLI attempts to write
one JSON document to stdout. It is not a distributed exactly-once execution
guarantee. Replaying a request can execute the same logical unit again, and
there is no deduplication, queue lease, durable retry coordinator or recovery
protocol. If stdout is broken, a network client disconnects, the process is
killed before writing, the host disappears, or the machine loses power, no
envelope can be promised. Treat missing terminal evidence as unknown or
infrastructure loss, never as success; reconcile it outside this interface.

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
would lose discovery, retry, grading and output semantics. The #761 local
compatibility mapping and its limits are documented in the
[migration and adapter reference](RUNNER_MIGRATION.md); not every historical
workflow is representable.

## Bounded container reference

The container is a bounded mock-only proof of concept, not a general runtime
image, production deployment recommendation or strong-isolation claim. It adds
no scheduler-specific code, external artifact store or remote service. See the
[bounded quickstart and maintainer policy](RUNNER_CONTAINER.md) for exact host
assumptions, output assertions, troubleshooting and publication steps.

```bash
git lfs pull
bash examples/runner/build.sh
bash examples/runner/smoke.sh --local
bash examples/runner/test.sh
```

The guide and scripts define the host requirements, input fixture, exact
selection, expected `.runner-reference/output/unit.runner.json`, and local vs
registry reference formats. The locally built image ID is not a public image
pin.

No published image digest is currently available. After merge and normal CI and
review, an authorized maintainer must dispatch **Publish bounded runner
reference** on `main` using the full reviewed main SHA. Only a successful
workflow that independently verifies anonymous manifest access and smoke-tests
the image by digest can supply a literal reference. Until then, mark the image
**not yet published** and do not invent, imply or expose a runnable placeholder
digest.

## Dashboard artifacts

The new backend endpoint `GET /api/v1/lab/runner-results` loads only
`*.runner.json` files from the configured results directory. It validates each
original wire document and returns a versioned
[`runner-results-1.1.schema.json`](../schemas/runner-results-1.1.schema.json) page:
`{schema_version:"1.1", results:[{artifact, result}|{artifact, error}], next_cursor}`.
Malformed, oversized, unreadable and nonregular artifacts yield per-entry errors,
never inferred outcomes, without hiding valid siblings. Each item retains the
16 MiB runner-result bound; the complete UTF-8 page is bounded to 17 MiB including
framing. `limit` defaults to 20 (range 1–100); the byte bound can end a page earlier.
Follow the opaque `next_cursor` with `?cursor=...` until it is null. Invalid or
unsupported-version cursors return HTTP 400. Entries use lexicographic artifact
path ordering (bytewise) across the root and `.runner/` child. Continuation is keyset-based,
not a snapshot: concurrent additions before the cursor require a refresh, while
later paths may appear on following pages. The client follows pages without an
aggregate 16 MiB cutoff, rejects non-progressing pagination, and discards a failed
refresh rather than retaining stale success. Historical result
listing remains separate; no synthetic passing `EvaluationOutcome` is created
for rejected or canceled requests.

The HTTP execution endpoint writes its private artifact under
`<results-dir>/.runner/`; CLI users can write stdout as `*.runner.json` directly
in the configured results directory. Private HTTP artifact publication is
supported on Linux/macOS. On other platforms it reports a
`publication_failed` infrastructure result rather than claiming persistence.
The Lab API reads both locations; a caller must save CLI stdout if it wants the
result included in Lab.

### Archive API compatibility and pagination

The Lab archive is versioned independently from the runner result envelope.
`GET /api/v1/lab/runner-results` now returns archive schema `1.1`, while each
embedded `waza.runner-result` remains schema `1.0`. The endpoint does not
negotiate or emit the former unpaginated `1.0` archive response. Consumers
expecting that shape must recognize `schema_version: "1.1"`, read each
`{artifact, result}` or `{artifact, error}` entry, and follow `next_cursor`;
they should continue to reject unknown versions rather than treat them as
empty success. The Lab browser client accepts both archive 1.0 and 1.1 during
the transition; the current server emits 1.1.

The 1.1 endpoint sorts artifact paths lexicographically (bytewise) and uses an
opaque, versioned keyset cursor. `limit` defaults to 20 and accepts 1–100
items. Each stored result is bounded to 16 MiB; the complete UTF-8 response is
bounded to 17 MiB, so the byte limit can end a page before its item limit.
Malformed, oversized, unreadable and non-regular artifacts return a
per-artifact error and do not hide unrelated valid results. Cursors continue
after an artifact path and are not snapshot tokens: refresh from the first
page to see additions that sort before the cursor. Invalid/unsupported
cursors and invalid limits return HTTP 400.

```bash
cursor=
while :; do
  page=$(curl --fail-with-body --silent --show-error --get \
    --data-urlencode 'limit=20' --data-urlencode "cursor=$cursor" \
    http://127.0.0.1:3000/api/v1/lab/runner-results)
  printf '%s\n' "$page"
  cursor=$(printf '%s' "$page" | jq -r '.next_cursor // empty')
  [ -n "$cursor" ] || break
done
```

The synchronous `POST /api/v1/runner/run` reads a bounded body (4 MiB plus
one overflow-detection byte, at most 30 seconds) before admission. A failed,
timed-out or canceled read before a complete request returns an HTTP transport
error, not a runner envelope or artifact. A fully detected size overflow
produces an `invalid_request` admission-rejection envelope without draining
the remaining body. Once admitted, cancellation or client disconnect still
converges on one terminal envelope and attempts private persistence when the
process and storage remain writable; delivery is not guaranteed.

If publication fails, the returned result is a bounded, schema-valid
`infrastructure_failure` with `publication_failed`; evidence and original
diagnostics are preserved as far as the result bound allows. Oversized
observations may be omitted with bounded diagnostics and an encoded size and
digest when available. If even terminal metadata is invalid or too large, a
schema-valid fallback records `evidence_omitted` rather than claiming a
complete observation. A publication-failure response is not evidence that an
archive artifact was saved.

Open **Runner results** in `waza dashboard --lab` (or `waza serve --lab`) at
`/#/lab/runner`. This read-only surface uses the typed client in
`web/src/api/runner.ts` and displays the supplied negotiation, requested/resolved
tuple, pre-execution selection validation, adjacent-work flag, identity
hierarchy/digest/version, terminal outcome, stable exit code, failure boundary,
linked diagnostics and recorded envelope validation. The client also checks the
published result schema and cross-field invariants without repairing data.
HTTP 200 and observation status never determine the displayed outcome.

Use **Refresh runner results** to reload saved artifacts explicitly. Missing
envelopes are unassessed; API errors hide prior success and never substitute
demo fixtures. Unsupported or malformed data is withheld with diagnostics.
The synthetic route `/#/lab/demo/runner` does not request runner data or create
runner fixtures. Runner artifacts remain separate from evaluation history and
its reports; the Lab does not submit work or provide scheduling controls.
**#761 and #762 require no dashboard UI change.**

## Conformance selection

`make test-runner` selects every `Runner`-named unit, CLI/API, historical-source,
wire rejection, frozen identity, subprocess, signal and Lab artifact test.
`make test-compat` additionally selects the runner package alongside the existing
compatibility corpus. Normal `go test ./...` and native-platform CI discover the
new package automatically. No paid/live model run is a substitute for these
deterministic gates.
The existing required Linux build job also executes the local container
build/smoke scripts; Windows still runs the portable Go conformance suite.
