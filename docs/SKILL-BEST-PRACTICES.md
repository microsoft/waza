# Skill Development Best Practices

This document provides guidance for creating high-quality SKILL.md files that integrate well with waza evaluations and follow industry best practices from the [Skills, Tools & MCP Development Guide](https://github.com/spboyer/azure-mcp-v-skills/blob/main/skills-mcp-development-guide.md).

## Skill Classification

Use a classification prefix in your description to clarify the skill type:

| Prefix | Use When | Example |
|--------|----------|---------|
| `**WORKFLOW SKILL**` | Multi-step orchestration | Deployment pipelines, setup wizards |
| `**UTILITY SKILL**` | Single-purpose helper | Code explanation, formatting |
| `**ANALYSIS SKILL**` | Read-only analysis/reporting | Security audits, code review |

## High-Compliance Frontmatter

A well-structured frontmatter helps the LLM correctly route user requests to your skill.

### Required Elements

```yaml
---
name: my-skill
description: |
  **{CLASSIFICATION} SKILL** - One-line description of what the skill does.
  USE FOR: trigger phrase 1, trigger phrase 2, trigger phrase 3.
  DO NOT USE FOR: scenario1 (use other-skill), scenario2 (use mcp-tool).
  INVOKES: `mcp-tool-1`, `mcp-tool-2` for execution.
  FOR SINGLE OPERATIONS: Use `mcp-tool` directly for simple queries.
---
```

> **Note:** Don't quote trigger phrases in `USE FOR:` - the parser will strip quotes automatically, but unquoted phrases are cleaner.

### Element Purposes

| Element | Purpose | Without It |
|---------|---------|------------|
| `**{TYPE} SKILL**` | Signals skill nature | LLM may route single ops here |
| `USE FOR:` | Explicit triggers | Skill won't trigger on relevant requests |
| `DO NOT USE FOR:` | Anti-triggers | False positives, conflicts |
| `INVOKES:` | MCP relationship | LLM doesn't know skill uses tools |
| `FOR SINGLE OPERATIONS:` | Bypass guidance | Users confused about skill vs. tool |

## Compliance Scoring

Skills are scored on compliance. Target **Medium-High** or better:

| Score | Requirements |
|-------|--------------|
| **Low** | Description < 150 chars OR no triggers |
| **Medium** | Description >= 150 chars AND has trigger keywords |
| **Medium-High** | Has "USE FOR:" AND "DO NOT USE FOR:" |
| **High** | Medium-High + routing clarity (INVOKES/FOR SINGLE OPERATIONS) |

### Before (Low Compliance)

```yaml
description: 'Explain code snippets'
```

### After (High Compliance)

```yaml
description: |
  **UTILITY SKILL** - Explain code snippets, functions, and algorithms in plain language.
  USE FOR: explain code, what does this function do, break down this algorithm.
  DO NOT USE FOR: writing new code (use code generation), fixing bugs (use debugging).
  INVOKES: file reading tools for code access, language detection for tailored explanations.
  FOR SINGLE OPERATIONS: Use file reading tools directly to just view code.
```

## Trigger Test Structure

When creating `trigger_tests.yaml`, include confidence levels:

```yaml
skill: my-skill

should_trigger_prompts:
  - prompt: "Explain this code to me"
    reason: "Direct explanation request"
    confidence: high

  - prompt: "Help me understand this"
    reason: "Implicit explanation request"
    confidence: medium

should_not_trigger_prompts:
  - prompt: "Write me a function to sort a list"
    reason: "Code writing request, not explaining"
    confidence: high

  - prompt: "Fix the bug in my code"
    reason: "Bug fixing requires action, not just explanation"
    confidence: medium
```

### Confidence Levels

| Level | When to Use |
|-------|-------------|
| `high` | Clear, unambiguous match/non-match |
| `medium` | Context-dependent or implicit |
| `low` | Edge cases, borderline scenarios |

## Token Budget

Keep skills lean to leave room in the context window:

| Component | Soft Limit | Hard Limit |
|-----------|------------|------------|
| `SKILL.md` | 500 tokens | 5,000 tokens |
| `references/*.md` | 1,000 tokens | 5,000 tokens |

## Skill Body Structure

```markdown
# Skill Title

## When to Use This Skill
Activate when user wants to:
- Specific action 1
- Specific action 2

## Prerequisites
- Required MCP tools: `tool-1`, `tool-2`
- Required permissions: list

## MCP Tools Used

| Step | MCP Tool | Command | Purpose |
|------|----------|---------|---------|
| 1 | `tool-1` | `command` | Gather data |
| 2 | `tool-2` | `create` | Execute action |

## Steps

### Step 1: Action Name
Detailed instructions...

## Related Skills
- For X: `azure-x-workflow`
- For Y: `azure-y-guide`
```

## Integration with waza

When generating evals with waza, the tool will automatically:

1. Parse your `USE FOR:` phrases into trigger test prompts
2. Parse your `DO NOT USE FOR:` into anti-trigger tests
3. Extract MCP tool references for grading
4. Generate fixture files based on detected patterns

### Generate and Test

```bash
# Generate eval from your skill (using 'waza new skill')
waza new skill my-skill --output-dir ./eval

# Run evaluation
waza run ./eval/eval.yaml --executor mock -v

# For real integration testing
waza run ./eval/eval.yaml --executor copilot-sdk --model claude-sonnet-4
```

## References

- [Skills, Tools & MCP Development Guide](https://github.com/spboyer/azure-mcp-v-skills/blob/main/skills-mcp-development-guide.md) - Comprehensive guide for building agent capabilities
- [MCP Protocol Specification](https://modelcontextprotocol.io/specification/latest) - Model Context Protocol details
- [waza Tutorial](./TUTORIAL.md) - Complete guide to using waza for skill evaluation
