# Integration Testing with Copilot SDK

This guide explains how to run real integration tests using the GitHub Copilot SDK.

## Prerequisites

1. **Install the Copilot SDK dependency:**
   ```bash
   pip install waza[copilot]
   ```

2. **Authenticate with Copilot CLI:**
   ```bash
   copilot
   # Follow prompts to authenticate
   ```

## Configuration

### Eval Spec Configuration

Update your `eval.yaml` to use the Copilot SDK executor:

```yaml
name: my-waza
skill: my-skill
version: "1.0"

config:
  trials_per_task: 3
  executor: copilot-sdk           # Use real Copilot SDK
  model: claude-sonnet-4-20250514  # Specify model
  timeout_seconds: 300
  
  # Skill directories for the SDK to load
  skill_directories:
    - ./skills
    - /path/to/other/skills
  
  # MCP server configurations (optional)
  mcp_servers:
    azure:
      type: stdio
      command: npx
      args: ["-y", "@azure/mcp", "server", "start"]
```

### CLI Override

You can override the executor and model at runtime:

```bash
# Run with Copilot SDK instead of mock
waza run eval.yaml --executor copilot-sdk --model claude-sonnet-4-20250514

# Run with verbose output to see conversation in real-time
waza run eval.yaml --executor copilot-sdk -v

# Provide project context files
waza run eval.yaml --executor copilot-sdk --context-dir ./my-project

# Save conversation transcript for debugging
waza run eval.yaml --executor copilot-sdk --log transcript.json

# Full debugging session
waza run eval.yaml \
  --executor copilot-sdk \
  --model claude-sonnet-4-20250514 \
  --context-dir ./fixtures \
  --log transcript.json \
  --output results.json \
  -v

# Compare different models
waza run eval.yaml --model gpt-4o -o results-gpt4o.json
waza run eval.yaml --model claude-sonnet-4-20250514 -o results-claude.json
waza compare results-gpt4o.json results-claude.json
```

## Executor Types

| Executor | Description | Use Case |
|----------|-------------|----------|
| `mock` | Simulates responses | Unit tests, CI without API keys |
| `copilot-sdk` | Real Copilot agent sessions | Integration tests, benchmarking |

## CopilotExecutor Features

The `CopilotExecutor` wraps the `@github/copilot-sdk` to provide:

- **Real LLM responses** from specified models
- **Skill invocation tracking** - verify your skill was called
- **Tool call validation** - ensure expected tools were used
- **Session event capture** - full transcript for analysis
- **Workspace isolation** - each trial runs in a temp directory

### Execution Result

Each execution returns an `ExecutionResult` with:

```python
result = await executor.execute(
    prompt="Deploy my app to Azure",
    context={"files": [{"path": "app.py", "content": "..."}]},
    skill_name="azure-deploy"
)

# Access results
print(result.output)              # Final assistant response
print(result.events)              # Session events (transcript)
print(result.tool_calls)          # Tools that were called
print(result.is_skill_invoked("azure-deploy"))  # Check skill activation
print(result.contains_keyword("deployed"))       # Check for keywords
```

## Model Comparison

Compare results across different models:

```bash
# Run the same eval with different models
waza run eval.yaml --model gpt-4o -o results/gpt-4o.json
waza run eval.yaml --model claude-sonnet-4-20250514 -o results/claude.json
waza run eval.yaml --model gpt-4o-mini -o results/gpt-4o-mini.json

# Generate comparison report
waza compare results/*.json -o comparison-report.md
```

### Comparison Output

```
Model Comparison Report

              Summary Comparison              
┏━━━━━━━━━━━━━━━━━┳━━━━━━━━┳━━━━━━━━━┳━━━━━━━━━━━━━┓
┃ Metric          ┃ gpt-4o ┃ claude  ┃ gpt-4o-mini ┃
┡━━━━━━━━━━━━━━━━━╇━━━━━━━━╇━━━━━━━━━╇━━━━━━━━━━━━━┩
│ Pass Rate       │ 100.0% │  95.0%  │      85.0%  │
│ Composite Score │   0.98 │   0.92  │        0.81 │
│ Tasks Passed    │   20/20│  19/20  │       17/20 │
└─────────────────┴────────┴─────────┴─────────────┘

🏆 Best: gpt-4o (score: 0.98)
```

## CI/CD Integration

### Skip Integration Tests in CI

Integration tests require authentication and are typically skipped in CI:

```yaml
# .github/workflows/test.yaml
- name: Run unit tests
  run: waza run eval.yaml --executor mock
  
- name: Run integration tests (manual only)
  if: github.event_name == 'workflow_dispatch'
  run: waza run eval.yaml --executor copilot-sdk
```

### Environment Variables

| Variable | Effect |
|----------|--------|
| `CI=true` | Auto-detected in CI; forces mock executor |
| `SKIP_INTEGRATION_TESTS=true` | Explicitly skip real SDK tests |

## Troubleshooting

### "Copilot SDK not installed"

```bash
pip install waza[copilot]
# or
pip install copilot-sdk
```

### "Authentication failed"

Run the Copilot CLI and authenticate:
```bash
copilot
```

### "Session timed out"

Increase timeout in config:
```yaml
config:
  timeout_seconds: 600  # 10 minutes
```

## Example: Full Integration Test

```yaml
# eval.yaml
name: azure-deploy-integration
skill: azure-deploy
version: "1.0"

config:
  trials_per_task: 3
  executor: copilot-sdk
  model: claude-sonnet-4-20250514
  timeout_seconds: 300
  skill_directories:
    - ../../skills

tasks:
  - tasks/*.yaml

graders:
  - type: code
    name: skill_invoked
    config:
      assertions:
        - "'azure-deploy' in str(transcript)"
  
  - type: text
    name: deployment_link
    config:
      regex_match:
        - "azurewebsites\\.net|azurestaticapps\\.net"
```

```bash
# Run with real Copilot SDK
waza run eval.yaml --executor copilot-sdk -o integration-results.json
```
