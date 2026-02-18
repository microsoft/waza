# Design: A/B Baseline Skill Impact Measurement (#194)

**Issue:** #194 - A/B Skill Impact Measurement (`waza run --baseline`)  
**Author:** Rusty (Lead / Architect)  
**Status:** Design (Ready for Implementation)  
**Date:** 2026-02-18

---

## Overview

The `--baseline` flag enables A/B testing by running the same evaluation twice:
1. **Skills-Enabled Run:** Normal execution with all skills loaded
2. **Baseline Run:** Re-execution with all skills stripped out

The runner compares pass rates between the two runs to compute a **skill_impact** metric that quantifies whether skills improve or hurt agent performance on a given task.

This addresses the use case: **"Do our skills actually help, or are they noise?"**

---

## 1. CLI Surface

### Flag Addition to `waza run`

```bash
waza run eval.yaml --baseline [--context-dir ./fixtures] [-v] [-o results.json]
```

**New Flag:**
- `--baseline` (bool, default: false)
  - When set, runs evaluation twice: skills-enabled, then skills-disabled
  - Computes and reports per-task skill impact
  - Adds `skill_impact` metrics to JSON output
  - Exit code semantics change (see §5)

**Behavior:**
- Without `--baseline`: Normal single-pass evaluation (unchanged)
- With `--baseline`: Two sequential passes, results paired and compared
- If eval has no skills configured (`skill_directories` and `required_skills` both empty/unset), `--baseline` is a no-op with a warning

**Example Output (Verbose):**

```
Running evaluation: code-explainer
Loaded 5 test cases
Config: executor=copilot, model=gpt-4o, trials=3

════════════════════════════════════════════════════════════════
PASS 1: Skills-Enabled Run (with skills)
════════════════════════════════════════════════════════════════
[1/5] Task: explain-variables ... ✅ PASS (2/3 trials)
[2/5] Task: explain-functions ... ✅ PASS (3/3 trials)
[3/5] Task: explain-classes ... ❌ FAIL (1/3 trials)
[4/5] Task: explain-imports ... ✅ PASS (2/3 trials)
[5/5] Task: explain-comments ... ❌ FAIL (1/3 trials)

Total: 3/5 passed (60%)

════════════════════════════════════════════════════════════════
PASS 2: Skills Baseline (skills stripped)
════════════════════════════════════════════════════════════════
[1/5] Task: explain-variables ... ❌ FAIL (0/3 trials)
[2/5] Task: explain-functions ... ✅ PASS (1/3 trials)
[3/5] Task: explain-classes ... ❌ FAIL (0/3 trials)
[4/5] Task: explain-imports ... ❌ FAIL (0/3 trials)
[5/5] Task: explain-comments ... ❌ FAIL (0/3 trials)

Total: 1/5 passed (20%)

════════════════════════════════════════════════════════════════
SKILL IMPACT ANALYSIS
════════════════════════════════════════════════════════════════
Overall Impact: +3.0x (60% vs 20%)
  • explain-variables: +100% (1→2 trials)
  • explain-functions: +200% (1→3 trials)  
  • explain-classes: no change (0 trials)
  • explain-imports: +∞ (0→2 trials)
  • explain-comments: no change (0 trials)

Conclusion: Skills have positive impact on 4/5 tasks.
Exit code: 0 (skills improved performance)
```

---

## 2. Config Changes

### BenchmarkSpec Addition

File: `internal/models/spec.go`

```go
type BenchmarkSpec struct {
    SpecIdentity `yaml:",inline"`
    SkillName    string            `yaml:"skill"`
    Version      string            `yaml:"version"`
    Config       Config            `yaml:"config"`
    Hooks        hooks.HooksConfig `yaml:"hooks,omitempty"`
    Inputs       map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
    TasksFrom    string            `yaml:"tasks_from,omitempty" json:"tasks_from,omitempty"`
    Range        [2]int            `yaml:"range,omitempty" json:"range,omitempty"`
    Graders      []GraderConfig    `yaml:"graders"`
    Metrics      []MeasurementDef  `yaml:"metrics"`
    Tasks        []string          `yaml:"tasks"`
    
    // NEW: Baseline comparison
    Baseline     bool              `yaml:"baseline,omitempty" json:"baseline,omitempty"`
}
```

**No YAML syntax change.** The `--baseline` flag overrides `spec.Baseline` at runtime (CLI flag takes precedence). Eval authors may also set `baseline: true` in `eval.yaml`, though the typical use case is CLI-driven A/B testing.

