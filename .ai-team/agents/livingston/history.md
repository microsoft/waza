# Project Context

- **Owner:** Shayne Boyer (spboyer@live.com)
- **Project:** Waza — Go CLI for evaluating AI agent skills (scaffolding, compliance scoring, cross-model testing)
- **Project:** Waza — Go CLI for evaluating AI agent skills
- **Stack:** Go, Cobra CLI, Copilot SDK, YAML specs
- **Created:** 2026-02-09

## Learnings

<!-- Append new learnings below. Each entry is something lasting about the project. -->

### 2025-07-25 — Phase 1 GitHub Issue Comments

Posted dependency/blocker comments on 5 issues in `spboyer/waza`:

**Tagged @richardpark-msft:**
- **#23** (Cobra refactoring) — CRITICAL BLOCKER for #25, #26, #27, #46. Asked to prioritize.
- **#28** (All 8 grader types) — Dependency for eval framework #39-#46. Pointed to existing grader interface.
- **#29** (Copilot SDK executor) — Dependency for multi-model work #39. Noted AgentEngine interface contract.

**Tagged @chlowell:**
- **#33** (Compliance scoring) — Foundation for E2 Sensei Engine (#32, #34-#38). Suggested `internal/sensei/` package.
- **#47** (Token counting) — Foundation for E4 Token Management (#48-#51). Suggested `internal/tokens/` package.
📌 Team update (2026-02-12): PR #111 tokens compare command approved and merged. Closes #51 (E4). — decided by Rusty
<!-- Append new learnings below. -->
