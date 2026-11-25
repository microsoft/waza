# Agent Instructions for waza Repository

## Overview

This repository contains `waza`, a CLI for testing and evaluating agent workflows,
custom agents, and skills with repeatable scenarios, graders, evidence, and CI
integration. The CLI and backend are implemented in Go at the repository root;
`web/` contains the React dashboard and `site/` contains the documentation site.
Runner contracts, artifact publication, and execution telemetry are part of the
current product surface.

When making changes, follow these guidelines to maintain consistency and quality.

## Squad Team

See [Meet the Squad](meet-the-squad.md) for current owners, the pinned Squad
tooling, and upgrade/verification procedures. `.squad/team.md` and
`.squad/routing.md` define the active team; `.squad/governance.md` defines bounded
collaboration and independent review. Persisted GPT-first role assignments live
in `.squad/config.json`, with code-review and fallback rules in
`.squad/model-policy.md`. Historical model directives are superseded.

## Project Tracking

**Keep issues and tracking up to date:**
- **Active feature tracker:** [#657 - Trustworthy regression testing](https://github.com/microsoft/waza/issues/657)
- **Delivery tracker:** [#866 - Roadmap delivery tracker](https://github.com/microsoft/waza/issues/866)
- **Historical platform roadmap:** [#8](https://github.com/microsoft/waza/issues/8) (closed)
- **PRD:** [docs/PRD.md](docs/PRD.md)
- When completing work, update the relevant GitHub issue
- Reference issue numbers in commit messages (e.g., `feat: Add tokens command #47`)

## Code Structure (Go)

```
cmd/waza/                  # CLI entrypoint
├── main.go                # Process entrypoint and exit handling
├── root.go                # Root command and subcommand registration
└── cmd_*.go               # Command constructors and handlers
internal/
├── config/                # Configuration with functional options
├── execution/             # AgentEngine interface and implementations
│   ├── engine.go          # Core engine interface
│   ├── mock.go            # Mock engine for testing
│   └── copilot.go         # Copilot SDK integration
├── graders/               # Grader interface and Create() factory
│   ├── grader.go          # Grader interface + Create() switch over grader kinds
│   ├── text_grader.go     # Text grader (one file per grader kind)
│   └── file_grader.go     # File grader
├── models/                # Data structures
│   ├── spec.go            # EvalSpec (eval configuration)
│   ├── testcase.go        # TestCase (task definition)
│   ├── grader_params.go   # models.*GraderParameters types
│   └── outcome.go         # EvaluationOutcome (results)
├── orchestration/         # EvalRunner for coordinating execution
│   └── runner.go          # Eval orchestration
└── scoring/               # Skill-adherence heuristic scoring
    └── scoring.go         # Scorer / HeuristicScorer
go.mod
go.sum
Makefile                   # Build and test commands
.golangci.yml
```

## Go Naming Conventions

The Go implementation uses idiomatic Go naming:

| Concept | Go Name | Defined in |
|---------|---------|------------|
| Eval configuration | `EvalSpec` | `internal/models/spec.go` |
| Executor | `AgentEngine` | `internal/execution/engine.go` |
| Grader | `Grader` | `internal/graders/grader.go` |
| Task | `TestCase` | `internal/models/testcase.go` |
| Result | `EvaluationOutcome` | `internal/models/outcome.go` |

## Key Go Patterns

### Functional Options for Configuration
```go
cfg := config.NewEvalConfig(spec,
    config.WithSpecDir("evals"),
    config.WithVerbose(true),
)
```

### Builder for Engine Construction
```go
// options may be nil in production; tests pass a NewCopilotClient factory
engine := execution.NewCopilotEngineBuilder(modelID, nil).Build()
```

### Interface-based Design
```go
type AgentEngine interface {
    Initialize(ctx context.Context) error
    Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error)
    Shutdown(ctx context.Context) error
    SessionUsage(sessionID string) *models.UsageStats
}
```

### Grader Factory
```go
// Create() switches over the models.*GraderParameters type to build a Grader
g, err := graders.Create(identifier, params)
```

## Review-derived Engineering Standards

These standards come from the last 150 merged PRs reviewed on 2026-10-15
(merged 2026-07-27 through 2026-10-15): 106 dependency PRs and 44 other PRs,
including 310 review threads and the review bodies and conversation comments.
The links below identify representative feedback, not a claim that every
suggestion was accepted or every merged PR was defect-free. Check the final
disposition and current implementation before reusing a proposed fix.

### Preserve wire contracts and field presence

- Treat Go loaders, eval/task JSON Schemas, preflight, result writers, API DTOs,
  TypeScript guards, exports, and documentation as separate consumers. Update
  every affected consumer, including task/checkpoint graders and regrading.
- Distinguish omitted, null, empty, explicit zero/false, and populated values
  wherever the contract does. Preserve that distinction through copies and
  read/regrade/write cycles; do not repair invalid types into valid defaults.
  For strict identity-bearing profiles, validate original wire types, duplicate
  keys/aliases, Unicode, and numeric precision before lossy decoding.
- Keep legacy reader defaults separate from new writer versions. Eval, result,
  snapshot, and sidecar versions are independent; task YAML has no independent
  version negotiation. Do not apply a new strict profile to unrelated historical
  payloads or tighten an intentionally tolerant loader without a reviewed
  compatibility decision.

Sources: [nested grader/schema parity](https://github.com/microsoft/waza/pull/573#pullrequestreview-5237174677),
[presence lost during regrade](https://github.com/microsoft/waza/pull/652#discussion_r4266695291),
[legacy admission regression](https://github.com/microsoft/waza/pull/666#discussion_r4236249110).

### Resolve effective configuration once, then use it consistently

- Reuse shared resolution and validation instead of adding command-specific
  copies. Run, grade, preflight, trigger, migration, and API discovery must agree
  on the supported filenames, spec-relative paths, task overrides, filtered
  skills, and original CSV row identities. Honor configured file/glob settings.
- Resolve agent selection and policy from the same effective paths as execution.
  Preserve explicit disable/deny-all behavior, source-qualified tool namespaces,
  and per-run policy across initial/resumed/follow-up turns; do not leak mutable
  policy between tasks or baseline passes.
- When adding behavior-affecting configuration, audit `internal/cache/cache.go`.
  Hash the relevant effective configuration/content, including interacting flags,
  or explicitly bypass incompatible cached results. Test both cache-key changes
  and actual request/session propagation.

Sources: [run/grade CSV divergence](https://github.com/microsoft/waza/pull/671#discussion_r4252290593),
[effective skill selection](https://github.com/microsoft/waza/pull/670#discussion_r4262603609),
[cache invalidation](https://github.com/microsoft/waza/pull/627#discussion_r4122534791),
[policy namespaces](https://github.com/microsoft/waza/pull/596#discussion_r4096082837).

### Report only observed, complete evidence

- Missing scores, credits, provenance, or observations are unavailable, not zero
  or success. Preserve explicit recorded zero. Keep mock, authored, cached, and
  live evidence distinguishable; a digest or configured mock does not prove
  execution, reconstruction, dependency isolation, or real-world assurance.
- Use authoritative final SDK usage, not token-price estimates. Count each
  session once, distinguish cumulative snapshots from deltas, include discarded
  retries/judges/responders, and exclude cached historical billing from the new
  evaluation. Incomplete inputs must not become authoritative partial totals;
  offline regrading must preserve execution billing.
- Bind evidence to its actual eval/task/run/attempt, including cache-source
  lineage, before presenting it or invoking graders. A syntactically valid
  reference is not verified content. Withhold invalid claims explicitly and
  retain metadata-only/unverified labels when content cannot be checked.
- Preserve numeric precision in serialized results and CSV; presentation rounding
  must not change stored values or statistical decisions. Refresh related
  summaries/details consistently so one view cannot mix old and new evidence.
  On reload failure, clear obsolete success data or explicitly label it stale.

Sources: [incomplete credit totals](https://github.com/microsoft/waza/pull/632#discussion_r4146599574),
[cumulative usage](https://github.com/microsoft/waza/pull/632#discussion_r4186242485),
[precision in CSV](https://github.com/microsoft/waza/pull/632#discussion_r4146766118),
[evidence binding](https://github.com/microsoft/waza/pull/672#discussion_r4273301346).

### Keep offline validation pure and failures explicit

- Offline/static paths must not invoke runtime constructors that read ambient
  project configuration, fetch external schema references, launch processes, or
  call models. Reuse the offline parsers, `graders.ValidateConfig`, and
  `internal/schemaloader` rather than the default JSON Schema loader.
- Do not mutate caller-owned nested configuration while compiling/defaulting.
  Test offline paths with file/network/process sentinels and unchanged-input
  controls, rather than assuming an offline flag proves isolation.
- Inspect independent declarations even when one prerequisite is unresolved.
  Retain invalid and unresolved diagnostics; an external-reference denial must
  not mask another invalid configuration or turn it into a passing preflight.
  Validate only evidence consumed by the selected grader.
- Propagate errors with context and preserve cause classification. A permission
  denial, mock-finalization failure, cancellation, or publication error must
  agree with the final success flag, status, exit policy, and saved artifact;
  an error string attached to an otherwise successful result is insufficient.

Sources: [hidden constructor I/O](https://github.com/microsoft/waza/pull/666#discussion_r4234415065),
[validator input mutation](https://github.com/microsoft/waza/pull/666#discussion_r4234334808),
[invalid-error precedence](https://github.com/microsoft/waza/pull/671#discussion_r4263654272),
[failure status](https://github.com/microsoft/waza/pull/635#discussion_r4167750297).

### Make filesystem and lifecycle guarantees match the implementation

- Use `filepath.Rel`/appropriate locality checks, not string prefixes, and apply
  each field's existing native-platform path contract. For confined reads/writes,
  retain rooted directory/file handles through the operation; checking a path
  and reopening it later does not prevent symlink or replacement races.
- No-overwrite creation requires exclusive leaf creation and appropriate parent
  admission. Cleanup may remove only verified invocation-owned entries, never
  competing replacements. Test write/close/unlink failures and report locatable
  sensitive residue rather than claiming successful cleanup.
- State the exact supported guarantee: exclusive creation is not a whole-scaffold
  transaction; `os.Rename` is not a universal atomic/durable publication contract;
  file identity or size alone does not prove stable content. Document partial
  output and unsupported platforms when stronger guarantees are out of scope.
- Keep cancellation, active-request bookkeeping, resource release, and final
  usage capture ordered and bounded. Use deterministic barriers/fault injection
  to test races and late failures, not sleeps alone.

Sources: [checked-path reopening](https://github.com/microsoft/waza/pull/668#discussion_r4235735682),
[parent-path escape](https://github.com/microsoft/waza/pull/670#discussion_r4250512652),
[unsafe cleanup ownership](https://github.com/microsoft/waza/pull/666#discussion_r4284754024),
[late cancellation](https://github.com/microsoft/waza/pull/667#discussion_r4272285506).

### Review dependency upgrades as integration changes

- Check upstream tagged contracts/advisories, peer and engine requirements,
  coordinated module families, and CI/container/deploy runtime versions. Migrate
  affected callers in the same change; do not hide incompatibility with casts,
  weakened tests, or an unrelated dependency downgrade.
- Keep manifests and lockfiles synchronized using the package manager. Preserve
  the existing version-range strategy unless changing it is deliberate. SDK
  updates also require checking the embedded CLI version, generated platform
  bundles/companions/licenses, and non-billing startup/protocol coverage.
- For releases, verify version files, extension/registry metadata, changelog,
  release notes, and comparison links all describe the same actual release.

Sources: [major API break](https://github.com/microsoft/waza/pull/684#discussion_r4252093562),
[runtime and peer requirements](https://github.com/microsoft/waza/pull/603#discussion_r4052816070),
[embedded SDK/runtime mismatch](https://github.com/microsoft/waza/pull/609#discussion_r4052815169),
[coordinated modules](https://github.com/microsoft/waza/pull/566#discussion_r3886209518),
[stale release metadata](https://github.com/microsoft/waza/pull/527#discussion_r3787431986).

### Preserve measured performance characteristics

- Optimize the production lifecycle at representative scale. Retain focused Go
  benchmarks with `-benchmem`; include construction, refresh, or serialization
  when production repeats that work. For dashboard changes, measure the relevant
  production bundle, DOM, or browser path. Compare against current `main` with
  interleaved runs rather than relying on a reusable-object microbenchmark alone.
- Scope caches and compiled state to the smallest real lifecycle: a grading pass,
  immutable assurance case, dashboard snapshot/refresh, or one staging call.
  Bound entries by admitted input/configuration, cache only validated successes,
  and replace refresh state transactionally. Use content identity when file
  metadata is insufficient; do not expose cached pointers/slices mutably or leak
  state between tasks, runs, or workspaces.
- Make expensive work lazy only through an explicit path whose callers do not
  consume the omitted result, such as metadata-only skill parsing, per-schema
  validation, or non-home route loading. Preserve general API behavior, wire
  field presence, ordering, errors, ownership checks, and accessibility. For
  large histories or captures, reduce repeated copies, per-item serialization,
  and DOM growth without dropping source data.

Sources: [production-lifecycle regex benchmark](https://github.com/microsoft/waza/pull/849),
[transactional dashboard projections](https://github.com/microsoft/waza/pull/858),
[explicit metadata-only parsing](https://github.com/microsoft/waza/pull/843),
[bounded trend-chart DOM](https://github.com/microsoft/waza/pull/856),
[owned capture buffers](https://github.com/microsoft/waza/pull/833).

## Building and Testing

> **Requires Go 1.26.4 or later**, matching `go.mod`. Go 1.26 language features and standard library APIs are safe to use. If you want to rely on features or stdlib additions introduced after Go 1.26, first bump the `go` version in `go.mod` to that minimum version.

The ordinary `cmd/waza` tests and `go test ./...` also require **Node.js 22.18+
or 24+** on `PATH`. The interoperability conformance test executes the retained
upstream TypeScript assertion oracle using Node's type stripping; it needs no
npm packages or inference. Missing Node is a test failure, not a skipped oracle.

```bash
# From the repository root, using Bash (Git Bash on Windows)
make build
```

`make build`, `make test`, `make install`, and `bash build.sh` install the locked
dashboard dependencies on a clean checkout. Later local runs reuse
`web/node_modules` until `web/package.json` or `web/package-lock.json` changes.
Use `make web-deps-force` to recover from a stale or corrupt dependency tree.
CI and release workflows continue to run `npm ci` explicitly on clean checkouts.

Direct `go build`, `go vet`, and `go test` commands do not build embedded dashboard
assets: run `make build-web` first when those assets are missing or stale. Use
genuine dashboard builds for asset tests and delivery, not compilation stubs.

### Offline Go checks

Before Go tests, reuse the same rejecting Copilot runtime guard as CI. This
subshell disables live-test opt-ins without changing the parent shell, proves
the guard can launch, and fails if a test attempts to use the runtime:

```bash
(
  set -euo pipefail
  offline_dir="$(mktemp -d)"
  trap 'rm -rf -- "$offline_dir"' EXIT
  runner_os=""
  if [ "$(go env GOHOSTOS)" = windows ]; then
    runner_os=Windows
  fi
  offline_env="$(RUNNER_TEMP="$offline_dir" RUNNER_OS="$runner_os" bash .github/scripts/go_offline_env.sh)"
  while IFS= read -r setting; do
    export "$setting"
  done <<< "$offline_env"

  make test
  if [ -e "$COPILOT_CLI_PATH.invoked" ]; then
    echo "Offline tests attempted to launch the Copilot runtime." >&2
    exit 1
  fi
)
```

`make test` builds the dashboard before running Go tests. For focused tests or
other Go checks from the matrix below, prepare assets with `make build-web` and
replace `make test` inside this guarded block with the applicable commands.
The compatibility corpus also requires Git and a working `python3` or `python`.
Intentional live evaluations are separate, explicitly authorized work, not an
offline validation step.

### Testing Requirements

**Every PR must leave tests in a passing state.** This is non-negotiable:

- **All existing tests must pass** — run the full Go suite using the [offline setup](#offline-go-checks) before pushing code. If your change breaks an existing test, fix it. Instruction-only prose changes use the documentation-only check below.
- **New features require new tests** — every new command, flag, grader, or internal function needs test coverage. No shipping untested code.
- **Bug fixes require regression tests** — if you fix a bug, add a test that would have caught it.
- **Playwright e2e tests** — if you change the dashboard (`web/`), run the dashboard checks in the matrix below and fix any failures.
- **CI is the gate** — required current-head checks must pass. Read the active repository rulesets rather than relying on copied job names.

### Regression evidence and required checks

Test the public path that carries the changed behavior, not only its helper:
CLI input to saved JSON, orchestration to captured SDK session configuration,
or persisted artifacts through the actual API to the UI. Handwritten browser
fixtures must also have producer/API contract coverage; do not use a mocked
response that production cannot emit. Assert the actual downloaded CSV/JSON,
row-scoped values, rendered navigation, and relevant failure status.

Use known-good, deliberately bad, and alternative-valid controls. Assert setup
actually changed the intended input and that the test inspected at least one
row/event. Cover absent/null/empty/zero, nested scopes, repeated calls,
initial/resumed sessions, configured paths, and LF/CRLF where relevant. Exact
output checks need anchored whole-value matchers and near-miss negatives.
Compatibility requires an independent frozen oracle, not two outputs from the
same changed implementation; preserve historical fixtures and normalize only
documented volatile fields.

Sources: [helper-only coverage](https://github.com/microsoft/waza/pull/627#discussion_r4122534702),
[impossible API fixture](https://github.com/microsoft/waza/pull/652#pullrequestreview-5512674815),
[independent compatibility oracle](https://github.com/microsoft/waza/pull/669#discussion_r4234045201),
[vacuous assertion](https://github.com/microsoft/waza/pull/394#discussion_r3496954092),
[LF/CRLF setup failure](https://github.com/microsoft/waza/pull/669#issuecomment-6088610941).

Format only changed Go files with `gofmt -w` before running checks. Run focused
tests while iterating, then the applicable completion gates:

| Change | Required checks |
|---|---|
| Instruction-only prose (`AGENTS.md`, Copilot instructions) | `git diff --check`; verify referenced paths, commands, and consistency between both instruction files. No Go/frontend build is required. |
| Go code | `go vet ./...`, `NO_COLOR=1 go test ./...`, `golangci-lint run`; add `-race -count=1` coverage for affected concurrency/lifecycle packages. |
| Existing workflow, loader, grader, result, or billing behavior | `NO_COLOR=1 make test-compat` in addition to the Go checks; see [the compatibility contract](docs/COMPATIBILITY.md). |
| Dashboard | From `web/`: `npm run lint`, `npm run build`, `npx --no-install playwright test --project=chromium --retries=0`. |
| Published site | From `site/`: `npm run build`, `npm run test:e2e -- --project=chromium --retries=0`. |
| Site chat checks | From `site/`: `npm run test:chat-types`, `npm run test:chat-metadata`, `npm run test:chat-contract`, `npm run test:private-chat`, `npm run test:chat -- --project=chromium --retries=0`, and `npm run test:chat-unavailable -- --project=chromium --retries=0`. These also run for published-site changes in CI. |
| Documentation examples | Build the current CLI with `make build`, then run `npm --prefix site run test:examples` with `WAZA_BIN` set to the absolute built executable path; follow the offline example job in [site CI](.github/workflows/site-ci.yml). |
| CI/workflow | Exercise changed filters, environment, failure branches, and test discovery; verify new specs actually run rather than trusting a green skipped job. Declare explicit workflow `permissions` and per-job `timeout-minutes` when adding/changing workflows. |
| Dependencies/releases | Run affected Go/site/dashboard gates and verify generated assets and version/changelog metadata agree with the actual diff. |

A missing tool, skipped test family, or unexecuted step is not a passing check.
`make lint` currently exits successfully when golangci-lint is absent: that is
not lint evidence. CI additionally validates the pinned release schema via
`bash .github/scripts/lint.sh` before linting; preserve that validation when
changing lint tooling. Offline gates must explicitly disable live-test opt-ins
and prove that the chosen runtimes work. `ENABLE_COPILOT_TESTS=false` alone does
not prevent ordinary SDK execution; use the [offline Go setup](#offline-go-checks).
Never claim native Windows coverage from cross-compilation or Unix tests.

## CI/CD

Repository protection/rulesets are authoritative for required checks and
approvals. Read the active rules with
`gh api repos/microsoft/waza/rules/branches/main`; do not assume workflow display
names are the required status contexts. Go validation is defined in
the single `.github/workflows/go-ci.yml`; preserve required native-platform tests
and lint. It covers PRs to every base and pushes to `main`, `develop`, `dev`, and
`insider`; do not reintroduce a duplicate Squad PR validation workflow.
Superseded PR runs are cancelled per workflow/event/ref, not release publication.

PR applicability is computed from the tested merge and both parents by
[the completeness plan](.github/workflows/pr-completeness-plan.yml), which
selects the applicable validation families. [Completeness admission](.github/workflows/pr-completeness-admission.yml)
requires source-bound receipts from the same workflow run and attempt.
Preserve this wiring when changing CI: an unrelated green job, a stale receipt,
or a skipped applicable family is not evidence that the current PR is complete.

Linux executes `internal/controlledcomparison` in five deterministic shards
through `.github/workflows/go-native-shards.yml`, balanced longest-first using
observed top-level durations in `.github/scripts/go_test_shard_durations.tsv`.
Python 3 computes placement only; discovery remains authoritative and new tests
use the median historical duration. The remaining packages run in four
deterministic package-level shards through
`.github/workflows/go-remaining-shards.yml`, balanced from
`.github/scripts/go_remaining_package_durations.tsv`; an unmeasured package
uses the median measured package duration. Package assignment keeps every test
in a package together, including all of `cmd/waza`. Each worker discovers the
same complete remaining-package and test inventories, executes only its
assigned packages with `-p=4`, race detection, atomic coverage, and a
30-minute test deadline, then retains its selection and execution receipt plus
a shard-bound timing observation. Workers also retain the locked npm package's
Go source files; the merger restores those same-run sources on every event
before validating the combined coverage profile.
Refresh package costs after material package, test, or runner-topology changes
from complete successful Linux remaining-test executions; retain the run,
attempt, and source commit, and never time from canceled or incomplete
executions.
The collector requires all four successful receipts, identical inventories,
exactly-once package and test selection, and valid atomic coverage before
merging the four remaining profiles into the single main profile. Windows
continues to execute the complete suite in one job.

The required `ubuntu-latest` gate explicitly fails if the build, any native or
remaining-package shard, or the coverage merger fails, is cancelled, or is
skipped. It requires the five native profiles and the merged remaining profile
(six atomic profiles total), identical native inventories, and exactly-once
native selection before uploading coverage. Do not replace it with a skipped
dependent job or remove a package without providing exhaustive required shards.
Exercise native and remaining-shard discovery, selection, failure handling, and
coverage merging with `bash .github/scripts/go_test_shard_test.sh` and
`bash .github/scripts/go_remaining_shard_test.sh`.

Squad Release and Squad Insider Release reuse the same five native shards and
30-minute race/atomic package deadline, running remaining validation in parallel.
Their publication job explicitly rejects failed, cancelled, or skipped validation
branches and verifies all six coverage profiles and exactly-once native selection
before publishing the tested binary artifact. Test the release wiring with
`go test -count=1 ./.github/scripts` and the offline runtime/integration controls
with `bash .github/scripts/release_gate_test.sh`. Release, ordinary Go, preview,
and native-shard tests install a verified rejecting Copilot runtime. The ordinary
and release mock integration requires a fresh saved JSON outcome with the four
expected tasks and all three trials,
consistent summary/task/run/grader statuses, and zero errors, skips, or cached
tasks. Exit 1 is accepted only for completed grader failures, never task/runtime
errors, cancellation, or missing/incomplete evidence. Any attempted runtime launch
also fails the gate.
Inline grader exceptions and text-grader regex configuration errors carry
`details.evaluation_errors` separately from false assertions and are rejected by
the integration gate. Both inline runtimes convert each caught exception to a
message once and reuse it for ordinary failures and structured error evidence.
The fixed mock example permits no checkpoint results and
requires grader scores to agree with verdicts (1 for pass, less than 1 for fail).
Each native shard checks
its own rejecting runtime marker after tests, including tolerated launches.
Publication uses `!cancelled()` as well as explicit shard-result checks.
The SIGTERM regression holds the production CLI inside a blocking grader
subprocess until cancellation, rather than synchronizing on console output.
The release workflows execute the production-CLI regression controls
in `.github/scripts`, including the otherwise indistinguishable task-error exit 1.
The shared integration requires `jq` on each runner.
The fixed release example is hook-free, requires one recorded attempt per run,
and pins each named grader result to its expected type; retries must not hide
discarded evaluation exceptions or substitute executable-launch failures.
Before execution it requires a complete offline preflight report, rejects invalid
configuration and colliding final grader names (including synthesized expectation
names), and leaves interpreter/model availability unresolved for the actual guarded
run to prove. This fixed integration admission does not change legacy runtime overwrite
behavior or preflight's default non-strict exit policy.
This is offline harness evidence, not live agent-quality evidence.

The Go lint job uses `.github/scripts/lint.sh` to require configuration
validation against a checksum-pinned upstream release schema before running lint.
When upgrading golangci-lint, update the workflow version and the script's
version check, upstream release commit, and schema checksum together.

## Releasing

[docs/RELEASE.md](docs/RELEASE.md) is the authoritative release procedure.
The `azd-publish` skill prepares metadata only; it does not replace the
qualification, tagging, publication, and verification gates in that procedure.

Before qualification, settle the milestone and backlog, then make the dated
release-metadata change the exact commit intended for the tag. Keep
`version.txt`, `extension.yaml`, changelog, README, release pages, samples,
documentation tests, and e2e expectations consistent. Use the same full
40-character source frontier in the changelog and release article; preserve
historical inspection dates. Keep draft/pending labels out of shared navigation.

`web/dist/index.html` and its favicon must match a clean dashboard build for
the release source. Web dependency changes can stale the tracked entrypoint and
fail exact-source qualification. If stale, rebuild with the locked dependencies
using `npm ci && npm run build` in `web/` with Node 22 as in release CI. Review
and land the generated tracked output, validate the PR in CI, and let release
qualification verify the clean-source match. Never hand-write hashed asset names.

Before dispatching Release Qualification, verify that the manual candidate
version in `.github/workflows/release-qualification.yml` matches the committed
`version.txt` and `extension.yaml`. The manual path currently hard-codes
`0.42.0`; for a different version, update and test its resolution in a reviewed
workflow change before qualification. Dispatch for the exact current `main` SHA
and tag only that qualified commit. Any later source change invalidates that evidence.
Inspect each publisher and its artifacts independently: a failed overall
workflow can follow successful external publication. If the workflow did not
create the registry-sync PR because policy denied permission, open it manually
from the pushed `release/vX.Y.Z` branch. Before any retry, inspect existing
tags, releases, registry state, and artifacts; do not assume a rerun is safe.
If native smoke was skipped, dispatch native-platform evidence separately and
describe a same-source rebuild as such, not as testing the published bytes.
Update registry-version guidance with the registry entry, and relabel the
release article only after publication is verified. Record source/tag SHA,
asset details, and relevant run IDs. The Pages fallback and its trigger
conditions are documented in [docs/RELEASE.md](docs/RELEASE.md).

Main is squash-only; required checks must pass on the current PR head. When
main advances, refresh a stale or stacked PR by merging the current base
normally. Never rebase or force-push pushed history. A PR-scope error saying
"Merge parents do not match event base and head" indicates stale event-base
evidence; refresh the head rather than weakening admission.

On Windows, if documentation tests fail only on CRLF input, rerun them from an
LF `git archive` export before attributing the failure to the source. Use
`git -c core.longpaths=true status` to avoid path-length artifacts. If the
configured npm feed cannot restore locked packages, report site/dashboard
builds and Playwright as not run locally and rely on required CI; do not claim
unexecuted checks passed.

## Fixture Isolation

Each task execution gets a **fresh isolated workspace** with fixtures copied in:

1. Runner reads files from original `--context-dir` (fixtures folder)
2. Executor creates a new workspace under the system temp directory; sandboxed Copilot evaluations use Waza's user-cache workspace root because system temp is denied by the native sandbox
3. Files copied into the workspace
4. Agent works in the workspace (edits happen here)
5. Workspace destroyed during executor shutdown (or retained only with the explicit debugging option)
6. Next task starts fresh with original fixtures

**The original fixtures directory is never modified.** This ensures task isolation.

CLI tests additionally allocate their temporary storage outside project ancestry,
even when `TMPDIR` points into a checkout. Their `TestMain` fails closed if the
canonical fixture path inherits `skills/`, `.waza.yaml`, or `.git`. Tests that
exercise default discovery must also change into their fixture directory.
`ENABLE_COPILOT_TESTS=false` only disables explicitly gated integration tests;
it does not disable the production SDK execution path. Deterministic CLI gates
must also use an absolute, verified `COPILOT_CLI_PATH` that rejects runtime launch.

For MCP process-test failures, distinguish CLI build time, OS process creation,
and protocol exchange/shutdown time. Record elapsed phases and context state
before cleanup; label invocation setup as `setup_elapsed` and context observed
immediately after `Start` returns as `post_start_ctx_err`. Inspect output buffers
only after `Wait` joins their copy
goroutines. An empty-stderr exit or a test's total duration does not establish a
protocol failure. Retain fresh production-CLI execution and existing deadlines
while diagnosing startup delays; a diagnostic-only pass is not a root-cause fix.
Compiled adapter admission controls record the same setup, process-start, wait,
and context phases while retaining their 15-second invocation deadline and
existing exit assertions.

## Documentation Requirements

**Use Mermaid for all diagrams** in markdown files (docs, design docs, plans). No ASCII art diagrams.

**Always update documentation when making changes.** The following files must be kept in sync:

| File | Purpose | Update When |
|------|---------|-------------|
| `README.md` | Main project overview | Any CLI change, new feature |
| `docs/PRD.md` | Product requirements | Feature scope changes |
| `AGENTS.md` | Agent coding guidelines | Process/pattern changes |
| `site/` (GitHub Pages) | Public docs site (microsoft.github.io/waza) | Any feature add/change |
| `web/` (Dashboard) | Interactive eval dashboard | New data in results JSON, new views needed |

### Documentation Checklist

When adding or updating any feature:
- [ ] Check if `site/src/content/docs/` pages need updating (graders, CLI reference, guides, eval YAML)
- [ ] Check if the dashboard (`web/`) needs updates or new views to surface the feature
- [ ] Update `README.md` if user-facing
- [ ] Update the root `README.md` usage section if CLI changes
- [ ] Build the docs site to verify: `cd site && npm run build`
- [ ] Add example in appropriate docs
- [ ] Update the relevant feature or delivery tracker if related to the roadmap

When adding a new CLI command or flag:
- [ ] Add to `site/src/content/docs/reference/cli.mdx`
- [ ] Add to `site/src/content/docs/guides/` if it needs a guide
- [ ] Update `README.md` Commands section

When completing a feature:
- [ ] Close related GitHub issue with comment
- [ ] Update the relevant feature or delivery tracker
- [ ] Verify GitHub Pages site reflects the change (pages deploy on merge to main)

## Documentation Maintenance

Documentation must be updated in real-time as features change. This is enforced by Saul (Documentation Lead) who reviews all PRs for doc impact.

### When to Update Docs

| Change Type | Required Doc Updates |
|---|---|
| New CLI command or flag | README.md Commands section, `site/` CLI reference, docs/GUIDE.md |
| Changed CLI behavior | README.md, `site/` guides, docs/GUIDE.md, affected tutorials |
| New/changed dashboard view | `site/` dashboard guide, regenerate screenshots, docs/DEMO-GUIDE.md |
| Changed eval YAML schema | README.md YAML section, `site/` eval-yaml reference, example files |
| New grader | README.md "Available Grader Types" section, `site/` graders page, docs/GUIDE.md |
| New sensei/dev feature | `site/` sensei guide, README.md |
| New data in results JSON | Check if dashboard (`web/`) needs a new view, column, or chart to surface it |

### Screenshot Maintenance

When dashboard UI changes, regenerate screenshots:
```bash
cd web && npm run refresh:docs-media
cd ../site && npm run refresh:docs-media
```
These explicit media-refresh commands save screenshots to `docs/images/` and
`site/public/images/`. Routine browser discovery excludes screenshot-only specs,
rather than emitting skipped results that fail strict execution receipts. Mixed
specs retain their assertions without updating tracked media.

Verify every referenced screenshot destination, including its `site/public/`
copy where used, against the current UI and fixture. Keep the tracked dashboard
entrypoint and its actual embedded hashed assets from the same build; compile
stubs are not evidence of a working dashboard. Do not force-add ignored bundles
or describe old screenshots as regenerated.

Validate copied documentation examples through the real schemas/loaders and
check commands against actual `--help`, exit policies, and configured filenames.
New site pages need sidebar entries and links that preserve the configured
`/waza` base; test rendered navigation, not just successful HTML generation.
Keep detailed workflows canonical and link to them rather than duplicating
instructions across README, guides, and agent files.

Sources: [invalid copied YAML](https://github.com/microsoft/waza/pull/672#discussion_r4249063333),
[deployed-base link](https://github.com/microsoft/waza/pull/666#discussion_r4236175862),
[wrong screenshot destination](https://github.com/microsoft/waza/pull/632#discussion_r4146731214),
[duplicated guidance](https://github.com/microsoft/waza/pull/633#discussion_r4168690483).

## Adding New Features

### Adding a CLI Command

1. Add a command constructor and handler in `cmd/waza/cmd_<command>.go`, following neighboring commands.
2. Register the command in `newRootCommand()` in `cmd/waza/root.go`; keep `main.go` focused on process entry and exit handling.
3. Implement logic in the appropriate `internal/` package.
4. Add tests in `*_test.go` files, including command registration and public behavior.
5. Update the root `README.md` and the CLI reference in `site/`.

### Adding a Grader

1. Implement the `Grader` interface in `internal/graders/` (one file per grader kind)
2. Add a `models.<Kind>GraderParameters` type in `internal/models/grader_params.go`
3. Add the matching `case` to `Create()` in `internal/graders/grader.go`
4. Add tests
5. Document in README and `site/`

### Adding an Engine (Executor)

1. Implement `AgentEngine` interface in `internal/execution/`
2. Add configuration options
3. Add tests
4. Document usage

## Code Ownership and Review

### CODEOWNERS File

The [.github/CODEOWNERS](.github/CODEOWNERS) file is the source of truth for
reviewer assignment. Read it instead of maintaining another owner list here.

### Required review and completion gates

- [ ] Review the full final diff, including inherited/stacked changes. Preserve
  `.github/PULL_REQUEST_TEMPLATE.md`; make scope, dependencies, implemented
  public surfaces, known limitations, and validation match what will merge.
- [ ] Record exact commands and results with the tested commit/tree and platform.
  Distinguish local results, parent-head evidence, skipped/blocked checks, and
  actual current-head CI. Do not relabel infrastructure failures or stale
  generated output as success.
- [ ] Read all paginated review bodies, conversation comments, and inline threads,
  including **Previously missed** and suppressed findings. Zero open threads,
  a green summary, or an earlier approval does not prove there are no findings.
- [ ] Give each substantive finding a disposition: verified fix with regression
  evidence, demonstrated non-reproduction, or an explicitly agreed scope
  deferral with limitations/follow-up recorded. Reply in the original thread;
  do not resolve a finding merely because the PR is old, merged, or green.
- [ ] After the last source/base update, refresh affected checks, generated
  artifacts, and review evidence. Inspect actual test execution/skips, especially
  Windows behavior and newly added browser specs. Fix new findings even when
  older checks passed; do not weaken assertions or retry unchanged code to
  conceal a product failure.
- [ ] Obtain the independent approvals and required checks enforced by the
  repository. An author's comment is not self-approval; workflow-start approval
  is not code review. Do not bypass protection or use admin merge without
  explicit user authorization, and never treat approval override as permission
  to waive failing checks or unfinished work.

Sources: [summary-only blockers despite zero threads](https://github.com/microsoft/waza/pull/671#issuecomment-6156624122),
[stale PR scope](https://github.com/microsoft/waza/pull/666#discussion_r4235635705),
[workflow approval distinction](https://github.com/microsoft/waza/pull/648#discussion_r4185388508),
[final-head evidence and author review](https://github.com/microsoft/waza/pull/672#pullrequestreview-5525720515).

### Branch Protection

Read the active rules described under CI/CD before handing off a PR to `main`.
Honor required checks, code-owner/independent approvals, stale-review dismissal,
and thread resolution. Auto-merge is not a substitute for these requirements
and must not be enabled without the user's request.

## Commit Messages

Use conventional commits:
- `feat:` New feature
- `fix:` Bug fix
- `docs:` Documentation only
- `ci:` CI/CD changes
- `chore:` Maintenance tasks
- `refactor:` Code restructuring

**Reference issues:** `feat: Add tokens command #47`

## Files to Ignore

These are generated/temporary and should not be committed:
- `results.json` - Eval results
- `coverage.txt` - Test coverage
- `waza` (binary) - Built executable

## Quick Reference

### Build and run
```bash
make build
./waza --help
```

### Run tests
Use the [guarded offline Go checks](#offline-go-checks); `make test` alone does not
install the rejecting runtime guard.

### Key CLI flags
- `-v, --verbose` - Verbose output
- `-o, --output` - Save results JSON
- `--context-dir` - Fixtures directory

## Epics and Priorities

See the active [feature tracker #657](https://github.com/microsoft/waza/issues/657)
and [delivery tracker #866](https://github.com/microsoft/waza/issues/866) for current
scope, priorities, and status; do not maintain a second priority list here. The
[platform roadmap #8](https://github.com/microsoft/waza/issues/8) is historical.