### No Changes to Config Struct

The `Config` struct (executor, model, timeout, etc.) remains unchanged. The `SkillPaths` and `RequiredSkills` fields are already present:

```go
type Config struct {
    // ... existing fields ...
    SkillPaths     []string       `yaml:"skill_directories,omitempty" json:"skill_paths,omitempty"`
    RequiredSkills []string       `yaml:"required_skills,omitempty" json:"required_skills,omitempty"`
    // ... other fields ...
}
```

---

## 3. Runner Changes

### Execution Model

**File:** `internal/orchestration/runner.go`

The `TestRunner` gains baseline support:

#### 3.1 Method Signature Extension

```go
// RunBenchmark executes the entire benchmark
// If Baseline is enabled, runs twice: skills-enabled and skills-disabled
func (r *TestRunner) RunBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {
    // ... existing code ...
    
    if baseline := r.cfg.Spec().Baseline; baseline {
        return r.runBaselineComparison(ctx)
    }
    
    // Normal single-pass execution (unchanged)
    return r.runNormalBenchmark(ctx)
}

// runNormalBenchmark is the existing RunBenchmark logic extracted
func (r *TestRunner) runNormalBenchmark(ctx context.Context) (*models.EvaluationOutcome, error) {
    // ... existing RunBenchmark body, no changes ...
}

// runBaselineComparison orchestrates A/B testing
func (r *TestRunner) runBaselineComparison(ctx context.Context) (*models.EvaluationOutcome, error) {
    spec := r.cfg.Spec()
    
    // Validation: eval must have skills configured
    if len(spec.Config.SkillPaths) == 0 && len(spec.Config.RequiredSkills) == 0 {
        fmt.Println("[WARN] --baseline specified but eval has no skills configured (skill_directories, required_skills empty). Skipping baseline comparison.")
        return r.runNormalBenchmark(ctx)
    }
    
    // PASS 1: Skills-Enabled
    fmt.Println("\n════════════════════════════════════════════════════════════════")
    fmt.Println("PASS 1: Skills-Enabled Run")
    fmt.Println("════════════════════════════════════════════════════════════════\n")
    outcomesWithSkills, err := r.runNormalBenchmark(ctx)
    if err != nil {
        return nil, fmt.Errorf("skills-enabled run failed: %w", err)
    }
    
    // PASS 2: Skills Disabled (baseline)
    // Temporarily strip skills from config
    savedSkillPaths := spec.Config.SkillPaths
    savedRequiredSkills := spec.Config.RequiredSkills
    spec.Config.SkillPaths = []string{}
    spec.Config.RequiredSkills = []string{}
    defer func() {
        spec.Config.SkillPaths = savedSkillPaths
        spec.Config.RequiredSkills = savedRequiredSkills
    }()
    
    fmt.Println("\n════════════════════════════════════════════════════════════════")
    fmt.Println("PASS 2: Skills Baseline (skills stripped)")
    fmt.Println("════════════════════════════════════════════════════════════════\n")
    outcomesWithoutSkills, err := r.runNormalBenchmark(ctx)
    if err != nil {
        return nil, fmt.Errorf("baseline run (skills disabled) failed: %w", err)
    }
    
    // Restore skills before returning
    spec.Config.SkillPaths = savedSkillPaths
    spec.Config.RequiredSkills = savedRequiredSkills
    
    // PASS 3: Compare and merge results
    return r.mergeBaselineOutcomes(outcomesWithSkills, outcomesWithoutSkills)
}
```

#### 3.2 Key Design Decisions

**Sequential Execution (Not Parallel)**
- Pass 1 (skills-enabled) completes fully before Pass 2 (baseline) starts
- **Rationale:** Simpler state management, avoids resource contention, cleaner output
- Each pass gets its own engine instance (created fresh inside `runNormalBenchmark`)
- Workspace directories are isolated per pass (fixture copying is already per-execution)

**Fixture Isolation**
- No changes needed. The existing fixture-copy pattern in `workspace` package already gives each execution a fresh temp directory
- Pass 1 works in `/tmp/waza-abc123/` → destroyed after
- Pass 2 works in `/tmp/waza-def456/` → destroyed after
- Original fixtures are never modified

**Config Mutation Pattern**
- Save `SkillPaths` and `RequiredSkills` before Pass 2
- Clear them on the spec config
- Restore them after Pass 2
- This pattern is used elsewhere in the codebase (e.g., task filters)

#### 3.3 Outcome Merging

