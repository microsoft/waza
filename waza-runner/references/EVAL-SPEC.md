# Eval Specification Reference

This document describes the full schema for `eval.yaml` files.

## Top-Level Fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | Yes | Unique identifier for this eval suite |
| `description` | string | No | Human-readable description |
| `skill` | string | Yes | Name of the skill being evaluated |
| `version` | string | No | Version of the eval spec (default: "1.0") |
| `config` | object | No | Runtime configuration |
| `metrics` | array | No | Metrics to calculate |
| `graders` | array | No | Graders to apply |
| `tasks` | array | Yes | Task file patterns |

## Config Object

```yaml
config:
  trials_per_task: 3      # Number of times to run each task
  timeout_seconds: 300    # Max time per trial
  parallel: false         # Run tasks concurrently
  workers: 0              # Auto-size parallel workers
  fail_fast: false        # Stop on first failure
  verbose: false          # Verbose output
```

## Metric Object

```yaml
metrics:
  - name: task_completion   # Metric identifier
    weight: 0.4             # Weight in composite score (0-1)
    threshold: 0.8          # Pass/fail threshold (0-1)
    enabled: true           # Whether to calculate this metric
```

### Available Metrics

| Metric | Description |
|--------|-------------|
| `task_completion` | Percentage of tasks completed successfully |
| `trigger_accuracy` | Precision/recall of skill triggering |
| `behavior_quality` | Quality of execution (efficiency, tool usage) |

## Grader Object

```yaml
graders:
  - type: code              # Grader type
    name: my_grader         # Unique name
    script: graders/my.py   # Path to script (for script type)
    rubric: graders/r.md    # Path to rubric (for llm type)
    model: gpt-4o-mini      # Model to use (for llm type)
    config:                 # Type-specific config
      assertions: []
```

### Grader Types

| Type | Description |
|------|-------------|
| `code` | Evaluate Python assertions |
| `regex` | Pattern matching on output |
| `tool_calls` | Validate tool usage patterns |
| `script` | Run external Python script |
| `llm` | LLM-as-judge with rubric |
| `llm_comparison` | Compare to reference output |
| `human` | Require human review |

## Task Patterns

```yaml
tasks:
  - include: tasks/*.yaml     # Glob pattern for task files
  - include: tasks/core/*.yaml
```

## Complete Example

```yaml
name: azure-deploy-eval
description: Evaluate the azure-deploy skill
skill: azure-deploy
version: "1.0"

config:
  trials_per_task: 3
  timeout_seconds: 300
  parallel: true
  workers: 0

metrics:
  - name: task_completion
    weight: 0.4
    threshold: 0.8
  - name: trigger_accuracy
    weight: 0.3
    threshold: 0.9
  - name: behavior_quality
    weight: 0.3
    threshold: 0.7

graders:
  - type: code
    name: basic_validation
    config:
      assertions:
        - "len(output) > 0"
        - "'error' not in output.lower()"
  
  - type: llm
    name: quality_check
    model: gpt-4o-mini
    rubric: |
      Score 1-5 on:
      1. Correctness
      2. Completeness
      3. Clarity

tasks:
  - include: tasks/*.yaml
```
