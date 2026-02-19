# Waza Demo Guide

**Comprehensive walk-through for demonstrating waza's capabilities.**

This guide provides step-by-step instructions for 9 practical demonstrations covering all major waza features. Each demo is self-contained and can be run independently.

---

## Quick Setup

Before any demo, ensure waza is installed:

```bash
# Option A: Binary install (recommended)
curl -fsSL https://raw.githubusercontent.com/spboyer/waza/main/install.sh | bash

# Option B: Build from source
cd /path/to/waza
make build

# Verify installation
waza run --help
```

---

## Demo 1: Quick Start Demo (2 min)

**What it shows:** How fast you can install waza and run your first evaluation.

### Setup

- **Location:** `examples/code-explainer/`
- **Prerequisite:** waza binary built
- **Model:** Uses mock executor (no API keys needed for demo)

### Commands

```bash
# Navigate to project root
cd /path/to/waza

# Show the example structure
echo "📁 Example directory structure:"
tree examples/code-explainer/ -L 2

# Run the evaluation with verbose output
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -v

# Save results to JSON for later analysis
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -o results.json

# Show the results
echo "📊 Results saved to results.json"
cat results.json | jq '.summary'
```

### Expected Output

- Real-time task execution with progress indicators
- Summary showing:
  - Number of tasks run
  - Overall score (0.0-1.0)
  - Metrics breakdown (task_completion, trigger_accuracy, behavior_quality)
  - Pass/fail per task
- JSON results file with full execution details

### Talking Points

1. **Single Binary:** "Everything you need is in one command. No Python venv, no package management."
2. **Fast Iteration:** "Run evals in seconds with mock executor, then switch to real models."
3. **Reproducible:** "Same eval.yaml runs the same way everywhere—CI, local, your teammate's machine."
4. **Structured Output:** "JSON results let you integrate with dashboards, reports, or CI/CD."

---

## Demo 2: Grader Showcase Demo (5 min)

**What it shows:** The five grader types in action—how to validate different aspects of agent behavior.

### Setup

- **Location:** `examples/grader-showcase/`
- **Skills Demonstrated:** Each task shows one grader type
  - `code-task.yaml` → Code grader (Python assertions)
  - `regex-task.yaml` → Regex grader (pattern matching)
  - `file-task.yaml` → File grader (file operations)
  - `behavior-task.yaml` → Behavior grader (efficiency constraints)
  - `action-sequence-task.yaml` → Action sequence grader (tool calls)

### Commands

```bash
# Show the grader-showcase eval structure
echo "📋 Grader types showcase:"
ls -lh examples/grader-showcase/tasks/

# Run the showcase with verbose output to see each grader in action
./waza-bin run examples/grader-showcase/eval.yaml \
  --context-dir examples/grader-showcase/fixtures \
  -v

# Run just the regex task to focus on one grader type
./waza-bin run examples/grader-showcase/eval.yaml \
  --context-dir examples/grader-showcase/fixtures \
  --task "regex-*" \
  -v

# Show the eval.yaml to highlight grader configuration
echo "🔍 Global graders in eval.yaml:"
grep -A 8 "^graders:" examples/grader-showcase/eval.yaml

# Show one task file to explain task-specific graders
echo "📄 Example task with graders:"
head -40 examples/grader-showcase/tasks/code-task.yaml
```

### Expected Output

- Each task runs with clear pass/fail status
- Grader output explains why validation passed/failed
- Summary showing:
  - Grader pass rates
  - Individual grader scores
  - Overall task score

### Talking Points

1. **Flexible Validation:** "Code graders for logic, regex for patterns, files for outputs, behavior for efficiency."
2. **Composable:** "Mix and match graders on each task. Use global graders for all tasks, task-specific for edge cases."
3. **Clear Feedback:** "Each grader tells you exactly what passed/failed and why."
4. **Production-Ready:** "These graders let you enforce quality standards in CI/CD pipelines."

### Deep Dive: Show Individual Grader Configs

