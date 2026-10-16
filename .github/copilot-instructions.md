# Repository-wide Copilot Instructions

Read [AGENTS.md](../AGENTS.md) before changing this repository. Its
[review-derived standards](../AGENTS.md#review-derived-engineering-standards),
[required checks](../AGENTS.md#regression-evidence-and-required-checks), and
[completion gates](../AGENTS.md#required-review-and-completion-gates) apply to
ordinary Copilot work as well as Squad work. Keep detailed standards there;
this file is the execution checklist, not a second specification.

## Before handing off any change

- Trace changed values through every affected consumer: native loaders,
  eval/task/checkpoint schemas, preflight, execution/resume, cache keys,
  run/regrade serialization, API, UI, exports, and documentation. Reuse shared
  effective-config/path/tool-name helpers; check overrides and disabled paths.
- Preserve omitted/null/empty/zero/false distinctions and intentional legacy
  compatibility. Keep artifact reader defaults and writer versions separate.
  Do not invent missing scores, billing, provenance, or verified evidence.
- Keep offline/static validation free of model, subprocess, ambient-config, and
  external-schema side effects. Aggregate independent diagnostics; unresolved
  prerequisites must not hide invalid configuration. Failures must propagate
  to final status/exit policy, not only an error message.
- For filesystem changes, test containment, symlink/parent replacement,
  exclusive creation, and write/close/cleanup failures. Retain owned handles
  for guarantees that require them; never delete competing/user-owned files or
  claim transaction, durability, or platform support that is not implemented.
- Add a regression through the production caller, not just the helper. Prove
  fixtures/setup exercise the intended case and assert the actual contract,
  including negative and alternative-valid cases. Historical compatibility
  needs a frozen oracle; two matching new outputs prove only repeatability.
- Review dependency updates for caller API changes, peer/engine minimums,
  coordinated modules, CI/deploy runtimes, lockfiles, and embedded SDK/CLI
  bundles. A package version bump is not automatically integration-safe.
- Run the applicable checks in `AGENTS.md` and record exact commands/results
  against the tested head/platform. Missing tools, skipped jobs/test families,
  cross-compilation, and helper-only smoke tests are not execution evidence.
  Instruction-only prose does not require Go/frontend builds.
- Read review bodies and conversation comments as well as inline threads,
  including **Previously missed** and suppressed findings. Do not equate
  "0 open findings" or green CI with completed review. Verify fixes before
  resolving threads; document demonstrated false positives or agreed deferrals.
- Keep the PR template, scope, docs/examples, and current generated output in
  sync with the full final diff, including stacked changes. Recheck after the
  last source/base update; obtain independent approval and required checks
  without silently bypassing branch protection.

## Squad-specific Instructions

You are working on a project that uses **Squad**, an AI team framework. When picking up issues autonomously or using Squad, follow these guidelines.

### Current Waza Team Policy

The current roster, ownership, and model assignments are in `.squad/team.md`,
`.squad/routing.md`, `.squad/governance.md`, and `.squad/model-policy.md`.
Follow these over historical model directives in decisions or member histories.
Code-review agent passes use `gpt-5.6-terra`; do not silently substitute the
authoring model or another provider. Other Squad assignments use the persisted
role settings in `.squad/config.json`. Hosts without model control must report
that limitation rather than claiming these preferences were enforced.

These files configure Squad; they do not turn ordinary Copilot work into a Squad
Coordinator session. The canary check below remains scoped to actual Squad sessions.

### Coordinator Canary Check

**IMPORTANT — run this check only when the session is expected to use Squad Coordinator behavior, before doing any Squad work:**

Look for the exact token `SQUAD_COORDINATOR_CANARY_a8f3` in your `agent_instructions`. This token is placed at the end of `squad.agent.md`. If it is missing, the coordinator file is missing or truncated.

Do not run this check for ordinary Copilot Coding Agent work, or when the user explicitly asks not to use Squad. In that case, proceed without Squad-specific routing, spawning, PR, or branch-protection behavior. Repository-wide protections still apply.

**When the token is missing and Squad Coordinator behavior is required, you MUST:**
1. **STOP** — do not proceed with standard Squad behavior.
2. **WARN the user** with this exact message:
   ```
   ⚠️ Squad coordinator (squad.agent.md) appears to be missing or truncated. The canary token was not found. Do NOT proceed with standard Squad behavior — Squad's safety rails are not loaded. Please restart your session.
   ```
3. Do not continue with normal Squad routing, spawning, PR, or branch-protection behavior after emitting the warning.

### Team Context

Before starting work on any issue:

1. Read `.squad/team.md` for the team roster, member roles, and your capability profile.
2. Read `.squad/routing.md` for work routing rules.
3. If the issue has a `squad:{member}` label, read that member's charter at `.squad/agents/{member}/charter.md` to understand their domain expertise and coding style — work in their voice.

### Capability Self-Check

Before starting work, check your capability profile in `.squad/team.md` under the **Coding Agent → Capabilities** section.

- **🟢 Good fit** — proceed autonomously.
- **🟡 Needs review** — proceed, but note in the PR description that a squad member should review.
- **🔴 Not suitable** — do NOT start work. Instead, comment on the issue:
  ```
  🤖 This issue doesn't match my capability profile (reason: {why}). Suggesting reassignment to a squad member.
  ```

### Branch Naming

Use the squad branch convention:
```
squad/{issue-number}-{kebab-case-slug}
```
Example: `squad/42-fix-login-validation`

### PR Guidelines

When opening a PR:
- Reference the issue: `Closes #{issue-number}`
- If the issue had a `squad:{member}` label, mention the member: `Working as {member} ({role})`
- If this is a 🟡 needs-review task, add to the PR description: `⚠️ This task was flagged as "needs review" — please have a squad member review before merging.`
- Follow any project conventions in `.squad/decisions.md`

### Decisions

If you make a decision that affects other team members, write it to:
```
.squad/decisions/inbox/copilot-{brief-slug}.md
```
The Scribe will merge it into the shared decisions file.

## Repository Gotchas (learned from past sessions)

These come up repeatedly across sessions. Handle them proactively.

### 1. Check embedded dashboard assets before Go commands

The Go binary embeds the dashboard via `//go:embed all:dist` (`web/embed.go`).
If there are no embeddable files, compilation fails. Create only missing stubs
when needed for compilation:

```bash
mkdir -p web/dist
if [ ! -f web/dist/index.html ]; then
  printf '<html><body>stub</body></html>\n' > web/dist/index.html
fi
if [ ! -f web/dist/favicon.svg ]; then
  printf '<svg xmlns="http://www.w3.org/2000/svg"></svg>\n' > web/dist/favicon.svg
fi
```

Never overwrite existing assets. Stubs satisfy embedding, not the full asset
tests or dashboard delivery.
If tests require hashed files referenced by the tracked index, build the matching
dashboard and inspect the resulting diff.

### 2. Preserve dashboard builds and existing worktree edits

`web/dist/assets/*` is gitignored but `web/dist/index.html` **is tracked**. Running
`cd web && npm run build` rewrites `index.html` with new hashed asset refs, which then
shows up as a diff. Before `git add`, inspect it:

```bash
git diff -- web/dist/index.html
git status --short
```

Only commit `web/dist/index.html` changes when intentionally delivering a
dashboard change, with the matching physical embedded assets. For an unrelated
build, restore only your own generated changes to their pre-build bytes; preserve
pre-existing edits and ask if ownership is unclear. Do not blindly check out the
index from Git or force-add ignored bundles.

### 3. Standard verify chain: format → test → lint

Format only changed Go files with `gofmt -w`, then use the
[change-specific check matrix](../AGENTS.md#regression-evidence-and-required-checks).
Do not pass `./...` to `gofmt`; it is a Go package pattern, not a filesystem path.
Run real lint: `make lint` can return success without running it when the binary
is missing. Both CI lint jobs must retain the checksum-pinned schema validation
in `.github/scripts/lint.sh`.

For dashboard/site changes, build and execute the applicable Chromium suites;
verify newly added specs are selected by CI. Build success alone does not prove
browser behavior, navigation, exports, or current screenshots.

### 4. Clean up scratch dirs before committing

Agent tooling can leave `.impeccable/` in the worktree, and Node builds create
`web/node_modules/` and `site/node_modules/`. Do not stage these. Inspect the full
tracked and untracked diff before staging explicit paths:

```bash
git status --short
git diff --stat
git diff --check
```

Remove only identified scratch artifacts created by this task, not pre-existing
work or installed dependencies indiscriminately. Avoid root-level `git add -A`;
stage only the intended paths.
