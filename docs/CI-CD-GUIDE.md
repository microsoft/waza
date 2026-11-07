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

## Prerequisites

- waza binary in your PATH (install with the script below)
- A skill repository with an `eval.yaml` file
- Credentials for LLM APIs (Copilot SDK, OpenAI, etc.) — usually stored as GitHub Actions secrets

## Installation in CI

The retained reusable workflows pin standalone Waza v0.41.0 and download
the release-owned installer and validator from that tag. Publish v0.41.0
before using them from another repository. Checks in the Waza repository build
the exact checkout with real dashboard assets instead of installing a mutable
release. The copied skills workflow keeps `eval/eval.yaml` for consumers; its
repository check uses `examples/code-explainer/eval.yaml` with the mock executor
and a verified rejecting Copilot runtime.

Workflow results must contain unique task IDs, valid trial records and the full
configured trial inventory for passed tasks. Summary counts and success rate
must agree with task statuses. Failed/skipped/error artifacts remain available
for diagnostics, but cannot become a successful workflow output. An omitted
context override leaves fixture resolution to the eval spec; explicit context
and output paths are forwarded unchanged.

The automatic/reusable `waza-eval.yml` result artifact contains only the selected
JSON file, including explicitly chosen hidden names or directories. It copies
the exact regular file into a fresh staging directory, preserving its basename;
custom paths are not interpreted as upload patterns. Missing files, symlink
results, or staging failures fail the workflow instead of uploading stale paths.
Optional non-hidden transcripts are uploaded separately under `<artifact-name>-transcripts`;
an empty or hidden-only transcript selection is ignored.
transcripts cannot substitute for missing result evidence. The skills example's
dispatch input is empty by default so its resolver selects the correct repository
or consumer fixture path.

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
          SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
          echo "Success rate: $SUCCESS_RATE"
          if ! jq -e '.summary.success_rate >= 0.8' results.json >/dev/null; then
            echo "❌ Success rate below threshold"
            exit 1
          fi

      - name: Upload results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: eval-results
          path: results.json
          if-no-files-found: error
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
        uses: actions/upload-artifact@v4
        with:
          name: eval-results-${{ matrix.model }}
          path: results-${{ matrix.model }}.json
          if-no-files-found: error
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
      set -euo pipefail
      SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
      echo "Success rate: $SUCCESS_RATE"
      if ! jq -e '.summary.success_rate >= 0.8' results.json >/dev/null; then
        echo "##vso[task.logissue type=error]Success rate below threshold"
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
- uses: actions/cache@v4
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

SUCCESS_RATE=$(jq -er '.summary.success_rate | numbers' results.json)
MIN_SUCCESS_RATE=0.85

if ! jq -e --argjson minimum "$MIN_SUCCESS_RATE" \
  '.summary.success_rate >= $minimum' results.json >/dev/null; then
  echo "❌ Success rate $SUCCESS_RATE is below minimum $MIN_SUCCESS_RATE"
  exit 1
fi

echo "✅ Success rate $SUCCESS_RATE meets threshold"
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
  uses: actions/upload-artifact@v4
  with:
    name: session-logs
    path: logs/
    if-no-files-found: error
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
- **Success Rate:** ${(results.summary.success_rate * 100).toFixed(1)}%
- **Tasks Succeeded:** ${results.summary.succeeded}/${results.summary.total_tests}
- **Aggregate Score:** ${results.summary.aggregate_score.toFixed(3)}
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
- **`1`** — Evaluation failed (grader failures or task/run errors; inspect recorded run statuses)
- **`2`** — Configuration or startup error before a complete evaluation

### Result JSON Structure

```json
{
  "schemaVersion": "1.9",
  "eval_id": "01JEXAMPLE",
  "skill": "my-skill",
  "eval_name": "my-skill-eval",
  "timestamp": "2026-11-06T10:30:00Z",
  "config": {
    "runs_per_test": 1,
    "model_id": "claude-sonnet-4-20250514",
    "engine_type": "copilot-sdk",
    "timeout_sec": 300
  },
  "summary": {
    "total_tests": 1,
    "succeeded": 1,
    "failed": 0,
    "errors": 0,
    "skipped": 0,
    "success_rate": 1,
    "aggregate_score": 1,
    "weighted_score": 1,
    "min_score": 1,
    "max_score": 1,
    "std_dev": 0,
    "duration_ms": 5000
  },
  "metrics": {},
  "tasks": [
    {
      "test_id": "task-001",
      "display_name": "Example Task",
      "status": "passed",
      "runs": [
        {
          "run_number": 1,
          "attempts": 1,
          "status": "passed",
          "duration_ms": 5000,
          "validations": {},
          "final_output": "Example explanation."
        }
      ]
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
    SCORE=$(jq -er '.summary.success_rate | numbers' results.json)
    if ! jq -e '.summary.success_rate >= 0.95' results.json >/dev/null; then
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
