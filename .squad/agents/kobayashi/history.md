# Kobayashi — History

## 2025-06-15: JUnit XML Reporter (#312, PR #326)

**Branch:** `squad/312-junit-reporter`

**What was built:**
- `internal/reporting/junit.go` — JUnit XML types (`JUnitTestSuites`, `JUnitTestSuite`, `JUnitTestCase`, etc.), `ConvertToJUnit()` converter, `WriteJUnitXML()` writer
- `internal/reporting/junit_test.go` — 9 unit tests covering passed/failed/error/empty outcomes, properties, duration fallback, valid XML roundtrip
- `cmd/waza/cmd_run.go` — `--reporter` flag (repeatable `StringArray`), `writeReporters()` helper called from `runCommandForSpec()`
- `cmd/waza/cmd_run_test.go` — 3 integration tests: JUnit output, flag parsing, unknown reporter error

**Key design decisions:**
- Reporter flag uses `StringArray` (repeatable) with `type:path` format (e.g. `junit:results.xml`)
- `json` reporter is a no-op since JSON output is handled by `--output`
- JUnit XML uses standard schema: `<testsuites>` → `<testsuite>` → `<testcase>` with `<failure>` or `<error>`
- Grader results mapped to failure body text with `[FAIL] grader (type): score — feedback` format
- Suite properties include skill, model, engine, aggregate score
- `writeReporters()` placed in `runCommandForSpec()` (not `runSingleModel()`) to avoid duplicate writes in multi-skill/multi-model paths

**Learnings:**
- The `cmd/waza/cmd_run.go` uses package-level globals for all flags — `resetRunGlobals()` in tests must be updated for any new flag
- `EvaluationOutcome.TestOutcomes[].Runs[].Validations` is a `map[string]GraderResults` — sort keys for deterministic output
- Multi-skill runs clear `outputPath` during the loop; independent flags like `reporters` need careful placement to avoid per-skill overwrites
- The `reporting` package already existed with `interpreter.go` — JUnit reporter fits naturally alongside it

## 2026-02-21: Waza Interactive Workflow Skill (#288, PR #363)

**Branch:** `squad/288-waza-skill`

**What was built:**
- `skills/waza-interactive/SKILL.md` — Conversational workflow partner skill with 5 numbered scenarios (create eval, run & interpret, compare models, debug failing, ship readiness check)
- `skills/waza-interactive/tests/eval.yaml` — Trigger test eval with 5 trigger scenarios + 1 anti-trigger

**Key design decisions:**
- Skill is a workflow orchestrator, not a tool catalog — each scenario is a numbered recipe with step-by-step MCP tool call chains
- MCP tool table at top for quick reference, scenarios below for guided execution
- Token budget kept at ~1,100 tokens (well under 2,500 SkillsBench sweet spot)
- Frontmatter uses USE FOR / DO NOT USE FOR pattern with clear anti-triggers separating from sensei and skill-authoring
- Ship readiness checklist uses a rendered checklist format for clear pass/fail verdicts
- Anti-trigger test case included to verify the skill doesn't activate for general coding requests

**Learnings:**
- Workflow skills need to balance reference (tool table) with orchestration (scenarios) — too much reference and the agent doesn't know what to chain, too much orchestration and it can't adapt
- Numbered steps in scenarios correlate with SkillsBench improvement (+18.8pp per the task spec)
- Existing `skills/waza/SKILL.md` is already comprehensive as a reference skill — the interactive skill complements it by adding conversational workflows, not duplicating commands
- Trigger test evals should include anti-triggers to verify routing accuracy — prevents the skill from over-activating on general requests
