# PR Description: Author 12-scenario Grader Assurance benchmark corpus (#661)

**PR Title**: `feat(assurance): Author 12-scenario Grader Assurance benchmark corpus (#661)`
**Branch**: `feat/issue-661-grader-assurance-corpus`
**Base Branch**: `main`
**Commit**: `149125c3`

---

## Summary

This pull request introduces a canonical 12-scenario reference benchmark corpus covering 48 labeled candidate test cases across MCP, CLI, and Repository domains to deterministically validate grading integrity under Issue #661 and parent Epic #657. It authors scenario definitions pairing dual-requirement checks (state-based `FileGrader` outcome verification and boundary `ToolCallsGrader` safety checks) against known-good, alternative-valid, bad-wrong-state, and bad-forbidden-action implementations. The user-visible outcome is bounded, zero-cloud verification of grader correctness, ensuring exact 1.0 acceptance of compliant states and definitive rejection of invalid or policy-violating attempts without executing non-deterministic or costly live model calls.

## Related issue

- Closes #661
- Related to #657 (Parent Epic: Grader Assurance & Qualification Lifecycle)

## Agent handoff

- **Scope**:
  Authored the complete offline grader assurance benchmark corpus generator, static reference fixture, and mechanical evaluation harness across 12 distinct scenarios spanning 3 primary domains (4 MCP, 4 CLI, 4 Repository), providing 48 human-labeled cases total (12 known-good, 12 alternative-valid, 12 bad-wrong-state, and 12 bad-forbidden-action).

- **Key files changed**:
  - `internal/assurance/benchmark_corpus.go`: Core domain definitions, `BenchmarkScenario`, `BenchmarkCaseKind`, `BenchmarkCase`, regex pattern generators, execution context builders, and `BuildBenchmarkCorpus()` snapshot/manifest generation logic.
  - `internal/assurance/benchmark_corpus_test.go`: 5 test suites verifying structural validity, 96 mechanical observation evaluations (both direct `ObserveMechanical` and `ObserveDeclaredMechanical`), reference document admission via `ParseReferences()`, fixture round-trip persistence, and `VerifyRequest` qualification builder.
  - `internal/assurance/testdata/benchmark_corpus.json`: Sealed, self-contained reference document containing 48 cases with cryptographic digests (`SHA-256`) and schema compliance.

- **Important decisions**:
  - **Zero-Cloud Requirement**: Relies exclusively on bounded, deterministic mechanical evaluation via `FileGrader` and `ToolCallsGrader` with isolated temporary workspaces, requiring no external network connectivity, live Copilot/LLM engines, or cloud credentials.
  - **Balanced Domain Representation**: Evenly divided into 4 MCP scenarios (`mcp-ticket-routing`, `mcp-feature-toggle`, `mcp-record-archive`, `mcp-resource-access`), 4 CLI scenarios (`cli-config-target`, `cli-export-format`, `cli-deploy-environment`, `cli-package-version`), and 4 Repository scenarios (`repository-config-fix`, `repository-doc-link`, `repository-dependency-pin`, `repository-migration`).
  - **Strict Four-Case Classification**: Every scenario enforces exactly 4 candidate states:
    1. `good`: Canonical file state and primary tool invocation.
    2. `alternative-valid`: Semantically equivalent state (formatting/whitespace variants) and secondary tool invocation.
    3. `bad-wrong-state`: Incorrect state outcome with standard tools.
    4. `bad-forbidden-action`: Valid final state achieved while calling a forbidden destructive tool.
  - **Schema Adherence**: Full compliance with `schemas/grader-reference-1.0.schema.json` and `schemas/evidence-manifest-1.0.schema.json`, maintaining 48 globally unique case IDs, 48 unique manifest SHA-256 digests, and relative snapshot paths (`snapshots/<id>.json`).

- **Follow-ups or known gaps**:
  - Offline benchmark validation only; live model qualification and multi-attempt retry policies belong to subsequent Epic #657 milestones.
  - Snapshot payload directory structure in `internal/assurance/testdata/snapshots/` can be materialized if external disk-based snapshot loaders are wired in future tasks.

## Type of change

- [ ] Bug fix
- [x] New feature
- [x] Documentation update
- [ ] Refactor or maintenance
- [ ] CI/CD or release change

## Validation

### Corpus Matrix & Architecture

```mermaid
flowchart TD
    subgraph Benchmark Corpus ["12 Scenarios (48 Cases)"]
        MCP["MCP Domain (4 Scenarios / 16 Cases)"]
        CLI["CLI Domain (4 Scenarios / 16 Cases)"]
        REPO["Repository Domain (4 Scenarios / 16 Cases)"]
    end

    subgraph Case Classifications ["4 Cases per Scenario"]
        C1["Good (Canonical state + Primary tool)"]
        C2["Alternative-Valid (Equivalent state + Alternate tool)"]
        C3["Bad-Wrong-State (Wrong outcome state)"]
        C4["Bad-Forbidden-Action (Forbidden tool called)"]
    end

    subgraph Grader Verification ["Dual Requirement Checks (96 Mechanical Observations)"]
        FG["Outcome Check (FileGrader)"]
        TCG["Boundary Check (ToolCallsGrader)"]
    end

    MCP --> C1 & C2 & C3 & C4
    CLI --> C1 & C2 & C3 & C4
    REPO --> C1 & C2 & C3 & C4

    C1 & C2 -->|"Expected Pass (Score 1.0)"| FG & TCG
    C3 -->|"Expected Outcome Fail (Score 1/3)"| FG
    C4 -->|"Expected Boundary Fail (Score 0.0)"| TCG
```

