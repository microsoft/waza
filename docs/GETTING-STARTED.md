# Getting Started with Waza

A complete walkthrough for creating, testing, and validating AI agent skills.

## Prerequisites

- **waza** installed (see [Installation](../README.md#installation))
- **Go 1.25+** (if building from source)
- A text editor for SKILL.md and eval configuration

## Two Workflows: Project vs Standalone

Waza supports two skill development workflows:

| Workflow | Best For | Structure |
|----------|----------|-----------|
| **Project Mode** | Multiple skills in one repo | `skills/` + `evals/` directories |
| **Standalone Mode** | Single skill, minimal setup | Self-contained skill directory |

Choose **Project Mode** if you're building multiple skills or contributing to [microsoft/skills](https://github.com/microsoft/skills). Use **Standalone** for quick single-skill experiments.

---

## Project Mode: Multi-Skill Workspace

### Step 1: Initialize Your Project

```bash
# Create a new project directory
mkdir my-skills-repo
cd my-skills-repo

# Initialize the workspace
waza init
```

This creates:
```
my-skills-repo/
├── skills/           # Skill definitions
├── evals/            # Evaluation suites
├── .github/workflows/eval.yml  # CI/CD pipeline
├── .gitignore
└── README.md
```

You'll be prompted to create your first skill. You can:
- Type a skill name (e.g., `code-explainer`) and continue
- Type `skip` to initialize without a skill
- Use `--no-skill` flag to skip the prompt entirely

### Step 2: Create a New Skill

```bash
cd my-skills-repo
waza new code-explainer
```

This scaffolds:
```
my-skills-repo/
├── skills/
│   └── code-explainer/
│       └── SKILL.md              # Skill definition
├── evals/
│   └── code-explainer/
│       ├── eval.yaml             # Eval configuration
│       ├── tasks/
│       │   ├── basic-usage.yaml
│       │   ├── edge-case.yaml
│       │   └── should-not-trigger.yaml
│       └── fixtures/
│           └── sample.py
```

### Step 3: Define Your Skill

Edit `skills/code-explainer/SKILL.md`:

```yaml
---
name: code-explainer
type: utility
description: |
  USE FOR: Explaining code, analyzing code patterns, refactoring suggestions
  DO NOT USE FOR: Running code, generating boilerplate
---

# Code Explainer

## Overview

Helps developers understand existing code by breaking down logic, identifying patterns, and explaining complex sections.

## Usage

**Triggers:**
- "Explain this Python function"
- "What does this code do?"
- "Walk me through this algorithm"

## References

- [Python AST Module](https://docs.python.org/3/library/ast.html)
- [Code Analysis Best Practices](https://example.com)
```

### Step 4: Write Evaluation Tasks

Edit `evals/code-explainer/tasks/basic-usage.yaml`:

```yaml
id: basic-usage-001
name: Basic Usage - Python Function
description: Test that the skill explains a simple Python function correctly.
tags:
  - basic
  - happy-path
inputs:
  prompt: "Explain this function"
  files:
    - path: sample.py
expected:
  output_contains:
    - "function"
    - "parameter"
    - "return"
  outcomes:
    - type: task_completed
  behavior:
    max_tool_calls: 5
    max_response_time_ms: 30000
```

Create additional tasks in `evals/code-explainer/tasks/` as needed:
- `edge-case.yaml` — boundary conditions, error handling
- `should-not-trigger.yaml` — negative tests (prompt doesn't match skill intent)
- `advanced.yaml` — complex scenarios

### Step 5: Add Test Fixtures

Place test files in `evals/code-explainer/fixtures/`:

**fixtures/sample.py:**
```python
def fibonacci(n):
    """Calculate the nth Fibonacci number."""
    if n <= 1:
        return n
    return fibonacci(n - 1) + fibonacci(n - 2)
```

**fixtures/complex.py:**
```python
class DataProcessor:
    def __init__(self, data):
        self.data = data
    
    def transform(self):
        return [x * 2 for x in self.data if x > 0]
```

### Step 6: Configure Your Evaluation

Edit `evals/code-explainer/eval.yaml`:

```yaml
name: code-explainer-eval
description: Evaluation suite for code-explainer skill
skill: code-explainer
version: "1.0"

config:
  trials_per_task: 1           # Run each task once
  timeout_seconds: 300         # 5-minute timeout
  parallel: false              # Run tasks sequentially
  executor: mock               # Use mock executor (no API calls)
  model: gpt-4o

graders:
  - type: code
    name: has_output
    config:
      assertions:
        - "len(output) > 100"
  
  - type: regex
    name: explains_concepts
    config:
      pattern: "(?i)(function|variable|parameter|return|logic)"
  
  - type: behavior
    name: reasonable_cost
    config:
      max_tool_calls: 10
      max_response_time_ms: 30000

tasks:
  - "tasks/*.yaml"
```

### Step 7: Run Evaluations

```bash
# Run all evaluations
waza run

# Run one skill's evaluations
waza run code-explainer

# Verbose output
waza run code-explainer -v

# Save results
waza run code-explainer -o results.json
```

Example output:
```
Running evaluations for code-explainer...
  ✓ basic-usage-001 passed (has_output, explains_concepts, reasonable_cost)
  ✓ edge-case-001 passed
  ✓ should-not-trigger-001 passed

Results: 3/3 tasks passed ✓
```

### Step 8: Check Skill Readiness

Validate your skill is production-ready:

```bash
# Check all skills
waza check

# Check one skill
waza check code-explainer

# Improve compliance interactively
waza dev code-explainer --target high --auto
```

Output:
```
🔍 Skill Readiness Check
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Skill: code-explainer

📋 Compliance Score: High
   ✅ Excellent! Your skill meets all compliance requirements.

📊 Token Budget: 420 / 500 tokens
   ✅ Within budget (80 tokens remaining).

🧪 Evaluation Suite: Found
   ✅ eval.yaml detected. Run 'waza run eval.yaml' to test.

✅ Your skill is ready for submission!
```

### Step 9: Commit and Push

```bash
# Stage changes
git add skills/ evals/

# Commit
git commit -m "feat: add code-explainer skill

- SKILL.md with comprehensive documentation
- 5 evaluation tasks covering basic, edge case, and negative tests
- Eval suite with code, regex, and behavior validators"

# Push to trigger CI
git push -u origin my-feature

# Open PR
# CI automatically runs: waza run code-explainer
# Results posted as workflow artifact
```

---

## Standalone Mode: Single-Skill Repository

Use this for a single skill, quick prototypes, or when you don't need a workspace.

### Step 1: Create a Standalone Skill

```bash
waza new my-translator
```

This creates a self-contained directory:
```
my-translator/
├── SKILL.md                    # Skill definition
├── evals/
│   ├── eval.yaml             # Eval spec
│   ├── tasks/
│   │   ├── basic-usage.yaml
│   │   ├── edge-case.yaml
│   │   └── should-not-trigger.yaml
│   └── fixtures/
│       └── sample.txt
├── .github/workflows/
│   └── eval.yml              # Ready-to-use CI
├── .gitignore
└── README.md
```

### Step 2-9: Same as Project Mode

Follow steps 3-9 above, but run commands from the skill root:

```bash
cd my-translator

# Define skill
edit SKILL.md

# Write tasks
edit evals/tasks/basic-usage.yaml

# Add fixtures
echo "Sample text for translation" > evals/fixtures/sample.txt

# Run evaluations
waza run evals/eval.yaml --context-dir evals/fixtures -v

# Check readiness
waza check

# Commit and push
git add .
git commit -m "feat: add my-translator skill"
git push
```

---

## Workspace Auto-Detection

Waza automatically detects your workspace structure and adapts commands:

### Detection Rules

Waza checks for workspace context in this order:

1. **Single-skill (SKILL.md in CWD)** → Use that skill directly
2. **Single-skill (SKILL.md in parent)** → You're inside a skill subdirectory
3. **Multi-skill (skills/ + evals/)** → Use project structure
4. **Standalone child skills** → Find immediate children with SKILL.md

### Examples

```bash
# Project mode: Run from project root
cd my-skills-repo
waza run              # Runs all skills' evals
waza run code-explainer  # Run one skill

# Project mode: Run from subdirectory
cd my-skills-repo/skills/code-explainer
waza check            # Finds SKILL.md in parent, checks readiness

# Standalone mode
cd my-translator
waza run evals/eval.yaml  # Self-contained
waza check               # Checks current skill

# Multi-skill sibling scan
cd sibling-skills
# Scans ./*/SKILL.md and ./evals/*/eval.yaml
waza run              # Run all discovered skills
```

---

## Advanced: Interactive Skill Wizard

Create skills with guided metadata collection:

```bash
waza new code-formatter --interactive
```

The wizard asks:
- Skill name
- Type (utility, analysis, generation, etc.)
- Triggers (example prompts)
- Description
- References

And generates a complete SKILL.md.

---

## Migration: Old Layout to New Separated Layout

If you have an old co-located layout:
```
my-skills-repo/
└── code-explainer/
    ├── SKILL.md
    └── eval.yaml
```

Migrate to the separated convention:

```bash
# Initialize new structure
cd my-skills-repo
waza init --no-skill

# Move existing skill
mkdir skills
mv code-explainer skills/

# Create eval directory structure
mkdir -p evals/code-explainer
mv skills/code-explainer/eval.yaml evals/code-explainer/
mkdir -p evals/code-explainer/{tasks,fixtures}

# Move tasks and fixtures if you have them
# (This depends on your existing structure)

# Test the new layout
waza run code-explainer
```

---

## Typical Development Workflow

```bash
# 1. Start project
waza init my-project && cd my-project

# 2. Create a skill
waza new my-skill

# 3. Define the skill
edit skills/my-skill/SKILL.md

# 4. Write evaluation tasks
edit evals/my-skill/tasks/*.yaml

# 5. Add test fixtures
cp ~/my-fixtures/* evals/my-skill/fixtures/

# 6. Run evaluations locally
waza run my-skill -v

# 7. Improve based on failures
# (edit SKILL.md or tasks as needed)
waza run my-skill -v

# 8. Check readiness
waza check my-skill

# 9. Optimize token usage
waza tokens count skills/my-skill/SKILL.md
waza tokens suggest skills/my-skill/SKILL.md

# 10. Commit and push
git add .
git commit -m "feat: add my-skill"
git push

# 11. CI runs automatically, results posted to PR
```

---

## Next Steps

- **[Grader Reference](GRADERS.md)** — Understand all grader types
- **[Eval Spec Format](../README.md#eval-spec-format)** — Full YAML schema
- **[CI/CD Integration](SKILLS_CI_INTEGRATION.md)** — GitHub Actions setup
- **[Token Management](TOKEN-LIMITS.md)** — Optimize skill size
- **[Demo Guide](DEMO-GUIDE.md)** — Live presentation scenarios

---

## Troubleshooting

### "skill not found in workspace"
Make sure you're in a project with `skills/` directory or a standalone skill with `SKILL.md`.

### "eval.yaml not found"
Check that:
- File is at `evals/{skill-name}/eval.yaml` (project mode)
- Or at `{skill}/evals/eval.yaml` (standalone)
- Or at `{skill}/eval.yaml` (legacy/co-located)

### "No tasks in eval.yaml"
Ensure your `eval.yaml` has:
```yaml
tasks:
  - "tasks/*.yaml"
```

And that you have `.yaml` files in `tasks/` directory.

### "Mock executor always passes"
The `mock` executor is meant for local iteration without API calls. For real evaluation, use `executor: copilot-sdk` and set `GITHUB_TOKEN`.

---

## Support

- **Issues:** [github.com/spboyer/waza/issues](https://github.com/spboyer/waza/issues)
- **Discussions:** [github.com/spboyer/waza/discussions](https://github.com/spboyer/waza/discussions)
