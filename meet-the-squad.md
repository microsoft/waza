# Meet the Waza Squad

Waza's team is configured for the current Go CLI/evaluation runtime, React
dashboard, opt-in Dashboard Lab, Astro/Starlight documentation, and cross-platform
distribution. The canonical roster and exact GPT model IDs are in
[team.md](.squad/team.md); use [routing.md](.squad/routing.md) to assign work.

Rusty owns architecture and independent code review. Linus owns Go/runtime
implementation, Basher test/evaluation quality, Turk measured performance, Yen web
design/frontend/accessibility, Livingston technical documentation/examples, Saul
documentation architecture, Danny developer relations/socialization drafts,
Tess reproducible release-media capture/edit/export, and Virgil
release/developer infrastructure. Scribe, Ralph, Rai, and Fact Checker are
bounded support identities, not specialist issue owners.

Squad membership and implementation routing are AI-only. Linus covers Copilot SDK
and backend responsibilities; Rusty covers architecture and independent review,
with Basher covering test strategy. No human issue assignments, implementation
consultations, or overflow handoffs are configured. Coverage gaps go to the
coordinator, not a human fallback. Existing AI identities, model assignments,
and reviewer/author lockouts are preserved. Tess is an AI media specialist, not
a Go revision owner or a replacement for existing product owners.
This does not remove repository collaborators, change CODEOWNERS or required
native approvals, or change Shayne Boyer's project-owner/requester role.
Historical decisions and member records remain intact.

[Governance](.squad/governance.md) gives each task one accountable owner and a
bounded independent gate. [Model policy](.squad/model-policy.md) applies GPT-first
role assignments and pins code-review passes to GPT-5.6 Terra. The config does not
override a host that cannot select per-agent models or grant access to a model.
Danny drafts announcements; publishing still requires explicit approval.

Every release has a [gated media task](docs/RELEASE-MEDIA.md), created or reused
at a manual release-checklist checkpoint. Tess uses `gpt-6.1-sol` for capture and
exports; Danny uses `gpt-6-luna` for narrative and copy; independent technical
review uses `gpt-5.6-terra`. Actual footage waits for verified readiness. These
are role-fit preferences, not proof of runtime enforcement or benchmark rankings;
the process neither auto-creates issues nor authorizes publication.

## Verify the installation

Use the pinned CLI rather than assuming a shared/global `squad` installation is
current. Node.js 22.5 or newer is required.

```bash
npm exec --yes --package=@bradygaster/squad-cli@1.0.0 -- squad doctor
npm exec --yes --package=@bradygaster/squad-cli@1.0.0 -- squad health --json
node --test .github/scripts/ralph-triage.test.cjs
node --test .github/scripts/squad-state.test.cjs
node --test .github/scripts/squad-worktree.test.cjs
node --test .github/scripts/squad-casting.test.cjs .github/scripts/squad-triage.test.cjs
node --test .github/scripts/squad-governance.test.cjs
```

The coordinator and managed templates are Squad 1.0.0; `.mcp.json` pins the same
package for `squad_state`. The repository has no new root Node application or
global CLI installation requirement.

## Upgrade without losing Waza configuration

Virgil owns upgrade integration. Check npm for the requested stable release;
a GitHub tag alone does not prove the corresponding npm package exists.
Preview with the chosen package version, for example:

```bash
npm exec --yes --package=@bradygaster/squad-cli@1.0.0 -- squad upgrade --dry-run
```

Before applying an upgrade, preserve user-owned state and inspect the proposed
changes. The upgrader overwrites all eleven Squad workflows, can replace Go CI
and release commands with generic stubs, and rewrites contributor instructions.
Keep Waza's working build/release commands, triggers, action pins, permissions,
and contributor safety guidance; do not accept generated no-op workflows.

Reapply the small project model-policy hook in
`.github/agents/squad.agent.md` and preserve the contributor hook in
`.github/copilot-instructions.md`. Keep HEAD/EOF canaries and the pinned MCP
package. Generic managed templates remain upstream-owned; do not maintain a
second hand-edited model catalog. The narrow project repairs to casting mutation
procedures are an explicit exception: see
[casting transactions](.squad/casting-transactions.md). Preserve these overrides
and their actual locked-writer tests; do not restore registry-only write advice. Re-render generated Team Capabilities with the
SDK after changing the roster, without rerunning the destructive workflow refresh.

