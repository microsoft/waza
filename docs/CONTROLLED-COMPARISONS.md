# Controlled comparison contract

The explicit workflow provides source-bound planning, fresh paired offline
collection, strict artifact reassessment, structural schemas and a separate
dashboard decision view. Affirmative collection currently supports **mock,
no-skills execution with native text/string checks only**. General live
engine/grader observability is not supported. An explicitly selected independent
offline assurance profile qualifies finite authored text cases and regrades
retained actual responses; it does not change the base 1.0 protocol.
These contracts do not make historical outcomes strict evidence or mock
observations agent-quality evidence.

### Internal native-task groundwork (not an enabled producer)

`internal/nativetask` contains engine-free draft input freezing/admission,
strict supplied-record validation and ordered local payload/terminal persistence.
It has no CLI/profile registration, collector wiring, native-hook import or
engine execution. Freezing a caller-supplied resolved request and rooted source
bytes does not establish native source resolution, current policy allocation,
authenticity or runtime readiness. Synthetic fixtures test these primitives;
they are not live-agent or reviewed-provider evidence.

Canonical SDK text is usable only when **every** contributing assistant message
has nonempty typed content and their complete concatenation matches the retained
response. Any empty/defaulted message makes output unavailable, even with other
nonempty messages. The SDK discards absent/null/empty wire distinctions before
public callbacks; actual-empty support needs an upstream presence contract.
The planned request must carry explicit nonnil deny-all policy, whose native
SDK filter, permission guard and pre-tool guard are client restrictions, not
server isolation.

The legacy `RunResult.SessionDigest` is a value struct. Neutral counts and empty
inventories in a draft row are **structural defaults, not observed session or
absent-tool evidence**. The draft reader's full-tool/session/runtime-version/
currency/assurance capabilities remain `not_assessed`; row usage is absent and
only separate finalized qualified accounting may contribute totals.
Payload fsync precedes the draft terminal fsync. Missing/orphan/torn records
remain incomplete/invalid without repair; a parsed local prefix does not prove
fsync acknowledgment or publish a final ledger. Existing public protocols and
API/dashboard required-assurance nonpass behavior remain unchanged.

The separate native dependency provides an explicitly installed diagnostic
observer, owned-client factory and independent received-model/final-usage
observations. These hooks are not connected to controlled collection. Observer
callbacks must not reenter engine lifecycle methods; delivery is synchronous,
not runtime reentrancy protection. The native SDK deny-all filter, permission
guard and pre-tool guard have offline create/resume controls, not server
isolation or permission to execute live tasks. Exact prompt injection is
separately opted in by a grader context and does not broaden assurance readers.

`internal/nativesnapshot` separately resolves actual two-arm source files with
engine-free `Prepare` and independent `Recheck`, and returns detached CLIENT
requests for inspection. It reuses native declaration parsing and request
construction, capturing raw eval/task/prompt/fixture/instruction bytes, present
lockfiles, task discovery order and the caller-selected evaluator executable.
An empty CLI context uses the corresponding eval directory's `fixtures`;
explicit CLI and task contexts keep their native cwd-relative semantics.
Borrowed root handles and original cwd associations cannot be retargeted during
recheck. Reads are bounded, rooted and cancellation-aware; aliases, unsupported
inputs and missing consumed resources reject rather than silently disappearing.
The shared platform opener preserves assurance-reader behavior. Unix file and
directory opens use nonblocking flags followed by caller-owned descriptor
checks; Windows directory discovery is explicitly unsupported and fails closed.
Cross-compilation does not establish Windows runtime behavior.

This resolver supports only independent, no-skills native-text declarations
with a fixed named model. Cross-root inputs, authored floating-point identities,
parallel/dependency/hook/multiturn modes and other graders remain unsupported.
Its private full CLIENT request projection is **not** the narrower
`nativetask` request-intent digest or a public integration wire. Source recapture
does not authenticate a hostile filesystem, the loaded executable, server
isolation or exhaustive runtime instructions. No current-review authorization,
allocation/budget admission, paired lifecycle collection or native execution is
enabled; unsupported required runtime/routing/output-presence/currency evidence
still cannot pass through existing protocols.

