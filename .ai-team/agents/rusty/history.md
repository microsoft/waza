# History — Rusty

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Architecture
- **Model selection directive (2026-02-18):** Coding in Claude Opus 4.6, reviews in GPT-5.3-Codex, design in Gemini Pro 3
- **Web UI styling:** Keep clean and functional — colors close to DevEx dashboard, no fancy gradients
- **Agent execution:** Go engine drives CLI, web UI for visualization

### Code Quality
- Test coverage is non-negotiable
- Interface-based design for flexibility (AgentEngine, Validator patterns in Go)
- Functional options for configuration (Go convention)

### Team Structure
- Linus owns Go backend implementation
- Basher owns all testing strategy
- Livingston/Saul own documentation
- Richard Park available for Copilot SDK questions

## Work Log

### 2025-07-25: #238 — True trajectory replay viewer (PR #243)
- **Branch:** `squad/238-trajectory-viewer`
- Full rewrite of `TrajectoryViewer.tsx` to consume real `TranscriptEvent` data
- Created `SessionDigestCard.tsx` (digest stats + tools used badges + errors)
- Created `ToolCallDetail.tsx` (expandable JSON viewers for args/result)
- Timeline: color-coded dots (blue=tool start, green/red=complete, emerald=turn, red=error)
- `toolCallId` correlation links Start ↔ Complete events
- Graceful fallback to grader-based heuristic when transcript is empty
- Depends on #237 (transcript + session digest in API)
