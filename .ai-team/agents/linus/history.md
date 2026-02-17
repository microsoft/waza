# Linus — History

## Learnings

- cmd_new.go implementation pattern: two-mode detection (skills/ dir presence via `findProjectRoot()` walking up from CWD). In-project mode creates under `skills/{name}/` and `evals/{name}/`, standalone mode creates self-contained `{name}/` directory with CI workflow, .gitignore, and README.
- Default eval templates: basic-usage (happy path with output_contains), edge-case (empty input), should-not-trigger (anti-trigger with output_not_contains). All three task files follow the same YAML schema with id, name, description, tags, inputs, expected.
- SKILL.md template includes USE FOR / DO NOT USE FOR stubs in the frontmatter description field, matching the pattern from examples/code-explainer/SKILL.md.
- The `writeFiles` helper checks `os.Stat` before writing — skips existing files with a message instead of overwriting. This is the safety contract for `waza new`.
- Default eval.yaml uses YAML field names matching BenchmarkSpec struct tags: `trials_per_task`, `timeout_seconds`, `parallel`, `executor`, `model` (not the JSON names).
- `internal/workspace` package reuses `internal/skill.Skill.UnmarshalText()` for SKILL.md frontmatter parsing rather than duplicating the parser. The `internal/generate.ParseSkillMD` function exists but uses its own `SkillFrontmatter` type — prefer `internal/skill` for richer data.
- Workspace detection walk-up capped at 10 parent levels (`maxParentWalk`) to prevent runaway traversal. Hidden directories (`.` prefix) are skipped during child scanning.
- FindEval 3-level priority: separated (`{root}/evals/{name}/eval.yaml`) > nested (`{skill-dir}/evals/eval.yaml`) > co-located (`{skill-dir}/eval.yaml`). This matches the E8 design decision in decisions.md.

## Completed Work

| Date | Issue | PR | Summary |
|------|-------|----|---------|
| 2026-02-17 | #172 | #175 | Implemented `internal/workspace/` package with DetectContext, FindSkill, FindEval. 15 tests all passing. |