```go
// mergeBaselineOutcomes pairs task results and computes skill impact
func (r *TestRunner) mergeBaselineOutcomes(
    withSkills, withoutSkills *models.EvaluationOutcome,
) (*models.EvaluationOutcome, error) {
    
    // Build maps: TestID → TestOutcome for quick lookup
    withMap := make(map[string]*models.TestOutcome)
    withoutMap := make(map[string]*models.TestOutcome)
    
    for i, to := range withSkills.TestOutcomes {
        withMap[to.TestID] = &withSkills.TestOutcomes[i]
    }
    for i, to := range withoutSkills.TestOutcomes {
        withoutMap[to.TestID] = &withoutSkills.TestOutcomes[i]
    }
    
    // Merge: for each task, compute skill_impact
    for testID, withTo := range withMap {
        withoutTo, ok := withoutMap[testID]
        if !ok {
            return nil, fmt.Errorf("baseline mismatch: task %q present in skills-enabled but not baseline", testID)
        }
        
        withTo.SkillImpact = computeSkillImpact(withTo, withoutTo)
    }
    
    // Check for extra tasks in baseline (shouldn't happen if test loading is consistent)
    for testID := range withoutMap {
        if _, ok := withMap[testID]; !ok {
            return nil, fmt.Errorf("baseline mismatch: task %q present in baseline but not skills-enabled", testID)
        }
    }
    
    // Return merged outcome (use withSkills as the primary result)
    withSkills.IsBaseline = true
    withSkills.BaselineOutcome = withoutSkills
    return withSkills, nil
}

// computeSkillImpact calculates per-task impact metric
func computeSkillImpact(withSkills, without *models.TestOutcome) *models.SkillImpactMetric {
    // Pass rates per run
    passRateWith := computePassRate(withSkills)
    passRateWithout := computePassRate(without)
    
    // Compute delta
    delta := passRateWith - passRateWithout
    
    // Compute % improvement (with div-by-zero guard)
    percentImprovement := 0.0
    denom := math.Max(passRateWithout, 0.01)
    percentImprovement = (delta / denom) * 100.0
    
    return &models.SkillImpactMetric{
        PassRateWithSkills: passRateWith,
        PassRateBaseline:   passRateWithout,
        Delta:              delta,
        PercentChange:      percentImprovement,
    }
}

func computePassRate(outcome *models.TestOutcome) float64 {
    if outcome.Trials == 0 {
        return 0.0
    }
    return float64(outcome.Passed) / float64(outcome.Trials)
}
```

---

## 4. Results & Comparison

### JSON Schema Additions

**File:** `internal/models/outcome.go`

```go
// TestOutcome represents the result of running a single test
type TestOutcome struct {
    TestID        string                 `json:"test_id"`
    DisplayName   string                 `json:"display_name"`
    Trials        int                    `json:"trials"`
    Passed        int                    `json:"passed"`
    Failed        int                    `json:"failed"`
    Status        Status                 `json:"status"`
    SkillImpact   *SkillImpactMetric     `json:"skill_impact,omitempty"`
    // ... existing fields ...
}

// SkillImpactMetric represents A/B comparison for a single task
type SkillImpactMetric struct {
    PassRateWithSkills float64 `json:"pass_rate_with_skills"`
    PassRateBaseline   float64 `json:"pass_rate_baseline"`
    Delta              float64 `json:"delta"`                    // Absolute change (0.0–1.0)
    PercentChange      float64 `json:"percent_change"`           // Percentage change
}

// EvaluationOutcome is the top-level result
type EvaluationOutcome struct {
    // ... existing fields ...
    IsBaseline       bool                  `json:"is_baseline,omitempty"`      // True if A/B comparison was run
    BaselineOutcome  *EvaluationOutcome    `json:"baseline_outcome,omitempty"` // Outcome without skills (if A/B)
}
```

### Example JSON Output

