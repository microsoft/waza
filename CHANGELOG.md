# Changelog

All notable changes to waza will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.1] - 2026-02-12

### Added

- `waza dev` command for interactive skill development and testing (#117)
- Prerelease input to azd publish workflow
- CHANGELOG.md as release notes source for azd extension releases
- `waza generate --skill <name>` - Filter to specific skill when using `--repo` or `--scan`

### Fixed

- Fixed azd extensions documentation link
- Corrected `azd ext source add` command syntax
- Branch release PR from origin/main to avoid workflow permission error (#121)

### Changed

- Removed path filters from Go CI to unblock non-code PRs
- Removed auto-merge from azd publish PR workflow
- Added azd extension installation instructions to README

## [0.2.0] - 2026-02-02

### Added

- **Skill Discovery** (#3)
  - `waza generate --repo <org/repo>` - Scan GitHub repos for SKILL.md files
  - `waza generate --scan` - Scan local directory for skills
  - `waza generate --all` - Generate evals for all discovered skills (CI-friendly)
  - Interactive skill selection with checkboxes when not using `--all`

- **GitHub Issue Creation** (#3)
  - Post-run prompt to create GitHub issues with eval results
  - Options: create for failed tasks only, all tasks, or none
  - Issues include results table, failed task details, and suggestions
  - `--no-issues` flag to skip prompts (CI-friendly)

- **New Modules**
  - `waza/scanner.py` - Skill discovery from GitHub repos and local directories
  - `waza/issues.py` - GitHub issue creation and formatting

### Changed

- Improved documentation with new feature guides
- Added skill discovery section to DEMO-SCRIPT.md
- Updated TUTORIAL.md with discovery and issue creation steps

## [0.1.0] - 2026-02-02

### Changed

- **Renamed project from `skill-eval` to `waza`** (技 - Japanese for "technique/skill")
  - New CLI command: `waza` (previously `skill-eval`)
  - New package name: `waza` (previously `skill_eval`)
  - Repository renamed to `waza`
- Bumped version to 0.1.0 to mark the rename milestone

### Migration

If you were using `skill-eval`, update your scripts:

```bash
# Old
skill-eval run ./eval.yaml
pip install skill-eval

# New
waza run ./eval.yaml
pip install waza
```

## [0.0.2] - 2026-02-01

### Added

- `--suggestions-file` option to save improvement suggestions to markdown file
- Improved progress display with step-by-step status (tool counts, activity indicators)
- Copilot SDK usage guide in AGENTS.md

### Fixed

- Fixed Copilot SDK import (`from copilot import CopilotClient` not `copilot_sdk`)
- Fixed Windows glob pattern in release workflow
- Fixed linting issues across codebase (import sorting, exception chaining, etc.)
- Clarified fixture isolation between tasks (each task gets fresh temp workspace)

## [0.0.1] - 2026-02-01

### Added

- **CLI Commands**
  - `waza run` - Run evaluation suites against skills
  - `waza generate` - Auto-generate evals from SKILL.md files
  - `waza init` - Initialize new eval suites interactively
  - `waza report` - Generate reports from results

- **Eval Generation**
  - Pattern-based generation from SKILL.md files
  - LLM-assisted generation with `--assist` flag for better tasks/fixtures
  - Support for multiple models (Claude, GPT-4, etc.)

- **Executors**
  - Mock executor for testing without LLM calls
  - Copilot SDK executor for real integration testing

- **Graders**
  - Code graders with Python assertions
  - Regex graders for pattern matching
  - LLM graders for semantic evaluation

- **Features**
  - Real-time progress display with conversation streaming (`-v`)
  - Transcript logging (`--log`)
  - Project context support (`--context-dir`)
  - LLM-powered improvement suggestions (`--suggestions`)

- **Documentation**
  - Comprehensive README with examples
  - Tutorial guide
  - Grader reference
  - Demo script for walkthroughs

### Fixed

- Grader eval context now includes `str`, `int`, `bool`, etc.
- Transcript normalization for proper tool call detection
- YAML escaping for regex patterns with backslashes
- Progress bar now shows 100% on completion

[Unreleased]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_0.2.1...HEAD
[0.2.1]: https://github.com/spboyer/waza/compare/azd-ext-microsoft-azd-waza_0.2.0...azd-ext-microsoft-azd-waza_0.2.1
[0.2.0]: https://github.com/spboyer/waza/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/spboyer/waza/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/spboyer/waza/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/spboyer/waza/releases/tag/v0.0.1
