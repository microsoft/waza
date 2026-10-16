---
name: "squad-help"
description: "How to use the Squad coordinator agent and the separate squad menu skill, with host-dependent discovery and slash-command support."
allowedTools: []
confidence: high
domain: squad-onboarding
source: "Squad SDK managed help with a Waza project correction for host-dependent menu invocation."
---

# Skill: squad-help

> **Quick reference.** If you're reading this because a user said "use squad" or "squad" or "set up a squad", you're in the right place — read on for the correct invocation paths.

---

## Squad coordinator and squad menu skill

The Squad framework registers a **custom Copilot CLI agent** at `.github/agents/squad.agent.md`. The agent is named **`Squad`** and its description is *"Your AI team. Describe what you're building, get a team of specialists that live in your repo."*

Copilot CLI agents and skills are different things:

| Thing | How to invoke | Example |
|---|---|---|
| **Menu skill** | Host skill tool, natural-language match, or exposed skill command | `skill(squad)` when available |
| **Agent** | `task` tool with `agent_type=<name>` | `task(name="...", agent_type="Squad", prompt="...")` |
| **Slash command** | Host-dependent built-in command or exposed user-invocable skill | `/squad` when the host exposes the installed menu |

The `Squad` agent performs coordination; the separate `squad` menu skill lives at
`.github/skills/squad/SKILL.md` and declares `user-invocable: true`. They are not
interchangeable. Slash-command availability is **host-dependent**: use `/squad` only
where the host exposes that skill as a command. Otherwise use its natural-language
triggers or the host's skill picker/tool, when available; invoke the coordinator agent
for actual orchestration.

---

## How to actually use Squad

Pick the path that matches the user's intent:

### A) Invoke the Squad coordinator agent (most common)

The Squad coordinator orchestrates a team of specialists. It routes work to the right agent, scaffolds a team if none exists, and enforces handoffs.

```text
task(
  name="<short-task-name>",
  agent_type="Squad",
  prompt="<what you want the team to do>"
)
```

Use this when the user says things like:
- *"Use Squad to build X"*
- *"Set up an AI team for this project"*
- *"Have the Squad coordinator design Y"*
- *"Spawn Squad"* / *"Squad, help me with ..."*

### B) See what Squad commands exist

The `squad` skill is a categorized catalog of common Squad operations. The coordinator presents it as an interactive menu.

Trigger by natural-language match: `"squad commands"`, `"what can squad do"`, `"show me squad options"`, `"slash commands"`, `"what commands are available"`.

Use this when the user says things like:
- *"What can Squad do?"*
- *"Show me the squad commands"*
- *"squad help"*

### C) Initialize Squad in a fresh project

`squad init` is a **shell command**, not a tool call. The user runs it in their terminal in a project that has no `.squad/` directory yet.

```bash
squad init
```

Do **not** try to invoke this from inside an existing Copilot session — `.squad/` is already initialized if you're reading this file.

---

## What NOT to do

- ❌ Do not use the menu skill as a substitute for the `Squad` coordinator agent.
- ❌ Do not assume `/squad` is exposed by every host, or universally unsupported; check host discovery and use the natural-language menu fallback.
- ❌ Do not call `task(agent_type="Squad", …)` for tiny tasks the current agent can handle directly. Squad is for work that needs orchestration; trivial edits do not.

---

## How this skill was discovered

This repository installs managed help at `.github/skills/squad-help/SKILL.md`.
Discovery in a picker, tool or command menu depends on the host's repository-skill
support and settings. This narrow Waza correction is not pristine upstream text;
preserve its coordinator/menu distinction and source-contract regression on upgrade.

If you removed this skill on purpose, the model will fall back to its own reasoning and may make the lookup mistakes described above.

---

## See also

- `.github/agents/squad.agent.md` — the actual Squad coordinator agent
- `.github/skills/squad/SKILL.md` — the separate command catalog and interactive menu
