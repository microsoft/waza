# CI/CD Integration Guide

This guide explains how to integrate waza evaluations into your CI/CD pipelines using GitHub Actions, Azure DevOps, and other platforms.

## Overview

Running waza evaluations in CI/CD allows you to:

- **Catch regressions** before merging pull requests
- **Benchmark skills** across multiple models in every PR
- **Track quality** metrics over time
- **Gate releases** on evaluation scores
- **Automate testing** without manual steps

## Waza repository native checks and stacked PRs

Waza's single `go-ci.yml` workflow runs on pull requests targeting any base
branch, including feature branches used by stacked PRs. It retains the
`opened`, `synchronize`, and `reopened` default activity types without PR branch
or path filters. Push triggers cover `main`, `develop`, `dev`, and `insider`.
The former `squad-ci.yml` duplicated the same validation and has been removed.
Superseded PR runs are cancelled within the same workflow/event/ref. Push
validations use independent run-ID groups, so they neither serialize nor replace
pending push runs. Release publication is not cancelled by that PR policy.

This is an ordinary `pull_request` workflow with `contents: read`, not
`pull_request_target` or release/dispatch workflows. The existing `Lint`,
`ubuntu-latest`, and `windows-latest` jobs, four native Linux shards, exhaustive
test-selection/coverage verification, and pinned lint-schema checks remain
required; enabling triggers is not evidence that native tests passed.

After integrating a trigger fix into a stack, inspect actual runs on each final
PR head following a qualifying PR event. Existing runs are not retroactively
scheduled. Keep the stack and branch protection intact, and obtain current-head
native Linux/Windows results and independent approvals before merging.

The lint job runs the trigger regression test. Run it locally with
`go test -count=1 -v ./.github/scripts`; it checks default and feature-base
selection, excluded activity types, the union of prior push branches, and mutations of the
actual workflow sources. Shard and lint failure-handling checks remain
`bash .github/scripts/go_test_shard_test.sh` and
`bash .github/scripts/lint_test.sh`.

The four native Linux shards use deterministic longest-first duration balancing.
`.github/scripts/go_test_shard_durations.tsv` records observed top-level
race/atomic durations and their source run. Update it from top-level test results,
not overlapping subtest times. Timing history affects placement only: discovery
still determines the complete inventory, new tests receive the median historical
duration, and removed tests are never resurrected. Python 3 (provided by the
Ubuntu runner) computes the plan. Every shard still retains its 30-minute test
deadline, atomic coverage, full discovery inventory, and selected test names;
the aggregate gate verifies identical inventories and exactly-once selection.

Squad Release and Squad Insider Release run remaining-package validation in
parallel with these shards. Publication waits for both branches to succeed,
verifies all five coverage profiles and native selection, and downloads the
binary produced by the validated build rather than rebuilding it. Failed,
cancelled, skipped, or missing evidence must prevent publication.

Site change detection is a short prerequisite job. Site browser validation and
offline documentation examples then run independently. Dashboard Chromium tests
use two CI workers at file granularity; serial process-backed suites retain their
ordering, and all tests still run with zero retries in dashboard CI.

The Docker dashboard build also consumes `examples/runner/unit.json`, the
captured request imported by Lab selection validation. Keep its explicit
`.dockerignore` admission, web-builder `COPY`, and Docker workflow path trigger
in sync. `go test -count=1 ./.github/scripts` checks these build inputs; an
actual Docker build is still required to verify container packaging.

## Proposed PR completeness gate

Issue #900 adds an **additive gate, not a replacement for native Go checks**.
The applicability and receipt controls are under implementation; the reusable
workflow routing and current-head adoption evidence are not yet active. Do not
require `PR completeness` in a ruleset until the migration below is reviewed and
its actual workflow executions are verified.

The proposed ordinary-PR entrypoint is the existing Go workflow, with local
reusable calls to the existing auxiliary validators. This avoids cross-run
`needs` assumptions and duplicate ordinary-PR suites. Non-PR triggers remain
separate. The initial applicability matrix is subject to production-input and
full-diff review:

