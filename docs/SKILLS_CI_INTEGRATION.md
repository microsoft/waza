# Waza Integration for microsoft/skills

This guide explains how to use waza for evaluating skills in the [microsoft/skills](https://github.com/microsoft/skills) repository.

## Overview

Waza is a Go CLI tool for running evaluations on AI agent skills. It's designed to integrate seamlessly with the microsoft/skills CI pipeline, allowing skill authors to validate their work before contributing.

## Prerequisites

- **Go 1.25+**: Required for building/installing waza
- **Git**: For cloning repositories
- **GitHub Actions** (for CI): Standard ubuntu-latest runner

## Installation Methods

### Option 1: Install from Source (Recommended for CI)

This is the recommended approach for CI/CD pipelines:

```bash
# Install latest version
go install github.com/spboyer/waza/cmd/waza@latest

# Verify installation
waza --version
```

Benefits:
- No Docker required
- Fast installation (~30 seconds)
- Always gets the latest version
- Works on all platforms

### Option 2: Docker

If you prefer containerized environments:

```bash
# Clone the waza repository
git clone https://github.com/spboyer/waza.git
cd waza

# Build the Docker image
docker build -t waza:local .

# Run waza in a container
docker run -v $(pwd):/workspace waza:local run eval.yaml
```

Benefits:
- Isolated environment
- Reproducible builds
- No local Go installation needed

### Option 3: Build from Source

For development or local testing:

```bash
# Clone the repository
git clone https://github.com/spboyer/waza.git
cd waza

# Build the binary
make build

# Run the binary
./waza --version
```

## Skill Repository Structure

Your skill repository should follow this structure:

```
your-skill/
├── SKILL.md              # Skill definition with frontmatter
├── eval/                 # Evaluation suite (optional but recommended)
│   ├── eval.yaml         # Main benchmark specification
│   ├── tasks/            # Individual task definitions
│   │   ├── task-1.yaml
│   │   └── task-2.yaml
│   └── fixtures/         # Context files for tasks
│       ├── file1.txt
│       └── file2.py
└── .github/
    └── workflows/
        └── eval.yml      # CI workflow for running evals
```

## Creating an Evaluation Suite

### Method 1: Interactive Init

```bash
# Navigate to your skill directory
cd your-skill

# Run the interactive wizard
waza init eval --interactive

# Follow the prompts to configure your evaluation
```

### Method 2: Generate from SKILL.md

If your SKILL.md has evaluation metadata in its frontmatter:

```bash
waza generate SKILL.md --output-dir eval
```

### Method 3: Manual Creation

Create `eval/eval.yaml`:

```yaml
name: my-skill-eval
skill: my-skill
version: "1.0"

config:
  trials_per_task: 1
  timeout_seconds: 300
  executor: mock          # Use mock for CI (no API keys)
  parallel: false

graders:
  - type: regex
    name: output_check
    config:
      must_match: ["expected pattern"]

tasks:
  - "tasks/*.yaml"
```

Create task files in `eval/tasks/`:

```yaml
# eval/tasks/example-task.yaml
id: example-task
name: Example Task
description: Demonstrate the skill

stimulus:
  message: "Explain what this code does"

context_files:
  - "example.py"

graders:
  - output_check
```

Add context files to `eval/fixtures/`:

```python
# eval/fixtures/example.py
def hello():
    print("Hello, world!")
```

## Running Evaluations Locally

```bash
# Basic run
waza run eval/eval.yaml --verbose

# Save results to JSON
waza run eval/eval.yaml --output results.json

# Run specific tasks only
waza run eval/eval.yaml --task "example-*"

# Run with parallel execution
waza run eval/eval.yaml --parallel --workers 4
```

## CI/CD Integration

### Step 1: Copy the Workflow Template

Copy the template workflow to your skill repository:

```bash
# From the waza repository
cp .github/workflows/skills-ci-example.yml \
   /path/to/your-skill/.github/workflows/eval.yml
```

Or download directly:
```bash
curl -o .github/workflows/eval.yml \
  https://raw.githubusercontent.com/spboyer/waza/main/.github/workflows/skills-ci-example.yml
```

### Step 2: Customize for Your Skill

Edit `.github/workflows/eval.yml`:

```yaml
on:
  pull_request:
    branches: [ main ]
    paths:
      - 'SKILL.md'
      - 'eval/**'
  push:
    branches: [ main ]

jobs:
  evaluate-skill:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      
      - name: Install Waza
        run: go install github.com/spboyer/waza/cmd/waza@latest
      
      - name: Run Evaluation
        run: waza run eval/eval.yaml --verbose --output results.json
      
      - name: Upload Results
        if: always()
        uses: actions/upload-artifact@v4
        with:
          name: evaluation-results
          path: results.json
```

### Step 3: Configure the Executor

For CI, use the **mock executor** (no API keys needed):

```yaml
# eval/eval.yaml
config:
  executor: mock  # Simulates agent behavior for testing
```

For production testing with real AI models, use the **copilot-sdk executor**:

```yaml
# eval/eval.yaml
config:
  executor: copilot-sdk
  model: claude-sonnet-4-20250514  # or gpt-4o, etc.
```

And set the `GITHUB_TOKEN` environment variable in your workflow:

```yaml
- name: Run Evaluation with Copilot
  env:
    GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
  run: waza run eval/eval.yaml --verbose
```

## Exit Codes

Waza uses exit codes to indicate success or failure in CI:

| Exit Code | Meaning | CI Behavior |
|-----------|---------|-------------|
| 0 | All tests passed | ✅ Workflow succeeds |
| 1 | One or more tests failed | ❌ Workflow fails |
| 2 | Configuration error (invalid YAML, missing files) | ❌ Workflow fails |

Example usage in CI:

```bash
# Run evaluation and fail the build if tests fail
waza run eval/eval.yaml || exit $?

# Capture exit code for custom handling
waza run eval/eval.yaml
EXIT_CODE=$?
if [ $EXIT_CODE -eq 1 ]; then
  echo "Tests failed - review results"
elif [ $EXIT_CODE -eq 2 ]; then
  echo "Configuration error - check eval.yaml"
fi
```

## Output Formats

### JSON Results

Save results in JSON format for programmatic analysis:

```bash
waza run eval/eval.yaml --output results.json
```

Output structure:

```json
{
  "benchmark": {
    "name": "my-skill-eval",
    "skill": "my-skill",
    "version": "1.0"
  },
  "config": {
    "executor": "mock",
    "model": "mock-model",
    "trials_per_task": 1
  },
  "outcomes": [
    {
      "task_id": "example-task",
      "status": "passed",
      "score": 1.0,
      "grader_results": [...]
    }
  ],
  "summary": {
    "total_tasks": 1,
    "passed": 1,
    "failed": 0,
    "pass_rate": 1.0
  }
}
```

### Transcript Files

Capture detailed execution logs:

```bash
waza run eval/eval.yaml --transcript-dir transcripts/
```

Creates one JSON file per task execution in `transcripts/`.

## Grader Types

Waza supports multiple grader types for validating agent output:

| Grader | Purpose | Example Use Case |
|--------|---------|------------------|
| `code` | Python/JavaScript assertions | Validate data structures |
| `regex` | Pattern matching | Check output format |
| `file` | File existence/content | Verify generated files |
| `behavior` | Agent behavior constraints | Limit tool calls, duration |
| `action_sequence` | Tool call sequence validation | Verify workflow steps |

See [docs/GRADERS.md](https://github.com/spboyer/waza/blob/main/docs/GRADERS.md) for complete documentation.

## Best Practices

### 1. Use Mock Executor for Fast Feedback

The mock executor runs instantly without API calls:

```yaml
config:
  executor: mock
```

Use this for:
- Pull request validation
- Quick local testing
- Grader validation

### 2. Test Locally Before CI

Always run locally first:

```bash
waza run eval/eval.yaml --verbose
```

This catches configuration errors before pushing.

### 3. Version Your Eval Suite

Include version in your eval.yaml:

```yaml
version: "1.0"
```

Update the version when making significant changes.

### 4. Use Descriptive Task IDs

```yaml
id: fix-authentication-bug  # Good
id: task-1                  # Bad
```

### 5. Document Expected Behavior

Add clear descriptions:

```yaml
description: |
  The agent should identify the authentication bug in auth.py
  and provide a fix that preserves backward compatibility.
```

### 6. Keep Fixtures Small

Use minimal context files to reduce token usage:
- Include only relevant code
- Remove comments and boilerplate
- Use snippets instead of full files

## Troubleshooting

### "Evaluation file not found"

Ensure your eval file is at `eval/eval.yaml` or update the workflow:

```yaml
- run: waza run path/to/your/eval.yaml
```

### "Configuration error (exit code 2)"

Check your YAML syntax:

```bash
# Validate YAML
waza run eval/eval.yaml --verbose
```

Common issues:
- Incorrect indentation
- Missing required fields
- Invalid task references

### "Tests failed (exit code 1)"

Review the results:

```bash
waza run eval/eval.yaml --output results.json
cat results.json | jq '.outcomes[] | select(.status == "failed")'
```

Check:
- Grader expectations match actual output
- Task descriptions are clear
- Fixtures contain necessary context

### "Go version too old"

Waza requires Go 1.25+:

```yaml
- uses: actions/setup-go@v5
  with:
    go-version: '1.25'  # Not 1.22 or earlier
```

## Example Workflows

### Basic Evaluation on PR

```yaml
name: Evaluate Skill
on:
  pull_request:
    branches: [ main ]

jobs:
  eval:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - run: go install github.com/spboyer/waza/cmd/waza@latest
      - run: waza run eval/eval.yaml --verbose
```

### Matrix Testing Across Models

```yaml
jobs:
  matrix-eval:
    strategy:
      matrix:
        model: [gpt-4o, claude-sonnet-4-20250514]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - run: go install github.com/spboyer/waza/cmd/waza@latest
      - run: |
          sed -i "s/model: .*/model: ${{ matrix.model }}/" eval/eval.yaml
          waza run eval/eval.yaml --output results-${{ matrix.model }}.json
```

### Nightly Comprehensive Testing

```yaml
on:
  schedule:
    - cron: '0 0 * * *'  # Daily at midnight

jobs:
  nightly:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - run: go install github.com/spboyer/waza/cmd/waza@latest
      - run: waza run eval/eval.yaml --verbose --output nightly-results.json
      - uses: actions/upload-artifact@v4
        with:
          name: nightly-results
          path: nightly-results.json
```

## Additional Resources

- **Main Documentation**: [README.md](https://github.com/spboyer/waza/blob/main/README.md)
- **Grader Reference**: [docs/GRADERS.md](https://github.com/spboyer/waza/blob/main/docs/GRADERS.md)
- **Example Evaluations**: [examples/](https://github.com/spboyer/waza/tree/main/examples)
- **CI Examples**: [examples/ci/](https://github.com/spboyer/waza/tree/main/examples/ci)

## Support

- **Issues**: [GitHub Issues](https://github.com/spboyer/waza/issues)
- **Discussions**: [GitHub Discussions](https://github.com/spboyer/waza/discussions)
- **Repository**: [github.com/spboyer/waza](https://github.com/spboyer/waza)
