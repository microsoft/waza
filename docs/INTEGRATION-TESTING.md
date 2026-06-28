# Integration Testing with Copilot SDK

This guide explains how to run real integration tests using the GitHub Copilot SDK.

## Prerequisites

1. **Install waza:**
   ```bash
   curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
   ```

2. **Authenticate with Copilot CLI:**
   ```bash
   copilot
   # Follow prompts to authenticate
   ```

   Waza bundles the GitHub Copilot CLI used by the `copilot-sdk` executor and extracts it to the local user cache on first use. Set `COPILOT_CLI_PATH` only when you need to force a specific Copilot CLI binary.

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
  model: claude-sonnet-4.6        # Specify model
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

### Hermetic MCP Mock Servers

For CI-safe Copilot SDK evals, prefer top-level `mcp_mocks` over live `config.mcp_servers`. Mock servers run as local stdio MCP servers managed by waza, so they do not bind ports, make network calls, or require service credentials. Because `mcp_mocks` is an additive eval schema field, set `schemaVersion: "1.1"`.

```yaml
name: github-triage
skill: issue-triage
schemaVersion: "1.1"
version: "1.0"

config:
  trials_per_task: 1
  executor: copilot-sdk
  model: claude-sonnet-4.6
  timeout_seconds: 300

mcp_mocks:
  - name: github
    tools:
      list_issues:
        input_schema:
          type: object
          properties:
            owner: { type: string }
            repo: { type: string }
          required: [owner, repo]
        responses:
          - match:
              owner: microsoft
              repo: waza
            return:
              issues:
                - number: 363
                  title: MCP server mocks for hermetic eval
          - match_schema:
              type: object
              required: [owner, repo]
            error: "No matching issue fixture"

tasks:
  - tasks/*.yaml
```

Responses are matched in order. Use `match` for exact full-argument fixtures, `match_schema` for JSON Schema matching, and `match_regex` for per-field regular expressions. Unknown tools or unmatched calls fail with a clear MCP tool error so missing fixtures do not pass silently.

### CLI Override

You can override the model and runtime options at launch:

```bash
# Run with a different model
waza run eval.yaml --model gpt-4o

# Run with verbose output to see conversation in real-time
waza run eval.yaml --model claude-sonnet-4.6 -v

# Provide project context files
waza run eval.yaml --model claude-sonnet-4.6 --context-dir ./my-project

# Save conversation transcript for debugging
waza run eval.yaml --model claude-sonnet-4.6 --session-log

# Full debugging session
waza run eval.yaml \
  --model claude-sonnet-4.6 \
  --context-dir ./fixtures \
  --session-log \
  --output results.json \
  -v

# Compare different models
waza run eval.yaml --model gpt-4o -o results-gpt4o.json
waza run eval.yaml --model claude-sonnet-4.6 -o results-claude.json
waza compare results-gpt4o.json results-claude.json
```

## Executor Types

| Executor | Description | Use Case |
|----------|-------------|----------|
| `mock` | Echoes task metadata, context, and file content previews (up to 1KB per file) | Unit tests, CI without API keys |
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
waza run eval.yaml --model claude-sonnet-4.6 -o results/claude.json
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
  run: waza run eval.mock.yaml
  
- name: Run integration tests (manual only)
  if: github.event_name == 'workflow_dispatch'
  run: waza run eval.yaml
```

### Environment Variables

| Variable | Effect |
|----------|--------|
| `CI=true` | Auto-detected in CI; forces mock executor |
| `SKIP_INTEGRATION_TESTS=true` | Explicitly skip real SDK tests |

## Troubleshooting

### "Copilot SDK not installed"

```bash
curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
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
  model: claude-sonnet-4.6
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
waza run eval.yaml --session-log -o integration-results.json
```