The engine-free `CheckPrimitiveJoin` compares an existing internal admission to
the resolver's retained request and complete selected-arm source inventory.
It reuses the primitive's exact request projector and JSON-v1 identity, while
separately retaining both arms' full serialized CLIENT/source identities.
Its opaque `Joined.Binding` is inspection-only: matching a provided attempt key
does not prove policy allocation, source currentness, execution authorization or
any unavailable capability. Actual origins retain the existing full-key rule.
The primitive's **1 MiB raw and encoded preparation limits** apply to the
selected arm; its larger complete inventory cannot join, and sources are never
omitted to fit. The other arm remains bound under unchanged resolver limits,
not admitted as a primitive. Joining one supplied key does not establish paired
collection eligibility: each future selected slot needs its own independent
join/admission. No filesystem reads, preparation, admission or execution occur
in the join itself. Callers must independently recheck sources for currentness.

`internal/controlledprojection` computes complete task/suite identity domains,
settings, arm digests and golden inventories from already prepared typed inputs.
The mock planner reuses its legacy projection without new limits or numeric
normalization. The separate bounded `NativePlan` supports only offline native-text,
no-skills, deny-all declarations; its dependency label describes declared client
configuration, not server isolation. Runtime implementation and model version
remain explicitly unavailable. Independent tests compare the original mock
planner and actual captured native request construction against every identity
domain. These intermediate projections do not reconstruct retained source
custody, allocate policy slots, certify retry state or enable a native collector.
No public profile, command, schema or decision handling changes.

## Offline planning and collection

```bash
waza compare-plan \
  --baseline-eval examples/controlled-comparison/eval.yaml \
  --candidate-eval examples/controlled-comparison/eval.yaml \
  --design examples/controlled-comparison/design.json \
  --requirements examples/controlled-comparison/requirements.json \
  --output policy.json
waza compare-collect \
  --baseline-eval examples/controlled-comparison/eval.yaml \
  --candidate-eval examples/controlled-comparison/eval.yaml \
  --release-policy policy.json --collection-dir new-collection
waza gate --release-policy policy.json --collection-dir new-collection --format json
```

The example intentionally exits `1` on assessment: its single deterministic
cluster does not establish independence or precision. It exercises the complete
offline protocol, not a statistically supported release claim. The new policy
file must not exist; the collection directory must not exist. Replanning writes
a different random allocation, never overwrites/adopts a commitment.

Design and requirements JSON use the respective policy 1.0 member shapes,
including explicit `assurance`, `runtime`, billing and identity requiredness.
The design's allocation must be empty; planning generates its seed and arm order
once. Planned trial counts must equal each eval's effective `trials_per_task`.
`--allow-change model` (or `engine,reasoning_effort`) expands only actual changed
settings to exact per-task before/after entries. Unlisted changes reject.
Only the currently supported mock engine is admitted, regardless of allowance.

`--baseline-context-dir` and `--candidate-context-dir` have the existing cwd
semantics, defaulting to the corresponding eval directory's `fixtures`.
Task-level context roots retain their existing cwd-relative resolution.
`inputs.context.fixture` retains its existing **eval-directory-relative**
semantics. Only actual copied input resources are bound; changing an unused file
does not fabricate drift. CSV/templates, external dependencies, discovered
skills/agents, hooks, parallelism, checkpoint/multiturn and non-text graders
are explicitly unsupported by this producer, not removed from ordinary runs.

Both sources are reinspected before BEGIN and every attempt. Execution consumes
the private frozen input bytes, never a second mutable fixture read. Each
durable start precedes a fresh initialized mock engine and workspace, shut down
before terminal publication. Source inventories preserve actual resource and
instruction order/multiplicity. Logical paths and separate source-byte digests
are wrapped by JSON-v1 identities; resolved prompts, scoped grader configuration
and remaining effective settings are bound separately. Rubric absence follows
native-grader admission; present `waza.lock` bytes are bound even when unused.
Local executable bytes bind implementation identity; `mock_no_provider` is an
explicit no-provider harness observation, **not an observed requested model
version or authenticity/loaded-image attestation**.

## Schemas, API and dashboard

