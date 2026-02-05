# Waza-Go

An **original** Go implementation of the waza evaluation framework for testing AI agent skills.

## Overview

Waza-Go is a from-scratch Go port that provides a fresh, Go-idiomatic approach to evaluating AI agent skills. While it maintains compatibility with the waza YAML specifications, it uses completely different internal architecture:

- **Functional Options Pattern** - Configuration uses Go's functional options for flexibility
- **Builder Patterns** - Engine construction uses builders for clean initialization
- **Interface-based Design** - Pluggable engines, validators, and orchestration
- **Original Naming** - Uses creative Go-specific names (BenchmarkSpec, AgentEngine, Validator, etc.)

## Architecture

```
waza-go/
├── cmd/waza/           - CLI entrypoint
├── internal/
│   ├── models/         - Data structures (BenchmarkSpec, TestCase, EvaluationOutcome)
│   ├── config/         - Configuration with functional options
│   ├── execution/      - AgentEngine interface and implementations
│   │   ├── engine.go   - Core engine interface
│   │   ├── mock.go     - Mock engine for testing
│   │   └── copilot.go  - Copilot SDK integration
│   ├── scoring/        - Validator interface and implementations
│   │   ├── validator.go          - Validator registry pattern
│   │   └── code_validators.go   - Code and regex validators
│   └── orchestration/  - TestRunner for coordinating execution
│       └── runner.go   - Benchmark orchestration
```

## Key Design Differences from Python

### 1. Naming Conventions
- `BenchmarkSpec` instead of `EvalSpec`
- `AgentEngine` instead of `BaseExecutor`
- `Validator` instead of `Grader`
- `TestCase` instead of `Task`
- `EvaluationOutcome` instead of `EvalResult`

### 2. Patterns
- Functional options for configuration
- Builder pattern for engine construction
- Registry pattern for validator extensibility
- Progress listeners instead of callbacks

### 3. Structure
- Separate `SpecDir` and `FixtureDir` for test resolution vs. resource loading
- Interface-based `AgentEngine` with pluggable implementations
- `ValidationContext` with clear separation of concerns

## Installation

```bash
# From waza-go directory
go build -o waza ./cmd/waza

# Or install to GOPATH
go install ./cmd/waza
```

## Usage

### Run Evaluations

```bash
# Run with mock engine (default)
./waza run path/to/eval.yaml --context-dir path/to/fixtures

# Run with verbose output
./waza run path/to/eval.yaml --context-dir path/to/fixtures -v

# Save results to JSON
./waza run path/to/eval.yaml --context-dir path/to/fixtures --output results.json

# Run with Copilot SDK (requires Copilot CLI installed)
# (Update eval.yaml to use executor: copilot-sdk)
./waza run path/to/eval.yaml --context-dir path/to/fixtures
```

### Example with code-explainer

```bash
cd waza-go
./waza run ../examples/code-explainer/eval.yaml \
  --context-dir ../examples/code-explainer/fixtures \
  -v \
  --output results.json
```

## Evaluation Specification

Waza-Go uses the same YAML spec format as waza:

```yaml
name: my-eval
skill: my-skill
version: "1.0"

config:
  trials_per_task: 3
  timeout_seconds: 300
  parallel: false
  executor: mock  # or copilot-sdk
  model: claude-sonnet-4-20250514

graders:
  - type: code
    name: basic_check
    config:
      assertions:
        - "len(output) > 10"

metrics:
  - name: task_completion
    weight: 0.5
    threshold: 0.8

tasks:
  - "tasks/*.yaml"
```

## Test Case Format

```yaml
id: test-001
name: My Test
description: Test description

inputs:
  prompt: "Explain this code"
  files:
    - path: example.py

expected:
  output_contains:
    - "function"
    - "returns"

graders:
  - name: custom_check
    type: code
    config:
      assertions:
        - "'function' in output.lower()"
```

## Validators (Graders)

### Built-in Validators

#### Code Validator
Evaluates assertions against output:

