# Waza

A Go CLI for evaluating AI agent skills — scaffold eval suites, run benchmarks, and compare results across models.

## Installation

### Binary Install (recommended)

Download and install the latest pre-built binary with the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash
```

The script auto-detects your OS and architecture (linux/darwin/windows, amd64/arm64), downloads the binary, verifies the checksum, and installs to `/usr/local/bin` (or `~/bin` if not writable).

Or download binaries directly from the [latest release](https://github.com/spboyer/waza/releases/latest).

### Install from Source

Requires Go 1.25+:

```bash
go install github.com/spboyer/waza/cmd/waza@latest
```

### Azure Developer CLI (azd) Extension

Waza is also available as an [azd extension](https://learn.microsoft.com/azure/developer/azure-developer-cli/extensions/overview):

```bash
# Add the waza extension registry
azd ext source add -n waza -t url -l https://raw.githubusercontent.com/spboyer/waza/main/registry.json

# Install the extension
azd ext install microsoft.azd.waza

# Verify it's working
azd waza --help
```

Once installed, all waza commands are available under `azd waza`. For example:

```bash
azd waza init my-eval --interactive
azd waza run examples/code-explainer/eval.yaml -v
```

## Quick Start

### For New Users: Get Started in 5 Minutes

See **[Getting Started Guide](docs/GETTING-STARTED.md)** for a complete walkthrough:

```bash
# Initialize a new project
waza init my-project && cd my-project

# Create a new skill
waza new my-skill

# Define the skill in skills/my-skill/SKILL.md
# Write evaluation tasks in evals/my-skill/tasks/
# Add test fixtures in evals/my-skill/fixtures/

# Run evaluations
waza run my-skill

# Check skill readiness
waza check my-skill
```

### All Commands

```bash
# Build
make build

# Initialize a project workspace
waza init [directory]

# Create a new skill
waza new skill-name

# Check if a skill is ready for submission
waza check skills/my-skill

# Generate evals from a SKILL.md
waza generate skills/my-skill/SKILL.md

# Run evaluations
waza run examples/code-explainer/eval.yaml --context-dir examples/code-explainer/fixtures -v

# Compare results across models
waza compare results-gpt4.json results-sonnet.json

# Count tokens in skill files
waza tokens count skills/

# Suggest token optimizations
waza tokens suggest skills/
```

## Commands

### `waza init [directory]`

Initialize a waza project workspace with separated `skills/` and `evals/` directories. Idempotent — creates only missing files.

| Flag | Description |
|------|-------------|
| `--interactive` | Project-level setup wizard (reserved for future use) |
| `--no-skill` | Skip the first-skill creation prompt |

Creates:
- `skills/` — Skill definitions directory
- `evals/` — Evaluation suites directory
- `.github/workflows/eval.yml` — CI/CD pipeline for running evals on PR
- `.gitignore` — Waza-specific exclusions
- `README.md` — Getting started guide for your project

**Example:**
```bash
waza init my-project
# Optionally creates first skill interactively

