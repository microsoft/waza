# Waza

A Go CLI for evaluating AI agent skills — scaffold eval suites, run benchmarks, and compare results across models.

📖 **[Getting Started / Docs](https://proud-beach-0e05cbe0f.2.azurestaticapps.net/)**

## Installation

### Binary Install (recommended)

Download and install the latest pre-built binary with the install script:

```bash
curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

The script auto-detects your OS and architecture (linux/darwin/windows, amd64/arm64), downloads the binary, verifies the checksum, and installs to `/usr/local/bin` (or `~/bin` if not writable).

Or download binaries directly from the [latest release](https://github.com/microsoft/waza/releases/latest).

### Install from Source

Requires Go 1.26+:

```bash
go install github.com/microsoft/waza/cmd/waza@latest
```

### Azure Developer CLI (azd) Extension

Waza is also available as an [azd extension](https://learn.microsoft.com/azure/developer/azure-developer-cli/extensions/overview):

```bash
# Add the waza extension registry
azd ext source add -n waza -t url -l https://raw.githubusercontent.com/microsoft/waza/main/registry.json

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

# Create a new eval scaffold from an existing SKILL.md
waza eval new skill-name

# Check if a skill is ready for submission
waza check skills/my-skill

# Suggest an eval suite from SKILL.md
waza suggest skills/my-skill --dry-run
waza suggest skills/my-skill --apply

# Note: 'generate' is available as an alias for 'new' (see below for new command)

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

### `waza eval new <skill-name>`

Scaffold an eval suite from an existing `SKILL.md` (reads frontmatter trigger hints from `USE FOR` and `DO NOT USE FOR`).

Creates:
- `evals/<skill-name>/eval.yaml`
- `evals/<skill-name>/tasks/positive-trigger-1.yaml`
- `evals/<skill-name>/tasks/positive-trigger-2.yaml`
- `evals/<skill-name>/tasks/negative-trigger-1.yaml`

| Flag | Description |
|------|-------------|
| `--output <path>` | Custom path for `eval.yaml` (tasks are generated under sibling `tasks/`) |

**Example:**
```bash
# Default output location
waza eval new code-explainer

# Custom eval path
waza eval new code-explainer --output evals/custom-code-explainer/eval.yaml
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
| `--reporter <spec>` | | Output reporters: `json` (default), `junit:<path>` (repeatable) |
| `--baseline` | | A/B testing mode — runs each task twice (without skill = baseline, with skill = normal) and computes improvement scores |
| `--discover` | | Auto skill discovery — walks directory tree for SKILL.md + eval.yaml (root/tests/evals) |
| `--strict` | | Fail if any SKILL.md lacks eval coverage (use with `--discover`) |
| `--suggest` | | Generate a Copilot suggestion report based on test outcomes (`mock` engine emits a deterministic fake report) |

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

# Generate JUnit XML for CI test reporting
waza run eval.yaml --reporter junit:results.xml

# Both JSON output and JUnit XML
waza run eval.yaml -o results.json --reporter junit:results.xml
```

**Note:** `waza generate` is an alias for `waza new`. Both commands support the same functionality with the `--output-dir` flag for specifying custom output locations.

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

Use `--copilot` for a non-interactive, single-pass markdown report that:
1. Summarizes current skill details and token usage
2. Loads trigger test prompts as examples (when `trigger_tests.yaml` exists)
3. Requests Copilot suggestions for improving skill selection
4. Prints the report to stdout without applying any changes

When `--copilot` is set, iterative mode flags (`--target`, `--max-iterations`, `--auto`) are invalid.

| Flag | Description |
|------|-------------|
| `--target <level>` | Target adherence level for iterative mode: `low`, `medium`, `medium-high`, `high` (default: `medium-high`) |
| `--max-iterations <n>` | Maximum improvement iterations for iterative mode (default: 5) |
| `--auto` | Apply improvements without prompting in iterative mode |
| `--copilot` | Generate a non-interactive markdown report with Copilot suggestions |
| `--model <id>` | Model to use with `--copilot` |

### `waza check [skill-path]`

Check if a skill is ready for submission with a comprehensive readiness report.

Performs five types of checks:
1. **Compliance scoring** — Validates frontmatter adherence (Low/Medium/Medium-High/High)
2. **Token budget** — Checks if SKILL.md is within token limits (configurable in `.waza.yaml` `tokens.limits`)
3. **Evaluation suite** — Checks for the presence of eval.yaml
4. **Spec compliance** — Validates the skill against the agentskills.io spec (frontmatter structure, required fields, naming rules, directory match, description length, compatibility, license, and version)
5. **Advisory checks** — Detects quality and maintainability issues (reference module count, complexity classification, negative delta risk patterns, procedural content, and over-specificity)

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

📐 Spec Compliance (agentskills.io)
   ✅ spec-frontmatter    Frontmatter structure valid with required fields
   ✅ spec-allowed-fields All frontmatter fields are spec-allowed
   ✅ spec-name           Name follows spec naming rules
   ✅ spec-dir-match      Directory name matches skill name
   ✅ spec-description    Description is valid
   ✅ spec-license        License field present
   ✅ spec-version        metadata.version present

🔬 Advisory Checks
   ✅ module-count        Found 2 reference modules (2-3 is optimal)
   ✅ complexity          Complexity: detailed (350 tokens, 2 modules)
   ✅ negative-delta-risk No negative delta risk patterns detected
   ✅ procedural-content  Description contains procedural language
   ✅ over-specificity    No over-specificity patterns detected

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
📈 Overall Readiness
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✅ Your skill is ready for submission!

🎯 Next Steps
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

✨ No action needed! Your skill looks great.

Consider:
  • Running 'waza run eval.yaml' to verify functionality
  • Sharing your skill with the community
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

### `waza suggest <skill-path>`

Use an LLM to analyze `SKILL.md` and generate suggested evaluation artifacts.

| Flag | Description |
|------|-------------|
| `--model <model>` | Model to use for suggestions (default: project default model) |
| `--dry-run` | Print suggested output to stdout (default) |
| `--apply` | Write files to disk |
| `--output-dir <dir>` | Output directory (default: `<skill-path>/evals`) |
| `--format yaml\|json` | Output format (default: `yaml`) |

**Examples:**
```bash
# Preview generated eval/task/fixture files as YAML
waza suggest skills/code-explainer --dry-run

# Write generated files to disk
waza suggest skills/code-explainer --apply

# Print JSON-formatted suggestion payload
waza suggest skills/code-explainer --format json
```

### `waza tokens count [paths...]`

Count tokens in markdown files. Paths may be files or directories (scanned recursively for `.md`/`.mdx`).

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `table` or `json` (default: `table`) |
| `--sort <field>` | Sort by: `tokens`, `name`, or `path` (default: `path`) |
| `--min-tokens <n>` | Filter files below n tokens |
| `--no-total` | Hide total row in table output |

### `waza tokens profile [skill-name | path]`

Structural analysis of SKILL.md files — reports token count, section count, code block count, and workflow step detection with a one-line summary and warnings.

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `text` or `json` (default: `text`) |
| `--tokenizer <t>` | Tokenizer: `bpe` or `estimate` (default: `bpe`) |

**Example output:**
```
📊 my-skill: 1,722 tokens (detailed ✓), 8 sections, 4 code blocks
   ⚠️  no workflow steps detected
```

### `waza tokens suggest [paths...]`

Suggest ways to reduce token usage in markdown files. Paths may be files or
directories (scanned recursively for `.md`/`.mdx`).

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `text` or `json` (default: `text`) |
| `--min-savings <n>` | Minimum estimated token savings for heuristic suggestions |
| `--copilot` | Enable Copilot-powered suggestions |
| `--model <id>` | Model to use with `--copilot` |

### `waza serve`

Start the waza dashboard server to visualize evaluation results. The HTTP server opens in your browser automatically and scans the specified directory for `.json` result files.

Optionally, run a JSON-RPC 2.0 server (for IDE integration) instead of the HTTP dashboard using the `--tcp` flag.

| Flag | Default | Description |
|------|---------|-------------|
| `--port <port>` | `3000` | HTTP server port |
| `--no-browser` | `false` | Don't auto-open the browser |
| `--results-dir <dir>` | `.` | Directory to scan for result files |
| `--tcp <addr>` | (off) | TCP address for JSON-RPC (e.g., `:9000`); defaults to loopback for security |
| `--tcp-allow-remote` | `false` | Allow TCP binding to non-loopback addresses (⚠️ no authentication) |

**Examples:**

Start the HTTP dashboard on port 3000:
```bash
waza serve
```

Start the HTTP dashboard on a custom port and scan a results directory:
```bash
waza serve --port 8080 --results-dir ./results
```

Start the dashboard without auto-opening the browser:
```bash
waza serve --no-browser
```

Start a JSON-RPC server for IDE integration:
```bash
waza serve --tcp :9000
```

**Dashboard Views:**

The dashboard displays evaluation results with:
- Task-level pass/fail status
- Score distributions across trials
- Model comparisons
- Aggregated metrics and trends

For detailed documentation on the dashboard and result visualization, see [docs/GUIDE.md](docs/GUIDE.md).

### `waza results`

Manage evaluation results stored in cloud or local storage.

#### `waza results list`

List all evaluation runs from configured cloud storage or local results directory.

| Flag | Description |
|------|-------------|
| `--limit <n>` | Maximum results to display (default: 20) |
| `--format <fmt>` | Output format: `table` or `json` (default: `table`) |

```bash
# List recent results
waza results list

# List with custom limit
waza results list --limit 20

# Output as JSON
waza results list --format json
```

#### `waza results compare <id1> <id2>`

Compare two evaluation runs side by side. Displays per-task score deltas, pass rate differences, and key metrics.

| Flag | Description |
|------|-------------|
| `--format <fmt>` | Output format: `table` or `json` (default: `table`) |

```bash
# Compare two runs
waza results compare run-20250226-001 run-20250226-002

# Output as JSON for further processing
waza results compare run-20250226-001 run-20250226-002 --format json
```

## Cloud Storage

Waza can automatically upload evaluation results to Azure Blob Storage for team collaboration and historical tracking.

### Configuration

Add a `storage:` section to your `.waza.yaml`:

```yaml
storage:
  provider: azure-blob
  accountName: "myteamwaza"
  containerName: "waza-results"
  enabled: true
```

| Field | Description | Required |
|-------|-------------|----------|
| `provider` | Cloud provider (`azure-blob` currently supported) | Yes |
| `accountName` | Azure Storage account name | Yes |
| `containerName` | Blob container name (default: `waza-results`) | No |
| `enabled` | Enable/disable uploads (default: `true` when configured) | No |

### Authentication

Waza uses **DefaultAzureCredential** — it automatically detects and uses available credentials in this order:

1. **Environment variables** (`AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID`)
2. **Managed Identity** (on Azure services)
3. **Azure CLI** (`az login`)
4. **Visual Studio Code** (if signed in)
5. **Azure PowerShell** (if signed in)

In most cases, running `az login` is all you need:

```bash
az login
waza run eval.yaml  # Results auto-upload to Azure Storage
```

### How It Works

1. **Auto-upload on run:** When `storage:` is configured, `waza run` automatically uploads results to Azure Blob Storage
2. **Organized by skill:** Results are stored as `{skill-name}/{run-id}.json`
3. **Local copy kept:** Results are also saved locally (via `-o` flag)
4. **List remote results:** Use `waza results list` to browse uploaded runs
5. **Compare runs:** Use `waza results compare` to diff two remote results

### Example Workflow

```bash
# Configure once (edit .waza.yaml)
cat > .waza.yaml <<EOF
storage:
  provider: azure-blob
  accountName: "myteamwaza"
  containerName: "waza-results"
  enabled: true
EOF

# Authenticate
az login

# Run evaluations — results auto-upload
waza run evals/my-skill/eval.yaml -v

# Browse uploaded results
waza results list

# Compare two runs
waza results compare run-id-1 run-id-2
```

For step-by-step setup and troubleshooting, see [Getting Started with Azure Storage](../docs/guides/azure-storage/) guide.

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
  max_attempts: 3          # Retry failed graders up to 3 times (default: 1, no retries)
  timeout_seconds: 300
  parallel: false
  executor: mock          # or copilot-sdk
  model: claude-sonnet-4-20250514
  group_by: model          # Group results by model (or other dimension)

# Custom input variables available as {{.Vars.key}} in tasks and hooks
inputs:
  api_version: v2
  environment: production
  max_retries: 3

hooks:
  before_run:
    - command: "echo 'Starting evaluation'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

  after_run:
    - command: "echo 'Evaluation complete'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

  before_task:
    - command: "echo 'Running task: {{.TaskName}}'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

  after_task:
    - command: "echo 'Task {{.TaskName}} completed'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

graders:
  - type: text
    name: pattern_check
    config:
      regex_match: ["\\d+ tests passed"]
  
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

# Task definitions: glob patterns or CSV dataset
tasks:
  - "tasks/*.yaml"

# Optional: Generate tasks from CSV dataset
# tasks_from: ./test-cases.csv
# range: [1, 10]  # Only include rows 1-10 (0-indexed, skips header)
```

### Custom Input Variables

Use the `inputs` section to define key-value variables available throughout your evaluation as `{{.Vars.key}}`:

```yaml
inputs:
  api_endpoint: https://api.example.com
  timeout: 30
  environment: staging

hooks:
  before_run:
    - command: "echo 'Testing against {{.Vars.environment}}'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false
```

Variables are accessible in:
- Hook commands
- Task prompts and fixtures (via template rendering)
- Grader configurations

### CSV Dataset Support

Generate tasks dynamically from a CSV file using `tasks_from`:

```yaml
# eval.yaml
tasks_from: ./test-cases.csv
range: [0, 50]  # Optional: limit to rows 0-50 (skip header at 0)
```

**CSV Format:**
```csv
prompt,expected_output,language
"Explain this function","Function explanation",python
"Review this code","Code review",javascript
```

**Task Generation:**
- **First row** is treated as column headers
- **Each subsequent row** becomes a task
- **Column values** are available as `{{.Vars.column_name}}`
- **Range filtering** (optional) allows limiting to a subset of rows

**Example task prompt using CSV variables:**

In your task file or inline prompt:
```yaml
prompt: "{{.Vars.prompt}}"
expected_output: "{{.Vars.expected_output}}"
language: "{{.Vars.language}}"
```

Tasks can also be mixed — use both explicit task files and CSV-generated tasks:

```yaml
tasks:
  - "tasks/*.yaml"        # Explicit tasks

tasks_from: ./test-cases.csv    # CSV-generated tasks
range: [0, 20]                  # Only first 20 rows
```

**CSV vs Inputs:**
- `inputs`: Static key-value pairs defined once in eval.yaml
- `tasks_from`: Generates multiple tasks from CSV rows
- **Conflict resolution**: CSV column values override `inputs` for the same key

### Retry/Attempts

Use `max_attempts` to retry failed grader validations within each trial:

```yaml
config:
  max_attempts: 3  # Retry failed graders up to 3 times (default: 1, no retries)
```

When a grader fails, waza will retry the task execution up to `max_attempts` times. The evaluation outcome includes an `attempts` field showing how many executions were needed to pass. This is useful for handling transient failures in external services or non-deterministic grader behavior.

**Output:** JSON results include `attempts` per task showing the number of executions performed.

### Grouping Results

Use `group_by` to organize results by a dimension (e.g., model, environment). Results are grouped in CLI output and JSON results include group statistics:

```yaml
config:
  group_by: model
```

Grouped results in JSON output include `GroupStats`:
```json
{
  "group_stats": [
    {
      "name": "claude-sonnet-4-20250514",
      "passed": 8,
      "total": 10,
      "avg_score": 0.85
    }
  ]
}
```

### Lifecycle Hooks

Use `hooks` to run commands before/after evaluations and tasks:

```yaml
hooks:
  before_run:
    - command: "npm install"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: true

  after_run:
    - command: "rm -rf node_modules"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

  before_task:
    - command: "echo 'Task: {{.TaskName}}'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false

  after_task:
    - command: "echo 'Done: {{.TaskName}}'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false
```

**Hook Fields:**
- `command` — Shell command to execute
- `working_directory` — Directory to run command in (relative to eval.yaml)
- `exit_codes` — List of acceptable exit codes (default: `[0]`)
- `error_on_fail` — Fail entire evaluation if hook fails (default: `false`)

**Lifecycle Points:**
- `before_run` — Execute once before all tasks
- `after_run` — Execute once after all tasks
- `before_task` — Execute before each task
- `after_task` — Execute after each task

**Template Variables in Hooks and Commands:**

Available variables in hook commands and task execution contexts:
- `{{.JobID}}` — Unique evaluation run identifier
- `{{.TaskName}}` — Name/ID of the current task (available in `before_task`/`after_task` only)
- `{{.Iteration}}` — Current trial number (1-indexed)
- `{{.Attempt}}` — Current attempt number (1-indexed, used for retries)
- `{{.Timestamp}}` — ISO 8601 timestamp of execution
- `{{.Vars.key}}` — User-defined variables from the `inputs` section or CSV columns

Custom variables can be defined in the `inputs` section and referenced in hooks:

```yaml
inputs:
  environment: production
  api_version: v2
  debug_mode: "true"

hooks:
  before_run:
    - command: "echo 'Starting eval {{.JobID}} in {{.Vars.environment}}'"
      working_directory: "."
      exit_codes: [0]
      error_on_fail: false
```

When using CSV-generated tasks, each row's column values are also available as `{{.Vars.column_name}}`.

## CI/CD Integration

Waza is designed to work seamlessly with CI/CD pipelines.

### Integrating Waza in CI

Waza can validate your skill in CI before publishing:

#### Installation in CI

**Option 1: Binary install (recommended)**
```bash
curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
```

**Option 2: Install from source**
```bash
# Requires Go 1.26+
go install github.com/microsoft/waza/cmd/waza@latest
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
        run: curl -fsSL https://raw.githubusercontent.com/microsoft/waza/main/install.sh | bash
      - run: waza run eval/eval.yaml --verbose --output results.json
      - uses: actions/upload-artifact@v4
        with:
          name: waza-evaluation-results
          path: results.json
```

#### Environment Requirements

| Requirement | Details |
|-------------|---------|
| **Go Version** | 1.26 or higher |
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
| `text` | Substring and pattern matching in output | [docs/GRADERS.md](docs/GRADERS.md#text---text-matching-grader) |
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
- **[CI Integration](docs/SKILLS_CI_INTEGRATION.md)** - GitHub Actions workflows for skill evaluation
- **[Token Management](docs/TOKEN-LIMITS.md)** - Tracking and optimizing skill context size

## Contributing

See [AGENTS.md](AGENTS.md) for coding guidelines.

- Use [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, etc.)
- Go CI is required: `Build and Test Go Implementation` and `Lint Go Code` must pass
- Add tests for new features
- Update docs when changing CLI surface

## Legacy Python Implementation

The Python implementation has been superseded by the Go CLI. The last Python release is available at [v0.3.2](https://github.com/microsoft/waza/releases/tag/v0.3.2). Starting with v0.4.0-alpha.1, waza is distributed exclusively as pre-built Go binaries.

## License

See [LICENSE](LICENSE).
