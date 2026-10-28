# Team Roster

> CLI tool for evaluating Agent Skills (Go primary, React web UI)

## Project Context

- **Owner:** Shayne Boyer
- **Stack:** Go (primary), TypeScript/React 19, Tailwind CSS v4, Vite, Playwright
- **Description:** waza evaluates how well AI agents can perform complex coding tasks
- **Repository:** microsoft/waza
- **Universe:** Ocean's Eleven (existing names preserved; metadata corrected 2026-10-15)
- **Current team policy:** `.squad/governance.md`
- **Current model policy:** `.squad/model-policy.md`; assignments persist in `.squad/config.json`

## Coordinator

| Name | Role | Notes |
|------|------|-------|
| Squad | Coordinator | Routes work, enforces handoffs and reviewer gates. Does not generate domain artifacts. |

## Members

| Name | Role | Charter | Preferred Model | Status |
|------|------|---------|-----------------|--------|
| Rusty | Lead / Architect | `.squad/agents/rusty/charter.md` | `gpt-5.6-terra` | ✅ Active |
| Linus | Go Backend & Evaluation Runtime | `.squad/agents/linus/charter.md` | `gpt-6.1-sol` | ✅ Active |
| Basher | Test & Evaluation Quality | `.squad/agents/basher/charter.md` | `gpt-6.1-sol` | ✅ Active |
| Livingston | Technical Documentation & Examples | `.squad/agents/livingston/charter.md` | `gpt-6-luna` | ✅ Active |
| Saul | Documentation Lead / Information Architecture | `.squad/agents/saul/charter.md` | `gpt-6-luna` | ✅ Active |
| Turk | Go Performance & Reliability | `.squad/agents/turk/charter.md` | `gpt-5.6-terra` | ✅ Active |
| Yen | Web Design & Frontend | `.squad/agents/yen/charter.md` | `gpt-6.1-sol` | ✅ Active |
| Danny | Developer Relations & Socialization | `.squad/agents/danny/charter.md` | `gpt-6-luna` | ✅ Active |
| Virgil | Release & Developer Infrastructure | `.squad/agents/virgil/charter.md` | `gpt-6.1-sol` | ✅ Active |

## Built-in Support

Support identities are not specialist issue-routing destinations. Activate them
only for bounded work under `.squad/governance.md`.

| Name | Role | Charter | Preferred Model |
|------|------|---------|-----------------|
| Scribe | Session Logger | `.squad/agents/scribe/charter.md` | `gpt-6-luna` |
| Ralph | Work Monitor | `.squad/agents/ralph/charter.md` | `gpt-6-luna` |
| Rai | Responsible AI Reviewer | `.squad/agents/rai/charter.md` | `gpt-5.6-terra` |
| Fact Checker | Evidence & Claim Verification | `.squad/agents/fact-checker/charter.md` | `gpt-5.6-terra` |

## AI-only Membership

Squad members and work-routing targets are AI agents only. Do not assign issues
or implementation consultation to humans. Linus covers Copilot SDK and backend
implementation; Rusty covers architecture and independent review, with Basher
covering test strategy. No additional specialist is needed.

Shayne Boyer remains the project owner/requester. Repository collaborators,
CODEOWNERS, required native approvals, and historical records are unchanged.
Existing reviewer/author lockouts remain in force.

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

- **Team and models (2026-10-15):** GPT-first assignments, independent Terra code reviews, dedicated frontend, DevRel, and release owners. Supersedes earlier model assignments, not historical project decisions.
- **Web UI styling (2026-02-18):** Dashboard colors close to DevEx Token Efficiency Benchmarks, no fancy gradients