waza init my-project --no-skill
# Skip skill creation prompt
```

### `waza new <skill-name>`

Create a new skill with scaffolded structure and evaluation suite. Detects workspace context and adapts output.

| Flag | Short | Description |
|------|-------|-------------|
| `--interactive` | `-i` | Run guided skill metadata wizard |
| `--template` | `-t` | Template pack (coming soon) |

**Modes:**

*Project mode* (detects `skills/` directory):
```
project/
├── skills/{skill-name}/SKILL.md
└── evals/{skill-name}/
    ├── eval.yaml
    ├── tasks/*.yaml
    └── fixtures/
```

*Standalone mode* (no `skills/` detected):
```
{skill-name}/
├── SKILL.md
├── evals/
│   ├── eval.yaml
│   ├── tasks/*.yaml
│   └── fixtures/
├── .github/workflows/eval.yml
├── .gitignore
└── README.md
```

**Example:**
```bash
# In project: creates skills/code-explainer/SKILL.md + evals/code-explainer/
waza new code-explainer

# Standalone: creates code-explainer/ self-contained directory
waza new code-explainer

# With wizard
waza new code-explainer --interactive
```

### `waza run <eval.yaml>`

Run an evaluation benchmark from a spec file.

| Flag | Short | Description |
|------|-------|-------------|
| `--context-dir <dir>` | | Fixture directory (default: `./fixtures` relative to spec) |
| `--output <file>` | `-o` | Save results to JSON |
| `--verbose` | `-v` | Detailed progress output |
| `--transcript-dir <dir>` | | Save per-task transcript JSON files |
| `--task <glob>` | | Filter tasks by name/ID pattern (repeatable) |
| `--parallel` | | Run tasks concurrently |
| `--workers <n>` | | Concurrent workers (default: 4, requires `--parallel`) |
| `--interpret` | | Print plain-language result interpretation |
| `--format <fmt>` | | Output format: `default` or `github-comment` (default: `default`) |
| `--cache` | | Enable result caching to speed up repeated runs |
| `--no-cache` | | Explicitly disable result caching |
| `--cache-dir <dir>` | | Cache directory (default: `.waza-cache`) |

**Result Caching**

Enable caching with `--cache` to store test results and skip re-execution on repeated runs:

```bash
# First run executes all tests and caches results
waza run eval.yaml --cache

# Second run uses cached results (much faster)
waza run eval.yaml --cache

# Clear the cache when needed
waza cache clear
```

Cached results are automatically invalidated when:
- Spec configuration changes (model, timeout, graders, etc.)
- Task definitions change
- Fixture files change

**Note:** Caching is automatically disabled for evaluations using non-deterministic graders (`behavior`, `prompt`).

**Exit Codes**

The `run` command uses exit codes to enable CI/CD integration:

| Exit Code | Condition | Description |
|-----------|-----------|-------------|
| `0` | Success | All tests passed |
| `1` | Test failure | One or more tests failed validation |
| `2` | Configuration error | Invalid spec, missing files, or runtime error |

Example CI usage:

```bash
# Fail the build if any tests fail
waza run eval.yaml || exit $?

# Capture specific exit codes
waza run eval.yaml
EXIT_CODE=$?
if [ $EXIT_CODE -eq 1 ]; then
  echo "Tests failed - check results"
elif [ $EXIT_CODE -eq 2 ]; then
  echo "Configuration error"
fi

# Post results as PR comment (GitHub Actions)
waza run eval.yaml --format github-comment > comment.md
gh pr comment $PR_NUMBER --body-file comment.md
```

### `waza generate <SKILL.md>`

Parse a SKILL.md file and generate an eval suite from its frontmatter.

| Flag | Short | Description |
|------|-------|-------------|
| `--output-dir <dir>` | `-d` | Output directory (default: `./eval-{skill-name}/`) |

### `waza compare <file1> <file2> [files...]`

Compare results from multiple evaluation runs side by side — per-task score deltas, pass rate differences, and aggregate statistics.

| Flag | Short | Description |
|------|-------|-------------|
| `--format <fmt>` | `-f` | Output format: `table` or `json` (default: `table`) |

### `waza cache clear`

Clear all cached evaluation results to force re-execution on the next run.

| Flag | Description |
|------|-------------|
| `--cache-dir <dir>` | Cache directory to clear (default: `.waza-cache`) |

### `waza dev [skill-path]`

Iteratively score and improve skill frontmatter in a SKILL.md file.

| Flag | Description |
|------|-------------|
| `--target <level>` | Target adherence level: `low`, `medium`, `medium-high`, `high` (default: `medium-high`) |
| `--max-iterations <n>` | Maximum improvement iterations (default: 5) |
| `--auto` | Apply improvements without prompting |

### `waza check [skill-path]`

Check if a skill is ready for submission with a comprehensive readiness report.

Performs three types of checks:
1. **Compliance scoring** — Validates frontmatter adherence (Low/Medium/Medium-High/High)
2. **Token budget** — Checks if SKILL.md is within token limits (default: 500 tokens)
3. **Evaluation suite** — Checks for the presence of eval.yaml

Provides a plain-language summary and actionable next steps to improve the skill.

**Example output:**
```
🔍 Skill Readiness Check
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

Skill: code-explainer

📋 Compliance Score: High
   ✅ Excellent! Your skill meets all compliance requirements.

📊 Token Budget: 450 / 500 tokens
   ✅ Within budget (50 tokens remaining).

🧪 Evaluation Suite: Found
   ✅ eval.yaml detected. Run 'waza run eval.yaml' to test.

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📈 Overall Readiness
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✅ Your skill is ready for submission!

🎯 Next Steps
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✨ No action needed! Your skill looks great.

Consider:
  • Running 'waza run eval.yaml' to verify functionality
  • Submitting a PR to microsoft/skills
```

**Usage:**
```bash
# Check current directory
waza check

# Check specific skill
waza check skills/my-skill

# Suggested workflow
waza check skills/my-skill     # Check readiness
waza dev skills/my-skill       # Improve compliance if needed
waza check skills/my-skill     # Verify improvements
```

### `waza tokens count [paths...]`

Count tokens in markdown files. Paths may be files or directories (scanned recursively for `.md`/`.mdx`).

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `table` or `json` (default: `table`) |
| `--sort <field>` | Sort by: `tokens`, `name`, or `path` (default: `path`) |
| `--min-tokens <n>` | Filter files below n tokens |
| `--no-total` | Hide total row in table output |

### `waza tokens suggest [paths...]`

Suggest ways to reduce token usage in markdown files. Paths may be files or
directories (scanned recursively for `.md`/`.mdx`).

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `text` or `json` (default: `text`) |
| `--min-savings <n>` | Minimum estimated token savings for heuristic suggestions |
| `--copilot` | Enable Copilot-powered suggestions |
| `--model <id>` | Model to use with `--copilot` |

## Building

```bash
make build          # Compile binary to ./waza
make test           # Run tests with coverage
make lint           # Run golangci-lint
make fmt            # Format code and tidy modules
make install        # Install to GOPATH
```

## Project Structure

```
cmd/waza/              CLI entrypoint and command definitions
  tokens/              Token counting subcommand
internal/
  config/              Configuration with functional options
  execution/           AgentEngine interface (mock, copilot)
  generate/            SKILL.md → eval suite generation
  graders/             Validator registry and built-in graders
  metrics/             Scoring metrics
  models/              Data structures (BenchmarkSpec, TestCase, EvaluationOutcome)
  orchestration/       TestRunner for coordinating execution
  reporting/           Result formatting and output
  transcript/          Per-task transcript capture
  wizard/              Interactive init wizard
examples/              Example eval suites
skills/                Example skills
```

## Eval Spec Format

```yaml
name: my-eval
skill: my-skill
version: "1.0"

config:
  trials_per_task: 3
  timeout_seconds: 300
  parallel: false
  executor: mock          # or copilot-sdk
  model: claude-sonnet-4-20250514

graders:
  - type: regex
    name: pattern_check
    config:
      must_match: ["\\d+ tests passed"]
  
  - type: behavior
    name: efficiency
    config:
      max_tool_calls: 20
      max_duration_ms: 300000
  
  - type: action_sequence
    name: workflow_check
    config:
      matching_mode: in_order_match
      expected_actions: ["bash", "edit", "report_progress"]

tasks:
  - "tasks/*.yaml"
```

## CI/CD Integration

Waza is designed to work seamlessly with CI/CD pipelines, including **microsoft/skills** repositories.

### For microsoft/skills Contributors

If you're contributing a skill to [microsoft/skills](https://github.com/microsoft/skills), waza can validate your skill in CI:

#### Installation in CI

**Option 1: Binary install (recommended)**
```bash
curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash
```

**Option 2: Install from source**
```bash
# Requires Go 1.25+
go install github.com/spboyer/waza/cmd/waza@latest
```

**Option 3: Use Docker**
```bash
docker build -t waza:local .
docker run -v $(pwd):/workspace waza:local run eval/eval.yaml
```

#### Quick Workflow Setup

Copy [`.github/workflows/skills-ci-example.yml`](.github/workflows/skills-ci-example.yml) to your skill repository:

```yaml
jobs:
  evaluate-skill:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Install waza
        run: curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash
      - run: waza run eval/eval.yaml --verbose --output results.json
      - uses: actions/upload-artifact@v4
        with:
          name: waza-evaluation-results
          path: results.json
```

#### Environment Requirements

| Requirement | Details |
|-------------|---------|
| **Go Version** | 1.25 or higher |
| **Executor** | Use `mock` executor for CI (no API keys needed) |
| **GitHub Token** | Only required for `copilot-sdk` executor: set `GITHUB_TOKEN` env var |
| **Exit Codes** | 0=success, 1=test failure, 2=config error |

#### Expected Skill Structure

```
your-skill/
├── SKILL.md              # Skill definition
└── eval/                 # Evaluation suite
    ├── eval.yaml         # Benchmark spec
    ├── tasks/            # Task definitions
    │   └── *.yaml
    └── fixtures/         # Context files
        └── *.txt
```

### For Waza Repository

This repository includes reusable workflows:

1. **[`.github/workflows/waza-eval.yml`](.github/workflows/waza-eval.yml)** - Reusable workflow for running evals
   ```yaml
   jobs:
     eval:
       uses: ./.github/workflows/waza-eval.yml
       with:
         eval-yaml: 'examples/code-explainer/eval.yaml'
         verbose: true
   ```

2. **[`examples/ci/eval-on-pr.yml`](examples/ci/eval-on-pr.yml)** - Matrix testing across models

3. **[`examples/ci/basic-example.yml`](examples/ci/basic-example.yml)** - Minimal workflow example

See [`examples/ci/README.md`](examples/ci/README.md) for detailed documentation and more examples.

### Available Grader Types

Waza supports multiple grader types for comprehensive evaluation:

| Grader | Purpose | Documentation |
|--------|---------|---------------|
| `code` | Python/JavaScript assertion-based validation | [docs/GRADERS.md](docs/GRADERS.md#code---assertion-based-grader) |
| `regex` | Pattern matching in output | [docs/GRADERS.md](docs/GRADERS.md#regex---pattern-matching-grader) |
| `file` | File existence and content validation | [docs/GRADERS.md](docs/GRADERS.md#file---file-system-validation) |
| `diff` | Workspace file comparison with snapshots and fragments | [docs/GRADERS.md](docs/GRADERS.md#diff---workspace-file-comparison) |
| `behavior` | Agent behavior constraints (tool calls, tokens, duration) | [docs/GRADERS.md](docs/GRADERS.md#behavior---agent-behavior-validation) |
| `action_sequence` | Tool call sequence validation with F1 scoring | [docs/GRADERS.md](docs/GRADERS.md#action_sequence---tool-call-sequence-validation) |
| `skill_invocation` | Skill orchestration sequence validation | [docs/GRADERS.md](docs/GRADERS.md#skill_invocation---skill-invocation-sequence-validation) |
| `prompt` | LLM-as-judge evaluation with rubrics | [docs/GRADERS.md](docs/GRADERS.md#prompt---llm-based-evaluation) |
| `trigger_tests` | Prompt trigger accuracy detection | [docs/GRADERS.md](docs/GRADERS.md#trigger-tests) |

See the complete [Grader Reference](docs/GRADERS.md) for detailed configuration options and examples.

## Documentation

- **[Getting Started](docs/GETTING-STARTED.md)** - Complete walkthrough: init → new → run → check
- **[Demo Guide](docs/DEMO-GUIDE.md)** - 7 live demo scenarios for presentations
- **[Grader Reference](docs/GRADERS.md)** - Complete grader types and configuration
- **[Tutorial](docs/TUTORIAL.md)** - Getting started with writing skill evals
- **[CI Integration](docs/SKILLS_CI_INTEGRATION.md)** - GitHub Actions workflows for microsoft/skills
- **[Token Management](docs/TOKEN-LIMITS.md)** - Tracking and optimizing skill context size

## Contributing

See [AGENTS.md](AGENTS.md) for coding guidelines.

- Use [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, etc.)
- Go CI is required: `Build and Test Go Implementation` and `Lint Go Code` must pass
- Add tests for new features
- Update docs when changing CLI surface

## Legacy Python Implementation

The Python implementation has been superseded by the Go CLI. The last Python release is available at [v0.3.2](https://github.com/spboyer/waza/releases/tag/v0.3.2). Starting with v0.4.0-alpha.1, waza is distributed exclusively as pre-built Go binaries.

## License

See [LICENSE](LICENSE).