```yaml
- type: code
  name: check_output
  config:
    assertions:
      - "len(output) > 10"
      - "'keyword' in output.lower()"
```

#### Regex Validator
Pattern matching:

```yaml
- type: regex
  name: pattern_check
  config:
    must_match:
      - "\\d+ tests passed"
    must_not_match:
      - "(?i)error|failed"
```

## Engines

### Mock Engine
Fast, deterministic engine for testing specs:

```go
engine := execution.NewMockEngine("claude-sonnet-4-20250514")
```

### Copilot Engine
Integrates with GitHub Copilot SDK:

```go
engine := execution.NewCopilotEngineBuilder("claude-sonnet-4-20250514").
    WithSkillPaths([]string{"./skills"}).
    WithTimeout(300).
    Build()
```

## Configuration Options

### Functional Options Pattern

```go
import "github.com/spboyer/waza/waza-go/internal/config"

cfg := config.NewBenchmarkConfig(spec,
    config.WithSpecDir("/path/to/spec/dir"),
    config.WithFixtureDir("/path/to/fixtures"),
    config.WithVerbose(true),
    config.WithOutputPath("results.json"),
)
```

### Builder Pattern for Engines

```go
engine := execution.NewCopilotEngineBuilder(modelID).
    WithSkillPaths(skillDirs).
    WithTimeout(seconds).
    WithStreaming(true).
    Build()
```

## Output Format

Results are saved as JSON with this structure:

```json
{
  "run_id": "run-1234567890",
  "skill_tested": "my-skill",
  "bench_name": "my-eval",
  "timestamp": "2024-02-05T19:21:56Z",
  "setup": {
    "runs_per_test": 3,
    "model_id": "claude-sonnet-4-20250514",
    "engine_type": "mock",
    "timeout_sec": 300
  },
  "digest": {
    "total_tests": 4,
    "succeeded": 4,
    "failed": 0,
    "errors": 0,
    "success_rate": 1.0,
    "aggregate_score": 1.0,
    "duration_ms": 1234
  },
  "test_outcomes": [...]
}
```

## Extending Waza-Go

### Custom Validators

Register custom validators:

```go
import "github.com/spboyer/waza/waza-go/internal/scoring"

func init() {
    scoring.RegisterValidator("custom", func(id string, params map[string]any) scoring.Validator {
        return &MyCustomValidator{identifier: id, params: params}
    })
}
```

### Custom Engines

Implement the `AgentEngine` interface:

```go
type AgentEngine interface {
    Initialize(ctx context.Context) error
    Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error)
    Shutdown(ctx context.Context) error
}
```

## Differences from Python Implementation

| Aspect | Python (waza) | Go (waza-go) |
|--------|---------------|--------------|
| Configuration | Dict-based | Functional options |
| Executors | Class inheritance | Interface implementation |
| Graders | Registry with decorators | Explicit registration |
| Progress | Callbacks | Listener pattern |
| Async | asyncio | Go contexts |
| Names | eval/task/grader | benchmark/test/validator |

## CLI Commands

```bash
# Run evaluation
waza run <spec.yaml> [options]

Options:
  --context-dir <dir>   Context/fixture directory
  --output, -o <file>   Save results to JSON file
  --verbose, -v         Verbose output

# Show version
waza version
```

## Development

### Building

```bash
go build -o waza ./cmd/waza
```

### Testing

```bash
# Run with example
./waza run ../examples/code-explainer/eval.yaml \
  --context-dir ../examples/code-explainer/fixtures \
  -v
```

### Dependencies

- `gopkg.in/yaml.v3` - YAML parsing
- `github.com/github/copilot-sdk/go` - Copilot SDK integration

## License

See root LICENSE file.

## Contributing

This is an original implementation created for educational purposes. When contributing:

1. Maintain the Go-idiomatic design patterns
2. Keep names and architecture distinct from Python implementation
3. Follow Go best practices and conventions
4. Add tests for new features
5. Update documentation

## Credits

Created as an original Go port of the waza evaluation framework, with completely new architecture and design patterns while maintaining compatibility with the YAML specification format.
