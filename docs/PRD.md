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

---

## User Personas

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

### Epic 1: Go CLI Foundation (P0)

Port existing Python waza functionality to Go for single-binary distribution.

| ID | User Story | Acceptance Criteria |
|----|------------|---------------------|
| E1-01 | As a developer, I can run evaluations with `waza run` | Parses eval.yaml, executes tasks, outputs results |
| E1-02 | As a developer, I can initialize new eval suites with `waza init` | Creates compliant directory structure |
| E1-03 | As a developer, I can create new skills with `waza new` | Scaffolds skill structure, supports --output-dir flag |
| E1-04 | As a developer, I can compare results across models with `waza compare` | Loads multiple result files, generates comparison report |
| E1-05 | As a developer, I can use all 8 grader types | code, model, regex, file, keyword, json, script, composite |
| E1-06 | As a developer, I can execute against Copilot SDK | Full integration with streaming responses |
| E1-07 | As a developer, I can use verbose mode for debugging | Real-time conversation display |
| E1-08 | As a developer, I can save transcripts for analysis | JSON log output with full conversation |

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