| Validation | Changed inputs |
|---|---|
| Go, lint, native Linux shards and Windows | Every ordinary PR, preserving the existing native gates. |
| Dashboard Chromium | Dashboard files, backend `cmd/`, `internal/`, schemas, root Go inputs, module versions, and runner examples. |
| Site and chat Chromium | Site/docs, documented metadata, schemas, model/embedded-runtime inputs and frozen-reader fixtures; `Makefile`, web Playwright config and lockfile content read by documentation checks; exact checked web/compatibility/interop and linked repository files; and `examples/` directory-existence dependencies. |
| Offline documentation examples | Site/docs/examples, backend/schema/module inputs, dashboard inputs, `Makefile`, and `examples/runner/unit.json` consumed by the production-CLI release example. |
| Offline Grader Assurance | Backend/schema/module inputs, assurance viewer and reader scripts, immutable `testdata/frozen-assurance-readers` custody inputs and their `.gitattributes` checkout-byte rules, grader docs and examples. |
| Docker image and native runner contracts | Container/context files, `.gitattributes` LFS checkout rules for embedded assets, backend/schema/module/root Go inputs, dashboard, runner examples and admitted native probe fixtures. |
| Squad tools on Linux and Windows | Squad state, skills/agent definitions, PR requirements, casting `.gitattributes`, and Ralph/Squad tooling scripts. |
| Pinned judge runtime on Linux and macOS | Embedded runtime, execution/graders/judge evidence/models, their repository package dependency closure (including filesystem admission, snapshot readers, schema loading, events, config, skills/tokens and compiled test helpers), root schemas, modules and `.gitattributes`. |

Workflow and completeness-interpreter changes select the affected gate families.
The current production documentation prebuild input trace observed 210 distinct
repository paths, not 210 content reads. Besides the three content inputs
`Makefile`, `web/playwright.config.ts` and `web/package-lock.json`, it found
existence/stat checks for `web/e2e/runner-negotiation.spec.ts`, linked
`.squad/model-policy.md`, `DEMO-SCRIPT.md`, `LICENSE`, `meet-the-squad.md`,
`internal/interop/testdata/agentv/PROVENANCE.md` and the runner compatibility
eval/task fixtures. These exact file inputs select site validation.
Eleven additional linked files and three directory targets are covered by the
existing containment-aware `examples/` root match. The root deliberately
over-selects descendant edits: the documentation checks consume directory
existence, not every descendant's content. Actual Git addition, deletion and
rename controls cover file leaves, including removal of a consumed directory's
last tracked leaf; a Git directory path is never fabricated.

Instruction-only `AGENTS.md` edits retain the existing exemption from expensive
site/chat auxiliary suites. Documentation link checks also observe its
existence/type, so deletion or type replacement remains a known coverage gap
under this exemption. No status-aware wire extension or separate cheap job is
implemented here; this matrix does not claim unconditional coverage of all
210 observed inputs or a future base's input closure.
Explicit `not_applicable` is different from absent, failed or skipped evidence.

GoLint selection controls resolve declarative `GOFLAGS` with workflow, job and
step precedence, retaining original YAML node types. The effective value must
be omitted or a literal empty string; a higher-precedence literal empty value
may override a lower unsafe value. Nonempty flags and unprovable expressions
cannot establish execution of the full controls. Explicit
`continue-on-error: false` is valid; true or dynamic values are not.

The ordinary source record binds the immutable event's PR head, base SHA/ref and
tested merge SHA, plus the actual caller workflow, workflow run and attempt.
Receipt capture reconstructs those fields from the runner event/environment and
compares them with the plan; it does not use the plan itself as proof of caller
identity. Both callable selection and receipt capture independently compare
the plan's complete path inventory with the actual tested merge. Changed paths come from the
complete NUL-delimited Git diff against that base, including old and new names
for renames. Discovery requires both merge parents; malformed or unavailable
source cannot become an empty path list. Every participating checkout needs
both merge parents, including receipt-producing jobs. A feature/stack base is valid and must
not be silently replaced with `main`.

