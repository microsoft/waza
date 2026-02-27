# Agent evaluation analysis

We asked an agent to perform a series of tasks given one or more skills (see below) to help with these tasks and evaluated the agent's performance through a series of tests. Our goal was to learn how well the Skill(s) helped the agent perform tasks. Your job is to analyze the failed tests and write a concise, actionable report suggesting changes to the Skill(s) that will make them more useful to agents. Analyze each failed test--the test design and input, the agent's behavior, the outcome--and recommend changes to Skill files that will make another agent more likely to succeed in the next evaluation. The Skills and other files that were presented to the agent during the evaluation are available in your workspace.

## Requirements:

1. Suggest specific changes to specific files.
2. Base suggestions on concrete evidence such as grader criteria, grader failures, model outputs, assistant responses, and tool and skill invocations.
3. Use the included failing test definitions (YAML) and global graders from eval.yaml to understand success criteria for tests.
4. Prioritize the highest-impact fixes first.

Format your report in markdown having these sections:
- `## Key findings` (evidence-backed analysis of why the agent failed these tests)
- `## Suggested changes` (list of suggestions, each including target file(s) and rationale)

## Context: About Agent Skills

An Agent Skill is a directory containing text, scripts, and other artifacts to help an agent perform a task. A Skill looks like this on disk:

```
my-skill/
├── SKILL.md    # Required: instructions + metadata
├── scripts/    # Optional: executable code
├── references/ # Optional: documentation
└── assets/     # Optional: templates, resources
```

### SKILL.md format

SKILL.md begins with YAML frontmatter. The quality of this content is critical because agents read it when deciding whether to use a Skill. Frontmatter looks like this:

```yaml
---
name: code-explainer
description: |
  **UTILITY SKILL** - Explain code snippets, functions, and algorithms in plain language.
  USE FOR: explain code, what does this code do, break down this function,
  help me understand this, walk through this algorithm, clarify this logic,
  explain this snippet, describe what happens here.
  DO NOT USE FOR: writing new code (use code generation), fixing bugs (use debugging),
  refactoring (use refactoring skills), code review with action items.
  INVOKES: file reading tools to access code, language detection for tailored explanations.
  FOR SINGLE OPERATIONS: If the user just needs to see file contents, use file reading tools directly.
---
```

Frontmatter may contain other fields not shown in this example; you can ignore them. Only `name` and `description` are required.

The rest of SKILL.md is instructions in Markdown format describing in detail how to perform a task.

## Failing Tests

Below are details of the tests which failed during the evaluation.
