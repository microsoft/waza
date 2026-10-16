---
name: "agent-collaboration"
description: "Standard collaboration patterns for all squad agents — worktree awareness, decisions, cross-agent communication"
domain: "team-workflow"
confidence: "high"
source: "extracted from charter boilerplate, with Waza's governance-aligned reviewer rejection override"
---

## Context

Every agent on the team follows identical collaboration patterns for worktree awareness, decision recording, and cross-agent communication. These were previously duplicated in every charter's Collaboration section (~300 bytes × 18 agents = ~5.4KB of redundant context). Now centralized here.

The coordinator's spawn prompt already instructs agents to read decisions.md and their history.md. This skill adds the patterns for WRITING decisions and requesting help.

## Patterns

### Worktree Awareness
Use the `TEAM ROOT` path provided in your spawn prompt. All `.squad/` paths are relative to this root. If TEAM ROOT is not provided (rare), run `git rev-parse --show-toplevel` as fallback. Never assume CWD is the repo root.

### Decision Recording
After making a decision that affects other team members, write it to:
`.squad/decisions/inbox/{your-name}-{brief-slug}.md`

Format:
```
### {date}: {decision title}
**By:** {Your Name}
**What:** {the decision}
**Why:** {rationale}
```

### Cross-Agent Communication
If you need another team member's input, say so in your response. The coordinator will bring them in. Don't try to do work outside your domain.

### Reviewer Protocol
If you have reviewer authority and reject work: include concrete counter-evidence,
the violated requirement, affected scope, and minimum condition for approval.
The Coordinator assigns a different qualified revision owner, not the locked-out
author or rejecting gate. Do not implement, pair on, or advise the immediate
revision; perform only the focused rereview. This is Squad coordination, not an
ordinary Copilot workflow.

## Anti-Patterns
- Don't read all agent charters — you only need your own context + decisions.md
- Don't write directly to `.squad/decisions.md` — always use the inbox drop-box
- Don't modify other agents' history.md files — that's Scribe's job
- Don't assume CWD is the repo root — always use TEAM ROOT
