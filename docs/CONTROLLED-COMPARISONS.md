# Controlled comparison contract

The explicit workflow provides source-bound planning, fresh paired offline
collection, strict artifact reassessment, structural schemas and a separate
dashboard decision view. Affirmative collection currently supports **mock,
no-skills execution with native text/string checks only**. General live
engine/grader observability and assurance verdict integration are not supported.
These contracts do not make historical outcomes strict evidence or mock
observations agent-quality evidence.

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

Policies requiring assurance cannot pass until independently attributable
assurance verdicts are integrated. Static requirement reference verification
and preflight inventory completeness never supply that verdict. Other selected
policies do not acquire a universal assurance dependency.