Transfer the full plan as a same-run, attempt-qualified artifact. Only its
artifact name and small applicability flags belong in job outputs; the callable
`pr_completeness_plan` string identifies that artifact, not an embedded JSON
manifest. This avoids passing a potentially large changed-path inventory through
job-output or environment limits. Consumers must download from the current run
and validate the artifact's source and complete paths before selection.

Each applicable job must provide its initial checkout commit, final checked-out
SHA and successful command outcomes/conclusions. Matrix members are enumerated
individually, not collapsed into last-writer-wins outputs. Browser receipts
require actual nonempty Chromium results with zero retries: both the selected
project's configured retry limit and each result's recorded retry index must be
zero. Missing retry evidence is unavailable. These fields follow the repository's
[locked Playwright 1.64.1 JSON reporter](https://github.com/microsoft/playwright/blob/v1.64.1/packages/playwright/src/reporters/json.ts).
The one intentional
ordinary-dashboard skip is the actual-producer assurance case; that exact case
must pass in Offline Grader Assurance with its real exported reports. Receipt
capture retains required outcomes and case results, not unrelated action outputs
or browser logs. This is workflow-execution evidence, not independent attestation
or a performance/model-quality claim.

Site's single compound `chat` step runs private-chat controls, chat Chromium and
unavailable-service Chromium in an `&&` chain. Its outcome covers that entire
chain, not independently observed outcomes for each subcommand. The two browser
reports remain required, but the receipt must not invent a separate
`chat_unavailable` or private-chat step outcome.

Final admission additionally requires a fresh PR API snapshot matching the
tested PR number, open state, repository, head SHA and base SHA/ref. Missing
API evidence or a head/base change during validation makes every scope
unavailable; independent command failures are still reported. The workflow
must fetch and check that snapshot immediately before evaluation. This is a
point-in-time check, not a guarantee that the PR cannot change afterward;
merge-time verification remains required.

The final `evaluate` CLI independently repeats the actual caller and tested
merge admission used by selection and receipt capture. A complete passing input
from another event, run, attempt or merge cannot supply ordinary completeness
success, and an omitted changed path is rejected. The exported pure `evaluate`
API remains offline; executing the CLI requires the runner event and checkout.

Body/title-only PR edits must use **distinct check names and concurrency** and
must not publish ordinary completeness success or cancel active validation.
Base-retarget edits require new head/base/tested-merge validation. Pushes and
manual cold/performance observations must not create a skipped or successful
ordinary `PR completeness` context. Docker's separately admitted edited-body
benchmark is not a completeness prerequisite or a substitute for normal image
and native-runner proof.

The retarget classifier follows the [pinned Octokit edited-event contract](https://github.com/octokit/webhooks/blob/aa80c24a3f55354465b8b1438d33dd5602956d0d/payload-schemas/api.github.com/pull_request/edited.schema.json):
both old base `ref.from` and `sha.from` are required. The current base ref must
differ; different branches at the same SHA remain valid. An arbitrary nonempty
change object is not retarget evidence, and unknown shapes fail closed. The
local event fixtures remain synthetic controls: schema verification does not
replace genuine retarget-event execution before adoption.

### Administrator migration plan

1. Review the final source and applicability matrix, including the admitted
   Docker measurement interface and CI capture changes. Preserve `Lint`,
   `ubuntu-latest`, `windows-latest`, `license/cla`, native five-profile coverage
   verification, source identity and current independent-review requirements.
2. Verify genuine current-head ordinary PR runs for production, documentation,
   unrelated-prose and feature-base cases. Inspect actual jobs, command outcomes,
   all matrix members and selected browser cases, not just the summary color.
   Verify base-retarget events refresh proof and body-only edits cannot replace
   it. Manual observation runs are not this admission cohort.
3. Exercise failures, missing dependencies/receipts, path omissions, failed
   detection, nested skips, retries and source/attempt/base mismatches through
   the production caller and actual workflow-source mutation controls. Confirm
   each prevents ordinary completeness success.
4. Re-read active branch rules and obtain explicit administrator authorization
   for an additive `PR completeness` requirement from the verified GitHub Actions
   source. Do not rename/remove legacy requirements or self-approve the change.
   This implementation does not write repository protection settings.
5. After authorized adoption, recheck current heads and stacked-base behavior.
   Missing or pending checks block admission; do not treat a fallback, bypass or
   manually dispatched cohort as passing ordinary validation.

Bounded local controls use `node --test .github/scripts/pr-completeness.test.cjs`
with the repository's Node 22.18+ or 24+ runtime. Trigger-parser and offline
runtime import-closure controls use
`go test -count=1 ./.github/scripts -run '^Test(WorkflowTriggerTypedInputs|CompletenessRuntimeImportClosure)$'`.
The closure control parses the selected Linux AMD64 and macOS ARM64 source
imports with the workflow's race tag, including compiled execution test helpers
and excluding inactive build tags; it does not compile or execute either
platform's runtime probe.
`TestCompletenessProductionCLIControls` runs all 16 Node controls through the
Go test package already selected unconditionally by Linux GoLint. It requires
the full passing TAP inventory with zero failures, cancellations or skips;
missing Node is a failure. `TestCompletenessControlsSelectedByGoLint` rejects
omitted-package, echo-only, skipped-step and narrowed-test workflow mutations.
The Git fixture retains rename, deletion, stack and helper-omission coverage on
Windows; only its literal newline filename uses a portable alternative there.
No native Windows execution is claimed from local Unix controls.
These checks do not by themselves prove GitHub routing, native platforms, browser
execution or ruleset adoption.

## Prerequisites

- waza binary in your PATH (install with the script below)
- A skill repository with an `eval.yaml` file
- Credentials for LLM APIs (Copilot SDK, OpenAI, etc.) — usually stored as GitHub Actions secrets

## Installation in CI

### GitHub Actions

```yaml
- name: Install waza
  run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

### Azure DevOps Pipelines

```yaml
- script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
  displayName: Install waza
```

## GitHub Actions Workflow

### Basic Evaluation Workflow

Create `.github/workflows/waza-eval.yml`:

```yaml
name: Run Skill Evaluations

on:
  pull_request:
    paths:
      - 'skills/**'
      - 'evals/**'
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Check results
        run: |
          PASS_RATE=$(jq '.summary.pass_rate' results.json)
          echo "Pass Rate: $PASS_RATE"
          if (( $(echo "$PASS_RATE < 0.8" | bc -l) )); then
            echo "❌ Pass rate below threshold"
            exit 1
          fi

      - name: Upload results
        if: always()
        uses: actions/upload-artifact@v3
        with:
          name: eval-results
          path: results.json
```

### Multi-Model Comparison

Compare performance across models in CI:

```yaml
name: Multi-Model Evaluation

on:
  pull_request:
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        model:
          - gpt-4o
          - gpt-4-turbo
          - claude-sonnet-4-20250514
    steps:
      - uses: actions/checkout@v4

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation with ${{ matrix.model }}
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --model ${{ matrix.model }} \
          --output results-${{ matrix.model }}.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Upload results
        uses: actions/upload-artifact@v3
        with:
          name: eval-results-${{ matrix.model }}
          path: results-${{ matrix.model }}.json
```

### Baseline Comparison

Track performance against a baseline:

```yaml
name: Baseline Comparison

on:
  pull_request:
  workflow_dispatch:

jobs:
  evaluate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash

      - name: Run evaluation (current)
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results-current.json
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Checkout main
        run: git fetch origin main:main

      - name: Run evaluation (main)
        run: |
          git stash
          git checkout main
          waza run evals/my-skill/eval.yaml \
            --context-dir evals/my-skill/fixtures \
            --output results-baseline.json
          git checkout -
          git stash pop
        env:
          COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}

      - name: Compare results
        run: waza compare results-baseline.json results-current.json
```

## Azure DevOps Pipelines

### Basic Evaluation Pipeline

Create `azure-pipelines.yml`:

```yaml
trigger:
  - main
  - develop

pr:
  paths:
    include:
      - skills/**
      - evals/**

pool:
  vmImage: 'ubuntu-latest'

steps:
  - script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
    displayName: Install waza

  - script: |
      waza run evals/my-skill/eval.yaml \
        --context-dir evals/my-skill/fixtures \
        --output results.json \
        --reporter junit:test-results.xml
    displayName: Run evaluation
    env:
      COPILOT_SDK_TOKEN: $(COPILOT_SDK_TOKEN)

  - script: |
      PASS_RATE=$(jq '.summary.pass_rate' results.json)
      echo "Pass Rate: $PASS_RATE"
      if (( $(echo "$PASS_RATE < 0.8" | bc -l) )); then
        echo "##vso[task.logissue type=error]Pass rate below threshold"
        exit 1
      fi
    displayName: Check results

  - task: PublishTestResults@2
    inputs:
      testResultsFormat: 'JUnit'
      testResultsFiles: 'test-results.xml'
    displayName: Publish test results
    condition: always()

  - task: PublishBuildArtifacts@1
    inputs:
      PathtoPublish: results.json
      ArtifactName: eval-results
    displayName: Upload results
    condition: always()
```

### Multi-Model Strategy

```yaml
strategy:
  matrix:
    gpt4o:
      MODEL: gpt-4o
    claude:
      MODEL: claude-sonnet-4-20250514
    gpt4turbo:
      MODEL: gpt-4-turbo

pool:
  vmImage: 'ubuntu-latest'

steps:
  - script: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
    displayName: Install waza

  - script: |
      waza run evals/my-skill/eval.yaml \
        --context-dir evals/my-skill/fixtures \
        --model $(MODEL) \
        --output results-$(MODEL).json
    displayName: Run evaluation with $(MODEL)
    env:
      COPILOT_SDK_TOKEN: $(COPILOT_SDK_TOKEN)
```

## GitHub Actions: Secrets Management

Store API credentials as GitHub secrets:

1. Go to **Settings → Secrets and variables → Actions**
2. Add `COPILOT_SDK_TOKEN` with your token value
3. Reference in workflows as `${{ secrets.COPILOT_SDK_TOKEN }}`

To create a Copilot SDK token, authenticate with the CLI:

```bash
copilot auth login
```

## Azure DevOps: Secrets Management

1. Go to **Pipelines → Library → Secure files**
2. Upload `.env` or store in **Variables**
3. Add variable `COPILOT_SDK_TOKEN` (check "Keep this value secret")
4. Reference as `$(COPILOT_SDK_TOKEN)` in YAML

## Best Practices

### 1. Cache Results for Faster CI

```yaml
- uses: actions/cache@v3
  with:
    path: |
      .waza-cache
      ~/.cache/waza
    key: waza-${{ hashFiles('evals/**') }}
    restore-keys: |
      waza-

- name: Run evaluation
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --cache \
    --output results.json
```

### 2. Fail on Score Thresholds

Use JSON queries to enforce quality gates:

```bash
#!/bin/bash
set -e

PASS_RATE=$(jq '.summary.pass_rate' results.json)
MIN_PASS_RATE=0.85

if (( $(echo "$PASS_RATE < $MIN_PASS_RATE" | bc -l) )); then
  echo "❌ Pass rate $PASS_RATE is below minimum $MIN_PASS_RATE"
  exit 1
fi

echo "✅ Pass rate $PASS_RATE meets threshold"
```

### 3. Run Tasks in Parallel for Speed

```yaml
- name: Run evaluation (parallel)
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --parallel \
    --output results.json
```

### 4. Filter Tasks by Pattern

Only run specific tasks to save time:

```yaml
- name: Run critical tasks only
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --task "critical-*" \
    --output results.json
```

### 5. Enable Session Logging for Debugging

```yaml
- name: Run evaluation with logging
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --session-dir ./logs \
    --session-log \
    --output results.json

- name: Upload logs
  if: failure()
  uses: actions/upload-artifact@v3
  with:
    name: session-logs
    path: logs/
```

### 6. Post Results as PR Comment

```yaml
- name: Comment on PR
  if: always()
  uses: actions/github-script@v7
  with:
    script: |
      const fs = require('fs');
      const results = JSON.parse(fs.readFileSync('results.json', 'utf8'));
      const comment = `
## Evaluation Results
- **Pass Rate:** ${(results.summary.pass_rate * 100).toFixed(1)}%
- **Tasks Passed:** ${results.summary.tasks_passed}/${results.summary.tasks_total}
- **Duration:** ${results.summary.duration_ms}ms
      `;
      github.rest.issues.createComment({
        issue_number: context.issue.number,
        owner: context.repo.owner,
        repo: context.repo.repo,
        body: comment
      });
```

### 7. Separate Fast and Slow Tests

```yaml
jobs:
  smoke-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - name: Run smoke tests
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --task "smoke-*" \
          --output results.json

  full-tests:
    runs-on: ubuntu-latest
    if: github.event_name == 'push' || github.event.pull_request.draft == false
    steps:
      - uses: actions/checkout@v4
      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - name: Run full test suite
        run: waza run evals/my-skill/eval.yaml \
          --context-dir evals/my-skill/fixtures \
          --output results.json
```

## Interpreting Results

### Exit Codes

- **`0`** — Evaluation passed (all tasks met thresholds)
- **`1`** — Evaluation failed (one or more tasks below threshold)
- **`2`** — Configuration or runtime error

### Result JSON Structure

```json
{
  "summary": {
    "eval_name": "my-skill-eval",
    "status": "passed",
    "pass_rate": 0.95,
    "tasks_passed": 19,
    "tasks_total": 20,
    "duration_ms": 5000,
    "timestamp": "2025-01-20T10:30:00Z"
  },
  "tasks": [
    {
      "id": "task-001",
      "name": "Example Task",
      "status": "passed",
      "score": 1.0,
      "duration_ms": 250,
      "trials": 1
    }
  ]
}
```

### Threshold Configuration

Set pass thresholds in `eval.yaml`:

```yaml
config:
  model: claude-sonnet-4-20250514
  pass_threshold: 0.8
  metrics:
    - name: task_completion
      threshold: 0.85
    - name: trigger_accuracy
      threshold: 0.90
```

## Troubleshooting

### Issue: "COPILOT_SDK_TOKEN not set"

**Solution:** Add the secret to your GitHub Actions workflow:
```yaml
env:
  COPILOT_SDK_TOKEN: ${{ secrets.COPILOT_SDK_TOKEN }}
```

### Issue: Tests timeout in CI

**Solution:** Increase timeout or use `--timeout` flag:
```yaml
- name: Run evaluation
  timeout-minutes: 10
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --output results.json
```

### Issue: Fixtures not found

**Solution:** Ensure `--context-dir` is relative to repository root:
```bash
waza run evals/my-skill/eval.yaml \
  --context-dir evals/my-skill/fixtures
```

### Issue: Out of tokens

**Solution:** Filter tasks or use caching:
```yaml
- name: Run evaluation (filtered)
  run: waza run evals/my-skill/eval.yaml \
    --context-dir evals/my-skill/fixtures \
    --task "priority-*" \
    --cache \
    --output results.json
```

## Advanced Workflows

### Approval-Based Releases

```yaml
- name: Check score before release
  run: |
    SCORE=$(jq '.summary.pass_rate' results.json)
    if (( $(echo "$SCORE < 0.95" | bc -l) )); then
      echo "❌ Score $SCORE requires approval"
      exit 1
    fi
```

### Trend Tracking

Store results in artifacts and compare over time:

```yaml
- name: Download previous results
  continue-on-error: true
  run: |
    gh run download -n eval-results --dir ./prev-results
  env:
    GH_TOKEN: ${{ github.token }}

- name: Compare trends
  run: |
    if [ -f ./prev-results/results.json ]; then
      waza compare ./prev-results/results.json results.json
    fi
```

## Next Steps

- **Explore the examples:** Check `examples/` in the waza repository for complete working workflows
- **Configure secrets:** Set up Copilot SDK tokens in your CI system
- **Define quality gates:** Establish pass/fail thresholds for your skills
- **Monitor trends:** Use stored results to track skill performance over time