```json
{
  "evaluation_name": "code-explainer",
  "is_baseline": true,
  "overall_pass_rate": 0.6,
  "test_outcomes": [
    {
      "test_id": "explain-variables",
      "display_name": "Explain Variables",
      "trials": 3,
      "passed": 2,
      "failed": 1,
      "status": "passed",
      "skill_impact": {
        "pass_rate_with_skills": 0.667,
        "pass_rate_baseline": 0.0,
        "delta": 0.667,
        "percent_change": 6670.0
      }
    },
    {
      "test_id": "explain-functions",
      "display_name": "Explain Functions",
      "trials": 3,
      "passed": 3,
      "failed": 0,
      "status": "passed",
      "skill_impact": {
        "pass_rate_with_skills": 1.0,
        "pass_rate_baseline": 0.333,
        "delta": 0.667,
        "percent_change": 200.0
      }
    },
    {
      "test_id": "explain-classes",
      "display_name": "Explain Classes",
      "trials": 3,
      "passed": 1,
      "failed": 2,
      "status": "failed",
      "skill_impact": {
        "pass_rate_with_skills": 0.333,
        "pass_rate_baseline": 0.0,
        "delta": 0.333,
        "percent_change": 3330.0
      }
    }
  ],
  "baseline_outcome": {
    "evaluation_name": "code-explainer (baseline)",
    "overall_pass_rate": 0.2,
    "test_outcomes": [
      {
        "test_id": "explain-variables",
        "display_name": "Explain Variables",
        "trials": 3,
        "passed": 0,
        "failed": 3,
        "status": "failed"
      },
      {
        "test_id": "explain-functions",
        "display_name": "Explain Functions",
        "trials": 3,
        "passed": 1,
        "failed": 2,
        "status": "failed"
      }
    ]
  }
}
```

### Console Output Integration

The runner prints a summary after merging:

```
════════════════════════════════════════════════════════════════
SKILL IMPACT ANALYSIS
════════════════════════════════════════════════════════════════

Overall Performance Delta:
  With Skills:   60% (3/5 tasks passed)
  Without Skills: 20% (1/5 tasks passed)
  Impact:        +3.0x (40 percentage points)

Per-Task Breakdown:
  • explain-variables      [IMPROVED]  0% → 67% (+67pp, +∞%)
  • explain-functions      [IMPROVED]  33% → 100% (+67pp, +200%)
  • explain-classes        [IMPROVED]  0% → 33% (+33pp, +∞%)
  • explain-imports        [IMPROVED]  0% → 67% (+67pp, +∞%)
  • explain-comments       [NEUTRAL]   0% → 0% (no change)

Verdict: Skills have POSITIVE IMPACT (improved 4/5 tasks)
════════════════════════════════════════════════════════════════
```

---

## 5. Exit Codes

The `--baseline` flag changes exit code semantics to signal whether skills are beneficial:

| Scenario | Exit Code | Rationale |
|----------|-----------|-----------|
| Skills-enabled pass rate > baseline | `0` | Skills improve performance — success |
| Skills-enabled pass rate ≤ baseline | `1` | Skills hurt or neutral — failure signal |
| Either run fails (infrastructure error) | `non-zero` | Fatal error (unchanged) |
| Baseline flag used but no skills configured | `0` | Warning issued, normal run executed |

**Examples:**

```bash
# Skills improve: 60% with, 20% without → exit 0
$ waza run eval.yaml --baseline
# (output shows +3.0x impact)
$ echo $?
0

# Skills hurt: 20% with, 40% without → exit 1
$ waza run eval.yaml --baseline
# (output shows -0.5x impact)
$ echo $?
1

# No skills configured: warning printed, normal run → exit 0
$ waza run eval-no-skills.yaml --baseline
[WARN] --baseline specified but eval has no skills configured...
(runs normal evaluation)
$ echo $?
0
```

**CI Integration:**
- `waza run --baseline` can be used as a quality gate: if skills regress, the pipeline fails
- Teams can require skills to maintain or improve pass rates to proceed with merges

---

## 6. Edge Cases

### Case 1: Eval Has No Skills Configured

**Scenario:** `skill_directories` and `required_skills` are both empty/unset in the eval spec.

**Behavior:**
- Print warning: `[WARN] --baseline specified but eval has no skills configured (skill_directories, required_skills empty). Skipping baseline comparison.`
- Execute normal single-pass evaluation (unchanged)
- Return success (exit 0)
- `SkillImpact` field is omitted from JSON output

**Rationale:** A/B comparison is meaningless without skills. Warn but don't fail.

### Case 2: Baseline Pass but Skills Fail

**Scenario:** Baseline (no skills) passes 80% of tasks, but skills-enabled run passes only 40%.

**Output:**
```
[SUMMARY] Skills have NEGATIVE IMPACT
  With Skills:   40% (2/5 passed)
  Without Skills: 80% (4/5 passed)
  Delta:         -2.0x (40 percentage points)
  
Exit code: 1 (regression detected)
```

**Rationale:** This is an important signal — our skills are *hurting* performance. Exit 1 gates the pipeline and alerts the team. Teams use this feedback to debug skill quality or prompt brittleness.