`schemas/release-*-1.0.schema.json` describe independently tagged policy, plan,
receipt, journal, binding, actual-result and decision artifacts. Regenerate
their structural definitions with
`go run internal/releasepolicy/schema_generate.go`. Structural schema validation
does not replace strict numeric-token identities, conditional availability,
source binding or ordered/statistical validation by the Go reader.

`waza serve --results-dir <local-root>` exposes `/api/release-collections` and
the **Release policies** dashboard tab. Decisions are recomputed from artifacts,
not trusted from a decision label. Sidecars never become legacy result rows.
The independently tagged `waza.release-decision-view` 1.0 API transport uses
usage strings to preserve exact token integers in JavaScript; it is not a
stored numeric-token `waza.release-decision` artifact.
The dashboard rejects malformed required dimensions/accounting, numeric usage
transport and a pass flag paired with any nonpass dimension. It renders the
server's independently recomputed assessment; it does not certify stored view
payloads or recompute statistical policy from this transport.
unavailable values remain absent, not zero. Invalid/partial publications remain
visible with distinct reasons. This endpoint reads only the configured local
root; remote storage remains an ordinary-results feature.

### Independent offline assurance profile

The base policy, journal and decision 1.0 semantics are frozen. A base policy
with `requirements.assurance: true` still produces a **nonpassing base decision**,
including through `/api/release-collections` and the dashboard. Only explicit
CLI assessment with an independent assurance contract can produce a passing
**outer** `waza.release-assured-decision` 1.0. Metadata, a stored report's pass
label or a dashboard payload cannot upgrade the base decision.

The supported subset is **mock / no_skills / native text / finite authored
outputs**. Both arms need fresh, exact offline1.0 grader assurance reports,
covering every actual native text endpoint through unambiguous requirement/check
mappings, plus deterministic regrading of retained actual execution responses.
Every base **non-assurance** pass condition still applies: compatibility,
completeness, observed operations, golden first-attempt veto, any required
available billing totals, and the predeclared statistical decision. Finite
supplied-corpus qualification is not full behavioral correctness, live-agent
reliability, statistical calibration or authenticated human review.

First create a base policy using `compare-plan`, with assurance explicitly
required in its requirements input. Then use the *same executable* and current
per-arm sources throughout planning, collection and assessment:

```bash
# Paths below are your authored inputs, not bundled ready-to-pass fixtures.
waza compare-assurance-plan \
  --release-policy policy.json \
  --baseline-eval baseline/eval.yaml --candidate-eval candidate/eval.yaml \
  --baseline-references baseline/references.json \
  --candidate-references candidate/references.json \
  --output assurance-contract.json
waza compare-collect \
  --release-policy policy.json --assurance-contract assurance-contract.json \
  --baseline-eval baseline/eval.yaml --candidate-eval candidate/eval.yaml \
  --baseline-references baseline/references.json \
  --candidate-references candidate/references.json \
  --collection-dir new-assured-collection
waza gate \
  --release-policy policy.json --assurance-contract assurance-contract.json \
  --collection-dir new-assured-collection \
  --baseline-eval baseline/eval.yaml --candidate-eval candidate/eval.yaml \
  --baseline-references baseline/references.json \
  --candidate-references candidate/references.json --format json
```

`compare` accepts the same assessment inputs instead of historical positional
results. `--baseline-context-dir` and `--candidate-context-dir` retain the
existing context resolution. `compare-assurance-plan --output` requires a
**new**, exclusive file; collection requires a **new** directory. Collection
and assessment both require explicit `--assurance-contract` and
`--release-policy`; assessment additionally requires `--collection-dir` and
current per-arm eval/reference inputs. Missing or empty required flags fail,
and selecting an empty contract never falls back to base or legacy assessment.
Old executables reject the new command/unknown flags; defaults remain unchanged.

