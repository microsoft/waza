# Work Routing

How to decide who handles what.

## Routing Table

| Work Type | Route To | Examples |
|-----------|----------|----------|
| Go backend / CLI / SDK execution | Linus | `cmd/waza/`, `internal/execution/`, `internal/orchestration/`, configuration, discovery, tool policies |
| Graders / requirements / evidence / assurance | Linus | `internal/graders/`, `internal/requirements/`, `internal/evidence/`, `internal/assurance/`, source/schema validation |
| Mock / private native / fault adapters | Linus | `internal/commandmock/`, `internal/mcpmock/`, `internal/faultfixture/`, `internal/faultsequence/`, `internal/nativesnapshot/` |
| Backend APIs / storage / release policy | Linus | `internal/webapi/`, `internal/webserver/`, `internal/mcp/`, `internal/jsonrpc/`, `internal/storage/`, `internal/releasepolicy/` |
| Frontend / web design / React / accessibility | Yen | `web/src/`, dashboard views, real-vs-demo Lab states, responsive/keyboard interaction |
| Testing / QA / evaluation quality | Basher | Go tests, unit tests, integration tests, CLI isolation, compatibility corpus, schema/reader preservation, Playwright, false-acceptance coverage |
| Technical documentation / examples | Livingston | README, `docs/`, `site/src/content/docs/`, `examples/`, usage skills, technical release notes |
| Documentation architecture / review | Saul | Navigation, terminology, style, reference accuracy, documentation impact |
| Go performance / reliability analysis | Turk | Reproducible profiles/benchmarks, allocation, contention, cancellation, capture/storage scaling |
| Developer relations / socialization | Danny | DevRel, demo narratives, adoption journeys, draft announcements/social copy, community feedback |
| Release / developer infrastructure | Virgil | CI, scripts, installers, embedded assets, Docker, azd packaging, Pages, dependency/toolchain maintenance |
| Architecture / Design decisions | Rusty | System design, component architecture, API contracts, large refactors |
| Code review | Rusty | Review all PRs, check quality, suggest improvements |
| Scope & priorities | Rusty | What to build next, trade-offs, epic planning |
| Async issue work (bugs, tests, small features) | @copilot 🤖 | Well-defined tasks matching capability profile |

Choose one accountable specialist, not multiple co-owners. Supporting context can
come from another member without creating a second implementation assignment.
Scribe, Ralph, Rai, and Fact Checker are bounded support identities under
`.squad/governance.md`, not specialist routing destinations.

Before any dispatch, apply `.squad/model-policy.md`. All code-review assignments
use `gpt-5.6-terra`, even when the selected reviewer's implementation model differs.

## Module Ownership

| Module | Primary | Secondary |
|--------|---------|-----------|
| `cmd/waza/` | Linus | Rusty |
| `internal/` | Linus | Rusty |
| `schemas/` | Linus | Basher |
| `web/src/` | Yen | Linus |
| `web/e2e/` | Basher | Yen |
| `site/src/content/docs/` | Livingston | Saul |
| `site/e2e/` | Basher | Saul |
| `.github/workflows/` | Virgil | Basher |
| `.github/scripts/` | Virgil | Basher |
| `internal/embedded/` | Virgil | Linus |
| `install.sh` | Virgil | Linus |
| `install.ps1` | Virgil | Linus |
| `Makefile` | Virgil | Linus |
| `Dockerfile` | Virgil | Linus |
| `docs/` | Livingston | Saul |
| `README.md` | Livingston | Saul |
| `examples/` | Livingston | Basher |
| `skills/` | Livingston | Linus |
| `DEMO-SCRIPT.md` | Danny | Livingston |
| `docs/DEMO-GUIDE.md` | Danny | Livingston |
| `.squad/config.json` | Rusty | Virgil |
| `.squad/governance.md` | Rusty | Saul |
| `.squad/model-policy.md` | Rusty | Virgil |

## AI-only Consultation

Linus owns Copilot SDK and backend implementation. Rusty provides architecture
and independent review; Basher provides bounded test-strategy input when needed.
Route issues and implementation questions only to qualified AI members, never
to humans. If coverage is missing, report the gap to the coordinator rather than
creating a human handoff. Preserve existing reviewer/author lockouts when choosing
an owner or reviewer; membership cleanup does not reset them.

This routing policy does not change repository access, CODEOWNERS, required
independent native approvals, or the project owner's decision authority.

## Issue Routing

| Label | Action | Who |
|-------|--------|-----|
| `squad` | Triage: analyze issue, evaluate @copilot fit, assign `squad:{member}` label | Rusty (Lead) |
| `squad:rusty` | Pick up issue and complete the work | Rusty |
| `squad:linus` | Pick up issue and complete the work | Linus |
| `squad:basher` | Pick up issue and complete the work | Basher |
| `squad:livingston` | Pick up issue and complete the work | Livingston |
| `squad:saul` | Pick up issue and complete the work | Saul |
| `squad:turk` | Investigate the bounded performance/reliability question | Turk |
| `squad:yen` | Own frontend implementation, design, or accessibility | Yen |
| `squad:danny` | Own a demo, adoption, feedback, or draft socialization task | Danny |
| `squad:virgil` | Own build, release, packaging, or developer infrastructure | Virgil |
| `squad:copilot` | Assign to @copilot for autonomous work (if enabled) | @copilot 🤖 |

