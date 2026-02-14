# CI/CD Examples for Waza

This directory contains example GitHub Actions workflows for running waza evaluations in CI/CD pipelines.

## For microsoft/skills Contributors

If you're contributing a skill to [microsoft/skills](https://github.com/microsoft/skills), see the comprehensive integration guide:

**📖 [microsoft/skills CI Integration Guide](../../docs/SKILLS_CI_INTEGRATION.md)**

The guide covers:
- Installation methods (go install, Docker)
- Skill repository structure
- Creating evaluation suites
- CI/CD workflow setup
- Exit codes and output formats
- Best practices and troubleshooting

Quick start: Copy [`.github/workflows/skills-ci-example.yml`](../../.github/workflows/skills-ci-example.yml) to your skill repo.

---

## Files

### `eval-on-pr.yml`

An example workflow that demonstrates:
- Running evaluations on pull requests
- Matrix testing with multiple models
- Using the reusable `waza-eval.yml` workflow
- Comparing results across different models

This is a **template workflow** - copy it to `.github/workflows/` in your repository and customize it for your needs.

## Using the Reusable Workflow

The main waza evaluation workflow is defined in `.github/workflows/waza-eval.yml`. You can use it in two ways:

### Option 1: Call it from another workflow

```yaml
jobs:
  run-eval:
    uses: ./.github/workflows/waza-eval.yml
    with:
      eval-yaml: 'path/to/your/eval.yaml'
      context-dir: 'path/to/fixtures'  # Optional
      verbose: true                     # Optional
      output-file: 'results.json'       # Optional
```

### Option 2: Let it trigger automatically

The workflow automatically runs on:
- Pull requests that modify evaluation files or skills
- Pushes to main/develop branches that modify evaluation files or skills

### Option 3: Trigger it manually

Go to Actions → Waza Evaluation → Run workflow, and provide the inputs.

## Matrix Testing

To test your skill/evaluation across multiple models, use a matrix strategy:

```yaml
jobs:
  matrix-eval:
    strategy:
      matrix:
        model:
          - claude-sonnet-4-20250514
          - gpt-4o
          - claude-opus-4-20250514
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Setup Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.25'
      - name: Build waza
        run: go build -o waza ./cmd/waza
      - name: Run eval
        run: |
          # Modify eval.yaml to use ${{ matrix.model }}
          ./waza run eval.yaml --verbose
```

See `eval-on-pr.yml` for a complete example.

## Inputs

The `waza-eval.yml` workflow accepts these inputs:

| Input | Required | Default | Description |
|-------|----------|---------|-------------|
| `eval-yaml` | Yes | - | Path to the evaluation YAML file |
| `context-dir` | No | `{eval-dir}/fixtures` | Directory containing fixture files |
| `verbose` | No | `true` | Enable verbose output |
| `output-file` | No | `results.json` | Path to save results JSON |

## Exit Codes

The `waza run` command uses exit codes to indicate success or failure:

| Exit Code | Meaning | Workflow Behavior |
|-----------|---------|-------------------|
| `0` | All tests passed | Workflow succeeds ✅ |
| `1` | One or more tests failed | Workflow fails ❌ |
| `2` | Configuration error | Workflow fails ❌ |

This enables proper CI/CD integration - if tests fail, the workflow will fail, preventing merges or deployments.

## Outputs

The workflow produces:
- **Artifact**: `waza-evaluation-results` containing:
  - Results JSON file
  - Transcript files (if generated)

## Example Use Cases

### 1. Test on every PR

```yaml
on:
  pull_request:
    branches: [ main ]

jobs:
  test:
    uses: ./.github/workflows/waza-eval.yml
    with:
      eval-yaml: 'examples/my-skill/eval.yaml'
```

### 2. Nightly evaluation runs

```yaml
on:
  schedule:
    - cron: '0 0 * * *'  # Daily at midnight

jobs:
  nightly:
    uses: ./.github/workflows/waza-eval.yml
    with:
      eval-yaml: 'examples/my-skill/eval.yaml'
      verbose: true
```

### 3. Multiple evaluations in parallel

```yaml
jobs:
  eval-skill-a:
    uses: ./.github/workflows/waza-eval.yml
    with:
      eval-yaml: 'skills/skill-a/eval.yaml'
  
  eval-skill-b:
    uses: ./.github/workflows/waza-eval.yml
    with:
      eval-yaml: 'skills/skill-b/eval.yaml'
```

## Tips

1. **Context Directory**: If your fixtures are not in `fixtures/` relative to the eval.yaml, specify `context-dir` explicitly.

2. **Verbose Mode**: Enable verbose mode during development to see detailed execution logs.

3. **Matrix Testing**: When testing multiple models, create separate eval files or modify the YAML dynamically in your workflow.

4. **Result Analysis**: Download the results artifact after the workflow completes to analyze evaluation metrics locally.

5. **Fail-fast**: Set `fail-fast: false` in your matrix strategy to run all model tests even if one fails.