```bash
# Show each task's grader config
echo "=== Code Grader Example ==="
grep -A 10 "^graders:" examples/grader-showcase/tasks/code-task.yaml

echo ""
echo "=== Regex Grader Example ==="
grep -A 10 "^graders:" examples/grader-showcase/tasks/regex-task.yaml

echo ""
echo "=== File Grader Example ==="
grep -A 10 "^graders:" examples/grader-showcase/tasks/file-task.yaml
```

---

## Demo 3: Token Management Demo (3 min)

**What it shows:** How to manage token budgets for skills using waza tokens commands.

### Setup

- **Location:** Any directory with markdown files (skill docs, eval configs)
- **Example:** `examples/code-explainer/SKILL.md`
- **Tools:** `waza tokens count`, `check`, `suggest`

### Commands

```bash
# Count tokens in a skill file
echo "📊 Count tokens in a skill:"
./waza-bin tokens count examples/code-explainer/SKILL.md

# Count tokens in entire directory
echo ""
echo "📊 Count tokens in all markdown files:"
./waza-bin tokens count examples/

# Count with JSON output for reporting
echo ""
echo "📊 Token count as JSON:"
./waza-bin tokens count examples/ --format json | jq '.'

# Check files against token limits (500 token limit per file)
echo ""
echo "✅ Check against token limits:"
./waza-bin tokens check examples/ --limit 500

# Get optimization suggestions
echo ""
echo "💡 Get optimization suggestions:"
./waza-bin tokens suggest examples/code-explainer/SKILL.md

# Compare tokens between git commits
echo ""
echo "🔄 Compare tokens between versions:"
./waza-bin tokens compare HEAD~1 HEAD -- examples/code-explainer/SKILL.md
```

### Expected Output

- **count:** Table with file paths and token counts
- **check:** Green checkmarks for under-limit files, warnings for over-limit
- **suggest:** Specific recommendations for shortening content while preserving meaning
- **compare:** Diff showing token count changes between versions

### Talking Points

1. **Budget Enforcement:** "Know exactly how many tokens each skill uses—critical for Azure OpenAI cost management."
2. **Continuous Monitoring:** "Track token changes across commits. Catch bloat before it hits production."
3. **Optimization Guidance:** "Get LLM-powered suggestions for trimming without losing functionality."
4. **CI Integration:** "Add token checks to your PR workflows—fail the build if skills get too large."

---

## Demo 4: Sensei Dev Loop Demo (5 min)

**What it shows:** Iterative skill development using `waza dev` with real-time compliance scoring.

### Setup

- **Location:** Any eval directory (e.g., `examples/code-explainer/`)
- **Process:** Score → Review → Fix → Score again (iterative loop)
- **Note:** Demo uses mock executor; in practice, use real models

### Commands

```bash
# Start the development loop with scoring
echo "🎯 Starting Sensei development loop..."
./waza-bin dev examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  --model claude-sonnet-4-20250514

# Explanation of what the loop shows:
echo ""
echo "📋 The dev loop provides:"
echo "  1. Initial compliance score (Low/Medium/Medium-High/High)"
echo "  2. Specific issues found (3-5 actionable feedback items)"
echo "  3. Improvement suggestions"
echo "  4. Option to re-run after you make changes"
echo "  5. Convergence when score reaches target"
```

### Expected Output

- **Iteration 1:**
  - Compliance score shown (e.g., "Medium - 65%")
  - List of 3-5 specific issues:
    - Missing docstrings
    - Unclear error handling
    - Incomplete examples
  - LLM-powered suggestions for each issue

- **After you make edits:**
  - Re-run `waza dev` with updated files
  - Score improves (e.g., "Medium-High - 78%")
  - Remaining issues refined

### Talking Points

1. **Guided Development:** "Not just pass/fail—get detailed feedback on what to improve."
2. **Real-Time Iteration:** "See your score change as you update your skill."
3. **LLM-Powered Insights:** "Claude/GPT reviews your skill like a peer would."
4. **Target-Driven:** "Set a score target (e.g., 'High') and loop until you reach it."

