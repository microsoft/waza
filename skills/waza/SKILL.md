---
name: waza
description: "**WORKFLOW SKILL** - Evaluate AI agent skills using structured benchmarks with YAML specs, fixture isolation, and pluggable validators. USE FOR: run waza, waza help, run eval, run benchmark, evaluate skill, test agent, generate eval suite, init eval, compare results, score agent, agent evaluation, skill testing, cross-model comparison. DO NOT USE FOR: improving skill frontmatter (use waza dev), creating new skills from scratch (use skill-creator), token counting or budget checks (use waza tokens). INVOKES: Copilot SDK executor, mock engine, code/regex validators. FOR SINGLE OPERATIONS: use waza run directly for a single benchmark."
---

# Waza

> "The way of technique — measure, refine, master."

A Go CLI tool for evaluating AI agent skills through structured benchmarks. Define test cases in YAML, run them against agent engines, and validate results with pluggable scoring validators.

## Help

When user says "waza help" or asks how to use waza:

```
╔══════════════════════════════════════════════════════════════════╗
║  WAZA - CLI Tool for Evaluating Agent Skills                     ║
╠══════════════════════════════════════════════════════════════════╣
║                                                                  ║
║  COMMANDS:                                                       ║
║    waza run <eval.yaml>        # Run an evaluation benchmark     ║
║    waza init [directory]       # Initialize a new eval suite     ║
║    waza generate <SKILL.md>    # Generate eval from SKILL.md     ║
║    waza compare <r1> <r2> ...  # Compare result files            ║
║    waza dev [skill-path]       # Improve SKILL.md compliance     ║
║                                                                  ║
║  RUN FLAGS:                                                      ║
║    --context-dir, -c   Fixtures directory (default: ./fixtures)  ║
║    --output, -o        Save results JSON to file                 ║
║    --verbose, -v       Verbose output                            ║
║    --task, -t          Filter tasks by name (repeatable)         ║
║    --parallel, -p      Run tasks in parallel                     ║
║    --workers, -w       Number of parallel workers                ║
║    --transcript-dir    Save per-task transcripts                 ║
║                                                                  ║
║  COMPARE FLAGS:                                                  ║
║    --format, -f        Output format: table or json              ║
║                                                                  ║
║  GENERATE FLAGS:                                                 ║
║    --output-dir, -d    Output directory for generated files      ║
║                                                                  ║
║  DEV FLAGS:                                                      ║
║    --target            Adherence level: low|medium|high          ║
║    --max-iterations    Max improvement iterations (default: 5)   ║
║    --auto              Auto-apply without prompting              ║
║                                                                  ║
║  WORKFLOW:                                                       ║
║    1. waza init my-eval        # Scaffold eval suite             ║
║    2. Edit eval.yaml + tasks   # Define test cases               ║
║    3. waza run eval.yaml -v    # Execute benchmark               ║
║    4. waza compare a.json b.json  # Cross-model comparison       ║
║                                                                  ║
║  FIXTURE ISOLATION:                                              ║
║    Each task gets a fresh temp workspace with fixtures copied    ║
║    in. Original fixtures are never modified.                     ║
║                                                                  ║
╚══════════════════════════════════════════════════════════════════╝
```

## Commands

### `waza run`

Run an evaluation benchmark from a YAML spec file.

```bash
# Run with default mock engine
waza run path/to/eval.yaml --context-dir path/to/fixtures

# Verbose output with results saved
waza run eval.yaml -c ./fixtures -v -o results.json

# Filter to specific tasks
waza run eval.yaml -t "task-name-1" -t "task-name-2"

# Parallel execution
waza run eval.yaml --parallel --workers 4

# Save per-task transcripts
waza run eval.yaml --transcript-dir ./transcripts
```

### `waza init`

Initialize a new evaluation suite with a compliant directory structure.

```bash
# Initialize in current directory
waza init

# Initialize in a named directory
waza init my-eval-suite
```

Creates: `eval.yaml`, `tasks/` with example task, `fixtures/` with example fixture.

### `waza generate`

Generate an eval suite from an existing SKILL.md file.

```bash
# Generate eval from SKILL.md
waza generate path/to/SKILL.md

# Specify output directory
waza generate SKILL.md --output-dir ./my-eval
```

Parses YAML frontmatter (name, description) and creates eval.yaml, starter tasks, and fixtures.

### `waza compare`

Compare results from multiple evaluation runs side by side.

```bash
# Compare two result files
waza compare run1.json run2.json

# Compare three or more
waza compare gpt4.json claude.json gemini.json

# JSON output
waza compare run1.json run2.json --format json
```

Shows per-task score deltas, pass rate differences, and aggregate statistics.

### `waza dev`

Iteratively improve SKILL.md frontmatter compliance with automated scoring.

```bash
# Score current skill and suggest improvements
waza dev skills/my-skill

# Target high compliance level
waza dev skills/my-skill --target high

# Auto-apply improvements without prompts
waza dev skills/my-skill --target medium --auto --max-iterations 3
```

**Compliance Levels:**
- **Low** (< 150 chars or no triggers) — Minimal description
- **Medium** (150+ chars, has triggers) — Basic trigger coverage
- **Medium-High** (+ anti-triggers) — Routing clarity improved
- **High** (+ routing markers like INVOKES/FOR SINGLE OPERATIONS) — Full compliance

**Scoring Checks:**
- Description length (150+ chars required, 1024 max)
- Trigger phrases (USE FOR: patterns)
- Anti-trigger phrases (DO NOT USE FOR: patterns)
- Routing clarity markers (**WORKFLOW SKILL**, INVOKES:, etc.)
- Token budget (500 soft limit, 5000 hard limit)

**Coming Soon:** Trigger accuracy tests (#36), `--skip-integration` (#37), `--fast` (#38), improvement suggestions engine (#34).

## Evaluation Spec Format

```yaml
name: my-eval
skill: my-skill
version: "1.0"
executor: mock          # or copilot-sdk
tasks:
  - id: task-1
    name: "Describe the task"
    prompt: "Your prompt to the agent"
    expected: "Expected behavior"
    validators:
      - type: code
        config:
          language: go
      - type: regex
        config:
          pattern: "expected pattern"
```

## Engines

| Engine | Use | Description |
|--------|-----|-------------|
| `mock` | Testing | Returns canned responses for validator development |
| `copilot-sdk` | Production | Executes via Copilot CLI SDK |

## Validators

| Validator | What it checks |
|-----------|---------------|
| `code` | Code compiles / passes syntax check |
| `regex` | Output matches regex pattern |

## Configuration

| Setting | Flag | Default |
|---------|------|---------|
| Fixtures dir | `--context-dir` | `./fixtures` |
| Output file | `--output` | (none) |
| Verbose | `--verbose` | `false` |
| Parallel | `--parallel` | `false` |
| Workers | `--workers` | CPU count |
| Transcript dir | `--transcript-dir` | (none) |

## Scoring Quick Reference

Each task produces an `EvaluationOutcome` with:

| Field | Description |
|-------|-------------|
| `score` | 0.0–1.0 normalized score |
| `pass` | Boolean pass/fail |
| `validator_results` | Per-validator details |
| `duration` | Execution time |
