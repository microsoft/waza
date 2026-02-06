# Agent Instructions for waza Repository

## Overview

This repository contains `waza`, a CLI tool for evaluating Agent Skills. **The primary implementation is Go** (`waza-go/`). The Python implementation (`waza/`) is legacy and no longer actively developed.

When making changes, follow these guidelines to maintain consistency and quality.

## Project Tracking

**Keep issues and tracking up to date:**
- **Tracking Issue:** [#66 - Waza Platform Roadmap](https://github.com/spboyer/waza/issues/66)
- **PRD:** [docs/PRD.md](docs/PRD.md)
- When completing work, update the relevant GitHub issue
- Reference issue numbers in commit messages (e.g., `feat: Add tokens command #47`)

## Code Structure (Go - Primary)

```
waza-go/
├── cmd/waza/              # CLI entrypoint
│   └── main.go            # Command parsing and execution
├── internal/
│   ├── config/            # Configuration with functional options
│   ├── execution/         # AgentEngine interface and implementations
│   │   ├── engine.go      # Core engine interface
│   │   ├── mock.go        # Mock engine for testing
│   │   └── copilot.go     # Copilot SDK integration
│   ├── models/            # Data structures
│   │   ├── spec.go        # BenchmarkSpec (eval configuration)
│   │   ├── testcase.go    # TestCase (task definition)
│   │   └── outcome.go     # EvaluationOutcome (results)
│   ├── orchestration/     # TestRunner for coordinating execution
│   │   └── runner.go      # Benchmark orchestration
│   └── scoring/           # Validator interface and implementations
│       ├── validator.go   # Validator registry pattern
│       └── code_validators.go  # Code and regex validators
├── go.mod
├── go.sum
├── Makefile               # Build and test commands
└── .golangci.yml          # Linter configuration
```

## Go Naming Conventions

The Go implementation uses idiomatic Go naming:

| Concept | Go Name | Python Equivalent |
|---------|---------|-------------------|
| Eval configuration | `BenchmarkSpec` | `EvalSpec` |
| Executor | `AgentEngine` | `BaseExecutor` |
| Grader | `Validator` | `Grader` |
| Task | `TestCase` | `Task` |
| Result | `EvaluationOutcome` | `EvalResult` |

## Key Go Patterns

### Functional Options for Configuration
```go
engine := execution.NewCopilotEngine(
    execution.WithModel("gpt-4o"),
    execution.WithTimeout(300 * time.Second),
    execution.WithVerbose(true),
)
```

### Interface-based Design
```go
type AgentEngine interface {
    Execute(ctx context.Context, testCase *models.TestCase) (*models.ExecutionResult, error)
    Shutdown() error
}
```

### Validator Registry
```go
registry := scoring.NewValidatorRegistry()
registry.Register("code", &scoring.CodeValidator{})
registry.Register("regex", &scoring.RegexValidator{})
```

## Building and Testing

```bash
cd waza-go

# Build
make build
# or: go build -o waza ./cmd/waza

# Run tests
make test
# or: go test -v ./...

# Lint
make lint
# or: golangci-lint run

# Run evaluation
./waza run ../examples/code-explainer/eval.yaml --context-dir ../examples/code-explainer/fixtures -v
```

## CI/CD

**Go CI is required for all PRs.** Branch protection enforces:
- `Build and Test Go Implementation` must pass
- `Lint Go Code` must pass

The workflow is defined in `.github/workflows/go-ci.yml`.

## Fixture Isolation

Each task execution gets a **fresh temp workspace** with fixtures copied in:

1. Runner reads files from original `--context-dir` (fixtures folder)
2. Executor creates new temp workspace (e.g., `/tmp/waza-abc123/`)
3. Files copied into temp workspace
4. Agent works in temp workspace (edits happen here)
5. Temp workspace destroyed after task
6. Next task starts fresh with original fixtures

**The original fixtures directory is never modified.** This ensures task isolation.

## Documentation Requirements

**Always update documentation when making changes.** The following files must be kept in sync:

| File | Purpose | Update When |
|------|---------|-------------|
| `README.md` | Main project overview | Any CLI change, new feature |
| `waza-go/README.md` | Go implementation details | Go code changes |
| `docs/PRD.md` | Product requirements | Feature scope changes |
| `AGENTS.md` | Agent coding guidelines | Process/pattern changes |

### Documentation Checklist

When adding a new CLI command:
- [ ] Update `waza-go/README.md` usage section
- [ ] Update `README.md` if user-facing
- [ ] Add example in appropriate docs
- [ ] Update tracking issue #66 if related to roadmap

When completing a feature:
- [ ] Close related GitHub issue with comment
- [ ] Update tracking issue #66 checkbox
- [ ] Update docs as needed

## Adding New Features

### Adding a CLI Command

1. Add command handling in `cmd/waza/main.go`
2. Implement logic in appropriate `internal/` package
3. Add tests in `*_test.go` files
4. Update `waza-go/README.md`

### Adding a Validator (Grader)

1. Implement `Validator` interface in `internal/scoring/`
2. Register in `ValidatorRegistry`
3. Add tests
4. Document in README

### Adding an Engine (Executor)

1. Implement `AgentEngine` interface in `internal/execution/`
2. Add configuration options
3. Add tests
4. Document usage

## Code Ownership and Review

### CODEOWNERS File

The `.github/CODEOWNERS` file automatically assigns reviewers:
- All files → @spboyer @chlowell @richardpark-msft

### Branch Protection

PRs to `main` require:
- Go CI must pass (`Build and Test Go Implementation`, `Lint Go Code`)
- Auto-merge enabled for convenience

## Commit Messages

Use conventional commits:
- `feat:` New feature
- `fix:` Bug fix
- `docs:` Documentation only
- `ci:` CI/CD changes
- `chore:` Maintenance tasks
- `refactor:` Code restructuring

**Reference issues:** `feat: Add tokens command #47`

## Files to Ignore

These are generated/temporary and should not be committed:
- `results.json` - Eval results
- `coverage.txt` - Test coverage
- `waza` (binary) - Built executable

## Quick Reference

### Build and run
```bash
cd waza-go
make build
./waza run ../examples/code-explainer/eval.yaml -v
```

### Run tests
```bash
cd waza-go
make test
```

### Key CLI flags
- `-v, --verbose` - Verbose output
- `-o, --output` - Save results JSON
- `--context-dir` - Fixtures directory

## Epics and Priorities

See [Tracking Issue #66](https://github.com/spboyer/waza/issues/66) for the full roadmap.

| Epic | Priority | Description |
|------|----------|-------------|
| E1: Go CLI Foundation | P0 | Core CLI commands |
| E2: Sensei Engine | P0 | Compliance scoring |
| E3: Evaluation Framework | P0 | Cross-model testing |
| E4: Token Management | P1 | Budget tracking |
| E5: Waza Skill | P1 | Conversational interface |
| E6: CI/CD Integration | P1 | GitHub Actions |
| E7: AZD Extension | P2 | azd packaging |