### Case 3: Both Runs Fail

**Scenario:** Neither the skills-enabled run nor the baseline run achieves any passing tasks (both 0%).

**Output:**
```
[SUMMARY] Skills-Enabled: 0% | Baseline: 0%
  Conclusion: Inconclusive (both runs failed)
  
Exit code: 0 (inconclusive, no regression detected)
```

**Rationale:** If both fail equally, skills are not making it worse. Don't gate the pipeline on inconclusive results. Teams should review the evaluation setup itself (graders, prompts, test cases).

### Case 4: Task Mismatch Between Passes

**Scenario:** Test loading produces different test cases in Pass 2 (shouldn't happen, but defensive programming).

**Behavior:**
- Return error: `"baseline mismatch: task 'foo' present in skills-enabled but not baseline"`
- Exit non-zero
- User must fix the underlying issue (e.g., non-deterministic task loading)

**Rationale:** A/B results are meaningless if the test sets don't align. Fail loudly.

### Case 5: Partial Failure Within a Run

**Scenario:** A single task's grader crashes mid-execution in Pass 2.

**Behavior:**
- The runner's existing error handling applies: task is marked `failed`, error logged
- Comparison continues with the failed task contributing 0% pass rate
- A/B analysis proceeds normally
- Exit code determined by overall pass rates as usual

**Rationale:** Grader errors are already handled in the normal execution path; baseline mode inherits this behavior.

---

## 7. Implementation Checklist

### Phase 1: Core Infrastructure
- [ ] Add `Baseline bool` field to `BenchmarkSpec` in `internal/models/spec.go`
- [ ] Add `--baseline` flag to `newRunCommand()` in `cmd/waza/cmd_run.go`
- [ ] Add `SkillImpactMetric` and update `TestOutcome`, `EvaluationOutcome` in `internal/models/outcome.go`
- [ ] Extract existing `RunBenchmark` logic into `runNormalBenchmark()` in `internal/orchestration/runner.go`
- [ ] Implement `runBaselineComparison()` method
- [ ] Implement `mergeBaselineOutcomes()` and `computeSkillImpact()` helpers

### Phase 2: CLI Integration & Output
- [ ] Route `--baseline` flag to `TestRunner.runBaselineComparison()`
- [ ] Implement console output formatting (ASCII tables, delta reporting)
- [ ] Update JSON marshaling to include `skill_impact` field
- [ ] Implement exit code logic (0 if improvement, 1 if regression/neutral)

### Phase 3: Tests
- [ ] Unit tests for `computeSkillImpact()` function (zero-division guard, edge cases)
- [ ] Integration test: baseline run with mock engine (skills-enabled vs. baseline)
- [ ] Integration test: no skills configured (warning check)
- [ ] Integration test: negative impact (verify exit 1)
- [ ] Integration test: JSON output includes `skill_impact` field
- [ ] Test task mismatch error handling

### Phase 4: Documentation
- [ ] Update `README.md` with `--baseline` usage example
- [ ] Add baseline section to `TUTORIAL.md` or `DEMO-GUIDE.md`
- [ ] Document exit code semantics in `waza run --help`
- [ ] Add example eval YAML with skills for baseline testing

---

## 8. Backward Compatibility

- **No breaking changes.** All existing code paths are preserved.
- Single-pass execution (without `--baseline`) is unchanged.
- `--baseline` is opt-in and off by default.
- JSON schema extensions use `omitempty` tags, so old clients ignore new fields.
- Exit code behavior only applies when `--baseline` is used.

---

## 9. Future Enhancements (Out of Scope)

1. **Parallel baseline runs** — Run both passes concurrently (requires careful workspace isolation)
2. **Baseline-specific graders** — Different graders/validators for skills-enabled vs. baseline
3. **Statistical significance testing** — P-values, confidence intervals
4. **Per-skill impact** — Isolate impact of individual skills (requires feature-flagging Copilot SDK)
5. **Cumulative skill impact** — Progressive skill-stripping analysis (skills 1+2, then 1, then 0)

---

## 10. References

- **Issue:** #194 — A/B Skill Impact Measurement
- **Related:** #39 (Multi-model execution, sequential pattern), #97 (Workspace isolation), #138 (Recommendation engine)
- **Related Decisions:**
  - 2026-02-15: Multi-model execution architecture (sequential pattern)
  - 2026-02-17: Workspace resource setup (shared across engines)
  - 2026-02-18: Lifecycle hooks implementation (hook execution model)
