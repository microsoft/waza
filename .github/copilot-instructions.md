# Copilot Coding Agent — Squad Instructions

You are working on a project that uses **Squad**, an AI team framework. When picking up issues autonomously or using Squad, follow these guidelines.

## Coordinator Canary Check

**IMPORTANT — run this check only when the session is expected to use Squad Coordinator behavior, before doing any Squad work:**

Look for the exact token `SQUAD_COORDINATOR_CANARY_a8f3` in your `agent_instructions`. This token is placed at the end of `squad.agent.md`. If it is missing, the coordinator file is missing or truncated.

Do not run this check for ordinary Copilot Coding Agent work, or when the user explicitly asks not to use Squad. In that case, proceed without Squad routing, spawning, PR, or branch-protection behavior.

**When the token is missing and Squad Coordinator behavior is required, you MUST:**
1. **STOP** — do not proceed with standard Squad behavior.
2. **WARN the user** with this exact message:
   ```
   ⚠️ Squad coordinator (squad.agent.md) appears to be missing or truncated. The canary token was not found. Do NOT proceed with standard Squad behavior — Squad's safety rails are not loaded. Please restart your session.
   ```
3. Do not continue with normal Squad routing, spawning, PR, or branch-protection behavior after emitting the warning.

## Team Context

Before starting work on any issue:

1. Read `.squad/team.md` for the team roster, member roles, and your capability profile.
2. Read `.squad/routing.md` for work routing rules.
3. If the issue has a `squad:{member}` label, read that member's charter at `.squad/agents/{member}/charter.md` to understand their domain expertise and coding style — work in their voice.

## Capability Self-Check

Before starting work, check your capability profile in `.squad/team.md` under the **Coding Agent → Capabilities** section.

- **🟢 Good fit** — proceed autonomously.
- **🟡 Needs review** — proceed, but note in the PR description that a squad member should review.
- **🔴 Not suitable** — do NOT start work. Instead, comment on the issue:
  ```
  🤖 This issue doesn't match my capability profile (reason: {why}). Suggesting reassignment to a squad member.
  ```

## Branch Naming

Use the squad branch convention:
```
squad/{issue-number}-{kebab-case-slug}
```
Example: `squad/42-fix-login-validation`

## PR Guidelines

When opening a PR:
- Reference the issue: `Closes #{issue-number}`
- If the issue had a `squad:{member}` label, mention the member: `Working as {member} ({role})`
- If this is a 🟡 needs-review task, add to the PR description: `⚠️ This task was flagged as "needs review" — please have a squad member review before merging.`
- Follow any project conventions in `.squad/decisions.md`

## Decisions

If you make a decision that affects other team members, write it to:
```
.squad/decisions/inbox/copilot-{brief-slug}.md
```
The Scribe will merge it into the shared decisions file.

## Repository Gotchas (learned from past sessions)

These come up repeatedly across sessions. Handle them proactively.

### 1. First `go build/test/lint` in a fresh worktree needs a `web/dist` stub

The Go binary embeds the dashboard via `//go:embed all:dist` (`web/embed.go`). In a fresh
worktree the built assets don't exist, so commands that compile packages including the
embedded dashboard assets fail until you scaffold them:

```bash
mkdir -p web/dist
if [ ! -f web/dist/index.html ]; then
  printf '<html><body>stub</body></html>\n' > web/dist/index.html
fi
if [ ! -f web/dist/favicon.svg ]; then
  printf '<svg xmlns="http://www.w3.org/2000/svg"></svg>\n' > web/dist/favicon.svg
fi
```

Run this **once** at the start of any session before `go build`, `go test`, or `golangci-lint run`.

### 2. Never restage a regenerated `web/dist/index.html`

`web/dist/assets/*` is gitignored but `web/dist/index.html` **is tracked**. Running
`cd web && npm run build` rewrites `index.html` with new hashed asset refs, which then
shows up as an unrelated diff. Before `git add`:

```bash
git diff -- web/dist/index.html   # if only hash refs changed, revert it
git checkout -- web/dist/index.html
```

Only commit `web/dist/index.html` changes when you intentionally shipped new dashboard code.

### 3. Standard verify chain: format → test → lint

`golangci-lint` fails on unformatted code, so always run `go fmt` first to avoid a
retry loop:

```bash
go fmt ./... && go test ./... && golangci-lint run
```

For the docs site and dashboard, add:

```bash
cd site && npm run build
cd ../web && npm run build && npm run lint
```

### 4. Clean up scratch dirs before committing

Agent tooling can leave `.impeccable/` in the worktree, and Node builds create
`web/node_modules/` and `site/node_modules/`. None of these should be staged. Before
`git add`:

```bash
rm -rf .impeccable web/node_modules site/node_modules
git status --short   # verify only intended files remain
```

`git add -A` from the repo root is risky — prefer explicit paths.
