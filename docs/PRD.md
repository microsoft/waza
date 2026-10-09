# Waza Skills Development Platform - Product Requirements Document

**Status:** Active  
**Version:** 1.0  
**Last Updated:** 2026-02-06  
**Owner:** @spboyer  
**Source:** [Squad Proposal](https://github.com/spboyer/azure-mcp-v-skills/blob/main/squad-proposal.md)

---

## Executive Summary

Waza (技 - Japanese for "skill/technique") is a unified CLI platform for creating, testing, and evaluating AI agent skills. It consolidates existing skill development tools into a single binary that provides the complete developer experience for contributing to [microsoft/skills](https://github.com/microsoft/skills).

### The Problem

The microsoft/skills repository hosts 132+ skills for AI coding agents, but the contribution process lacks automated tooling for:

- **Compliance validation** — No standardized scoring before PR submission
- **Trigger testing** — Manual verification of skill activation patterns
- **Cross-model evaluation** — No framework for testing skills across GPT-4o, Claude, etc.
- **Token budget enforcement** — Guidelines exist but aren't automatically checked

### The Solution

A single `waza` CLI built in **Go** that automates the skill development workflow:

| Phase | Capability |
|-------|------------|
| **Scaffold** | Generate compliant skill structure matching microsoft/skills conventions |
| **Develop** | Iterate with real-time compliance scoring (Sensei engine) |
| **Test** | Run agentic test loops with real LLM execution via Copilot SDK |
| **Evaluate** | Cross-model comparison with task completion, trigger accuracy, behavior quality metrics |

Custom agent evaluations enforce the selected `.agent.md` tool policy with the Copilot SDK (#585): omitted tools remain unrestricted, empty lists deny all tools, and populated lists allow named tools with shared runtime/grader aliases. Policy selection follows task-level skill paths and `SKILL.md` precedence. Initial and resumed turns use native filtering, pre-tool checks, and fail-closed permission checks. Denials fail the run and are surfaced in results schema 1.3, session logs, and the dashboard. Tool policies are not host filesystem/network sandboxing.

---

## User Personas

### Grader Assurance Scope (#661)

The initial integrated assurance path is an offline preserved-file/finite-output
challenge workflow, reusing native graders and approved scoped requirement/evidence
contracts. Reports distinguish actual observations, finite-case label agreement,
critical false acceptance, missing coverage, exact binding domains and supplied
current-source review eligibility. Every selected task, including requirement-free
tasks, must have mapped and covered requirements for strict corpus pass. Existing grader defaults, exits, golden tasks
and skill flows remain unchanged. Bundled labels are unreviewed; declarations do
not authenticate human review. Unknown input completeness and absence from a
subset cannot become assurance success.

The independent authored-output profile is evaluator-supplied finite text,
not historical execution, complete tool evidence or observed billing. Native
historical consumers reject its exact outer `schemaVersion: "2.0"` and
`kind: "waza.grader-reference-input"` compatibility fences; payload `1.0` and
manifest `1.1` remain independent, not native historical schema upgrades.
Strict reports bind original label/envelope bytes
and separate reviewed-source eligibility from actual native agreement.

Paid model calibration and final dashboard/example delivery remain acceptance work.
Selecting an unavailable calibration plan records not assessed with zero executions
and null observed usage/credits; it is not measured calibration or confidence.
`max_judge_executions` never bounds SDK follow-up billable calls or spend.

### Workflow Author
- **Role:** Developer evaluating MCP, CLI or repository workflows
- **Goals:** Author scenario suites without an unrelated `SKILL.md`, retaining optional skill/custom-agent context
- **Scope:** `waza new eval <name> --scenario --template repository|cli|mcp` uses existing graders and real agent execution; mocked dependencies are explicitly harness-only evidence
- **Compatibility:** Scenario evals opt into exact schema `2.0` so old executables reject changed discovery semantics before execution; legacy evals and task/result artifacts retain their prior formats

### Primary: Skill Author
- **Role:** Developer contributing skills to microsoft/skills
- **Goals:** Create high-quality skills that pass CI, work across models
- **Pain Points:** Manual testing, unclear compliance requirements, no cross-model validation

### Secondary: Skill Reviewer
- **Role:** Maintainer reviewing skill PRs
- **Goals:** Quickly assess skill quality, ensure compliance
- **Pain Points:** Inconsistent quality, manual verification

### Tertiary: Platform Engineer
- **Role:** CI/CD pipeline maintainer
- **Goals:** Automate skill validation in pipelines
- **Pain Points:** Lack of CLI tools for automation

---

## Feature Requirements

### Existing-workflow preservation contract

The additive evaluation work tracked in #657 must preserve current command,
exit, grader, skill/custom-agent, multi-turn, mock, billing/cache, snapshot and
historical dashboard behavior. The fixed offline corpus and package-owned
coverage inventory are specified in [COMPATIBILITY.md](COMPATIBILITY.md) (#658).
The required integration gate is `NO_COLOR=1 make test-compat`, with historical
browser checks when dashboard surfaces change. Known bugs are tracked
separately rather than accepted as new semantics; recorded/mock outcomes are
not real-agent quality or full assurance claims.

### Epic 1: Go CLI Foundation (P0)

#### Agent-free execution planning (#660)

Before an eval starts, `waza preflight` inspects offline schemas, tasks and IDs,
resource/instruction paths, grader configuration and locked cached modules,
effective mocks, scoped requirement references, and static executor support.
Inspection must count zero engine/model/live-service/subprocess calls and never
launch mock services or update checks. Unavailable credentials, external state,
model/interpreter availability and conditional checkpoint execution remain
unresolved rather than verified.
Eager schema decoding must be guarded across eval/task/checkpoint sources,
cached presets, and merged overrides. External prerequisites remain unresolved
and prevent complete inventory claims without changing native runtime loaders.

Optional task-local requirements categorize outcome, boundary, recovery and
quality intent using stable IDs and explicit references to current graders.
They compose current checks, not a new assertion language. Valid alternatives
are unconstrained unless a selected existing grader intentionally specifies
sequence semantics. Reference resolution is not requirement satisfaction,
runtime enforcement, or assurance of external side effects.

All existing v1 inputs and runtime/default/exit semantics remain supported.
New preflight invalid diagnostics exit 1; unresolved/unsupported warnings exit 0
unless explicit `--strict` opts into exit 1. Empty check lists mean uncovered.
Descriptive v1 metadata cannot enforce hard requirements in old readers; future
hard semantics must select a new artifact boundary those readers reject.
Preflight reports have their own typed artifact/version and never enter the
historical results/dashboard pipeline. No new evaluation result fields are
introduced. The #658 corpus remains the offline preservation gate.

Port existing Python waza functionality to Go for single-binary distribution.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E1-01 | As a developer, I can run evaluations with `waza run` | Parses eval.yaml, executes tasks, outputs results |
| E1-02 | As a developer, I can initialize new eval suites with `waza init` | Creates compliant directory structure |
| E1-03 | As a developer, I can create new skills with `waza new` | Scaffolds skill structure, supports --output-dir flag |
| E1-04 | As a developer, I can compare results across models with `waza compare` | Loads multiple result files, generates comparison report |
| E1-04a | As a release owner, I can explicitly select a precommitted fixed-design paired comparison without changing legacy gates | Source-bound offline mock/native-text planning and collection; independently versioned schemas and strict journal/raw-result admission; full/partial trial/attempt accounting; first-attempt versus retry recovery; golden veto; distinct missing assurance/golden/billing and invalid/inconclusive decisions; separate API/dashboard. Live operational observability and assurance verdict integration remain unsupported (#665). |
| E1-05 | As a developer, I can use all 8 grader types | code, model, regex, file, keyword, json, script, composite |
| E1-06 | As a developer, I can execute against Copilot SDK | Full integration with streaming responses |
| E1-07 | As a developer, I can use verbose mode for debugging | Real-time conversation display |
| E1-08 | As a developer, I can save transcripts for analysis | JSON log output with full conversation |
| E1-09 | As a developer, public artifacts are schema-versioned | `eval.yaml` and `results.json` include `schemaVersion`; readers warn on same-major drift and reject cross-major versions with a migration hint |

### Epic 2: Sensei Engine (P0)

Compliance scoring and iterative improvement loop.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E2-01 | As a developer, I can run `waza dev` to start the improvement loop | Iterative scoring with feedback |
| E2-02 | As a developer, I can see my compliance score (Low/Medium/Medium-High/High) | Clear scoring rubric applied |
| E2-03 | As a developer, I get specific improvement suggestions | Actionable feedback per issue |
| E2-04 | As a developer, I can set a target score | Loop until target reached |
| E2-05 | As a developer, I can run trigger accuracy tests | shouldTrigger/shouldNotTrigger prompts |
| E2-06 | As a developer, I can skip integration tests with `--skip-integration` | Unit + trigger tests only |
| E2-07 | As a developer, I can use fast mode with `--fast` | Skip tests for rapid iteration |

### Epic 3: Evaluation Framework (P0)

Cross-model testing and comprehensive metrics.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E3-01 | As a developer, I can run evals against multiple models | Model parameter support |
| E3-02 | As a developer, I can see task completion metrics | Pass rate, composite score |
| E3-03 | As a developer, I can see trigger accuracy metrics | Activation pattern validation |
| E3-04 | As a developer, I can see behavior quality metrics | Response quality scoring |
| E3-05 | As a developer, I can run trials for statistical confidence | Multiple runs per task |
| E3-06 | As a developer, I can get LLM-powered improvement suggestions | --suggestions flag |
| E3-07 | As a developer, I can run tasks in parallel | --parallel flag |
| E3-08 | As a developer, I can filter to specific tasks | --task flag |
| E3-09 | As a developer, I can reuse shared evals and graders from a registry | Registry design covers search/add/get UX, versioned refs, lockfile reproducibility, and safe plugin extensibility ([design](research/waza-eval-registry-design.md)) |
| E3-10 | As a developer, I can verify eval coverage against SKILL.md requirements | `waza spec verify` maps description, trigger, anti-trigger, and parameter requirements to task coverage with CI-gateable output |

### Epic 4: Token Management (P1)

Budget tracking and optimization tools.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E4-01 | As a developer, I can count tokens with `waza tokens count` | Token count for all markdown files |
| E4-02 | As a developer, I can check limits with `waza tokens check` | Validate against budget |
| E4-03 | As a developer, I can use strict mode with `--strict` | Exit 1 if limits exceeded |
| E4-04 | As a developer, I can get optimization suggestions with `waza tokens suggest` | LLM-powered reduction tips |
| E4-05 | As a developer, I can compare with previous commits | `waza tokens compare HEAD~1` |

### Epic 5: Waza Skill (P1)

Conversational interface for guided skill development.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E5-01 | As a developer, I can use waza as a skill in Copilot | SKILL.md published to microsoft/skills |
| E5-02 | As a developer, I get guided requirements gathering | Interactive prompts for skill creation |
| E5-03 | As a developer, I can check readiness conversationally | "Is my skill ready?" triggers validation |
| E5-04 | As a developer, I get interpreted results | Plain language explanation of scores |
| E5-05 | As a developer, the skill invokes CLI commands | Skill wraps waza CLI |

### Epic 6: CI/CD Integration (P1)

GitHub Actions and microsoft/skills compatibility.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E6-01 | As a developer, I can run waza in GitHub Actions | Action workflow template |
| E6-02 | As a developer, I can fail PRs on low compliance | Exit codes for CI |
| E6-03 | As a developer, I can post results to PR comments | GitHub reporter output |
| E6-04 | As a developer, waza works with microsoft/skills CI | Compatible with existing test harness |
| E6-05 | As a developer, I can cache evaluation results | Incremental testing support |

### Epic 7: AZD Extension (P2)

Package waza as an Azure Developer CLI extension.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E7-01 | As a developer, I can install waza with `azd extension install waza` | Published to azd extension registry |
| E7-02 | As a developer, I can run `azd waza <command>` | All commands available via azd |
| E7-03 | As a developer, I get IntelliSense for waza commands | Metadata support |
| E7-04 | As a developer, waza integrates with azure.yaml | Configuration schema support |

---

## Technical Architecture

### System Overview

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           DEVELOPER WORKFLOW                            │
│                                                                         │
│   ┌─────────────────────────────────────────────────────────────────┐   │
│   │                          WAZA CLI (Go)                          │   │
│   │                                                                 │   │
│   │   init → generate → dev → run → compare                        │   │
│   └─────────────────────────────────────────────────────────────────┘   │
│                                    │                                    │
│              ┌─────────────────────┼─────────────────────┐              │
│              │                     │                     │              │
│              ▼                     ▼                     ▼              │
│   ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐      │
│   │   Sensei Engine  │  │  Eval Framework  │  │   Waza Skill     │      │
│   │   (Compliance)   │  │ (Testing/Metrics)│  │   (Guidance)     │      │
│   └──────────────────┘  └──────────────────┘  └──────────────────┘      │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

### Component Breakdown

| Component | Language | Purpose |
|-----------|----------|---------|
| waza CLI | Go | Main binary, all commands |
| Sensei Engine | Go | Compliance scoring, improvement loop |
| Eval Framework | Go | Task execution, grading, metrics |
| Copilot Executor | Go | Copilot SDK integration |
| Waza Skill | Markdown | SKILL.md for conversational interface |

### Directory Structure

```
/
├── cmd/waza/           # CLI entrypoint
├── internal/
│   ├── config/         # Configuration loading
│   ├── execution/      # Executors (mock, copilot)
│   ├── models/         # Data models (spec, task, outcome)
│   ├── orchestration/  # Runner, task coordination
│   ├── scoring/        # Graders, validators
│   └── sensei/         # Compliance engine (new)
├── go.mod
└── Makefile
```

---

## Compliance Scoring System

Skills are scored on frontmatter compliance. Target: **Medium-High** or better for publishing.

| Score | Requirements | Description |
|-------|--------------|-------------|
| **Low** | Description < 150 chars OR no triggers | Basic, agent can't route reliably |
| **Medium** | Description >= 150 chars AND has trigger keywords | Functional but may have false positives |
| **Medium-High** | Has "USE FOR:" AND "DO NOT USE FOR:" | Clear boundaries, reliable routing |
| **High** | Medium-High + INVOKES + FOR SINGLE OPERATIONS | Full routing clarity, MCP integration |

---

## Success Metrics

| Metric | Target | Measurement |
|--------|--------|-------------|
| Skill Compliance Rate | >80% Medium-High | Automated scoring via `waza dev` |
| Trigger Accuracy | >90% | Evaluation framework pass rate |
| Time to First Skill | <30 minutes | Developer surveys |
| Cross-Model Consistency | >85% pass rate across 3+ models | Comparison reports |
| Token Efficiency | <500 lines for SKILL.md | `waza tokens check` |

---

## Dependencies

| Dependency | Status | Risk Level |
|------------|--------|------------|
| Copilot SDK | Available | Medium - API stability |
| Go Runtime | Stable | Low |
| AZD Extension Framework | Available (alpha) | Medium - API changes |
| microsoft/skills repo | Exists (132+ skills) | Low - Established |

---

## Phased Roadmap

### Primary Phase (Core Features)
| Epic | Description | Priority |
|------|-------------|----------|
| E1: Go CLI Foundation | Port Python features to Go CLI | P0 |
| E2: Sensei Engine | Compliance scoring & dev loop | P0 |
| E3: Evaluation Framework | Cross-model testing & metrics | P0 |
| E4: Token Management | Budget tracking & optimization | P1 |
| E5: Waza Skill | Conversational skill for guidance | P1 |

### Secondary Phase (Integration & Extensions)
| Epic | Description | Priority |
|------|-------------|----------|
| E6: CI/CD Integration | GitHub Actions & microsoft/skills | P1 |
| E7: AZD Extension | Package as `azd extension` | P2 |

---

## Appendix: References

| Source | Description |
|--------|-------------|
| [microsoft/skills](https://github.com/microsoft/skills) | Target repository, contribution conventions |
| [waza repo](https://github.com/microsoft/waza) | Current implementation |
| [Squad Proposal](https://github.com/spboyer/azure-mcp-v-skills/blob/main/squad-proposal.md) | Original proposal document |
| [AZD Extension Framework](https://github.com/Azure/azure-dev/blob/main/cli/azd/docs/extensions/extension-framework.md) | Extension packaging guide |
