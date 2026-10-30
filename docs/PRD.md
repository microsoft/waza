# Waza Workflow Evaluation Platform - Product Requirements Document

**Status:** Active requirements; original skill-platform plan retained below, not a shipping checklist
**Version:** 1.0  
**Last Updated:** 2026-10-15
**Owner:** @spboyer  
**Source:** [Squad Proposal](https://github.com/spboyer/azure-mcp-v-skills/blob/main/squad-proposal.md)

---

## Executive Summary

Waza (技 - Japanese for "skill/technique") evaluates agent workflows, skills and
custom agents. Repository, CLI and MCP scenarios provide a workflow-first path;
`SKILL.md` and `.agent.md` context remain supported. See
[Concepts](../site/src/content/docs/concepts.mdx) for the learning order.

Upcoming main increments include recorded reliability, opt-in dashboard Lab,
selected evidence/regrade, conservative release policies with an offline mock
default and a separately selected bounded Copilot SDK collector, and bounded
authored-case assurance with explicitly selected calibration. Stable standalone `v0.38.10` was published
October 9, 2026; these main features are not declared part of it.
This document does not assert current CI is green or announce a release.
Scenario evals support finite MCP/CLI fault sequences and report configured
dependency modes. These fixtures test agent behavior, not real CLI/server
implementations; they do not automatically retry, enforce a host sandbox, or
provide per-request MCP stdio cancellation. Universal release authority, a full
human-labelled corpus, sandbox enforcement and default paid calibration are
**not delivered**.

The historical skill-specific requirements below remain product context, not
authority to infer current native grader keys or completed feature scope.

The v0.40.0 runner preview adds offline version/capability negotiation (#757)
and one exact captured mock/text task/trial/attempt through CLI and HTTP (#758).
Admitted receipts carry deterministic logical identities and their derivation
version (#759). Live Lab displays input-derived lineage and pre-execution
selection validation without launching a runtime.
The exact path bypasses discovery, cache and retries; local commands/artifacts
are unchanged. Native execution, terminal envelopes, compatibility
adapters and the container reference remain unavailable under #754.
See the [runner contract](RUNNER_CONTRACT.md).

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

Portable evaluation evidence (#663): independently versioned manifest `1.0` in ordinary snapshot `1.1`, retaining legacy task semantics and historical result versions while new results use `1.7`, records full source origin, every trial, separate source/content digests, availability/completeness, redaction, nullable/unavailable versions, requested native skill/tool controls, and typed operational uncertainty. Cached evidence retains source origin; historical absence is unassessed and invalid metadata is withheld. These are observations, not enforcement, complete workspace/external-state certification, causal classification, requirement scores, or changed pass/fail defaults.

Capture sanitizes typed JSON and structural sensitive keys before serialization, preserves numeric lexical tokens, rejects opaque bytes/unsafe identifiers/keys/duplicate or trailing JSON/excessive nesting, and isolates rule-match counters (including selected files). Pattern matching is not a confidentiality proof. Reviewed agent-visible UTF-8 regular-file capture is explicitly allow-listed, excludes evaluator-only files/credentials, bounded (1 MiB/file, 10 MiB total by default), private, non-overwriting, and supported only on Linux/macOS. Allow-listing is not authorization proof. Partial capture and publication failure have safe unavailable states; there is no automatic export/upload/path fetch.

New captures track core artifact redaction by changes to each artifact's materialized content, separately from global rule-match counts. Validation changes propagate only to their enclosing result; metadata and workspace-only changes do not mark unchanged core artifacts. Representation-only JSON normalization and no-op replacements do not count as content changes; materialized field removal does. Historical flags, wire versions and reader behavior remain unchanged.

Metadata-only evaluator exclusion admission is capped at 256 entry inspections/depth 32 and precedes selected-content reads. Inspection, limit and resource failures prevent capture without pruning or raising process limits. Identity observations include inspected subtree aliases while preserving unrelated hard links and missing-exclusion neighbors. No-follow component admission permits only validated macOS system aliases. Before/after file metadata and prepublication materialized expected-byte checks are bounded observations, not atomic/authentic state or continued protection against mutable same-user namespaces. A late close failure after verified publication retains a residual path with unavailable evidence and an explicit diagnostic.

Selected-state destination publication on Linux/macOS admits/creates every component without following arbitrary ancestor symlinks, creates missing directories `0700`, and requires only the final directory to be `0700`. Non-creating identity rechecks precede evidence writes and surround linking. Lexical cleaned destination spelling and ordinary metadata-only writer behavior remain unchanged. Failures may leave newly created empty directories; there is no directory rollback or moving-namespace guarantee.

Immediate selected capture uses private publication even when no files survive capture. Its request marker is not serialized; loaded/manual zero-file artifacts retain legacy writer routing. Failure manifests preserve validated source eval/task identity when available, otherwise explicitly report unavailable attribution. Numeric-only lineage is not complete identity; historical binding and original execution result fields are unchanged.

Private-file cleanup failures preserve at most one independently verified complete residual path, preferring an eligible temporary name, with explicit errors and unavailable capture evidence. Additional possible residue and unavailable locations are diagnosed without concatenated paths, guessed moved-root names, or claims of successful cleanup. Partial/changed bytes are not reported as complete snapshots; inode, name, byte and metadata checks remain bounded observations.

`grade --evidence-snapshot` verifies one selected task/run's original full origin, manifest hash, and complete unredacted text/digests, then privately materializes only explicit required file-grader inputs. Other grader types, output-based expectations, wildcard/absence checks, ambiguous runs, and explicit `--workspace` combinations are rejected. Checks cover the materialized captured subset, not original-workspace absence; `must_not_match` checks complete verified text of a selected file. Regrading retains the original capture hash but invalidates original result/validation/grader-configuration/checkpoint links; existing workspace grading remains unchanged and may use a paid prompt judge. Offline replay checks event sequencing and stored score/pass consistency or bisects tapes; it does not rerun graders, reconstruct a workspace, or support live replay. See the [portable evidence guide](../site/src/content/docs/guides/snapshot-replay.mdx).

Scenario evals with exact `schemaVersion: "2.0"` can define finite MCP and CLI
response sequences. Each matching call consumes one step; sequences are bounded
to 100 steps and delays to 300,000 ms. Unmatched calls and exhausted sequences
fail closed. Task-level CLI mock lists preserve replacement semantics, including
`command_mocks: []`. Sanitized CLI attempts remain in
`runs[].command_invocations`; MCP failures and later calls remain in the
transcript/checkpoint evidence. Results schema `1.7` records per-run configured
dependency modes (`live` or `mocked`); this describes configuration, not proof
of invocation. There is no recorded-replay mode.

The private `internal/faultsequence` groundwork provides cross-process step
reservation and cancellable, overflow-checked delays for the CLI shim. State
consumes steps at exclusive creation and does not roll back after cancellation
or process exit. Concurrent allocation is unique, not a guarantee of execution
order or machine-crash durability.

The canonical `internal/models` source validators check JSON/YAML response
fragments before normalization erases field presence. Finite sequences require
the explicit enclosing scenario version; mixed zero/empty/null fields, nested
matchers, duplicate finite JSON keys and invalid delays fail explicitly.

Canonical eval/task byte loaders retain an immutable, nonserialized copy of their
input before prompt-file resolution or eager grader decoding. Selected mock
response sources detach YAML aliases and merges while preserving scalar tags and
duplicate finite fields; payload properties are not treated as configuration.
Task sequence eligibility is checked against the enclosing scenario eval version.
Preflight reuses captured eval/task bytes; YAML source expansion is bounded and
budget exhaustion fails explicitly rather than returning partial sources.

Shared MCP matcher validation preserves an explicitly supplied schema resource loader. Its legacy wrapper retains compiler defaults, including local file references; offline callers must select a denying loader rather than assuming compiler defaults prohibit resource access.

Private adapters exercise source validators and reservation primitives against
native request/output shapes. Their subprocess envelopes retain raw source
presence, cached CLI fixture bytes and immutable workspace configuration. MCP
sequence counters are scoped by server, tool and matcher. Fixture errors are
delivered payloads, while cancellation and exhaustion remain explicit errors.
These adapters do not prove external integration or provide sandbox enforcement.

Optional, unregistered native serving hooks now place private dispatch around actual stdout/stderr and JSON-RPC writes. Both streams must emit completely before a private command receipt is published; attempted calls are counted separately from delivered receipts, and missing/failed capture returns an error rather than inventing an exit code. Native receipt corruption cannot turn a missing/null exit status into success. JSON-RPC responses and notifications reject short writes. Serial hooks preserve legacy defaults and leave synchronous I/O cancellation with their caller.

A separate private MCP owned-serving path performs serial irreversible preparation,
once-only delivery and bounded concurrent response work. Cancellation guarantees
apply only to injected endpoints that explicitly guarantee concurrent close
interrupts I/O; they do not certify public process/platform stdio. EOF drains
accepted calls while cancellation supervision remains active.

Source-fragment MCP validation selects `schemaloader.Offline` before processing, refusing unregistered file and HTTP schema references without fetching them. The explicit offline MCP constructor shares existing fixture normalization, while the legacy runtime constructor retains its resource behavior. This schema-loading policy is not a filesystem/network sandbox.

The owned-serving capacity is explicit and positive; duplicate active native IDs
and capacity overflow reject before reservation. Public per-request stdio
cancellation, platform process-pipe guarantees and resumed SDK attempt ownership
remain unverified. The [dependency recovery guide](../site/src/content/docs/guides/dependency-recovery.mdx)
documents fixture use and these limits.

### Dashboard workspace exploration (2026-10-06)

`waza serve --lab` opens the opt-in `/#/lab` evaluation workspace without replacing
the current dashboard. It connects saved-result overview metrics, artifact-derived
activity replay, searchable history, stable-task baseline comparisons, recorded
evidence inspection, reports, and an observed skill/eval catalog. Local artifact
refresh and explicit missing-data states are core requirements. An isolated
synthetic UX demo remains at `/#/lab/demo`; API errors never substitute fixtures.

This is not a production execution or hosting feature. Authentication, run
control, durable history, API identity/version compatibility, repetition-level
statistics, and cloud workers remain future design work. The [dashboard lab guide](../site/src/content/docs/guides/dashboard-lab.mdx)
records the research, prototype boundaries, and proposed architecture.

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

The separately selected internal preserved-native mechanical operation uses
report `1.2`, checking whole selected tool artifacts and private selected-file
materialization with original byte/presence/provenance admission. Native grader
semantics are reused, including distinct canonical versus digest arguments.
Unknown historical capture, unsupported modes and file-subset absence remain
nonpass; no paid calibration or automatic CLI/dashboard dispatch is introduced.

Private engine-free qualification primitives provide bounded original-byte
custody, selected-source inventory binding, immutable handles, a label-free
execution-view projection and capture-only equivalence controls. The private
journal extension admits source-bound manifests, derives fixed-role artifacts,
and tests accepted-host local process-lock/sync barriers with read-only recovery
and injected invocation-wide freshness seams. It introduces no public wire/API,
task execution, deployed backend, production currentness authority or lifecycle.
These checks do not authenticate human review, establish global reservation or
turn source seals into execution evidence or current publication authorization.

The independent authored-output profile is evaluator-supplied finite text,
not historical execution, complete tool evidence or observed billing. Native
historical consumers reject its exact outer `schemaVersion: "2.0"` and
`kind: "waza.grader-reference-input"` compatibility fences; payload `1.0` and
manifest `1.1` remain independent, not native historical schema upgrades.
Strict reports bind original label/envelope bytes
and separate reviewed-source eligibility from actual native agreement.

Paid model calibration remains acceptance work. The dashboard offers a separate
read-only local report inspector and historical not-assessed states; imports are
supplied claims, not verified evidence or native-run associations. The runnable
authored-output example remains unreviewed with an expected not-assessed result.
Selecting an unavailable calibration plan records not assessed with zero executions
and null observed usage/credits; it is not measured calibration or confidence.
`max_judge_executions` never bounds SDK follow-up billable calls or spend.

The separate evaluator-selected calibration API has no default factory.
A distinctly selected `assure calibrate` CLI operation requires explicit current
review-source acceptance, rubric root and default-false `--accept-paid-calls`,
with no interactive fallback. Existing offline `assure --calibrate` remains
unavailable with zero paid calls. The new operation requires a pre-initialization
notice carrying the reviewed plan
and admitted unique-job count, frozen evaluator-owned declarations/finite inputs/
local rubric bytes, and source-bound native grading with finalized diagnostics,
independent model attribution and accounting. Version-1.1 claims require explicit
consumer selection; strict agreement does not erase operational failure or
critical false acceptance. Tested client-side callback policy/guards do not
certify exhaustive server isolation. Missing review, evidence, attribution,
accounting or unsupported calibration modes remain non-passing.
Partial reports are output even alongside execution errors; runtime, invalid,
notice-I/O, output and root-close errors exit 2, ordinary nonpass exits 1.
Ambient custom-provider redirects are rejected rather than silently changing
the reviewed provider route. The cooperative assessment timeout is not a spend
cap or a forced cleanup guarantee.

### Workflow Author
- **Role:** Developer evaluating MCP, CLI or repository workflows
- **Goals:** Author scenario suites without an unrelated `SKILL.md`, retaining optional skill/custom-agent context
- **Scope:** `waza new eval <name> --scenario --template repository|cli|mcp` uses existing graders and real agent execution; mocked dependencies are explicitly harness-only evidence
- **Compatibility:** Scenario evals opt into exact schema `2.0` so old executables reject changed discovery semantics before execution; legacy evals and task/result artifacts retain their prior formats
- **Admission correction (2026-10-12):** User-directed native YAML/JSON schema parity rejects present null/non-string/blank scenario identities, including earlier versionless/`1.x` null acceptance and scalar coercion. Remove the field for legacy behavior or provide a nonblank string with exact `2.0`; absent fields preserve legacy defaults.
- **Discovery:** No target or explicit skill directories disables ambient skills on new/resumed SDK sessions; explicit empty task directories disable discovery and `--no-skills` overrides opt-ins
- **Cache limitation:** Scenario evals bypass result caching with a notice until external workflow dependencies can be fully fingerprinted; legacy cache/default/billing behavior is unchanged
- **Scaffold parent policy:** Scenario generation rejects selected symlinked output parents, including the scaffold root and nested generated directories, and retains admitted directory handles for exclusive file creation. Relative output uses the retained working directory; absolute output is admitted from the filesystem root with only verified literal macOS `/var` and `/tmp` aliases. This does not add whole-suite rollback, filesystem sandboxing or protection against moving an already admitted directory; legacy scaffold behavior is unchanged.
- **Repository fixture preservation:** The repository template uses an existing file grader to require the final workspace inventory bytes to equal its generated fixture. Missing or changed content fails independently of a valid report. This checks final state, not transient writes or filesystem isolation; CLI/MCP templates retain their existing checks.
- **Git checkout policy:** Repository scaffolds emit fixture-local Git attributes preserving inventory LF bytes, with a safe root fallback when custom task globs select local metadata. If neither path is safe, generation fails before writing with a narrower-glob/different-filename migration; legacy and CLI/MCP scaffolds are unchanged. Existing working copies are not retroactively rewritten.
- **Generated task selection:** All scenario templates reject task globs selecting any generated non-task file before writing. Loadable layouts such as `fixtures/*.yaml` remain supported; `fixtures/*` must be narrowed because it selects `.gitkeep` and repository inventory.

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
Task discovery accepts individually unmatched valid globs when tasks exist;
CSV containment requires lexical containment and then checks resolved symlinks, including filesystem/volume-root eval
directories, and range selection preserves original fallback row identities.
Execution, preflight, regrading, and run analysis must share CSV selection,
original row IDs/names, template context, and bounded resolved-path admission.
Aliased eval bases admit lexically contained relative sources, not alternate
physical absolute spellings outside their lexical root.
Ranged results must remain addressable by their original task IDs in `grade`.
Preflight diagnoses file-resource names rejected by the existing runtime loader
without changing runtime acceptance or inline content handling.
Contained absolute CSV paths must retain their source identity in task plans and
diagnostics. Workspace-root destinations must be invalid for resource/instruction
and file/diff checks, including inline resources, consistent with runtime limits.
Malformed MCP fields and non-external schema loader
errors are invalid rather than unresolved availability.
Inspection must count zero engine/model/live-service/subprocess calls and never
launch mock services or update checks. Unavailable credentials, external state,
model/interpreter availability and conditional checkpoint execution remain
unresolved rather than verified.
Eager schema decoding must be guarded across eval/task/checkpoint sources,
cached presets, and merged overrides. External prerequisites remain unresolved
and prevent complete inventory claims, including later grader schema-file and
MCP input-schema inspection, without changing native runtime loaders.

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
| E1-04a | As a release owner, I can explicitly select a precommitted fixed-design paired comparison without changing legacy gates | Source-bound offline mock and explicit native Copilot SDK collection profiles; independently versioned native envelope/task profile and core policy protocol version 1.0; strict journal/raw-result admission; full/partial trial/attempt accounting; first-attempt versus retry recovery; golden veto; separate API/dashboard decision. Native profile is fixed named-model, single-turn, no-skills, deny-all client tools and native text/string checks only; it may incur provider usage, is not server isolation or authenticated assurance, and durable creation supports Linux/macOS only. Final observed token/AI-credit budgets use best-effort start checks, block further starts when required prior usage is unknown or limits are reached, and can overshoot asynchronously. Runtime version, provider currency and ambiguous output presence remain unavailable; required unsupported capabilities do not pass. Additive explicit offline assurance contract, retained actual-response ledger, fresh per-arm finite-authored-output qualification and deterministic regrade can pass only the outer decision with all base non-assurance conditions satisfied; frozen base 1.0 required-assurance decisions/API/dashboard remain nonpassing. Current separately supplied review-source acceptance is not authenticated human review; examples remain unreviewed. No paid calibration, new auth provider, general provider abstraction, server-isolation claim, merge/publication authority or default paid execution (#665). |
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
| E3-05 | As a developer, I can run trials for statistical confidence | Multiple runs per task; recorded task/suite Wilson pass-rate intervals and pass^k reliability in result schema 1.5, surfaced consistently in CLI/API/dashboard. Historical absent metrics remain unassessed; paired significance is a separate capability. |
| E3-06 | As a developer, I can get LLM-powered improvement suggestions | --suggestions flag |
| E3-07 | As a developer, I can run tasks in parallel | --parallel flag |
| E3-08 | As a developer, I can filter to specific tasks | --task flag |
| E3-09 | As a developer, I can reuse shared evals and graders from a registry | Registry design covers search/add/get UX, versioned refs, lockfile reproducibility, and safe plugin extensibility ([design](research/waza-eval-registry-design.md)) |
| E3-10 | As a developer, I can verify eval coverage against SKILL.md requirements | `waza spec verify` maps description, trigger, anti-trigger, and parameter requirements to task coverage with CI-gateable output |
| E3-11 | As a developer, I can judge recorded actor evidence independently (#702) | Opt-in final eval/task prompt graders freeze required bounded evidence before final grading; use a fresh isolated pinned-runtime judge with only grade tools; regrade without actor/workspace access; preserve failed-attempt observations and separate new billing. Reject unsupported scopes and unusable evidence. Metadata-only run/grader reporting distinguishes submission from unobservable inspection; no accuracy, bias, authenticity or OS-sandbox claim. |

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