### Optional: Show Score Breakdown

```bash
# Explain the scoring rubric
echo "📊 Compliance Score Breakdown:"
echo "  Low (0-40%):          Needs major revisions"
echo "  Medium (41-65%):      Functional, but missing key details"
echo "  Medium-High (66-85%): Good—minor improvements needed"
echo "  High (86-100%):       Excellent—ready for production"

# Show what triggers each score level
echo ""
echo "✅ Factors for High score:"
echo "  • Complete SKILL.md with all sections"
echo "  • Clear trigger patterns"
echo "  • Input/output examples"
echo "  • Error handling documented"
echo "  • Eval tests passing"
```

---

## Demo 5: CI/CD Integration Demo (3 min)

**What it shows:** How waza integrates into GitHub Actions pipelines with exit codes and reporting.

### Setup

- **Location:** `.github/workflows/` (example patterns in `examples/ci/`)
- **Concepts:**
  - Exit codes for pass/fail decisions
  - Matrix testing across models
  - Result comparison and reporting

### Commands

```bash
# Show the CI workflow example
echo "🔄 Example CI workflow:"
cat examples/ci/README.md | head -60

# Explain exit codes
echo ""
echo "🚦 Exit Codes (for CI decisions):"
echo "  0: All tests passed → ✅ Merge allowed"
echo "  1: One or more tests failed → ❌ Block merge"
echo "  2: Configuration/runtime error → ⚠️ Check logs"

# Demonstrate exit code behavior
echo ""
echo "📌 Example: Run eval and check exit code"
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures
echo "Exit code: $?"

# Show how to use in GitHub Actions
echo ""
echo "🐙 GitHub Actions example:"
cat << 'EOF'
jobs:
  test-skill:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Run waza evaluation
        run: |
          waza run examples/code-explainer/eval.yaml -v
          # Exit code automatically fails the workflow if tests fail
      - name: Compare results
        if: always()
        run: waza compare results-old.json results-new.json
EOF

# Show result comparison
echo ""
echo "📊 Compare results across models:"
./waza-bin compare results.json results.json \
  --format summary
```

### Expected Output

- Workflow file showing practical CI integration patterns
- Exit code explanation with examples
- Result comparison showing pass/fail differences
- Model-to-model performance deltas

### Talking Points

1. **Automated Gates:** "Fail PRs automatically if evals don't pass—no manual review needed."
2. **Matrix Testing:** "Test your skill against GPT-4o, Claude, and others in parallel."
3. **Actionable Results:** "Compare models side-by-side to find which performs best."
4. **Reproducible Pipelines:** "Same eval runs consistently across dev, staging, production."

---

## Demo 6: Multi-Skill Orchestration Demo (5 min)

**What it shows:** Running evals across multiple skills with dependencies and cross-skill validation.

### Setup

- **Concepts:** `skill_directories`, `required_skills`, `skill_invocation` grader
- **Example:** Code-explainer skill using utility functions from another skill
- **Note:** Advanced feature—start here only after mastering basic demos

### Commands

```bash
# Show how multi-skill eval is configured
echo "📚 Multi-skill orchestration in eval.yaml:"
cat << 'EOF'
config:
  # Directory containing multiple skills
  skill_directories:
    - ./skills/
    - ../shared-skills/
  
  # Skills required before running main eval
  required_skills:
    - helpers
    - validators
  
  # Automatically invoke when conditions met
  skill_invocation:
    triggers:
      - on_error: invoke_debugger
      - on_slow_response: invoke_optimizer
EOF

# Show task with skill_invocation grader
echo ""
echo "🎯 Task-level skill invocation:"
cat << 'EOF'
tasks:
  - id: complex-task
    prompt: "Explain this code"
    graders:
      - type: skill_invocation
        name: helper_validation
        config:
          required_skills: [helpers]
          assertions:
            - "helper_called"
            - "helper_returned_valid_result"
EOF

# Run example showing skill coordination
echo ""
echo "⚙️  Running multi-skill evaluation:"
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -v

# Show invocation patterns in results
echo ""
echo "📋 Check which skills were invoked:"
cat results.json | jq '.tasks[].skill_invocations'
```

