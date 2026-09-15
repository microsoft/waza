# Agent Instructions for waza Repository

## Overview

This repository contains `waza`, a CLI tool for evaluating Agent Skills. **Go is the only implementation**, and it lives in the repository root. (An early Python implementation was removed in February 2026.)

When making changes, follow these guidelines to maintain consistency and quality.

## Project Tracking

**Keep issues and tracking up to date:**
- **Tracking Issue:** [#66 - Waza Platform Roadmap](https://github.com/microsoft/waza/issues/66)
- **PRD:** [docs/PRD.md](docs/PRD.md)
- When completing work, update the relevant GitHub issue
- Reference issue numbers in commit messages (e.g., `feat: Add tokens command #47`)

## Code Structure (Go)

```
cmd/waza/                  # CLI entrypoint
└── main.go                # Command parsing and execution
internal/
├── config/                # Configuration with functional options
├── execution/             # AgentEngine interface and implementations
│   ├── engine.go          # Core engine interface
│   ├── mock.go            # Mock engine for testing
│   └── copilot.go         # Copilot SDK integration
├── graders/               # Grader interface and Create() factory
│   ├── grader.go          # Grader interface + Create() switch over grader kinds
│   ├── text_grader.go     # Text grader (one file per grader kind)
│   └── file_grader.go     # File grader
├── models/                # Data structures
│   ├── spec.go            # EvalSpec (eval configuration)
│   ├── testcase.go        # TestCase (task definition)
│   ├── grader_params.go   # models.*GraderParameters types
│   └── outcome.go         # EvaluationOutcome (results)
├── orchestration/         # EvalRunner for coordinating execution
│   └── runner.go          # Eval orchestration
└── scoring/               # Skill-adherence heuristic scoring
    └── scoring.go         # Scorer / HeuristicScorer
go.mod
go.sum
Makefile                   # Build and test commands
.golangci.yml
```

## Go Naming Conventions

The Go implementation uses idiomatic Go naming:

| Concept | Go Name | Defined in |
|---------|---------|------------|
| Eval configuration | `EvalSpec` | `internal/models/spec.go` |
| Executor | `AgentEngine` | `internal/execution/engine.go` |
| Grader | `Grader` | `internal/graders/grader.go` |
| Task | `TestCase` | `internal/models/testcase.go` |
| Result | `EvaluationOutcome` | `internal/models/outcome.go` |

## Key Go Patterns

### Functional Options for Configuration
```go
cfg := config.NewEvalConfig(spec,
    config.WithSpecDir("evals"),
    config.WithVerbose(true),
)
```

### Builder for Engine Construction
```go
// options may be nil in production; tests pass a NewCopilotClient factory
engine := execution.NewCopilotEngineBuilder(modelID, nil).Build()
```

### Interface-based Design
```go
type AgentEngine interface {
    Initialize(ctx context.Context) error
    Execute(ctx context.Context, req *ExecutionRequest) (*ExecutionResponse, error)
    Shutdown(ctx context.Context) error
    SessionUsage(sessionID string) *models.UsageStats
}
```

### Grader Factory
```go
// Create() switches over the models.*GraderParameters type to build a Grader
g, err := graders.Create(identifier, params)
```

## Building and Testing

> **Requires Go 1.26 or later.** The module targets `go 1.26` (`go.mod`), which means Go 1.26 language features and standard library APIs are safe to use. If you want to rely on features or stdlib additions introduced after Go 1.26, first bump the `go` version in `go.mod` to that minimum version.

```bash
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
./waza run examples/code-explainer/eval.yaml --context-dir examples/code-explainer/fixtures -v
```

### Testing Requirements

**Every PR must leave tests in a passing state.** This is non-negotiable:

- **All existing tests must pass** — run `go test ./...` before pushing. If your change breaks an existing test, fix it.
- **New features require new tests** — every new command, flag, grader, or internal function needs test coverage. No shipping untested code.
- **Bug fixes require regression tests** — if you fix a bug, add a test that would have caught it.
- **Playwright e2e tests** — if you change the dashboard (`web/`), run `cd web && npx playwright test --project=chromium` and fix any failures.
- **CI is the gate** — `Build and Test Go Implementation` and `Lint Go Code` must pass. PRs with failing tests do not merge.

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

**Use Mermaid for all diagrams** in markdown files (docs, design docs, plans). No ASCII art diagrams.

**Always update documentation when making changes.** The following files must be kept in sync:

| File | Purpose | Update When |
|------|---------|-------------|
| `README.md` | Main project overview | Any CLI change, new feature |
| `docs/PRD.md` | Product requirements | Feature scope changes |
| `AGENTS.md` | Agent coding guidelines | Process/pattern changes |
| `site/` (GitHub Pages) | Public docs site (microsoft.github.io/waza) | Any feature add/change |
| `web/` (Dashboard) | Interactive eval dashboard | New data in results JSON, new views needed |

### Documentation Checklist

When adding or updating any feature:
- [ ] Check if `site/src/content/docs/` pages need updating (graders, CLI reference, guides, eval YAML)
- [ ] Check if the dashboard (`web/`) needs updates or new views to surface the feature
- [ ] Update `README.md` if user-facing
- [ ] Update the root `README.md` usage section if CLI changes
- [ ] Build the docs site to verify: `cd site && npm run build`
- [ ] Add example in appropriate docs
- [ ] Update tracking issue #66 if related to roadmap

When adding a new CLI command or flag:
- [ ] Add to `site/src/content/docs/reference/cli.mdx`
- [ ] Add to `site/src/content/docs/guides/` if it needs a guide
- [ ] Update `README.md` Commands section

When completing a feature:
- [ ] Close related GitHub issue with comment
- [ ] Update tracking issue #66 checkbox
- [ ] Verify GitHub Pages site reflects the change (pages deploy on merge to main)

## Documentation Maintenance

Documentation must be updated in real-time as features change. This is enforced by Saul (Documentation Lead) who reviews all PRs for doc impact.

### When to Update Docs

| Change Type | Required Doc Updates |
|---|---|
| New CLI command or flag | README.md Commands section, `site/` CLI reference, docs/GUIDE.md |
| Changed CLI behavior | README.md, `site/` guides, docs/GUIDE.md, affected tutorials |
| New/changed dashboard view | `site/` dashboard guide, regenerate screenshots, docs/DEMO-GUIDE.md |
| Changed eval YAML schema | README.md YAML section, `site/` eval-yaml reference, example files |
| New grader | README.md "Available Grader Types" section, `site/` graders page, docs/GUIDE.md |
| New sensei/dev feature | `site/` sensei guide, README.md |
| New data in results JSON | Check if dashboard (`web/`) needs a new view, column, or chart to surface it |

### Screenshot Maintenance

When dashboard UI changes, regenerate screenshots:
```bash
cd web && npx playwright test e2e/screenshots.spec.ts --project=chromium
```
Screenshots are saved to `docs/images/` and referenced throughout documentation.

## Adding New Features

### Adding a CLI Command

1. Add command handling in `cmd/waza/main.go`
2. Implement logic in appropriate `internal/` package
3. Add tests in `*_test.go` files
4. Update the root `README.md`

### Adding a Grader

1. Implement the `Grader` interface in `internal/graders/` (one file per grader kind)
2. Add a `models.<Kind>GraderParameters` type in `internal/models/grader_params.go`
3. Add the matching `case` to `Create()` in `internal/graders/grader.go`
4. Add tests
5. Document in README and `site/`

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
make build
./waza run examples/code-explainer/eval.yaml -v
```

### Run tests
```bash
make test
```

### Key CLI flags
- `-v, --verbose` - Verbose output
- `-o, --output` - Save results JSON
- `--context-dir` - Fixtures directory

## Epics and Priorities

See [Tracking Issue #66](https://github.com/microsoft/waza/issues/66) for the full roadmap.

| Epic | Priority | Description |
|------|----------|-------------|
| E1: Go CLI Foundation | P0 | Core CLI commands |
| E2: Sensei Engine | P0 | Compliance scoring |
| E3: Evaluation Framework | P0 | Cross-model testing |
| E4: Token Management | P1 | Budget tracking |
| E5: Waza Skill | P1 | Conversational interface |
| E6: CI/CD Integration | P1 | GitHub Actions |
| E7: AZD Extension | P2 | azd packaging |