### How Issue Assignment Works

1. When a GitHub issue gets the `squad` label, **Rusty (Lead)** triages it — analyzing content, evaluating @copilot's capability profile, assigning the right `squad:{member}` label, and commenting with triage notes.
2. **@copilot evaluation:** Rusty checks if the issue matches @copilot's capability profile (🟢 good fit / 🟡 needs review / 🔴 not suitable). If it's a good fit, Rusty may route to `squad:copilot` instead of a squad member.
3. When a `squad:{member}` label is applied, that member picks up the issue in their next session.
4. When `squad:copilot` is applied and auto-assign is enabled, `@copilot` is assigned on the issue and picks it up autonomously.
5. Members can reassign by removing their label and adding another member's label.
6. The `squad` label is the "inbox" — untriaged issues waiting for Rusty's review.

### Lead Triage Guidance for @copilot

When triaging, Rusty should ask:

1. **Is this well-defined?** Clear title, reproduction steps or acceptance criteria, bounded scope → likely 🟢
2. **Does it follow existing patterns?** Adding a test, fixing a known bug, updating a dependency → likely 🟢
3. **Does it need design judgment?** Architecture, API design, UX decisions → likely 🔴
4. **Is it security-sensitive?** Auth, encryption, access control → always 🔴
5. **Is it medium complexity with specs?** Feature with clear requirements, refactoring with tests → likely 🟡

## Rules

1. **One accountable owner** — dispatch the smallest sufficient specialist assignment. Add parallel work only for concrete independent deliverables.
2. **Bounded support** — Scribe records meaningful accepted decisions/handoffs only when needed; no automatic per-task logger or routine review fan-out.
3. **Quick facts → coordinator answers directly.** Don't spawn an agent for "what port does the server run on?"
4. **When two agents could handle it**, pick the one whose domain is the primary concern.
5. **"Team, ..." requests coordination, not an unlimited fan-out.** Identify owners and dependencies first; avoid duplicate work.
6. **Test planning follows scope.** Bring Basher in for concrete acceptance/test-design needs, not speculative downstream work.
7. **Issue-labeled work** — when a `squad:{member}` label is applied to an issue, route to that member. Rusty handles all `squad` (base label) triage.
8. **@copilot routing** — when evaluating issues, check @copilot's capability profile in `team.md`. Route 🟢 good-fit tasks to `squad:copilot`. Flag 🟡 needs-review tasks for PR review. Keep 🔴 not-suitable tasks with squad members.
9. **Documentation impact** — Changes to CLI, internal contracts, schemas, and dashboard UX must assess documentation impact using the matrix below. Saul owns that assessment; Livingston writes technical updates. Do not automatically add another mandatory reviewer.
10. **Selected gate** — Use one independent gate for ordinary work under `.squad/governance.md`. Saul normally gates documentation; Rusty normally gates code. Authors never self-approve.
11. **Publication is separate** — Danny drafts; Virgil prepares releases. Neither publishes, posts, tags, merges, or incurs paid eval usage without the applicable explicit authorization.

## Documentation Impact Matrix

| Path Changed | Doc Files to Check | What to Verify |
|---|---|---|
| `cmd/waza/` | README.md, docs/GUIDE.md, `site/src/content/docs/reference/cli.mdx` | Flags, defaults, exits, examples, and paid-call consent match |
| `internal/graders/`, `internal/scoring/`, `internal/requirements/` | README.md, docs/GUIDE.md, `site/src/content/docs/` | Grader/requirement semantics and examples match |
| `internal/evidence/`, `internal/snapshot/`, `internal/assurance/`, `internal/releasepolicy/` | docs/PRD.md, docs/GRADER-CHALLENGES.md, docs/CONTROLLED-COMPARISONS.md, `site/src/content/docs/` | Versions, availability, binding/claim limits, private vs public scope |
| `web/src/` | docs/GUIDE.md, docs/DEMO-GUIDE.md, `site/src/content/docs/`, docs/images/ | Screenshots, accessibility, real/demo separation, missing-data states |
| `schemas/`, `internal/models/` | README.md, docs/GUIDE.md, `site/src/content/docs/`, examples/ | Native source/schema parity, historical reader behavior, example versions |
| `install.sh` | README.md (Install), docs/GUIDE.md (Install) | Install instructions current |
| `install.ps1`, `internal/embedded/`, `.github/workflows/release.yml` | README.md, docs/RELEASE.md, `site/src/content/docs/` | Cross-platform installs, bundled runtime, release/azd distribution |
| `DEMO-SCRIPT.md`, release messaging | README.md, CHANGELOG.md, docs/DEMO-GUIDE.md | Runnable/current claims, audience, explicit approval before publication |