### Expected Output

- Eval.yaml showing `skill_directories` and `required_skills` config
- Tasks showing `skill_invocation` grader usage
- Results with `skill_invocations` field showing which skills were called
- Sequence of skill invocations in transcript

### Talking Points

1. **Composable Skills:** "Reuse common functionality across multiple skills."
2. **Automatic Coordination:** "Waza orchestrates skill sequencing based on task needs."
3. **Cross-Skill Validation:** "Verify that skills work correctly together."
4. **Real-World Complexity:** "Mirror how agents actually invoke multiple skills in sequence."

### Show Real-World Example

```bash
echo "🌍 Real-world scenario:"
echo "  Task: 'Deploy Azure function'"
echo "  Required skills:"
echo "    1. azure-functions (main)"
echo "    2. azure-cli (utility)"
echo "    3. error-handler (error recovery)"
echo ""
echo "  Waza ensures all skills are available, invokes them correctly,"
echo "  and validates the full orchestration end-to-end."
```

---

## Demo 7: Cross-Model Comparison Demo (3 min)

**What it shows:** Running the same eval against multiple AI models and comparing results.

### Setup

- **Models:** Claude Sonnet, GPT-4o, Claude Opus (or whichever you have API keys for)
- **Inputs:** Multiple result JSON files from separate runs
- **Tool:** `waza compare`

### Commands

```bash
# Run evaluation with different models (save results separately)
echo "🤖 Running evals with multiple models..."

# Run with Claude Sonnet (mocked for demo)
echo ""
echo "Running with mock executor (represents Sonnet)..."
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -o results-sonnet.json

# Simulate results from other models (in real scenario, change model config)
cp results-sonnet.json results-gpt4.json
cp results-sonnet.json results-opus.json

# Compare results across models
echo ""
echo "📊 Comparing results across models:"
./waza-bin compare \
  results-sonnet.json \
  results-gpt4.json \
  results-opus.json

# Show detailed comparison with formatting
echo ""
echo "📋 Detailed model comparison:"
./waza-bin compare \
  results-sonnet.json \
  results-gpt4.json \
  --format detailed

# Export comparison to JSON for reporting
echo ""
echo "📊 Save comparison as JSON:"
./waza-bin compare \
  results-sonnet.json \
  results-gpt4.json \
  --output comparison.json

# Show the comparison
cat comparison.json | jq '.model_deltas'
```

### Expected Output

- Summary table showing per-model scores
- Deltas showing differences (e.g., "+5% on task_completion with GPT-4o")
- Grader-by-grader breakdown per model
- Task-level pass/fail rates per model
- Recommendations (e.g., "GPT-4o best for behavior_quality")

### Talking Points

1. **Model-Aware Strategy:** "Know which model is best for your use case."
2. **Regression Detection:** "Compare new model versions—catch performance drops."
3. **Resource Optimization:** "Maybe cheaper Claude performs as well as GPT-4o."
4. **Data-Driven Decisions:** "Let evals guide which model to use in production."

### Show Real Scenario

```bash
echo "💼 Real-world use case:"
echo ""
echo "Scenario: Choosing a model for your production skill"
echo ""
echo "Results Summary:"
echo "  Claude Sonnet:  Score 0.85 | Cost: ~$0.01/task"
echo "  Claude Opus:    Score 0.92 | Cost: ~$0.05/task"
echo "  GPT-4o:         Score 0.90 | Cost: ~$0.03/task"
echo ""
echo "Decision:"
echo "  ✅ Use Claude Sonnet in prod (best cost/quality ratio)"
echo "  🔄 Monitor Claude Opus quarterly (check for improvements)"
echo "  ❌ Skip GPT-4o for now (not worth 5x cost for 5% quality gain)"
```

---

## Demo 8: Prompt Grader Use Cases (5 min)