### Verification Results

- [x] **Mechanical Observation Suite**:
  - Evaluated **96 mechanical observations** across all 48 cases (48 outcome checks + 48 boundary checks).
  - **24/24 valid cases** (`good` and `alternative-valid`) passed with score `1.0` and feedback (`"All file checks passed"` / `"all tool_calls checks passed"`).
  - **12/12 bad-wrong-state cases** failed outcome requirements (score `1/3`) with explicit feedback (`"missing expected pattern"` / `"contains forbidden pattern"`).
  - **12/12 bad-forbidden-action cases** failed boundary requirements (score `0.0`) with explicit feedback (`"forbidden tool <name> was called"`).
  - **24/24 critical-bad cases** cleanly rejected with 100% agreement.
- [x] **Reference Admission & Schema Checks**:
  - Validated `internal/assurance/testdata/benchmark_corpus.json` against `schemas/grader-reference-1.0.schema.json` via `ParseReferences()`.
  - Verified 48 globally unique case IDs, 48 unique manifest SHA-256 digests, and valid domain requirements (16 minimum cases per domain, 1.0 agreement).
- [x] **Git Diff Whitespace Check**:
  - `git diff --check origin/main..HEAD` passed with zero errors or trailing whitespace issues.
- [x] **Automated Test Coverage**:
  - `TestBenchmarkCorpusStructure`: Verified 12 scenarios, 48 cases, 3 domains, correct case classification counts.
  - `TestBenchmarkCorpusObserveMechanical`: Verified 96 mechanical observations through `ObserveMechanical`.
  - `TestBenchmarkCorpusReferenceAdmission`: Verified parse, admission, and cryptographic integrity of `ReferenceDocument`.
  - `TestBenchmarkCorpusDeclaredMechanical`: Verified `ObserveDeclaredMechanical` against declared `TestCase` and `EvalSpec`.
  - `TestBenchmarkCorpusFixturePersistence`: Verified static fixture round-trip serialization and presence of all 48 reference snapshots.
  - `TestBenchmarkCorpusObservePreservedMechanical`: Verified mechanical evaluation against serialized snapshot bytes.
  - `TestBenchmarkCorpusVerifyPreserved`: Verified `VerifyPreserved` preserved assessment against committed reference testdata and snapshots.

## Documentation

- [ ] README updated, if user-facing behavior changed
  - _Not applicable_: Internal test suite and benchmark corpus; public CLI commands and user eval syntax remain unchanged.
- [ ] `site/` docs updated, if CLI, YAML, dashboard, or validator behavior changed
  - _Not applicable_: No public schema, UI dashboard, or website documentation modifications.
- [x] Examples updated, if relevant
  - Comprehensive reference fixtures authored at `internal/assurance/testdata/benchmark_corpus.json`.
- [x] Documentation updates & Limitations:
  - Authored this maintainer-facing PR specification at `docs/PR_ISSUE_661.md`.
  - **Scope Limitation**: Strictly scoped to bounded offline grader assurance benchmark corpus validation. Does not execute or claim live model runtime guarantees, which are handled in subsequent stages of parent Epic #657.

## Risk and rollback

- **Risk level**: Low
  - The changes are strictly additive within the `internal/assurance/` package and its test data.
  - No existing core execution pipelines, engine drivers, CLI interfaces, or production grader logic are modified.
- **Rollback plan**:
  - Can be reverted cleanly with `git revert 4333bef6` without affecting any other repository components or workflows.

## Notes for reviewers

- **Corpus Symmetry**:
  All 12 scenarios adhere to a consistent 4-case schema:
  - **MCP Domain**: `mcp-ticket-routing`, `mcp-feature-toggle`, `mcp-record-archive`, `mcp-resource-access`.
  - **CLI Domain**: `cli-config-target`, `cli-export-format`, `cli-deploy-environment`, `cli-package-version`.
  - **Repository Domain**: `repository-config-fix`, `repository-doc-link`, `repository-dependency-pin`, `repository-migration`.
- **Cryptographic Sealing**:
  Manifests and task declarations are hashed using SHA-256 canonical JSON representations via `evidence.JSONDigest` and sealed with `evidence.Seal()`.
- **Review Checkpoint**:
  Reviewers can focus on `internal/assurance/benchmark_corpus.go` for the scenario configuration table and regex assertions, and `internal/assurance/benchmark_corpus_test.go` for the mechanical evaluation assertions.
