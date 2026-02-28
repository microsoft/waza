# Team Roster

> CLI tool for evaluating Agent Skills (Go primary, React web UI)

## Project Context

- **Owner:** Shayne Boyer
- **Stack:** Go (primary), TypeScript/React 19, Tailwind CSS v4, Vite, Playwright
- **Description:** waza evaluates how well AI agents can perform complex coding tasks
- **Repository:** spboyer/waza
- **Universe:** The Usual Suspects

## Coordinator

| Name | Role | Notes |
|------|------|-------|
| Squad | Coordinator | Routes work, enforces handoffs and reviewer gates. Does not generate domain artifacts. |

## Members

| Name | Role | Charter | Status |
|------|------|---------|--------|
| Rusty | Lead / Architect | `.squad/agents/rusty/charter.md` | ✅ Active |
| Linus | Backend Developer | `.squad/agents/linus/charter.md` | ✅ Active |
| Basher | Tester / QA | `.squad/agents/basher/charter.md` | ✅ Active |
| Livingston | Documentation Specialist | `.squad/agents/livingston/charter.md` | ✅ Active |
| Saul | Documentation Lead | `.squad/agents/saul/charter.md` | ✅ Active |
| Turk | Go Performance Specialist | `.squad/agents/turk/charter.md` | ✅ Active |
| Scribe | Session Logger | `.squad/agents/scribe/charter.md` | 📋 Silent |
| Ralph | Work Monitor | — | 🔄 Monitor |

## Human Members

| Name | Handle | Role | Notes |
|------|--------|------|-------|
| Richard Park | @richardpark-msft | Copilot SDK Expert | 👤 Human |
| Charles Lowell | @chlowell | Backend Developer | 👤 Human |
| Wallace Breza | @wbreza | — | 👤 Human |

## Coding Agent

<!-- copilot-auto-assign: false -->

| Name | Role | Charter | Status |
|------|------|---------|--------|
| @copilot | Coding Agent | — | 🤖 Coding Agent |

### Capabilities

**🟢 Good fit — auto-route when enabled:**
- Bug fixes with clear reproduction steps
- Test coverage (adding missing tests, fixing flaky tests)
- Lint/format fixes and code style cleanup
- Dependency updates and version bumps
- Small isolated features with clear specs
- Boilerplate/scaffolding generation
- Documentation fixes and README updates

**🟡 Needs review — route to @copilot but flag for squad member PR review:**
- Medium features with clear specs and acceptance criteria
- Refactoring with existing test coverage
- CLI command additions following established patterns
- Internal package additions following established patterns

**🔴 Not suitable — route to squad member instead:**
- Architecture decisions and system design
- Multi-system integration requiring coordination
- Ambiguous requirements needing clarification
- Security-critical changes (auth, encryption, access control)
- Performance-critical paths requiring benchmarking
- Changes requiring cross-team discussion

## Key Decisions

See `.squad/decisions.md` for team decisions. Notable:

- **Model selection (2026-02-18):** Coding in Claude Opus 4.6 (premium), reviews in GPT-5.3-Codex, design in Gemini Pro 3
- **Web UI styling (2026-02-18):** Dashboard colors close to DevEx Token Efficiency Benchmarks, no fancy gradients
