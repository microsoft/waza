# CI/CD Integration Guide

This guide explains how to integrate waza evaluations into your CI/CD pipelines using GitHub Actions, Azure DevOps, and other platforms.

## Overview

Running waza evaluations in CI/CD allows you to:

- **Catch regressions** before merging pull requests
- **Benchmark skills** across multiple models in every PR
- **Track quality** metrics over time
- **Gate releases** on evaluation scores
- **Automate testing** without manual steps

## Waza repository native checks and stacked PRs

Waza's single `go-ci.yml` workflow runs on pull requests targeting any base
branch, including feature branches used by stacked PRs. It retains the
`opened`, `synchronize`, and `reopened` activity types plus `edited`, without PR
branch or path filters. Push triggers cover `main`, `develop`, `dev`, and `insider`.
The former `squad-ci.yml` duplicated the same validation and has been removed.
Superseded PR runs are cancelled within the same workflow/event/ref. Push
validations use independent run-ID groups, so they neither serialize nor replace
pending push runs. Release publication is not cancelled by that PR policy.

An `edited` event that changes the base (`github.event.changes.base`) is a full
validation run in the PR group, so retargeting a PR reruns the gate and cancels
the superseded run. A body- or title-only edit uses its own run-ID group and
cannot cancel validation. Every validation job is skipped on that run, and the
required jobs (`Lint`, `ubuntu-latest`, `windows-latest`) are renamed
`PR metadata (<check> not selected)`. The skipped run therefore never publishes
a context with a required name, so the results from the last push stand.

This is an ordinary `pull_request` workflow with `contents: read`, not
`pull_request_target` or release/dispatch workflows. The existing `Lint`,
`ubuntu-latest`, and `windows-latest` jobs, four native Linux shards, exhaustive
test-selection/coverage verification, and pinned lint-schema checks remain
required; enabling triggers is not evidence that native tests passed.

After integrating a trigger fix into a stack, inspect actual runs on each final
PR head following a qualifying PR event. Existing runs are not retroactively
scheduled. Keep the stack and branch protection intact, and obtain current-head
native Linux/Windows results and independent approvals before merging.

The lint job runs the trigger regression test. Run it locally with
`go test -count=1 -v ./.github/scripts`; it checks default and feature-base
selection, excluded activity types, the union of prior push branches, and mutations of the
actual workflow sources. Shard and lint failure-handling checks remain
`bash .github/scripts/go_test_shard_test.sh` and
`bash .github/scripts/lint_test.sh`.

The four native Linux shards use deterministic longest-first duration balancing.
`.github/scripts/go_test_shard_durations.tsv` records observed top-level
race/atomic durations and their source run. Update it from top-level test results,
not overlapping subtest times. Timing history affects placement only: discovery
still determines the complete inventory, new tests receive the median historical
duration, and removed tests are never resurrected. Python 3 (provided by the
Ubuntu runner) computes the plan. Every shard still retains its 30-minute test
deadline, atomic coverage, full discovery inventory, and selected test names;
the aggregate gate verifies identical inventories and exactly-once selection.
The planner places whole top-level tests, so a single long test bounds every
shard. Keep each native top-level test well under a quarter of the total; split
long table-driven tests into separate top-level tests (as the frozen 15c parity,
observed-failure, and public-payload tests are) rather than relying on subtests.

Squad Release and Squad Insider Release run remaining-package validation in
parallel with these shards. Publication waits for both branches to succeed,
verifies all five coverage profiles and native selection, and downloads the
binary produced by the validated build rather than rebuilding it. Failed,
cancelled, skipped, or missing evidence must prevent publication.

Site change detection is a short prerequisite job. Site browser validation and
offline documentation examples then run independently. Dashboard Chromium tests
use two CI workers at file granularity; serial process-backed suites retain their
ordering, and all tests still run with zero retries in dashboard CI.

The Docker dashboard build also consumes `examples/runner/unit.json`, the
captured request imported by Lab selection validation. Keep its explicit
`.dockerignore` admission, web-builder `COPY`, and Docker workflow path trigger
in sync. `go test -count=1 ./.github/scripts` checks these build inputs; an
actual Docker build is still required to verify container packaging.

### PR #836 bounded container-cache measurement

The existing Docker workflow also has a **parent-operated, opt-in measurement**
for #835/#836. Ordinary `opened`, `synchronize`, `reopened` and push validation
retain their original steps, action pins, permissions and deadlines. The added
`edited` event excludes those two ordinary jobs; a title edit does not launch a
measurement. This is not `workflow_dispatch`, `pull_request_target`, a label
trigger or a default-branch activation change.
The skipped ordinary jobs use distinct metadata-only names on these direct
edited events, so they cannot supersede same-head ordinary check receipts.
All ordinary-event job names remain unchanged. The measurement's separate
serial group never cancels ordinary validation.

After reviewing the published candidate's source and fresh applicable CI, the
landing coordinator must separately authorize one measurement. Only `spboyer`
may append one canonical, standalone authorization comment to #836's **full
existing body**, preserving the repository template and all its evidence:

```text
<!-- waza-cache-measurement:v1 candidate=FULL_HEAD_SHA baseline=eda88ccd7d2a59fefafa7662a95828c9977dbc16 request=NEW_UUID_V4 -->
```

Replace the two placeholders with the exact published 40-character lowercase
head SHA and a new lowercase UUID v4 allocated by the parent for this request.
The baseline is the owner-approved current-main pin, not the historical A69
experiment. The unchanged source-difference allowlist still rejects unrelated
changes. Historical markers are not valid requests under this pin; never rewrite
one during a routine handoff or treat the baseline correction as activation.
Before the body edit, verify identity/access, open/unmerged state, disabled
GitHub auto-merge, expected head/base and live body. Use the guarded REST PR-body
editing tool with the live `base_sha`; a concurrent edit requires a fresh read,
not an overwrite. Do not insert the marker during routine handoff updates.