**What it shows:** How to use the `prompt` grader for qualitative checks that are hard to express with regex/code assertions.

### Setup

- **Location:** A temporary demo folder (all files included in the commands)
- **Prerequisite:** waza binary installed and Copilot authenticated (the prompt grader runs an LLM judge)
- **Use Cases:** explanation quality, requirement adherence, and output completeness checks

### Commands

```bash
# Navigate to a temporary working directory
cd /<working-directory>

# Create a small demo eval
mkdir -p waza-prompt-demo/{tasks,fixtures}

cat > waza-prompt-demo/fixtures/sample.py << 'EOF'
def normalize_price(cents):
    return cents / 100.0
EOF

cat > waza-prompt-demo/tasks/prompt-quality.yaml << 'EOF'
id: prompt-quality-001
name: Prompt Grader Quality Demo
description: Demonstrates qualitative grading with LLM-as-judge

inputs:
  prompt: "Explain what sample.py does, include one example, and mention edge cases"
  files:
    - path: sample.py

graders:
  - type: prompt
    name: explanation_quality
    config:
      model: gpt-4o-mini
      continue_session: true
      prompt: |-
        Evaluate the assistant's response for:
        1) technical correctness,
        2) inclusion of at least one concrete example,
        3) mention of at least one edge case.

        If all checks pass, call set_waza_grade_pass with a short reason.
        If any check fails, call set_waza_grade_fail with the missing criteria.
EOF

cat > waza-prompt-demo/eval.yaml << 'EOF'
name: prompt-grader-demo
description: Demo for prompt grader use cases
skill: general-coding-assistant
version: "1.0"

config:
  trials_per_task: 1
  timeout_seconds: 180
  parallel: false
  executor: copilot-sdk
  model: claude-sonnet-4-20250514

metrics:
  - name: quality
    weight: 1.0
    threshold: 0.8

tasks:
  - "tasks/*.yaml"
EOF

# Run the demo
/path/to/waza/waza-bin run waza-prompt-demo/eval.yaml \
  --context-dir waza-prompt-demo/fixtures \
  -v
```

### Expected Output

- Prompt grader appears in task results (e.g., `explanation_quality`)
- Pass/fail reflects whether the judge called `set_waza_grade_pass` or `set_waza_grade_fail`
- Feedback includes grader reasoning when checks fail
- Overall task score changes based on prompt grader outcome

### Talking Points

1. **Qualitative Validation:** "Use prompt graders when you need judgment-based checks, not just exact string matches."
2. **Policy Enforcement:** "Prompt graders are great for rubric-like requirements: style, completeness, safety, and adherence."
3. **Composable:** "Combine prompt graders with code/regex/file graders for both hard constraints and soft quality criteria."
4. **Model Flexibility:** "Run the task model and judge model independently (`config.model` inside the prompt grader)."
5. **Explicit Verdicts:** "Your judge prompt should always call `set_waza_grade_pass` or `set_waza_grade_fail` with reasoning."

---

## Demo 9: Web Dashboard — Visualizing Results (5 min)

**What it shows:** The interactive web dashboard for exploring eval results, comparing runs, and tracking trends over time.

### Setup

- **Prerequisite:** Run an evaluation first to generate results
- **Location:** `examples/code-explainer/`
- **Server:** HTTP dashboard auto-opens in browser at `http://localhost:3000`
- **Flags:** `--port` to customize port, `--results-dir` to specify results directory, `--no-browser` to skip auto-open

### Commands

```bash
# Step 1: Run an evaluation to generate results
echo "📊 Running evaluation to generate results..."
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -o results.json

# Step 2: Start the dashboard
echo ""
echo "🚀 Starting waza dashboard..."
./waza-bin serve

# Dashboard auto-opens in browser at http://localhost:3000
# Press Ctrl+C to stop the server

# Alternative: Run on custom port
echo ""
echo "🔧 Start on custom port 8080:"
./waza-bin serve --port 8080

# Alternative: Load results from different directory
echo ""
echo "📂 Load results from specific directory:"
./waza-bin serve --results-dir ./eval-results

# Alternative: Start without auto-opening browser
echo ""
echo "🖥️  Start without auto-opening browser:"
./waza-bin serve --no-browser
```

