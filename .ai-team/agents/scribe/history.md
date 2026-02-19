# History — Scribe

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Decision Recording
- **Format:** `.ai-team/decisions.md` is append-only (merge=union)
- **Inbox location:** `.ai-team/decisions/inbox/{agent-name}-{slug}.md`
- **Merge process:** Scribe reads inbox, appends to decisions.md, cleans up inbox
- **Merge driver:** Configured in .gitattributes for conflict-free merges

### Team Decisions Established
- **Model directive (2026-02-18):** Coding in Claude Opus 4.6, reviews in GPT-5.3-Codex, design in Gemini Pro 3
- **Web UI styling (2026-02-18):** Clean and functional, colors like DevEx dashboard
- **Test coverage:** Non-negotiable
- **Code review:** Required before merge

### Session Structure
- Lead (Rusty) makes architectural decisions
- Team members execute assigned work
- Decisions captured in inbox, merged by Scribe
