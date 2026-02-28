# Work Routing

How to decide who handles what.

## Routing Table

| Work Type | Route To | Examples |
|-----------|----------|----------|
| Go backend / CLI commands | Linus | Internal packages, API endpoints, CLI flags, execution engine |
| React / frontend / Web UI | Rusty | Components, pages, styling, state management, Vite config |
| Testing / QA | Basher | Go unit tests, integration tests, Playwright E2E tests, test infrastructure |
| Documentation | Livingston, Saul | README, docs/, changelogs, API docs, design notes |
| Go performance / optimization | Turk | Profiling, allocation analysis, concurrency, I/O optimization, Azure SDK tuning |
| Architecture / Design decisions | Rusty | System design, component architecture, API contracts, large refactors |
| Copilot SDK integration | Richard Park 👤 | SDK usage, API contracts, integration patterns |
| Backend overflow | Charles Lowell 👤 | Secondary backend review, overflow capacity |
| Code review | Rusty | Review all PRs, check quality, suggest improvements |
| Scope & priorities | Rusty | What to build next, trade-offs, epic planning |
| Async issue work (bugs, tests, small features) | @copilot 🤖 | Well-defined tasks matching capability profile |
| Session logging | Scribe | Automatic — never needs routing |

## Issue Routing

| Label | Action | Who |
|-------|--------|-----|
| `squad` | Triage: analyze issue, evaluate @copilot fit, assign `squad:{member}` label | Rusty (Lead) |
| `squad:rusty` | Pick up issue and complete the work | Rusty |
| `squad:linus` | Pick up issue and complete the work | Linus |
| `squad:basher` | Pick up issue and complete the work | Basher |
| `squad:livingston` | Pick up issue and complete the work | Livingston |
| `squad:saul` | Pick up issue and complete the work | Saul |
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

1. **Eager by default** — spawn all agents who could usefully start work, including anticipatory downstream work.
2. **Scribe always runs** after substantial work, always as `mode: "background"`. Never blocks.
3. **Quick facts → coordinator answers directly.** Don't spawn an agent for "what port does the server run on?"
4. **When two agents could handle it**, pick the one whose domain is the primary concern.
5. **"Team, ..." → fan-out.** Spawn all relevant agents in parallel as `mode: "background"`.
6. **Anticipate downstream work.** If a feature is being built, spawn Basher to write test cases simultaneously.
7. **Issue-labeled work** — when a `squad:{member}` label is applied to an issue, route to that member. Rusty handles all `squad` (base label) triage.
8. **@copilot routing** — when evaluating issues, check @copilot's capability profile in `team.md`. Route 🟢 good-fit tasks to `squad:copilot`. Flag 🟡 needs-review tasks for PR review. Keep 🔴 not-suitable tasks with squad members.
9. **Doc-review gate** — When a PR touches `cmd/waza/`, `internal/`, or `web/src/`, Saul reviews whether documentation needs updating. Check: are new flags documented? Are screenshots still accurate? Do examples still work?
10. **Doc-consistency gate** — When a PR touches `docs/`, `README.md`, `DEMO-SCRIPT.md`, or any markdown documentation file, Saul reviews for style consistency, cross-references, and accuracy.

## Documentation Impact Matrix

| Path Changed | Doc Files to Check | What to Verify |
|---|---|---|
| `cmd/waza/*.go` | README.md (Commands), docs/GUIDE.md | New flags documented, examples updated |
| `internal/scoring/` | README.md (Validators), docs/GUIDE.md | Validator docs match implementation |
| `web/src/` | docs/GUIDE.md (Dashboard), docs/DEMO-GUIDE.md | Screenshots current, view descriptions match |
| `schemas/` | README.md (YAML Schema), docs/GUIDE.md | Schema examples match definitions |
| `install.sh` | README.md (Install), docs/GUIDE.md (Install) | Install instructions current |
