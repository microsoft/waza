# Waza

A Go CLI for evaluating AI agent skills — scaffold eval suites, run benchmarks, and compare results across models.

## Installation via Azure Developer CLI (azd)

Waza is available as an [azd extension](https://learn.microsoft.com/azure/developer/azure-developer-cli/azure-developer-cli-extensions). Add the custom extension source and install:

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

```bash
# Build
make build

# Scaffold a new eval suite
./waza init my-eval --interactive

# Generate evals from a SKILL.md
./waza generate skills/my-skill/SKILL.md

# Run evaluations
./waza run examples/code-explainer/eval.yaml --context-dir examples/code-explainer/fixtures -v

# Compare results across models
./waza compare results-gpt4.json results-sonnet.json

# Count tokens in skill files
./waza tokens count skills/

# Suggest token optimizations
./waza tokens suggest skills/
```

## Commands

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

### `waza init [directory]`

Scaffold a new eval suite with `eval.yaml`, `tasks/`, and `fixtures/` directories.

| Flag | Description |
|------|-------------|
| `--interactive` | Guided wizard that collects skill metadata and generates a SKILL.md scaffold |

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

### `waza dev [skill-path]`

Iteratively score and improve skill frontmatter in a SKILL.md file.

| Flag | Description |
|------|-------------|
| `--target <level>` | Target adherence level: `low`, `medium`, `medium-high`, `high` (default: `medium-high`) |
| `--max-iterations <n>` | Maximum improvement iterations (default: 5) |
| `--auto` | Apply improvements without prompting |

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

tasks:
  - "tasks/*.yaml"
```

## Contributing

See [AGENTS.md](AGENTS.md) for coding guidelines.

- Use [conventional commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:`, etc.)
- Go CI is required: `Build and Test Go Implementation` and `Lint Go Code` must pass
- Add tests for new features
- Update docs when changing CLI surface

## Legacy Python Implementation

The Python implementation has been superseded by the Go CLI. The last Python release is available at [v0.3.2](https://github.com/spboyer/waza/releases/tag/v0.3.2).

## License

See [LICENSE](LICENSE).