Reference-label documents and their authored-output envelopes follow the
existing [grader challenge contracts](GRADER-CHALLENGES.md#strict-file-content-assurance),
not a new label API. References stay evaluator-only, outside agent execution
inputs. Bind labels to the exact eval/config/task/native grader declarations
and executable. Each arm can separately supply `--baseline-review` /
`--candidate-review` and explicitly accept its current source with
`--baseline-accept-review-source` / `--candidate-accept-review-source`.
Add these inputs consistently at all three stages when selecting review.
Supplying a review file alone is **not acceptance**; accepting a source without
a supplied review is invalid. Revoked, unreviewed, missing or unaccepted current
review cannot pass. Current-source acceptance is a caller declaration, not
authentication of a person; withheld revocations cannot be discovered.
Examples are unreviewed by default; test-only reviewed declarations are
explicitly synthetic and are not real review evidence.

Verification rechecks current source bytes, evaluator evidence and review
inputs after verification, before final publication, and again before outer
assessment returns. Changed inputs invalidate the commitment; stored reports
are never adopted as fresh qualification. No calibration plan, paid1.1 report,
live SDK, provider billing support or automatic paid validation is admitted.

| Artifact | Independent kind / version | Purpose |
|---|---|---|
| `assurance-contract.json` | `waza.release-assurance-contract` / `1.0` | Nonce and exact per-arm source/declaration bindings before BEGIN |
| Each `assurance.ndjson` row | `waza.release-assurance-attempt` / `1.0` | Contiguous attempt key, origin, result-row identity and actual response availability |
| Each `assurance-rows.ndjson` payload | Existing full `ActualRunRow` JSON | Evaluator-owned payload synced before its assurance record and core terminal; positional JSON-v1 identity must match the sealed row and final raw results |
| `assurance-ledger.json` | `waza.release-assurance-ledger` / `1.0` | Final rows, core journal identity, exact row tape and raw result identities |
| Outer JSON assessment | `waza.release-assured-decision` / `1.0` | Nonpassing base decision, fresh reports, separate assurance/regrade dimensions and limitations |

Fixed release artifacts use bounded (16 MiB), rooted regular-file reads. Additive
assessment supports cancellation; oversized evidence is invalid, not silently
truncated. Full payload whitespace may be JSON-v1-equivalent, but missing,
duplicate, reordered, torn or extra records never pass.

The contract, final ledger and result-row digests use **JSON-v1**; contract/
ledger self-digests omit the `digest` member entirely. Policy/plan/config/task/
grader declaration and core-journal identities are JSON-v1 too. Eval source,
executable, labels, supplied review, authored-input documents, final row tape
and raw result documents use **exact `source-bytes`**, including whitespace and
newlines. Formatting-only source changes therefore invalidate source-byte
bindings. Collection identity is the independent contract's digest, not the
policy digest. Hashes and a local nonce establish consistency, **not authenticity,
global uniqueness or globally witnessed precommitment**.

Available output is `{"availability":"available","value":""}` when the actual
response is explicitly empty. Unavailable output has a nonblank `reason` and
**no `value` member**; it cannot be manufactured from an empty/default
`final_output`. Deterministic regrading must match preserved native grader
verdicts/scores and actual result-row lineage; no successful attempt is selected
to replace an earlier failure.

The four dedicated `release-assurance-{contract,attempt,ledger}-1.0.schema.json`
and `release-assured-decision-1.0.schema.json` schemas do not broaden the frozen
base artifact schema. Regenerate only this profile with
`go run schemas/assurance_generate.go`. Schema validation enforces structural
tags, unknown-member rejection, digest domains and output availability; raw
readers additionally reject duplicate keys, verify identities/order/freshness
and enforce the actual assessment. A structurally passing label is not proof.

Use `--format json` for the outer machine-readable contract, or human output
(`gate` default, `compare` default table selection renders human text).
Do not assume selected assurance output reproduces legacy markdown/table/
GitHub Actions layouts. Selected nonpass exits `1`; a passing outer assessment
does not turn its embedded base decision into a pass.

## Explicit selection and compatibility

```bash
waza compare --release-policy policy.json --collection-dir new-collection --format json
waza gate --release-policy policy.json --collection-dir new-collection --format json
```

These assess an already completed new protocol collection. Both flags are
required, including when an explicitly supplied flag value is empty. The
selected policy's exact identity must match the durable precollection copy.
Do not combine this mode with historical positional comparisons or legacy gate
baseline/current/threshold/golden/task-set flags.

Selected rejection, invalid, missing, operational or inconclusive evidence exits
`1`; output keeps its separate reasons. Default comparison remains descriptive.
Default gate thresholds, golden union, task-set policies and `0/1/2/3` exits are
unchanged. Old executables reject these unknown flags with their ordinary
unknown-flag error; policy metadata alone never enables enforcement.

## Fixed design before collection

The estimand is the expected candidate-minus-baseline selected pass endpoint for
the **declared fixed suite under its repeated-execution mechanism**, not a claim
about arbitrary future tasks. Select one endpoint, first-attempt pass or
predeclared retry-policy pass. Both reliabilities and recovered trial counts are
reported. A trial's retries are dependent; tasks and repeated trials inside a
cluster are unrestrictedly dependent and do not add independent samples.

Clusters must be independent bounded differences in `[-1,1]`, with the mechanism,
justification and limitations recorded before collection. Fresh workspaces,
fresh sessions and precommitted ordering alone do not establish independence:
shared service/time/state can invalidate it. Unjustified independence is
descriptive/inconclusive, never a strict pass.

For cluster coefficients `w`, family error probability `alpha`, and externally
declared conservative family bound `M`, the two-sided radius is:

```text
h = sqrt(2 * log(2*M/alpha) * sum(w*w))
```

Only one selected candidate-minus-baseline contrast is implemented here.
Larger `M` is an external conservative declaration, not a verified exhaustive
family inventory. For equal independent clusters, `.05`, `M=1`, and `h<=.1`
require at least 738 clusters mathematically. This is a **precision bound, not
power, cost or an execution-count promise**. Validate the actual allocation
before collection; arithmetic boundaries may need a preplanned reserve.

Weights use canonical Go JSON float64 numeric tokens. Admission requires
positive coefficients and unit mass within only the representation-rounding
budget. Then the entire declared set is normalized exactly as rational numbers;
the estimate and squared-mass radius use those same coefficients. Routine
uniform allocations of 3, 12 and 738 clusters and ordinary relative weights
work. Hidden extra precision, omitted cluster mass and omitted task mass are
rejected, not repaired. Never drop missing samples or change their denominator.

No adaptive stopping, outcome-selected seeds, post-hoc endpoints/weights,
chosen successful attempts, historical adoption, resume or extension is
supported. The seed and recomputable pair order are sealed before collection;
this is not independently witnessed randomization or a causal guarantee.

## Independent versioned identities

Policy, resolved-plan, receipt, journal, result-binding and decision artifacts
use independent kind/version tags. Strict readers reject duplicate properties,
unknown nested properties, trailing input, unsupported versions and mismatched
identities. JSON-v1 uses the imported normative Go JSON projection with original
numeric tokens, sorted keys and Go escaping. A self-identity member is removed
entirely, not replaced with a null or empty value.

Plan settings and expected-runtime constraints are recorded **per task**.
Remaining effective settings are bound by their explicit projection digest.
Only exact declared per-task engine/model/reasoning changes are allowed;
no whole-domain ignore can hide other timeout/retry/judge/dependency drift.
Actual runtime observations belong to receipts/attempts, not an engine-free
plan. Requested model labels are not observed model version identities.
Changed model permits only its model-version dimension; changed engine permits
only its implementation dimension. Unrelated runtime drift still rejects.

Task-scoped identities cover task definition, executed prompt, fixture source
inventory, instruction source inventory, effective scoped grader configuration
and dependency mode. Suite-scoped identities cover actual grader implementation
bytes/version inventory, rubric inventory and lock inventory. Implementation
names alone cannot manufacture implementation identity. Rubric/lock
`not_applicable` means a **verified empty set** with its canonical projection
digest; it is not missing applicable evidence. Required unknown identities
remain nonpass.

Source identity, stored-content digest and aggregate manifest identity are
different domains. Never compare them interchangeably. Existing evidence
availability, unknown completeness and redaction remain descriptive;
captured content is not necessarily complete, unredacted or certified.
Full allocated eval/task/run/attempt origin must match actual captured lineage.
`run_number` equals planned `trial_ordinal`. No binding operation may rewrite
old origin into compliance; snapshot `prior_attempts` metadata is not a tape.

## Durable local collection and accounting

The protocol creates a **new** private directory and exclusive artifact files.
It syncs file contents and containing directory entries before starting engines.
Both arm BEGIN receipts and the initial event stream exist durably first.
Every allocated attempt start is synced before request construction; one
terminal follows actual execution/grading. Actual order is cluster, assigned
arm, task, trial, then contiguous retry ordinal. Pass or operational/unknown
outcome terminates the retry policy.

The exact event vocabulary is `begin_arm`, `cluster_start`, `attempt_start`,
`attempt_terminal`, `trial_terminal`, `end_arm`, `collection_end`. Every event
has a contiguous sequence and shared collection/policy identity; extra fields,
missing transitions, duplicate terminals and unplanned work reject. A complete
journal binds the exact durable NDJSON **source bytes** separately from its
JSON-v1 aggregate identity. Readers recheck the full stream, both BEGINs,
final receipts, repeated summaries, independently published result-binding
projection and actual result rows. Actual run/attempt and grader verdict/score
must agree, not merely have separately valid hashes.

CLI and API readers open fixed artifacts beneath the collection root and reject
symlinks and non-regular files. The selected policy digest is checked against the
same admitted commitment used for assessment, without an independent reread.

Interrupted publication exposes validated durable-prefix planned/started trial
and started/completed attempt counts. Without final independently published raw
result cross-checks, endpoint rates remain unavailable and decisions nonpass.
Prefix accounting never repairs, resumes or seals an interrupted collection.

The new private/durable protocol supports Linux and macOS; unsupported platforms
fail closed for this protocol without changing legacy writers. File/directory
sync failures abort. Process-exit persistence is checked; machine-crash
durability still depends on the filesystem/storage honoring the primitives.
Local hashes and receipts are consistency checks, **not authenticity, live
external-state verification or independently witnessed precommitment**.

## Separate decision dimensions

| Dimension | Important nonpass states |
|---|---|
| Compatibility | invalid, mismatched, inconclusive required identity |
| Completeness | missing, partial |
| Assurance | required but not assessed; historical absence is never assured |
| Golden | observed first-attempt failure versus missing required evidence |
| Billing | unavailable final attributable total versus observed budget exceeded |
| Statistics | unjustified independence, insufficient declared precision, regression, inconclusive |
| Operations | setup/grader/cancellation/unsupported/unknown instrumentation |

Every required golden task must pass its **first attempt in both arms for all
planned trials**, regardless of selected endpoint. Retry recovery cannot erase
a golden failure, including when a later retry becomes operationally invalid.
Golden completeness and operational errors stay outside statistical averaging.

Arm aggregation preserves the strongest dimension state and every diagnostic:
invalid/mismatched comparison cannot become inconclusive, missing publication
cannot become merely partial, known golden failure cannot become missing, and
observed budget exceedance cannot become unavailable. Malformed billing remains
invalid while retaining other arms' unavailable/exceeded reasons.

`Assess` is a verified-receipt calculation, not a raw-admission entry point.
Public callers must use `ReadDecision`/`ReadSelectedDecision` or perform equivalent
raw admission plus durable stream, BEGIN, receipt/binding and actual-row checks
before calling it. A sidecar or transport pass boolean is never adopted.

Operational rejection cannot be inferred from score or prose. Program graders
can return nil-error process launch/timeout rejection; such paths are not
certified behavioral evidence. Affirmative attempt observability currently
supports only actual mock execution with ambient skills disabled and native
builtin text/string checks, without collisions, hooks, cache, concurrency,
repositories or multiturn dependencies. Ordinary evaluations retain every
existing feature. Mock observations test the harness, **not agent quality**.

Tokens, AI credits and provider currency remain independent axes. Token values
are exact nonnegative integer JSON tokens. Unavailable axes have no value, not
zero. Only final, complete, attributable available totals satisfy required
budgets; partial zero does not. Final observed spend may exceed a declared
budget because asynchronous overshoot is not prevented by an atomic spend cap.

Base 1.0 policies requiring assurance remain nonpassing. Only the explicitly
selected independent offline profile described above can satisfy the outer
assurance dimension, with fresh qualification and actual-response regrading.
Static requirement reference verification and preflight inventory completeness
never supply that verdict. Other selected policies do not acquire a universal
assurance dependency.
