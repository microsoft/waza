# Agent Instructions for waza Repository

## Overview

This repository contains `waza`, a CLI tool for evaluating Agent Skills. **Go is the only implementation**, and it lives in the repository root. (An early Python implementation was removed in February 2026.)

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
- **Tracking Issue:** [#66 - Waza Platform Roadmap](https://github.com/microsoft/waza/issues/66)
- **PRD:** [docs/PRD.md](docs/PRD.md)
- When completing work, update the relevant GitHub issue
- Reference issue numbers in commit messages (e.g., `feat: Add tokens command #47`)

## Code Structure (Go)

```
cmd/waza/                  # CLI entrypoint
└── main.go                # Command parsing and execution
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

## Building and Testing

> **Requires Go 1.26 or later.** The module targets `go 1.26` (`go.mod`), which means Go 1.26 language features and standard library APIs are safe to use. If you want to rely on features or stdlib additions introduced after Go 1.26, first bump the `go` version in `go.mod` to that minimum version.

```bash
# Build
make build
# or: go build -o waza ./cmd/waza

# Run tests
make test
# or: go test -v ./...

# Lint
golangci-lint run

# Run evaluation
./waza run examples/code-explainer/eval.yaml --context-dir examples/code-explainer/fixtures -v
```

### Testing Requirements

**Every PR must leave tests in a passing state.** This is non-negotiable:

- **All existing tests must pass** — run `go test ./...` before pushing code. If your change breaks an existing test, fix it. Instruction-only prose changes use the documentation-only check below.
- **New features require new tests** — every new command, flag, grader, or internal function needs test coverage. No shipping untested code.
- **Bug fixes require regression tests** — if you fix a bug, add a test that would have caught it.
- **Playwright e2e tests** — if you change the dashboard (`web/`), run `cd web && npx playwright test --project=chromium` and fix any failures.
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
| CI/workflow | Exercise changed filters, environment, failure branches, and test discovery; verify new specs actually run rather than trusting a green skipped job. Declare explicit workflow `permissions` and per-job `timeout-minutes` when adding/changing workflows. |
| Dependencies/releases | Run affected Go/site/dashboard gates and verify generated assets and version/changelog metadata agree with the actual diff. |

A missing tool, skipped test family, or unexecuted step is not a passing check.
`make lint` currently exits successfully when golangci-lint is absent: that is
not lint evidence. CI additionally validates the pinned release schema via
`bash .github/scripts/lint.sh` before linting; preserve that validation when
changing lint tooling. Offline gates must explicitly disable live-test opt-ins
and prove that the chosen runtimes work. `ENABLE_COPILOT_TESTS=false` alone does
not prevent ordinary SDK execution; retain the rejecting runtime guard described
below. Never claim native Windows coverage from cross-compilation or Unix tests.

## CI/CD

Repository protection/rulesets are authoritative for required checks and
approvals. Read the active rules with
`gh api repos/microsoft/waza/rules/branches/main`; do not assume workflow display
names are the required status contexts. Go validation is defined in
`.github/workflows/go-ci.yml` and `.github/workflows/squad-ci.yml`; preserve
required native-platform tests and lint.

Both Go lint workflows use `.github/scripts/lint.sh` to require configuration
validation against a checksum-pinned upstream release schema before running lint.
When upgrading golangci-lint, update both workflow versions and the script's
version check, upstream release commit, and schema checksum together.

## Fixture Isolation

Each task execution gets a **fresh temp workspace** with fixtures copied in:

1. Runner reads files from original `--context-dir` (fixtures folder)
2. Executor creates new temp workspace (e.g., `/tmp/waza-abc123/`)
3. Files copied into temp workspace
4. Agent works in temp workspace (edits happen here)
5. Temp workspace destroyed after task
6. Next task starts fresh with original fixtures

**The original fixtures directory is never modified.** This ensures task isolation.

CLI tests additionally allocate their temporary storage outside project ancestry,
even when `TMPDIR` points into a checkout. Their `TestMain` fails closed if the
canonical fixture path inherits `skills/`, `.waza.yaml`, or `.git`. Tests that
exercise default discovery must also change into their fixture directory.
`ENABLE_COPILOT_TESTS=false` only disables explicitly gated integration tests;
it does not disable the production SDK execution path. Deterministic CLI gates
must also use an absolute, verified `COPILOT_CLI_PATH` that rejects runtime launch.

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
- [ ] Update tracking issue #66 if related to roadmap

When adding a new CLI command or flag:
- [ ] Add to `site/src/content/docs/reference/cli.mdx`
- [ ] Add to `site/src/content/docs/guides/` if it needs a guide
- [ ] Update `README.md` Commands section

When completing a feature:
- [ ] Close related GitHub issue with comment
- [ ] Update tracking issue #66 checkbox
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
cd web && npx playwright test e2e/screenshots.spec.ts --project=chromium
```
Screenshots are saved to `docs/images/` and referenced throughout documentation.

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

1. Add command handling in `cmd/waza/main.go`
2. Implement logic in appropriate `internal/` package
3. Add tests in `*_test.go` files
4. Update the root `README.md`

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
./waza run examples/code-explainer/eval.yaml -v
```

### Run tests
```bash
make test
```

### Key CLI flags
- `-v, --verbose` - Verbose output
- `-o, --output` - Save results JSON
- `--context-dir` - Fixtures directory

## Epics and Priorities

See [Tracking Issue #66](https://github.com/microsoft/waza/issues/66) for the full roadmap.

| Epic | Priority | Description |
|------|----------|-------------|
| E1: Go CLI Foundation | P0 | Core CLI commands |
| E2: Sensei Engine | P0 | Compliance scoring |
| E3: Evaluation Framework | P0 | Cross-model testing |
| E4: Token Management | P1 | Budget tracking |
| E5: Waza Skill | P1 | Conversational interface |
| E6: CI/CD Integration | P1 | GitHub Actions |
| E7: AZD Extension | P2 | azd packaging |