### Dashboard Overview

**KPI Cards (Top Section)**
- Total runs executed
- Overall pass rate (percentage)
- Average score across all runs

**Run Table (Main Section)**
- Sortable columns: Run ID, Date, Model, Overall Score, Pass Rate
- Click a row to view detailed results
- Filter by date range or status

### Talking Points

1. **Instant Visibility:** "The dashboard gives you instant visibility into skill quality across all eval runs."
2. **Side-by-Side Comparison:** "Compare models side-by-side to pick the best one for your skill."
3. **Trend Tracking:** "Track quality trends over time as you iterate on your skill."
4. **Task Breakdown:** "Drill into individual tasks to see which ones consistently fail."

### Expected Output

- Web server starts on port 3000
- Browser opens automatically to dashboard
- Shows KPI cards with summary metrics
- Run table with sortable columns
- Navigation menu for different views

### Variations

**View Task-Level Results**
```
Run detail view shows:
  - Individual task results (pass/fail indicators)
  - Score for each task
  - Grader-specific feedback
```

**Compare Multiple Runs**
```
Compare view shows:
  - Side-by-side model performance
  - Per-task differences
  - Score deltas and winner indicators
```

**Historical Trends**
```
Trends view shows:
  - Pass rate over time (line chart)
  - Score progression (area chart)
  - Model comparison trends
```

---

## Complete Demo Flow (20 min)

Run all demos in sequence for a comprehensive showcase:

```bash
echo "🎬 Starting complete waza demo flow..."

# 1. Quick Start (2 min)
echo "Part 1: Quick Start"
./waza-bin run examples/code-explainer/eval.yaml \
  --context-dir examples/code-explainer/fixtures \
  -v

# 2. Grader Showcase (5 min)
echo ""
echo "Part 2: Grader Types"
./waza-bin run examples/grader-showcase/eval.yaml \
  --context-dir examples/grader-showcase/fixtures \
  -v

# 3. Token Management (3 min)
echo ""
echo "Part 3: Token Management"
./waza-bin tokens count examples/ --format json

# 4. CI Integration (3 min)
echo ""
echo "Part 4: CI/CD Patterns"
cat examples/ci/README.md | head -40

# 5. Results Comparison (3 min)
echo ""
echo "Part 5: Cross-Model Comparison"
./waza-bin compare results-sonnet.json results-gpt4.json

echo ""
echo "✅ Demo complete!"
```

---

## Troubleshooting

### Binary not found
```bash
make build
./waza-bin run examples/code-explainer/eval.yaml --help
```

### Mock executor not working
Ensure eval.yaml contains:
```yaml
config:
  executor: mock
```

### Results not saved
Check that output path is writable:
```bash
./waza-bin run eval.yaml -o /tmp/results.json
cat /tmp/results.json
```

### Token commands failing
Ensure paths exist and contain markdown files:
```bash
./waza-bin tokens count examples/ --format json
```

---

## Advanced Topics (Beyond Basic Demos)

### Custom Graders

See [docs/GRADERS.md](./GRADERS.md) for implementing custom script graders.

### Parallel Execution

```bash
./waza-bin run eval.yaml --parallel --workers 8 -v
```

### Filtering Tasks

```bash
./waza-bin run eval.yaml --task "explain-*" --task "validate-*" -v
```

### Transcript Capture

```bash
./waza-bin run eval.yaml --transcript-dir ./transcripts/ -v
```

---

## See Also

- **[README.md](../README.md)** — Project overview and quick start
- **[TUTORIAL.md](./TUTORIAL.md)** — Writing evals from scratch
- **[GRADERS.md](./GRADERS.md)** — Complete grader reference
- **[DEMO-SCRIPT.md](../DEMO-SCRIPT.md)** — Presentation script for live demos
- **[examples/README.md](../examples/README.md)** — Example descriptions

