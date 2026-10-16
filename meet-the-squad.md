# Meet the Waza Squad

Waza's team is configured for the current Go CLI/evaluation runtime, React
dashboard, opt-in Dashboard Lab, Astro/Starlight documentation, and cross-platform
distribution. The canonical roster and exact GPT model IDs are in
[team.md](.squad/team.md); use [routing.md](.squad/routing.md) to assign work.

Rusty owns architecture and independent code review. Linus owns Go/runtime
implementation, Basher test/evaluation quality, Turk measured performance, Yen web
design/frontend/accessibility, Livingston technical documentation/examples, Saul
documentation architecture, Danny developer relations/socialization drafts, and
Virgil release/developer infrastructure. Scribe, Ralph, Rai, and Fact Checker are
bounded support identities, not specialist issue owners.

[Governance](.squad/governance.md) gives each task one accountable owner and a
bounded independent gate. [Model policy](.squad/model-policy.md) applies GPT-first
role assignments and pins code-review passes to GPT-5.6 Terra. The config does not
override a host that cannot select per-agent models or grant access to a model.
Danny drafts announcements; publishing still requires explicit approval.

## Verify the installation

Use the pinned CLI rather than assuming a shared/global `squad` installation is
current. Node.js 22.5 or newer is required.

```bash
npm exec --yes --package=@bradygaster/squad-cli@1.0.0 -- squad doctor
npm exec --yes --package=@bradygaster/squad-cli@1.0.0 -- squad health --json
node --test .github/scripts/ralph-triage.test.cjs
node --test .github/scripts/squad-state.test.cjs
node --test .github/scripts/squad-worktree.test.cjs
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
second hand-edited model catalog. Re-render generated Team Capabilities with the
SDK after changing the roster, without rerunning the destructive workflow refresh.

The installed standalone triage script includes Waza-local routing and GitHub API
fixes. Preserve them when refreshing managed assets unless the upstream release
already contains them. The regression command above runs in Squad Tools CI and
checks all specialist routes, ambiguous/duplicate matches, and public/Enterprise API defaults.
On Ubuntu and native Windows it also checks role-model agreement and a real Git
`core.autocrlf=true` checkout: the hash-bound casting files must stay LF while a
control file converts to CRLF. CI fetches the integrity-pinned SDK 1.0.0 tarball
without installing dependencies or executing package scripts, then uses its real
`readCastingRegistryPair` reader to accept the checkout and reject CRLF corruption.
For the same SDK check locally, set `SQUAD_SDK_READER` to that package's
`dist/casting/durable-registry.js`; without it only the SDK subtest is skipped.
Preserve the casting LF rules in `.gitattributes` and do not regenerate the commit
manifest merely to accommodate checkout line-ending conversion.
The worktree regression creates an isolated repository and a genuine linked Git
worktree, then runs the same 31 triage/state checks there, including the SDK check
when configured. Both CI platforms run it. Test scratch directories are bounded
under the actual checkout root and cleaned afterward; `.git` may be a file, not
a directory, in a linked worktree.

Verify config/charter/roster agreement, active casting identities and commit
manifest integrity, required support charters, live references, and unchanged
historical member files. Run the doctor and health commands above. Backups,
transaction scratch files, and local machine paths must not enter the commit.

The pre-1.0 casting files are archived verbatim under
`.squad/casting/legacy-v0.10.0/`; they are history, not alternative live registries.