The sole measurement job requires a real `changes.body.from` string, prefix
absent in the old body and present exactly once in the new body, a canonical
marker, same-repository source, actor `spboyer`, attempt 1, and event/live/head
agreement. Marker-retaining edits, removal, duplicates, stale heads, forks,
other actors and reruns cannot enter the build loop. Read-only GitHub admission
rejects prior non-skipped measurement jobs for the candidate and retained
artifacts for the request nonce. Concurrency is an additional serialisation
guard, **not an exactly-once delivery guarantee**. Deleted/expired artifacts are
not an enduring nonce registry; never reuse a request nonce.
Only this new job adds `actions: read` beside `contents: read` for its read-only
run/artifact inventory checks; ordinary-job permissions remain unchanged.

One genuine LFS checkout supplies two immutable input snapshots: frozen baseline
`eda88ccd7d2a59fefafa7662a95828c9977dbc16` and the actual new candidate, not
historical `cdfd`. Manifests admit every selected difference, Git blob/mode,
compiler/web/dependency input and all 32 real LFS payloads. Unexpected source
differences, sensitive filenames, symlinks and missing/mismatched payloads fail
admission. There are no copied
module caches, alternate registries, TLS relaxations, host networking or
inference calls.

The native AMD64 harness resolves and records the same immutable Node, Go,
Alpine, frontend and BuildKit platform digests for A and B. Six fresh,
invocation-owned BuildKit daemons with `remote` Buildx registrations execute
AB/BA/AB, at most 12 builds.
Each variant retains only its cold-to-warm state; the warm build adds the same
real Go input for that round and must execute the production compile while
reusing both npm/web layers. No GHA/layer cache import/export, extra calibration
build or shared prune is permitted.
The predefined [`BUILDKIT_SYNTAX` frontend selector](https://docs.docker.com/build/buildkit/frontend/)
pins the frontend without altering the canonical Dockerfiles. Build logs must
show that exact frontend resolution and each pinned base-context resolution;
an unrelated digest string is not sufficient.

The explicit-daemon candidate avoids Buildx 0.38.1's managed-driver injection
of `network.host`: it copies an exact deny-entitlement configuration into the
returned container ID, verifies copied and effective bytes, then verifies
native workers before registering `docker-container://<owned-ID>` as a remote
endpoint. It never supplies a denial sentinel, relaxes the entitlement guard,
uses host networking or mounts a host config/socket/root/device. Rootful
`Privileged=true` and `Init=true` are explicit parity with the tagged managed
producer's intent, **not observed baseline equivalence, human-reviewed native
privilege approval or improved security isolation**. Actual privilege, caps,
security options, mounts, network, config, workers and cgroups are captured
before admission and retained on failure.

Every Buildx command, probe and logged client receives an absolute invocation-
private `BUILDX_CONFIG`. Its leaf is acquired by nonrecursive exclusive
`mkdir` requesting `0700` under the admitted private evidence directory;
preexisting leaves and detected parent/leaf identity or permission changes
fail without shared metadata fallback. Fresh registration omits `--node` and
`--append`, avoiding the tagged Create API's existing-node upsert. Docker
configuration, credentials and contexts are not copied or overridden.
These are leaf acquisition and path/dev/ino checks, **not rooted-handle race
protection, exhaustive verification of unread Buildx store internals or
observed native metadata isolation**. Populated private metadata descendants
are retained, never recursively deleted. Unix permission enforcement is not
inferred from authored Windows mode bits.

Each daemon gets a Docker-created anonymous, no-copy state volume, with no
caller-supplied volume name. Docker documents
[anonymous volumes](https://docs.docker.com/engine/storage/volumes/#named-and-anonymous-volumes)
as unique and not automatically reused across containers; the returned
container ID, anonymous mount specification and local volume metadata must
all agree. This creation-coupled attribution is not a named-volume exclusive
creation primitive or a race-free filesystem transaction.

Actual container admission requires two CPUs, 8 GiB and zero swap. The 60-minute
aggregate budget starts **before checkout/setup** and includes runtime capture
and cleanup; each build is limited to six minutes and remaining aggregate
budget minus two minutes for cleanup. The job's 65-minute deadline accommodates
evidence upload, not a longer experiment. Five-second owned-storage/host-free
sampling stops above 18 GiB owned or below 20 GiB host free, or on accounting
failure. GC16GB and a 2 GiB margin are **monitored controls, not a hard20GiB
quota**; unsampled transient overruns remain possible. Shared-host load is
recorded, never assumed quiet.
The active cache-size probe (`du -sk /var/lib/buildkit`) retries up to three
times only when `du` exits 1 and every error line reports a vanished
`/var/lib/buildkit/...` path (`No such file or directory`), because BuildKit may
remove snapshots mid-walk. Each retry is recorded in the builder cgroup
observations; any other error, or a third failure, still stops the run.
Cache usage is refreshed after each completed build and immediately before
pausing its daemon for runtime/binary capture. Over-limit growth stops before
capture; the paused sampler retains that pre-pause cache observation while
resampling context/artifact storage and host free space. This remains sampled
accounting, not an atomic storage quota.
Active and paused accounting both exclude daemon images present in the initial
inventory and charge images absent from that inventory.
Cancellation still permits owned cleanup, but its terminal stop reason is
retained in the final failure list and requires a nonzero exit even when all
twelve timed attempts completed before cancellation.
Command, probe and logged clients share one asynchronously awaited runner;
no synchronous subprocess path remains in admission, startup, sampling,
build/capture or teardown. Captured outputs are bounded to 16 MiB per client;
output-limit, cancellation and process-error events initiate termination,
not a claim of physical close. Active resource probes have bounded deadlines;
a sample still pending at a five-second tick skips that tick and records it in
`samplerOverruns` (up to three consecutive ticks, within the 15-second probe
deadline); a fourth consecutive overrun stops the allocation rather than
silently dropping accounting. Build termination and its five-second
force-kill grace do not wait for a blocking Docker inspection. Build elapsed
time is captured on child completion, before cancelled probes are joined.
Create, config-copy, start, worker readiness and remote registration share one
absolute six-minute-or-remaining-minus-cleanup deadline, never a fresh budget
per stage. Startup is sampled too. Readiness probes are bounded by 15 seconds
and that deadline; only classified connection-not-ready errors can poll, at
most 16 times, including BuildKit's `failed to list workers:` wrapper without
retrying permanent RPC errors. Worker JSON uses the upstream `id` and `platforms`
fields; the retained receipt must contain a valid Linux/AMD64 platform.
Empty or non-native workers, config drift, process errors and
expired/hung probes stop before timing. No restart or replacement builder is
attempted. Probes retain immediate force-kill on failure; command/logged
clients retain TERM then force-KILL after five seconds. Post-KILL physical-close
joins have at most five seconds, shortened by the remaining aggregate budget.
All grace is included in elapsed evidence and consumes the existing cleanup
reserve, not a reset or extension of business deadlines. An unjoined client leaves
locatable residue and blocks destructive teardown rather than extending the
allocation or pretending cleanup succeeded.
Errors with a live client, failures before a PID is observed, physical close
and late close are recorded distinctly. A still-live client remains registered
after finite failure; abandoned capture handles are closed/unreferenced without
claiming termination. Logs/digests disclose incomplete capture when a client
does not join or exceeds its output cap; a late close cannot retroactively make
abandoned bytes complete. Final complete capture requires observed physical
close. Hung sampler joins are also bounded and block destructive cleanup.
The budget starts before checkout. Only successful exclusive result-directory
creation and identity capture export its path and identity to later steps;
collisions export neither and cannot select competing public files for upload.
Exports precede receipt writing so an owned partial setup failure remains
upload-eligible. An initial setup receipt normally exists even if checkout or
host-tool setup fails; receipt-write failure is not complete setup evidence.
This sampled path identity does not provide rooted-handle race protection or
transactional artifact publication.
The outer existing-coreutils watchdog sends TERM at minute58 and KILL at
minute60, reserving two minutes for cooperative owned cleanup and bounding
blocked controller I/O. A forced kill can leave recorded owned residue and
incomplete cleanup evidence; it is not reported as successful teardown.

Portable Node activation, source, ownership and workflow controls remain
selected on Windows. The separate `Windows offline container cache controls`
job directly runs `node --test .github/scripts/docker_cache_benchmark.test.mjs`
on container-applicable opened, synchronize and reopened PR events. It uses
pinned checkout and Node 24 actions, read-only permissions and a ten-minute
deadline, without LFS, npm, Docker or provider execution. Push and edited-only
events receive a distinct skipped-job name instead of overwriting native proof.
The shared trigger filter includes the benchmark implementation, its Node
controls and `.gitattributes`, which affects LFS/source admission. Single-file
PRs for these inputs deliberately select the unchanged ordinary Docker jobs
as well as Windows controls; no separate workflow or job policy is added.
Native Windows executed 35 controls (34 passed, one POSIX skip) for published
`ed4e7ad3`; that proves its trigger/callback source, not this private
explicit-daemon candidate. Candidate Windows execution remains pending
separate publication admission. Green canonical Go CI does not prove this hidden
`.github/scripts` bridge ran on Windows.
Only executable-shebang/POSIX-shell and actual ignored-POSIX-SIGTERM controls report an
explicit Windows skip; native measurement still requires Linux AMD64.
Workflow text oracles accept LF and CRLF, but Git blob, payload and recipe
digests retain original bytes. Canonical Git modes remain recorded on every
host; executable filesystem modes are verified on the admitted POSIX host,
not inferred from Windows writable-file mode bits.

Every failed/timed-out attempt and partial round is retained without retry.
Loaded-image static/version/cache-path checks, actual binaries/buildinfo and
generated web asset bytes are captured without extra export builds. Asset
capture reads completed, invocation-owned BuildKit snapshots; unsupported,
missing or ambiguous snapshots fail rather than inventing asset identity.
Original URL-query-bearing logs remain private; durable public logs are
query-redacted and accompanied by original byte digests. The pinned repository
artifact action uploads partial/final evidence even on failure only when both
the acquired result path and identity are nonempty. Cleanup checks
owned resource identities, records failures/residue and never prunes shared
objects. A creation attempt is not ownership: the returned daemon ID, remote
private metadata-root identity/binding and anonymous cache attribution are independently acquired.
Physically joined clients and samplers precede teardown; matching remote metadata is removed separately
from the exact daemon ID. Metadata failure does not block safe independent
daemon cleanup. Only a still-matching, creation-coupled anonymous mount/volume
permits Docker's container-coupled `rm -v`; uncertain caches are retained while
the verified daemon can be removed without `-v`. Cache absence is checked and
remaining or replaced volumes are never deleted by name. Failed/colliding
creation leaves locatable, unverified possible partials without adopting or
deleting matching competitors. Removal errors are retained, not retried away.

The two admitted source snapshots remain immutable. Each variant/round gets a
separate byte-verified working context so warm Go inputs need no unlink/reset
between rounds. All populated source/working contexts, private Buildx metadata and raw logs are
retained for ephemeral hosted-runner teardown, not recursively deleted by the
harness: a matching directory root does not prove descendant ownership. Their
storage remains sampled throughout the allocation. Upload explicitly selects
public receipts, redacted logs, binaries and generated assets; it excludes
contexts and private raw directories. Retention may exhaust the storage budget
before all 12 builds; no complete filesystem cleanup within the experiment is
claimed. Shared pinned tool/base images are also
retained regardless of initial absence or current container references; those
observations do not establish exclusive ownership. Docker artifact and acquired
builder/container/cache-volume cleanup still verifies recorded identities and
reports failures/residue. Three complete paired rounds are necessary for
medians; no invented
speedup threshold or automatic merge/acceptance decision is provided.

Run the offline controls with
`NO_COLOR=1 node --test .github/scripts/docker_cache_benchmark.test.mjs`.
Existing `.github/scripts` Go CI selects them through
`TestDockerCacheMeasurementOfflineControls`. These tests use synthetic commands
and events, not Docker, npm, Go builds or providers; they do not establish
measurement availability or measured improvement. The original local failed
baseline and separate npm-network diagnosis remain historical, distinct
evidence. The activated `ed4e7ad3` allocation failed before timing because the
managed driver granted `network.host`; it produced zero attempts or pairs.
That source/nonce cannot be retried. The explicit-daemon candidate has authored
offline caller controls only: publication, complete required checks and a new
actual native allocation each require separate admission. #835 acceptance stays
open until the parent reviews genuine results.
## Proposed PR completeness gate

Issue #900 adds an **additive gate, not a replacement for native Go checks**.
`go-ci.yml` now publishes a `completeness / PR completeness` check on ordinary
pull requests. It is **not yet a required context**: do not require
`PR completeness` in a ruleset until the migration below is reviewed and its
actual workflow executions are verified.

### Reusable plan/admission boundary

`pr-completeness-plan.yml` and `pr-completeness-admission.yml` expose the
same-run transport boundary without adding another runnable PR workflow. They
have only `workflow_call` triggers; `go-ci.yml` is their only ordinary-PR caller.

The plan job checks out the tested merge with both parents, invokes the existing
source/path interpreter, uploads only `plan.json`, and exports the attempt-bound
artifact name and small applicability flags. The admission job consumes that
artifact plus the caller's full `toJSON(needs)` dependency inventory. It downloads
individual same-run, same-attempt receipt artifacts without merging their
directories. The adapter rejects wrong filenames, extra files, ambiguous JSON,
artifact-name disagreements and non-regular leaves; the existing interpreter
rejects missing receipts and unsuccessful dependencies/required commands.
These are checks of the downloaded workflow evidence, not a race-resistant
filesystem confinement or independent attestation guarantee.

Only after assembling local evidence does admission read the current PR using
the job's read-only GitHub token. `contents: read` permits checkout; admission
also declares `pull-requests: read` for this fresh snapshot. Other permissions
are none, existing workflow defaults are unchanged, and reusable calls cannot
raise the caller's permission ceiling. Tokens are never written to plans,
dependency inputs, receipts, results or summaries. A missing token or an API
permission denial fails admission rather than falling back to ambient credentials.
Both reusable checkouts explicitly disable credential persistence: subsequent
Git operations only read the local tested merge and its parents.
The production interpreter then independently
revalidates event/run/attempt identity, the tested merge and its full path list.
A completed evaluation writes `result.json` and a family-by-family summary,
including visible `not_applicable` scopes. A rejected evaluation retains its
diagnostics and exits nonzero. API, decoding or source-admission failure exits
nonzero without inventing a passing result; a result artifact may be unavailable.

The concrete caller controls execute the `run` commands extracted from these
production workflows against real local merge commits and downloaded-artifact
layouts, with a rejecting/offline PR API executable fixture. Authored successful
receipts in these controls prove the admission interface, **not** that any hosted
suite, native platform or browser actually ran.

Dashboard (`web-ci.yml`), site/examples (`site-ci.yml`) and Docker
(`docker-ci.yml`) now expose the same optional `pr_completeness_plan` receipt
interface as assurance, tools and runtime. It is inactive unless a caller passes
a plan; with a plan the dashboard bypasses its path filter, and site/examples use
the caller's plan-selected `site`/`examples` inputs instead of path filters.
Receipt capture still re-verifies applicability and fails closed for an
unselected family. Contract step ids reuse the existing workflow ids (`tests`,
`chat-tests`, `ci_tests`, `native_tests`) so evidence and receipts share one
execution. Plan-mode runs use a separate `-completeness` concurrency group so a
caller cannot cancel or be cancelled by a standalone run.

### Go caller wiring

On `pull_request` events `go-ci.yml` runs `pr-scope` (the plan workflow), then:

- Every native Go job (`Lint`, Linux, Windows, the four native shards and the
  coverage `linux-gate`) records its tested checkout and captures a `go` receipt
  for its contract unit in an `always()` final step. Each job regenerates the
  deterministic plan locally, so the required native jobs keep their existing
  needs and run on every validation event; `go-native-shards.yml` captures receipts only
  when its caller passes `pr_completeness: true`.
- The `dashboard`, `documentation`, `assurance`, `container`, `tools` and
  `runtime` jobs call the auxiliary reusable workflows with the plan artifact
  only when the plan selects them; unselected families remain visible as
  `not_applicable`.
- `completeness` calls `pr-completeness-admission.yml` with `always()` and
  `toJSON(needs)` over every job above, so a failed, cancelled or missing
  producer fails the check.

`TestGoCompletenessReceiptWiring` rejects removed step ids, unguarded or
non-`always()` receipts, dropped dependencies and unwired callers.

The dashboard, site/examples, assurance, tools and runtime workflows no longer
have standalone `pull_request` triggers. Pull requests reach them only through
these plan-selected `go-ci.yml` callers, so each suite runs once per PR attempt
and now covers stacked PRs on any base. Their `push`, `workflow_dispatch` and
`workflow_call` entrypoints are unchanged. `docker-ci.yml` keeps its standalone
path-filtered trigger for the separately admitted edited-body benchmark, so
container validation can still run twice on Docker-relevant PRs.

Receipt capture stays fail-closed. If GitHub builds the merge commit after
`main` advanced, the payload `base.sha` is stale and source binding rejects it
("Merge parents do not match event base and head"). Each `Capture PR
completeness receipt` step is `continue-on-error: true`, so that race does not
fail `Lint`, `ubuntu-latest`, `windows-latest` or a native shard. The upload
step only runs on `steps.receipt.outcome == 'success'`, so admission still fails
`PR completeness` on the missing receipt and the PR must be updated. Test
failures inside the job still fail it; `TestGoCompletenessReceiptWiring` rejects
removing `continue-on-error` from a receipt.

The proposed ordinary-PR entrypoint is the existing Go workflow, with local
reusable calls to the existing auxiliary validators. This avoids cross-run
`needs` assumptions and duplicate ordinary-PR suites. Non-PR triggers remain
separate. The initial applicability matrix is subject to production-input and
full-diff review:

| Validation | Changed inputs |
|---|---|
| Go, lint, native Linux shards and Windows | Every ordinary PR, preserving the existing native gates. |
| Dashboard Chromium | Dashboard files, backend `cmd/`, `internal/`, schemas, root Go inputs, module versions, runner examples, and `.github/scripts/ci-evidence*` collectors. |
| Site and chat Chromium | Site/docs, documented metadata, schemas, model/embedded-runtime inputs and frozen-reader fixtures; `Makefile`, web Playwright config, `package.json`, lockfile and screenshot specs read by documentation checks; registered serve flags in `cmd/waza/cmd_serve.go`; exact checked web/compatibility/interop and linked repository files; and `examples/` directory-existence dependencies. |
| Offline documentation examples | Site/docs/examples, backend/schema/module inputs, dashboard inputs, `Makefile`, and `examples/runner/unit.json` consumed by the production-CLI release example. |
| Offline Grader Assurance | Backend/schema/module inputs, assurance viewer and reader scripts, immutable `testdata/frozen-assurance-readers` custody inputs and their `.gitattributes` checkout-byte rules, grader docs and examples. |
| Docker image and native runner contracts | Container/context files, `.gitattributes` LFS checkout rules for embedded assets, backend/schema/module/root Go inputs, dashboard, runner examples and admitted native probe fixtures. |
| Squad tools on Linux and Windows | Squad state, skills/agent definitions, PR requirements, casting `.gitattributes`, and Ralph/Squad tooling scripts. |
| Pinned judge runtime on Linux and macOS | Embedded runtime, execution/graders/judge evidence/models, their repository package dependency closure (including filesystem admission, snapshot readers, schema loading, events, config, skills/tokens and compiled test helpers), root schemas, modules and `.gitattributes`. |

Workflow and completeness-interpreter changes select the affected gate families.
The initial production documentation prebuild input trace observed 210 distinct
repository paths, not 210 content reads. Besides the three content inputs
`Makefile`, `web/playwright.config.ts` and `web/package-lock.json`, it found
existence/stat checks for `web/e2e/runner-negotiation.spec.ts`, linked
`.squad/model-policy.md`, `DEMO-SCRIPT.md`, `LICENSE`, `meet-the-squad.md`,
`internal/interop/testdata/agentv/PROVENANCE.md` and the runner compatibility
eval/task fixtures. These exact file inputs select site validation.
Eleven additional linked files and three directory targets are covered by the
existing containment-aware `examples/` root match. The root deliberately
over-selects descendant edits: the documentation checks consume directory
existence, not every descendant's content. Actual Git addition, deletion and
rename controls cover file leaves, including removal of a consumed directory's
last tracked leaf; a Git directory path is never fabricated.

That historical trace predates the serve-flag documentation guard. Its new
content input, `cmd/waza/cmd_serve.go`, selects site validation through an exact
leaf match; the guard's CLI reference and dashboard guide inputs are already
covered by `site/`. The incoming workflow and Go site-filter inventories retain
the same exact serve-source leaf. This integration traces the added declaration
and reader inputs, not a new complete prebuild-input census.

Instruction-only `AGENTS.md` edits retain the existing exemption from expensive
site/chat auxiliary suites. Documentation link checks also observe its
existence/type, so deletion or type replacement remains a known coverage gap
under this exemption. No status-aware wire extension or separate cheap job is
implemented here; this matrix does not claim unconditional coverage of all
210 observed inputs or a future base's input closure.
Explicit `not_applicable` is different from absent, failed or skipped evidence.

GoLint selection controls resolve declarative `GOFLAGS` with workflow, job and
step precedence, retaining original YAML node types. The effective value must
be omitted or a literal empty string; a higher-precedence literal empty value
may override a lower unsafe value. Nonempty flags and unprovable expressions
cannot establish execution of the full controls. Explicit
`continue-on-error: false` is valid; true or dynamic values are not.

The same ordinary execution helper resolves typed shell declarations from
workflow `defaults.run.shell`, job defaults and the selected step, in that order.
Omission preserves the runner's implicit Bash; an explicit higher-precedence
literal can override an unsafe lower value, while explicit empty/null values do
not fall back. Supported forms are `bash`, `bash -e {0}`,
`bash -e -o pipefail {0}` and
`bash --noprofile --norc -e -o pipefail {0}`. Other/custom shells, expressions,
non-string values and aliases cannot establish this Bash-only execution contract.
Shared ordinary offline checks apply the same resolution to their required
steps; argv-recorder and failure controls execute the resolved runner-shaped
script invocation rather than assuming a hard-coded shell.
This policy is scoped to the existing Bash-script callers, not generic Windows
PowerShell/CMD workflows. Linux omission keeps implicit Bash; these Windows
callers need their existing explicit Bash defaults rather than treating the
runner's implicit PowerShell as Bash. The intentionally Linux-only inventory
step is not shell-validated as Windows execution, and unrelated optional steps
do not acquire a new shell restriction.

The existing required Windows job also selects the two hidden shell-control
families explicitly, since ordinary `go test ./...` omits `.github/scripts`.
The Windows-only step runs `TestCompletenessGoLintShellExecution` and
`TestOrdinaryShellOverridesAndDisabledPaths` uncached and verbose with a
five-minute Go deadline, after Node and the rejecting runtime are ready.
Its Bash pipeline propagates command failures, normalizes only terminal CRLF,
and requires both named top-level RUN/PASS records with no skipped controls.
The subsequent runtime-marker check covers this step too. The Linux job skips
this Windows-only step while its existing GoLint package selection remains
unchanged. Static selection regressions are not native Windows execution:
that evidence requires the current-head Windows job to actually run and pass
the selected families.

The ordinary source record binds the immutable event's PR head, base SHA/ref and
tested merge SHA, plus the actual caller workflow, workflow run and attempt.
Receipt capture reconstructs those fields from the runner event/environment and
compares them with the plan; it does not use the plan itself as proof of caller
identity. Both callable selection and receipt capture independently compare
the plan's complete path inventory with the actual tested merge. Changed paths come from the
complete NUL-delimited Git diff against that base, including old and new names
for renames. Discovery requires both merge parents; malformed or unavailable
source cannot become an empty path list. Every participating checkout needs
both merge parents, including receipt-producing jobs. A feature/stack base is valid and must
not be silently replaced with `main`.

Transfer the full plan as a same-run, attempt-qualified artifact. Only its
artifact name and small applicability flags belong in job outputs; the callable
`pr_completeness_plan` string identifies that artifact, not an embedded JSON
manifest. This avoids passing a potentially large changed-path inventory through
job-output or environment limits. Consumers must download from the current run
and validate the artifact's source and complete paths before selection.

Each applicable job must provide its initial checkout commit, final checked-out
SHA and successful command outcomes/conclusions. Matrix members are enumerated
individually, not collapsed into last-writer-wins outputs. Browser receipts
require actual nonempty Chromium results with zero retries: both the selected
project's configured retry limit and each result's recorded retry index must be
zero. Missing retry evidence is unavailable. These fields follow the repository's
[locked Playwright 1.64.1 JSON reporter](https://github.com/microsoft/playwright/blob/v1.64.1/packages/playwright/src/reporters/json.ts).
The one intentional
ordinary-dashboard skip is the actual-producer assurance case; that exact case
must pass in Offline Grader Assurance with its real exported reports. Receipt
capture retains required outcomes and case results, not unrelated action outputs
or browser logs. This is workflow-execution evidence, not independent attestation
or a performance/model-quality claim.

Site's single compound `chat-tests` step runs private-chat controls, chat Chromium and
unavailable-service Chromium in an `&&` chain. Its outcome covers that entire
chain, not independently observed outcomes for each subcommand. The two browser
reports remain required, but the receipt must not invent a separate
`chat_unavailable` or private-chat step outcome.

Final admission additionally requires a fresh PR API snapshot matching the
tested PR number, open state, repository, head SHA and base SHA/ref. Missing
API evidence or a head/base change during validation makes every scope
unavailable; independent command failures are still reported. The workflow
must fetch and check that snapshot immediately before evaluation. This is a
point-in-time check, not a guarantee that the PR cannot change afterward;
merge-time verification remains required.

The final `evaluate` CLI independently repeats the actual caller and tested
merge admission used by selection and receipt capture. A complete passing input
from another event, run, attempt or merge cannot supply ordinary completeness
success, and an omitted changed path is rejected. The exported pure `evaluate`
API remains offline; executing the CLI requires the runner event and checkout.

Body/title-only PR edits must use **distinct check names and concurrency** and
must not publish ordinary completeness success or cancel active validation.
Base-retarget edits require new head/base/tested-merge validation. Pushes and
manual cold/performance observations must not create a skipped or successful
ordinary `PR completeness` context. Docker's separately admitted edited-body
benchmark is not a completeness prerequisite or a substitute for normal image
and native-runner proof.

The retarget classifier follows the [pinned Octokit edited-event contract](https://github.com/octokit/webhooks/blob/aa80c24a3f55354465b8b1438d33dd5602956d0d/payload-schemas/api.github.com/pull_request/edited.schema.json):
both old base `ref.from` and `sha.from` are required. The current base ref must
differ; different branches at the same SHA remain valid. An arbitrary nonempty
change object is not retarget evidence, and unknown shapes fail closed. The
local event fixtures remain synthetic controls: schema verification does not
replace genuine retarget-event execution before adoption.

### Administrator migration plan

1. Review the final source and applicability matrix, including the admitted
   Docker measurement interface and CI capture changes. Preserve `Lint`,
   `ubuntu-latest`, `windows-latest`, `license/cla`, native five-profile coverage
   verification, source identity and current independent-review requirements.
2. Verify genuine current-head ordinary PR runs for production, documentation,
   unrelated-prose and feature-base cases. Inspect actual jobs, command outcomes,
   all matrix members and selected browser cases, not just the summary color.
   Verify base-retarget events refresh proof and body-only edits cannot replace
   it. Manual observation runs are not this admission cohort.
3. Exercise failures, missing dependencies/receipts, path omissions, failed
   detection, nested skips, retries and source/attempt/base mismatches through
   the production caller and actual workflow-source mutation controls. Confirm
   each prevents ordinary completeness success.
4. Re-read active branch rules and obtain explicit administrator authorization
   for an additive `PR completeness` requirement from the verified GitHub Actions
   source. Do not rename/remove legacy requirements or self-approve the change.
   This implementation does not write repository protection settings.
5. After authorized adoption, recheck current heads and stacked-base behavior.
   Missing or pending checks block admission; do not treat a fallback, bypass or
   manually dispatched cohort as passing ordinary validation.

Bounded local controls use `node --test .github/scripts/pr-completeness.test.cjs`
with the repository's Node 22.18+ or 24+ runtime. Trigger-parser and offline
runtime import-closure controls use
`go test -count=1 ./.github/scripts -run '^Test(WorkflowTriggerTypedInputs|CompletenessRuntimeImportClosure)$'`.
The closure control parses the selected Linux AMD64 and macOS ARM64 source
imports with the workflow's race tag, including compiled execution test helpers
and excluding inactive build tags; it does not compile or execute either
platform's runtime probe.
`TestCompletenessProductionCLIControls` runs all 16 Node controls through the
Go test package already selected unconditionally by Linux GoLint. It requires
the full passing TAP inventory with zero failures, cancellations or skips;
missing Node is a failure. `TestCompletenessControlsSelectedByGoLint` rejects
omitted-package, echo-only, skipped-step and narrowed-test workflow mutations.
The Git fixture retains rename, deletion, stack and helper-omission coverage on
Windows; only its literal newline filename uses a portable alternative there.
No native Windows execution is claimed from local Unix controls.
These checks do not by themselves prove GitHub routing, native platforms, browser
execution or ruleset adoption.
## Prerequisites

- waza binary in your PATH (install with the script below)
- A skill repository with an `eval.yaml` file
- Credentials for LLM APIs (Copilot SDK, OpenAI, etc.) — usually stored as GitHub Actions secrets

## Installation in CI

The retained reusable workflows pin standalone Waza v0.41.0 and download
the release-owned installer and validator from that tag. Publish v0.41.0
before using them from another repository. Checks in the Waza repository build
the exact checkout with real dashboard assets instead of installing a mutable
release. The copied skills workflow keeps `eval/eval.yaml` for consumers; its
repository check uses `examples/code-explainer/eval.yaml` with the mock executor
and a verified rejecting Copilot runtime.

Workflow results must contain unique task IDs, valid trial records and the full
configured trial inventory for passed tasks. Summary counts and success rate
must agree with task statuses. Workflow success also requires every recorded
final and checkpoint grader to pass, no recorded grader evaluation errors, and
no failed or stopped checkpoint; task/run status alone is not sufficient.
Failed/skipped/error or contradictory artifacts remain available
for diagnostics, but cannot become a successful workflow output. An omitted
context override leaves fixture resolution to the eval spec; explicit context
and output paths are forwarded unchanged.

The automatic/reusable `waza-eval.yml` result artifact contains only the selected
JSON file, including explicitly chosen hidden names or directories. It copies
the exact regular file into a fresh staging directory, preserving its basename;
custom paths are not interpreted as upload patterns. Missing files, symlink
results, or staging failures fail the workflow instead of uploading stale paths.
Optional non-hidden transcripts are uploaded separately under `<artifact-name>-transcripts`;
an empty or hidden-only transcript selection is ignored.
transcripts cannot substitute for missing result evidence. The skills example's
dispatch input is empty by default so its resolver selects the correct repository
or consumer fixture path.

### GitHub Actions

```yaml
- name: Install waza
  run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

### Azure DevOps Pipelines

```yaml
- script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
  displayName: Install waza
```

## GitHub Actions Workflow

### Basic Evaluation Workflow

Create `.github/workflows/waza-eval.yml`:

```yaml
name: Run Skill Evaluations

on:
  pull_request:
    paths:
      - 'skills/**'
      - 'evals/**'
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Check results
        run: |
          SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
          echo "Success rate: $SUCCESS_RATE"
          if ! jq -e '.summary.success_rate >= 0.8' results.json >/dev/null; then
            echo "❌ Success rate below threshold"
            exit 1
          fi

      - name: Upload results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: eval-results
          path: results.json
          if-no-files-found: error
```

### Multi-Model Comparison

Compare performance across models in CI:

```yaml
name: Multi-Model Evaluation

on:
  pull_request:
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        model:
          - gpt-4o
          - gpt-4-turbo
          - claude-sonnet-4-20250514
    steps:
      - uses: actions/checkout@v4

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation with ${{ matrix.model }}
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --model ${{ matrix.model }} \
          --output results-${{ matrix.model }}.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Upload results
        uses: actions/upload-artifact@v4
        with:
          name: eval-results-${{ matrix.model }}
          path: results-${{ matrix.model }}.json
          if-no-files-found: error
```

### Baseline Comparison

Track performance against a baseline:

```yaml
name: Baseline Comparison

on:
  pull_request:
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation (current)
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results-current.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Checkout main
        run: git fetch origin main:main

      - name: Run evaluation (main)
        run: |
          git stash
          git checkout main
          waza run evals/my-skill/eval.yaml \
            --context-dir evals/my-skill/fixtures \
            --output results-baseline.json
          git checkout -
          git stash pop
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Compare results
        run: waza compare results-baseline.json results-current.json
```

## Azure DevOps Pipelines

### Basic Evaluation Pipeline

Create `azure-pipelines.yml`:

```yaml
trigger:
  - main
  - develop

pr:
  paths:
    include:
      - skills/**
      - evals/**

pool:
  vmImage: 'ubuntu-latest'

steps:
  - script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
    displayName: Install waza

  - script: |
      waza run evals/my-skill/eval.yaml \
        --context-dir evals/my-skill/fixtures \
        --output results.json \
        --reporter junit:test-results.xml
    displayName: Run evaluation
    env:
      COPILOT_SDK_TOKEN: $(COPILOT_SDK_TOKEN)

  - script: |
      set -euo pipefail
      SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
      echo "Success rate: $SUCCESS_RATE"
      if ! jq -e '.summary.success_rate >= 0.8' results.json >/dev/null; then
        echo "##vso[task.logissue type=error]Success rate below threshold"
        exit 1
      fi
    displayName: Check results

  - task: PublishTestResults@2
    inputs:
      testResultsFormat: 'JUnit'
      testResultsFiles: 'test-results.xml'
    displayName: Publish test results
    condition: always()

  - task: PublishBuildArtifacts@1
    inputs:
      PathtoPublish: results.json
      ArtifactName: eval-results
    displayName: Upload results
    condition: always()
```

### Multi-Model Strategy

```yaml
strategy:
  matrix:
    gpt4o:
      MODEL: gpt-4o
    claude:
      MODEL: claude-sonnet-4-20250514
    gpt4turbo:
      MODEL: gpt-4-turbo

pool:
  vmImage: 'ubuntu-latest'

steps:
  - script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
    displayName: Install waza

  - script: |
      waza run evals/my-skill/eval.yaml \
        --context-dir evals/my-skill/fixtures \
        --model $(MODEL) \
        --output results-$(MODEL).json
    displayName: Run evaluation with $(MODEL)
    env:
      COPILOT_SDK_TOKEN: $(COPILOT_SDK_TOKEN)
```

## GitHub Actions: Secrets Management

Store API credentials as GitHub secrets:

1. Go to **Settings → Secrets and variables → Actions**
2. Add `COPILOT_SDK_TOKEN` with your token value
3. Reference in workflows as `${{ secrets.COPILOT_SDK_TOKEN }}`

To create a Copilot SDK token, authenticate with the CLI:

```bash
copilot auth login
```

## Azure DevOps: Secrets Management

1. Go to **Pipelines → Library → Secure files**
2. Upload `.env` or store in **Variables**
3. Add variable `COPILOT_SDK_TOKEN` (check "Keep this value secret")
4. Reference as `$(COPILOT_SDK_TOKEN)` in YAML

## Best Practices

### 1. Cache Results for Faster CI

```yaml
- uses: actions/cache@v4
  with:
    path: |
      .waza-cache
      ~/.cache/waza
    key: waza-${{ hashFiles('evals/**') }}
    restore-keys: |
      waza-

- name: Run evaluation
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --cache \
    --output results.json
```

### 2. Fail on Score Thresholds

Use JSON queries to enforce quality gates:

```bash
#!/bin/bash
set -e

SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
MIN_SUCCESS_RATE=0.85

if ! jq -e --argjson minimum "$MIN_SUCCESS_RATE" \
  '.summary.success_rate >= $minimum' results.json >/dev/null; then
  echo "❌ Success rate $SUCCESS_RATE is below minimum $MIN_SUCCESS_RATE"
  exit 1
fi

echo "✅ Success rate $SUCCESS_RATE meets threshold"
```

### 3. Run Tasks in Parallel for Speed

```yaml
- name: Run evaluation (parallel)
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --parallel \
    --output results.json
```

### 4. Filter Tasks by Pattern

Only run specific tasks to save time:

```yaml
- name: Run critical tasks only
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --task "critical-*" \
    --output results.json
```

### 5. Enable Session Logging for Debugging

```yaml
- name: Run evaluation with logging
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --session-dir ./logs \
    --session-log \
    --output results.json

- name: Upload logs
  if: failure()
  uses: actions/upload-artifact@v4
  with:
    name: session-logs
    path: logs/
    if-no-files-found: error
```

### 6. Post Results as PR Comment

```yaml
- name: Comment on PR
  if: always()
  uses: actions/github-script@v7
  with:
    script: |
      const fs = require('fs');
      const results = JSON.parse(fs.readFileSync('results.json', 'utf8'));
      const comment = `
## Evaluation Results
- **Success Rate:** ${(results.summary.success_rate * 100).toFixed(1)}%
- **Tasks Succeeded:** ${results.summary.succeeded}/${results.summary.total_tests}
- **Aggregate Score:** ${results.summary.aggregate_score.toFixed(3)}
- **Duration:** ${results.summary.duration_ms}ms
      `;
      github.rest.issues.createComment({
        issue_number: context.issue.number,
        owner: context.repo.owner,
        repo: context.repo.repo,
        body: comment
      });
```

### 7. Separate Fast and Slow Tests

```yaml
jobs:
  smoke-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - name: Run smoke tests
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --task "smoke-*" \
          --output results.json

  full-tests:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' || github.event.pull_request.draft == false
    steps:
      - uses: actions/checkout@v4
      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - name: Run full test suite
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results.json
```

## Interpreting Results

### Exit Codes

- **`0`** — Evaluation passed (all tasks met thresholds)
- **`1`** — Evaluation failed its grading or quality threshold
- **`2`** — Configuration, startup, or operational task/run error. A result file
  may still be saved; inspect its task and run statuses for diagnostics.

### Result JSON Structure

```json
{
  "schemaVersion": "1.9",
  "eval_id": "01JEXAMPLE",
  "skill": "my-skill",
  "eval_name": "my-skill-eval",
  "timestamp": "2026-11-06T10:30:00Z",
  "config": {
    "runs_per_test": 1,
    "model_id": "claude-sonnet-4-20250514",
    "engine_type": "copilot-sdk",
    "timeout_sec": 300
  },
  "summary": {
    "total_tests": 1,
    "succeeded": 1,
    "failed": 0,
    "errors": 0,
    "skipped": 0,
    "success_rate": 1,
    "aggregate_score": 0,
    "weighted_score": 0,
    "min_score": 0,
    "max_score": 0,
    "std_dev": 0,
    "duration_ms": 5000
  },
  "metrics": {},
  "tasks": [
    {
      "test_id": "task-001",
      "display_name": "Example Task",
      "status": "passed",
      "runs": [
        {
          "run_number": 1,
          "attempts": 1,
          "status": "passed",
          "duration_ms": 5000,
          "validations": {},
          "final_output": "Example explanation."
        }
      ]
    }
  ]
}
```

### Threshold Configuration

Set pass thresholds in `eval.yaml`:

```yaml
config:
  model: claude-sonnet-4-20250514
  pass_threshold: 0.8
  metrics:
    - name: task_completion
      threshold: 0.85
    - name: trigger_accuracy
      threshold: 0.90
```

## Troubleshooting

### Issue: "COPILOT_SDK_TOKEN not set"

**Solution:** Add the secret to your GitHub Actions workflow:
```yaml
env:
  COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}
```

### Issue: Tests timeout in CI

**Solution:** Increase timeout or use `--timeout` flag:
```yaml
- name: Run evaluation
  timeout-minutes: 10
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --output results.json
```

### Issue: Fixtures not found

**Solution:** Ensure `--context-dir` is relative to repository root:
```bash
waza run evals/my-skill/eval.yaml \
  --context-dir evals/my-skill/fixtures
```

### Issue: Out of tokens

**Solution:** Filter tasks or use caching:
```yaml
- name: Run evaluation (filtered)
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --task "priority-*" \
    --cache \
    --output results.json
```

## Advanced Workflows

### Approval-Based Releases

```yaml
- name: Check score before release
  run: |
    SCORE=$(jq -er '.summary.success_rate | numbers' results.json)
    if ! jq -e '.summary.success_rate >= 0.95' results.json >/dev/null; then
      echo "❌ Score $SCORE requires approval"
      exit 1
    fi
```

### Trend Tracking

Store results in artifacts and compare over time:

```yaml
- name: Download previous results
  continue-on-error: true
  run: |
    gh run download -n eval-results --dir ./prev-results
  env:
    GH_TOKEN: ${{ github.token }}

- name: Compare trends
  run: |
    if [ -f ./prev-results/results.json ]; then
      waza compare ./prev-results/results.json results.json
    fi
```

## Next Steps

- **Explore the examples:** Check `examples/` in the waza repository for complete working workflows
- **Configure secrets:** Set up Copilot SDK tokens in your CI system
- **Define quality gates:** Establish pass/fail thresholds for your skills
- **Monitor trends:** Use stored results to track skill performance over time
