# Waza feature coverage audit: October 5–19, 2026

This inventory reconciles the merged pull requests and default-branch commits in
the requested window. It records the shipped contract, the existing and updated
documentation, and the applicability of the opt-in Lab. It is an audit of the
current source tree, not a claim that every listed change is in the latest
standalone release.

## Scope and release boundary

The source inventory is the GitHub merged-PR query
[`repo:microsoft/waza is:merged merged:2026-10-05..2026-10-19`](https://github.com/microsoft/waza/pulls?q=is%3Apr+is%3Amerged+merged%3A2026-10-05..2026-10-19)
and the default-branch commit list from 2026-10-05 through 2026-10-19 UTC.
The PR query contains **50 merged PRs** and the default-branch commit query
contains 50 commits. Forty-seven commit subjects include the corresponding
`(#PR)` suffix. The other three were reconciled to merged PRs by title and
timestamps; no standalone direct-to-default-branch commit was found.

The window includes the v0.38.9 release metadata update ([#651](https://github.com/microsoft/waza/pull/651)).
The source-only Lab and the other post-release changes are not represented as
available in standalone v0.38.10. See the [release availability matrix](../site/src/content/docs/reference/releases.mdx)
and the [Lab guide](../site/src/content/docs/guides/dashboard-lab.mdx) before
choosing a binary.

## Complete linked inventory

### Product, documentation, and reliability changes

| PR | Merged change |
|---|---|
| [#720](https://github.com/microsoft/waza/pull/720) | Deterministic MCP and CLI fault-recovery scenarios |
| [#704](https://github.com/microsoft/waza/pull/704) | Evidence-backed independent prompt judging |
| [#703](https://github.com/microsoft/waza/pull/703) | Private native retry-evidence joining |
| [#700](https://github.com/microsoft/waza/pull/700) | Review-derived engineering standards |
| [#699](https://github.com/microsoft/waza/pull/699) | Squad team and model-policy maintenance |
| [#698](https://github.com/microsoft/waza/pull/698) | Clean-checkout assurance CI and digest-work cleanup |
| [#697](https://github.com/microsoft/waza/pull/697) | Workflow release-readiness guidance and regression examples |
| [#688](https://github.com/microsoft/waza/pull/688) | Recorded trial reliability and dashboard evidence |
| [#673](https://github.com/microsoft/waza/pull/673) | Skill base directory in injected context |
| [#672](https://github.com/microsoft/waza/pull/672) | Portable eval evidence and scoped requirement observations |
| [#671](https://github.com/microsoft/waza/pull/671) | Agent-free eval preflight and descriptive requirements |
| [#670](https://github.com/microsoft/waza/pull/670) | Scenario-first eval authoring without a required skill |
| [#669](https://github.com/microsoft/waza/pull/669) | Offline v1 compatibility corpus |
| [#668](https://github.com/microsoft/waza/pull/668) | Controlled release policies and opt-in offline assurance |
| [#667](https://github.com/microsoft/waza/pull/667) | Private native fault delivery and cancellation |
| [#666](https://github.com/microsoft/waza/pull/666) | Strict offline grader assurance and reference contracts |
| [#655](https://github.com/microsoft/waza/pull/655) | Eval-suite discovery relative to the target skill |
| [#652](https://github.com/microsoft/waza/pull/652) | Opt-in real-results dashboard Lab |
| [#648](https://github.com/microsoft/waza/pull/648) | Real checks for release-metadata PRs |
| [#632](https://github.com/microsoft/waza/pull/632) | Authoritative Copilot AI Credits and per-model dashboard usage |
| [#651](https://github.com/microsoft/waza/pull/651) | v0.38.9 registry and version synchronization |

### Dependency and runtime changes

These are included in the inventory because dependency/runtime updates can
change supported behavior or required environments. The PRs below are
maintenance updates, not new user-facing Waza features.

| PR | Merged update |
|---|---|
| [#721](https://github.com/microsoft/waza/pull/721) | Consolidated 13 Dependabot dependency updates |
| [#712](https://github.com/microsoft/waza/pull/712) | Copilot Go SDK 1.0.15 → 1.0.21 |
| [#711](https://github.com/microsoft/waza/pull/711) | TanStack React Query 5.104.1 → 5.104.2 |
| [#687](https://github.com/microsoft/waza/pull/687) | site smol-toml 1.8.0 → 1.9.1 |
| [#686](https://github.com/microsoft/waza/pull/686) | web source-map-js 1.2.1 → 1.2.2 |
| [#684](https://github.com/microsoft/waza/pull/684) | web TanStack React Table v9 migration |
| [#683](https://github.com/microsoft/waza/pull/683) | web TanStack React Query 5.104.0 → 5.104.1 |
| [#682](https://github.com/microsoft/waza/pull/682) | Go Azure Blob SDK 1.8.1 → 1.8.2 |
| [#681](https://github.com/microsoft/waza/pull/681) | web Vite React plugin 6.1.1 → 6.1.2 |
| [#680](https://github.com/microsoft/waza/pull/680) | site Astro 7.3.5 → 7.3.8 |
| [#679](https://github.com/microsoft/waza/pull/679) | Go OpenTelemetry OTLP exporter 1.46.0 → 1.47.0 |
| [#678](https://github.com/microsoft/waza/pull/678) | web typescript-eslint 8.71.0 → 8.71.1 |
| [#677](https://github.com/microsoft/waza/pull/677) | Go OpenTelemetry 1.46.0 → 1.47.0 |
| [#676](https://github.com/microsoft/waza/pull/676) | Go stdout trace exporter 1.46.0 → 1.47.0 |
| [#675](https://github.com/microsoft/waza/pull/675) | web globals 17.12.0 → 17.13.0 |
| [#650](https://github.com/microsoft/waza/pull/650) | site devalue 5.9.2 → 5.9.4 |
| [#649](https://github.com/microsoft/waza/pull/649) | web brace-expansion 5.0.9 → 5.0.12 |
| [#647](https://github.com/microsoft/waza/pull/647) | web TanStack React Query 5.103.2 → 5.104.0 |
| [#646](https://github.com/microsoft/waza/pull/646) | Go klauspost/compress 1.19.2 → 1.20.1 |
| [#645](https://github.com/microsoft/waza/pull/645) | web lucide-react 1.28.0 → 1.49.0 |
| [#644](https://github.com/microsoft/waza/pull/644) | Go azd CLI 1.34.0 → 1.35.0 |
| [#643](https://github.com/microsoft/waza/pull/643) | web typescript-eslint 8.70.1 → 8.71.0 |
| [#642](https://github.com/microsoft/waza/pull/642) | site Astro 7.3.3 → 7.3.5 |
| [#641](https://github.com/microsoft/waza/pull/641) | Copilot Go SDK 1.0.14 → 1.0.15 |
| [#640](https://github.com/microsoft/waza/pull/640) | web Vite 8.3.0 → 8.3.1 |
| [#639](https://github.com/microsoft/waza/pull/639) | site sharp 0.35.4 → 0.35.5 |
| [#638](https://github.com/microsoft/waza/pull/638) | Go Azure core SDK 1.23.1 → 1.23.2 |
| [#637](https://github.com/microsoft/waza/pull/637) | site Starlight 0.42.2 → 0.42.5 |
| [#636](https://github.com/microsoft/waza/pull/636) | Go x/term 0.45.0 → 0.46.0 |

### Default-branch commit reconciliation

| Commit | Matched merged PR | Reconciliation |
|---|---|---|
| [`54bac1b`](https://github.com/microsoft/waza/commit/54bac1ba9d9579fc2465825921f1a05e8c09728c) | [#697](https://github.com/microsoft/waza/pull/697) | Commit subject names issues #693–#696; its 2026-10-15T21:54:23Z timestamp matches the PR merge. |
| [`04871bb`](https://github.com/microsoft/waza/commit/04871bbac449ffb6b86e3f0854c072c4b87c0da3) | [#698](https://github.com/microsoft/waza/pull/698) | Commit subject names issues #691–#692; its 2026-10-15T20:48:18Z timestamp is one second before the PR merge. |
| [`fb9a097`](https://github.com/microsoft/waza/commit/fb9a097abab1b0f0028c3e995fb267fcf600e846) | [#672](https://github.com/microsoft/waza/pull/672) | Commit subject names issue #663; its 2026-10-14T22:43:02Z timestamp and feature title match the PR merge. |

The 13 dependency PRs superseded by consolidated PR #721 are represented by
#721, not double-counted as merged PRs.
The consolidated change updated Go `klauspost/compress` 1.20.3, `x/runewidth`
0.31, `x/sync` 0.24.0, `x/sys` 0.49.0, and `x/term` 0.47.0; site Starlight
0.42.6 and Playwright 1.64.0; and web lucide-react 1.57.0, Playwright 1.64.0,
ESLint 10.12.0, and typescript-eslint 8.71.2. It also refreshed action pins
across 15 workflows and added missing workflow permissions/timeouts without
changing their selected runtimes. The already-merged SDK and React Query updates
remain separately inventoried as #712 and #711.

## Coverage matrix

“Lab” refers only to the opt-in `waza serve --lab` real-results workspace.
“Classic” refers to the original dashboard and is not replaced by Lab.
Documentation paths below are canonical sources; the Astro sidebar and rendered
link checks make the linked site pages discoverable under the `/waza` base path.

| Source | User-visible contract | Repository documentation | Published site | Lab/API/export disposition | Regression evidence and gap disposition |
|---|---|---|---|---|---|
| [#652](https://github.com/microsoft/waza/pull/652), [#632](https://github.com/microsoft/waza/pull/632), [#688](https://github.com/microsoft/waza/pull/688) | Opt-in viewer over saved results; authoritative AI Credits and model usage; recorded reliability is not inferred from quality scores. | README usage and result guidance; `AGENTS.md` guards evidence, credits, and missing values. | [Lab guide](../site/src/content/docs/guides/dashboard-lab.mdx), [CLI](../site/src/content/docs/reference/cli.mdx), [release matrix](../site/src/content/docs/reference/releases.mdx). Explicitly labels main-only, experimental, and `--lab`; preserves classic root route. | Summary/detail API exposes recorded usage, trials, intervals, and `pass^k`. Lab shows reliability in inspector and model usage in recorded API/configuration metadata; no dedicated session-ledger view. JSON includes detail; CSV is run-level summary only. | `internal/webapi/reliability_test.go`, browser real-results fixtures, and `web/e2e/dashboard-lab-real.spec.ts`. Added evidence-tab UI; documented absent historical values and limits. |
| [#670](https://github.com/microsoft/waza/pull/670), [#655](https://github.com/microsoft/waza/pull/655), [#673](https://github.com/microsoft/waza/pull/673) | Scenario-first evals do not require a skill; suite discovery is relative to target skill; injected skill context includes the base directory. | Existing authoring and skill docs describe optional skill and paths. | [Eval YAML guide](../site/src/content/docs/guides/eval-yaml.mdx), [CLI reference](../site/src/content/docs/reference/cli.mdx), skill/custom-agent guides. | N/A for authoring/execution controls: Lab is read-only. It filters suites only from recorded artifacts and distinguishes unavailable skill metadata from eval identity. | Eval loader/CLI and execution tests cover no-skill and path behavior; Lab filters preserve absent/empty distinctions. Verified docs already describe scenario authoring and discovery; no remaining gap identified. |
| [#671](https://github.com/microsoft/waza/pull/671), [#666](https://github.com/microsoft/waza/pull/666) | Static preflight validates without an agent; requirement declarations are descriptive; offline grader assurance is opt-in and avoids runtime/external side effects. | README and CLI usage identify `preflight`; compatibility and grader guidance preserve offline and historical boundaries. | [CLI reference](../site/src/content/docs/reference/cli.mdx), [grader guide](../site/src/content/docs/guides/graders.mdx), [eval YAML guide](../site/src/content/docs/guides/eval-yaml.mdx). | N/A as interactive operations: Lab reports only persisted results; no preflight or assurance execution is triggered by viewing. Recorded evidence metadata is shown with non-verification caveats. | Preflight, grader assurance, and offline-contract tests. No Lab controls added; docs avoid representing descriptive requirements as successful enforcement. |
| [#704](https://github.com/microsoft/waza/pull/704) | Independent prompt judging records frozen bundle/callback observations and usage; a callback does not prove that a model inspected every item. | Prompt grader behavior and evidence limits are documented with grader references. | [Graders](../site/src/content/docs/guides/graders.mdx) and [Lab guide](../site/src/content/docs/guides/dashboard-lab.mdx). | API returns `promptJudgingRuns` and regrade usage. Lab displays valid metadata and credits, labels cached source evidence, withholds invalid data, and does not claim inspection. JSON includes it; CSV does not. | `internal/webapi/prompt_judging_test.go` covers producer → result reader → API; browser coverage checks valid, historical-missing, and invalid metadata. |
| [#720](https://github.com/microsoft/waza/pull/720) | Deterministic recovery scenarios cover unmatched MCP arguments, cancellation, finite sequences, and recovery evidence; mocks are not live-service evidence. | Recovery guidance and example workflows already cover the shipped cases. | [Dependency recovery](../site/src/content/docs/guides/dependency-recovery.mdx) and [eval YAML](../site/src/content/docs/guides/eval-yaml.mdx). | N/A for authoring or executing scenarios: Lab can show only persisted artifacts and does not control workers. | Recovery scenario/CLI tests and documented fixture behavior. Audit verified existing coverage; no docs gap found. |
| [#667](https://github.com/microsoft/waza/pull/667), [#703](https://github.com/microsoft/waza/pull/703) | Native fault delivery, cancellation, and retry-evidence joining remain private/opt-in implementation boundaries; they do not imply a public live-worker feature. | Private fault adapters are not presented as stable user configuration. | N/A as a public feature page: these private native paths are not advertised as supported public functionality. The release and Lab guides distinguish unreleased/opt-in scope. | N/A: Lab is an artifact viewer; no fault injection, retry, or worker control. | Private adapter and retry-joining tests. Deliberately not exposed or promoted; no follow-up is required to meet the public contract. |
| [#668](https://github.com/microsoft/waza/pull/668), [#697](https://github.com/microsoft/waza/pull/697), [#698](https://github.com/microsoft/waza/pull/698) | Controlled release policies and offline assurance are opt-in; release readiness combines checks and examples without claiming unexecuted evidence. | Release-readiness and regression workflow guidance updated. | [CI/CD and release workflow](../site/src/content/docs/guides/ci-cd.mdx), [controlled comparisons](../site/src/content/docs/guides/controlled-comparisons.mdx), CLI and grader references. | N/A for policy authoring/check execution: Lab does not compute readiness or release decisions. It does not infer assurance from result summaries. | Policy, assurance, release metadata, and docs-link tests. Existing release docs were verified and updated with current workflow guidance. |
| [#669](https://github.com/microsoft/waza/pull/669), [#698](https://github.com/microsoft/waza/pull/698) | v1 historical workflows have an offline compatibility corpus; clean-checkout CI must not rely on ambient workspace state. | `docs/COMPATIBILITY.md` defines the compatibility contract and corpus expectations. | [Compatibility guide](../site/src/content/docs/guides/compatibility.mdx) and [release availability](../site/src/content/docs/reference/releases.mdx). | N/A: compatibility admission and CI execution are not Lab views or result fields. Lab continues to read supported historical artifacts and leaves absent fields unavailable. | `make test-compat`, clean-checkout assurance workflow. The change is covered in compatibility documentation; no Lab-specific behavior was needed. |
| [#641](https://github.com/microsoft/waza/pull/641), [#712](https://github.com/microsoft/waza/pull/712) | Copilot SDK is a runtime dependency; SDK 1.0.21 compatibility includes the bundled CLI 1.0.85 legacy permission/event behavior. | Current SDK and embedded CLI versions are documented; README dependency notes remain distinct from standalone release availability. | [CLI reference](../site/src/content/docs/reference/cli.mdx), [custom agents](../site/src/content/docs/guides/custom-agents.mdx), [releases](../site/src/content/docs/reference/releases.mdx). | N/A as new Lab behavior. Existing result APIs remain the source; no SDK runtime is launched by viewing a Lab artifact. | SDK protocol/event and execution tests. Corrected stale SDK 1.0.14/1.0.15 wording to distinguish current main SDK 1.0.21 from the released binary. |
| [#651](https://github.com/microsoft/waza/pull/651), [#648](https://github.com/microsoft/waza/pull/648), [#636](https://github.com/microsoft/waza/pull/636)–[#650](https://github.com/microsoft/waza/pull/650), [#675](https://github.com/microsoft/waza/pull/675)–[#687](https://github.com/microsoft/waza/pull/687), [#711](https://github.com/microsoft/waza/pull/711), [#721](https://github.com/microsoft/waza/pull/721) | Release metadata and supported build/runtime dependency versions are kept synchronized. Other dependency updates are routine maintenance, with no new Waza behavior found in their PRs. | Version/release metadata and required runtimes are documented in the release and contributor references. | [Release availability](../site/src/content/docs/reference/releases.mdx); dependency-only changes do not need individual feature pages. | N/A: no new result schema or Lab contract from the dependency-only PRs. Existing UI/API behavior remains unchanged. | Release metadata checks and dependency CI. Verified all 50 merged PRs; grouped dependency PRs by maintenance area above. |
| [#699](https://github.com/microsoft/waza/pull/699), [#700](https://github.com/microsoft/waza/pull/700) | Team/tooling policy and review-derived contributor standards affect agent/contributor workflows, not end-user evaluation behavior. | `AGENTS.md`, Squad files, and [Meet the Squad](../meet-the-squad.md). | N/A for user-facing documentation: these are contributor instructions. | N/A: no API, result field, or Lab behavior. | Instruction consistency and contributor workflow checks. |

## Lab producer-to-display and export contract

The Lab does not reconstruct claims from configuration or scores. The flow
checked for this audit is:

1. Waza produces a result with recorded task/suite reliability, portable
   evidence, prompt-judging, and usage fields.
2. The result reader and web API expose those fields in summary/detail responses
   with their presence and provenance intact.
3. The real-results Lab requests details on demand and renders validated API
   metadata. Missing historical fields remain **Not assessed**; invalid
   evidence/judging metadata is withheld. An explicit zero credit remains zero.
4. JSON reports include selected API run details. CSV is explicitly a run-level
   summary and does not claim to export task intervals, evidence provenance, or
   judge observations. A failed detail request aborts JSON export rather than
   producing a partial report.

The Lab comparison is descriptive and task-score based. It does not compare
confidence intervals, `pass^k`, provenance, or prompt-judging observations.
Current Runs is saved-artifact replay, not a live worker monitor. Neither route
exposes the private native fault controls. The classic dashboard remains at
`/#/`; `--lab` selects the opt-in workspace at `/#/lab`.

## Verification status

The audit's implementation adds a **Recorded evidence** inspector tab and
producer-backed API/browser regression coverage for valid, invalid, malformed,
null-row, and historical-missing metadata. The Lab guide documents these semantics, the
comparison limits, JSON-versus-CSV scope, and the new screenshot. Existing
documentation for scenario-first authoring, preflight, recovery, release
readiness, compatibility, and private fault boundaries was checked before
recording those rows as covered. The new Lab screenshot is generated from an
explicit mock fixture, labels the mock provenance and unverified state, and is
byte-identical in `docs/images/` and `site/public/images/`.

Validation on this checkout (Linux; Go 1.26.4, Node 24.21.0/npm 11.19.0):

| Exact command | Result |
|---|---|
| `cd web && npm run lint` | PASS |
| `cd web && npm run build` | PASS; generated embed assets and entrypoint were cleaned/restored after validation. |
| `cd web && npx --no-install playwright test --project=chromium --retries=0` | PASS: 235 passed, 1 explicit opt-in skip (`assurance.spec.ts`, requires actual deterministic Calibrate/Verify export gate). |
| `cd site && npm run build` | PASS: documentation tests 9/9; 30 site pages built. |
| `cd site && npm run test:e2e -- --project=chromium --retries=0` | PASS: 36/36; checked 29 built pages, 756 internal routes/fragments, and 19 images under `/waza`. |
| `cd site && WAZA_BIN=/tmp/waza-docs-validation npm run test:examples` | PASS: 6/6 offline documentation-example/fixture tests using a built current-source CLI and rejecting Copilot runtime guard. |
| `go build -o /tmp/waza-docs-validation ./cmd/waza` | PASS; temporary binary removed after example validation. |
| `go test ./internal/webapi -run 'Reliability\|Evidence\|PromptJudging'` | PASS; focused persisted-result/API producer coverage. |
| `git diff --check` | PASS. |

The configured dashboard npm feed could not resolve in this environment. To run
the dashboard checks, the locked manifest was copied to `/tmp` and only its
feed URL prefixes were mapped to corresponding public npm URLs; `npm ci`
completed with zero reported advisories. No repository manifest or lockfile
was changed. Full Go suite, `go vet`, `golangci-lint`, and `make test-compat`
were not run because no Go implementation or compatibility contract changed;
the affected API producer tests and executable documentation examples did run.