The installed standalone triage script includes Waza-local routing and GitHub API
fixes. Preserve them when refreshing managed assets unless the upstream release
already contains them. The regression command above runs in Squad Tools CI and
checks all specialist routes, ambiguous/duplicate matches, and public/Enterprise API defaults.
The active `issues:labeled` handler in `.github/workflows/squad-triage.yml` now
calls `.github/scripts/squad-triage.cjs`, which uses that same shared router.
This is the deliberate exception to preserving the eleven existing customized
workflows unchanged; the other ten remain unchanged. It fixes substring routing
such as `ui` inside `Build Docker image`, which must route to Virgil, not Yen.
Actual workflow-script tests retain the event/label gate, existing-owner labels,
coding-agent capability/assignment opt-in, review warning, and API-failure guards.
Fallback and ambiguous matches use the handler's already-resolved team lead,
including a Coordinator, rather than a second Lead/Architect-only lookup.
Coding-agent routing reads the actual `### Capabilities` profile: bold multiline
tiers and inline comma-separated keywords are supported with LF or CRLF.
Bullet text and its leading task subjects supply keywords; short task names
such as `bug fix` remain supported through simple word/plural normalization.
Red-tier parenthetical examples are also exclusion keywords: `auth`, `encryption`
and `access control` block coding-agent routing even alongside `bug fix`.
Unsuitable matches win over review matches, which win over good-fit matches.
Commas inside parentheses do not split tier entries. Parenthetical
examples or qualifying `with`/`following` prose in positive tiers do not grant
standalone eligibility (for example, `acceptance criteria`); unbalanced
or nested parentheses fail closed instead of dropping exclusions. The same
rules apply to inline and multiline profiles and preserve base task subjects.
This remains keyword routing, not proof of semantic eligibility. Missing,
empty, malformed or duplicate tiers disable coding-agent routing and assignment
with a diagnostic; there are no hardcoded default capability lists.
The generic router can still parse human roster rows, but Waza's active roster
and policy configure AI targets only; do not add human fallback rows. Assigning
`copilot` still requires explicit opt-in and no existing assignee. Existing
human assignees are left untouched. Real-roster workflow tests exercise
every declared bullet and both ambiguous-requirements/multi-system exclusions.
On Ubuntu and native Windows it also checks role-model agreement and a real Git
`core.autocrlf=true` checkout: the hash-bound casting files must stay LF while a
control file converts to CRLF. CI fetches the integrity-pinned SDK 1.0.0 tarball
without installing dependencies or executing package scripts, then uses its real
`readCastingRegistryPair` reader to accept the checkout and reject CRLF corruption.
For the same SDK check locally, set `SQUAD_SDK_READER` to that package's
`dist/casting/durable-registry.js`; without it the SDK-dependent checks report
skips. CI configures the real reader and requires zero skips, including in the
linked-worktree child suite.
Preserve the casting LF rules in `.gitattributes` and do not regenerate the commit
manifest merely to accommodate checkout line-ending conversion.
The worktree regression creates an isolated repository and a genuine linked Git
worktree, then runs the original 31 triage/state checks plus the paired-mutation
and actual-workflow-handler regressions there, including the SDK checks when
configured. Both CI platforms run it. Test scratch directories are bounded
under the actual checkout root and cleaned afterward; `.git` may be a file, not
a directory, in a linked worktree.

Reviewer seeding and rejection procedures also have narrow Waza project repairs,
not pristine upstream bytes. Preserve the Fact Checker/Rai policy templates,
active/template coordinator boundaries, reviewer skills and gate-wiring examples
on upgrade: concrete counter-evidence, a different qualified revision owner, and
focused rereview without locked-out-author or rejecting-gate pairing. Init Mode
seeds both reviewers' repaired policy and charter together. Successful focused rereview of
an independently corrected revision may return approval/PASS, never native repository
approval or merge authority. Charter output restrictions override policy and generic
logging/inbox directions: reviewers return findings; only an authorized coordinator or
Scribe handles required persistence. These are Squad-only rules, not
ordinary Copilot activation. Source-contract tests exercise the actual Init Mode
seed declarations and a bounded role/policy/caller consistency matrix, rejecting contradictory
ownership, pairing, evidence, gate-clearance and reviewer-persistence instructions.

Scribe procedures are also a narrow project override, not pristine upstream bytes.
Only the Coordinator accepts decisions; it may delegate mechanical merges of explicit
accepted inbox IDs to Scribe. Active/template hierarchies and the on-demand after-agent
procedure preserve that distinction. The decision-only spawn does not write logs,
specialist histories or durable health reports; it returns its merge result to the
Coordinator. Backend-aware archival safety remains a separate Coordinator responsibility.
Preserve these boundaries and their positive/negative source regressions on upgrade.

Production routing also retains specific red-tier subjects before `needing` or
`requiring` clauses, so abbreviated ambiguous-requirements and multi-system-integration
requests cannot become Copilot good-fit assignments. Generic one-word subjects such as
`Changes` are not derived as exclusions, and positive-tier explanations never grant eligibility.
Handler regressions cover abbreviated titles/bodies, inline/multiline profiles, both
line endings and assignment settings, plus ordinary bug-fix and generic-subject controls.

Managed help/menu invocation has a project correction too: the `Squad` coordinator
agent is distinct from the installed `squad` menu skill. Slash invocation is host-dependent;
help, menu metadata and both coordinator copies retain natural-language fallbacks.

Verify config/charter/roster agreement, active casting identities and commit
manifest integrity, required support charters, live references, and unchanged
historical member files. Run the doctor and health commands above. Backups,
transaction scratch files, and local machine paths must not enter the commit.

The pre-1.0 casting files are archived verbatim under
`.squad/casting/legacy-v0.10.0/`; they are history, not alternative live registries.
Their immutable bytes are checkout-conversion-disabled. SDK preparation rejects
these files before a mutation callback can run: follow the transaction guide's
explicit, approved **archive-and-initialize** procedure, not in-place callback
normalization. It requires stopped old readers/writers, a verified durable
archive, a genuinely empty new active target, and disclosure of downtime,
non-atomic archive steps and limited historical/provenance continuity. Corrupt
managed pairs are never automatically treated as legacy.
