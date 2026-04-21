# CI/CD Integration Guide

This guide explains how to integrate waza evaluations into your CI/CD pipelines using GitHub Actions, Azure DevOps, and other platforms.

## Overview

Running waza evaluations in CI/CD allows you to:

- **Catch regressions** before merging pull requests
- **Benchmark skills** across multiple models in every PR
- **Track quality** metrics over time
- **Gate releases** on evaluation scores
- **Automate testing** without manual steps

## Prerequisites

- Go 1.26+ or waza binary in your PATH
- A skill repository with an `eval.yaml` file
- Credentials for LLM APIs (Copilot SDK, OpenAI, etc.) — usually stored as GitHub Actions secrets

## Installation in CI

### GitHub Actions

```yaml
- name: Install waza
  run: go install github.com/microsoft/waza/cmd/waza@latest
```

Or use the official installer:

```yaml
- name: Install waza
  run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

### Azure DevOps Pipelines

```yaml
- script: go install github.com/microsoft/waza/cmd/waza@latest
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

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.26'

      - name: Install waza
        run: go install github.com/microsoft/waza/cmd/waza@latest

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

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.26'

      - name: Install waza
        run: go install github.com/microsoft/waza/cmd/waza@latest

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

      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.26'

      - name: Install waza
        run: go install github.com/microsoft/waza/cmd/waza@latest

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
  - task: GoTool@0
    inputs:
      version: '1.26'
    displayName: Set up Go

  - script: go install github.com/microsoft/waza/cmd/waza@latest
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
  - task: GoTool@0
    inputs:
      version: '1.26'

  - script: go install github.com/microsoft/waza/cmd/waza@latest
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
      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.26'
      - name: Install waza
        run: go install github.com/microsoft/waza/cmd/waza@latest
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
      - name: Set up Go
        uses: actions/setup-go@v4
        with:
          go-version: '1.26'
      - name: Install waza
        run: go install github.com/microsoft/waza/cmd/waza@latest
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
