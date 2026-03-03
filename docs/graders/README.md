# Grader Reference

Complete reference for all available grader types in waza.

## Overview

Graders evaluate skill execution and produce scores. Each grader returns:
- `score`: 0.0 to 1.0
- `passed`: boolean
- `feedback`: human-readable result text
- `details`: additional metadata

## Grader Types

- [`action_sequence` - Tool Call Sequence Validation](action_sequence.md)
- [`behavior` - Agent Behavior Validation](behavior.md)
- [`code` - Assertion-Based Grader](code.md)
- [`diff` - Workspace File Comparison](diff.md)
- [`file` - File Existence & Content Grader](file.md)
- [`human` - Manual Review Grader (not implemented)](human.md)
- [`human_calibration` - Calibration Grader (not implemented)](human_calibration.md)
- [`json_schema` - JSON Schema Validation Grader](json_schema.md)
- [`llm` - LLM-as-Judge Grader (not implemented)](llm.md)
- [`llm_comparison` - Reference Comparison Grader (not implemented)](llm_comparison.md)
- [`program` - External Program Grader](program.md)
- [`prompt` - LLM-Based Evaluation](prompt.md)
- [`script` - External Script Grader (not implemented)](script.md)
- [`skill_invocation` - Skill Invocation Sequence Validation](skill_invocation.md)
- [`text` - Pattern Matching Grader](text.md)
- [`tool_calls` - Tool Usage Grader (not implemented)](tool_calls.md)
- [`tool_constraint` - Tool Usage Constraint Grader](tool_constraint.md)

## Inline vs Program Graders

Graders can be defined in two ways:

### Inline Graders (in eval.yaml or task files)

Best for simple validation logic that fits in YAML:

```yaml
graders:
  - type: code
    name: basic_check
    config:
      assertions:
        - "len(output) > 10"
        - "'success' in output.lower()"

  - type: text
    name: format_check
    config:
      regex_match:
        - "deployed to .+"
```

### Program Graders (external scripts)

Best for complex, multi-criteria evaluation logic:

```
my-skill/
├── eval.yaml
├── tasks/
└── graders/
    └── quality_checker.py    # Complex custom logic
```

Reference in eval.yaml:
```yaml
graders:
  - type: program
    name: quality_checker
    config:
      command: python3
      args: ["graders/quality_checker.py"]
```

**When to use program graders:**
- Multi-criteria scoring (5+ checks)
- Domain-specific business logic
- Reusable across multiple evals
- Complex pattern matching or analysis
- Integration with external services

The program grader passes agent output via stdin and the workspace directory via the `WAZA_WORKSPACE_DIR` environment variable. Exit code 0 means pass, non-zero means fail. See the [program grader docs](program.md) for details.

## Azure ML Evaluator Integration

Waza's grader system is designed to be compatible with [Azure AI Evaluation SDK](https://learn.microsoft.com/azure/ai-studio/how-to/develop/evaluate-sdk) patterns. The `prompt` grader specifically implements the LLM-as-judge pattern used by Azure ML evaluators.

### Supported Azure ML Evaluator Patterns

The following Azure ML evaluator types map to Waza graders:

| Azure ML Evaluator       | Waza Grader | Description                               |
| ------------------------ | ----------- | ----------------------------------------- |
| `RelevanceEvaluator`     | `prompt`    | Uses LLM to judge response relevance      |
| `CoherenceEvaluator`     | `prompt`    | Evaluates logical flow and coherence      |
| `FluencyEvaluator`       | `prompt`    | Assesses natural language quality         |
| `GroundednessEvaluator`  | `prompt`    | Checks factual grounding                  |
| `ContentSafetyEvaluator` | `prompt`    | Validates content safety                  |
| Custom evaluators        | `prompt`    | Any custom LLM-based evaluator            |

### Adapting Azure ML Evaluators as Waza Rubrics

You can adapt Azure ML evaluators to Waza by converting their evaluation criteria into rubrics:

**Example: Azure ML Relevance Evaluator → Waza Rubric**

```yaml
# Azure ML pattern
evaluator = RelevanceEvaluator(model_config)
result = evaluator(query=query, response=response)

# Equivalent Waza configuration
- type: prompt
  name: relevance_check
  config:
    model: gpt-4o-mini
    prompt: |
      Evaluate how relevant the agent's response is to the user's query.

      Query: [Available in the test case prompt; the agent's response is provided as 'output']

      Rate relevance (1-5):
      1 - Completely irrelevant
      2 - Slightly relevant
      3 - Moderately relevant
      4 - Very relevant
      5 - Perfectly relevant and comprehensive

      If the response scores 4 or above, call set_waza_grade_pass with a description and reason.
      Otherwise, call set_waza_grade_fail with a description and reason.
```

**Example: Custom Azure ML Evaluator → Waza Rubric**

```python
# Azure ML custom evaluator
from promptflow.core import Prompty

evaluator = Prompty.load("security_evaluator.prompty")

# Convert to Waza rubric
```

```yaml
- type: prompt
  name: security_check
  config:
    model: gpt-4o
    prompt: |
      Evaluate the code for security vulnerabilities:

      Criteria:
      1. Input Validation: Are inputs properly validated?
      2. Authentication: Is auth implemented correctly?
      3. Data Protection: Is sensitive data protected?
      4. Error Handling: Are errors handled securely?

      For each criterion, call set_waza_grade_pass if it is satisfied,
      or set_waza_grade_fail with specific findings if it is not.
```

### Using Azure ML Prompt Flow Templates

Azure ML Prompt Flow `.prompty` files can be adapted to Waza rubrics:

1. **Extract the system prompt** from the `.prompty` file
2. **Convert to YAML prompt** in your grader config
3. **Use tool calls** to signal pass/fail (call `set_waza_grade_pass` or `set_waza_grade_fail`)
4. **Preserve scoring logic** from the original evaluator

**Example Conversion:**

```prompty
name: CodeQualityEvaluator
description: Evaluates code quality
inputs:
  code: string
outputs:
  score: integer
  reasoning: string
system:
  Evaluate the code quality on a scale of 1-5...
  [evaluation criteria]
```

Becomes:

```yaml
- type: prompt
  name: code_quality
  config:
    prompt: |
      Evaluate the code quality on a scale of 1-5...
      [evaluation criteria - copied from .prompty]

      If the code scores 4 or above, call set_waza_grade_pass.
      Otherwise call set_waza_grade_fail with an explanation.
```

### Creating Custom LLM-as-Judge Graders

To create graders that match Azure ML evaluator patterns:

1. **Define clear criteria**: What aspects are you evaluating?
2. **Use tool calls**: Call `set_waza_grade_pass` or `set_waza_grade_fail` for each check
3. **Request chain-of-thought**: Ask the LLM to explain its reasoning in the `reason` field
4. **Use per-criterion calls for partial credit**: One pass/fail tool call per criterion gives partial scoring

**Template:**

```yaml
- type: prompt
  name: my_custom_evaluator
  config:
    model: gpt-4o-mini
    prompt: |
      Evaluate [what you're assessing] based on:

      1. [Criterion 1]: [Description]
      2. [Criterion 2]: [Description]
      3. [Criterion 3]: [Description]

      For each criterion:
      - Consider [specific aspects]
      - Rate honestly and critically
      - Provide specific examples

      For each criterion, call:
      - set_waza_grade_pass if the criterion is met (include description and reason)
      - set_waza_grade_fail if it is not (include description and reason)

      Make exactly one call per criterion (3 total calls).
```

## Task-Level Graders

You can also define graders per-task:

```yaml
# In task YAML
graders:
  - name: task_specific_check
    type: code
    assertions:
      - "specific_condition"
    weight: 0.5  # Weight within this task
```

## Grader Weights

When multiple graders are used, results are combined:

```yaml
graders:
  - type: code
    name: basic_check
    # Default weight: 1.0

  - type: text
    name: format_check
    # Default weight: 1.0
```

**Final Score:** Average of all grader scores (weighted if specified)

## Trigger Tests

Trigger tests measure whether a skill activates for the right prompts and stays
silent for the wrong ones. They run automatically when a `trigger_tests.yaml`
file exists alongside `eval.yaml`.

### File Format

```yaml
skill: code-explainer

should_trigger_prompts:
  - prompt: "Explain this code to me"
    reason: "Direct explanation request"        # optional, for documentation
    confidence: high                            # high (default) or medium

  - prompt: "I don't understand what this code is doing"
    confidence: medium

should_not_trigger_prompts:
  - prompt: "Write me a function to sort a list"
    reason: "Code writing request, not explanation"
    confidence: high

  - prompt: "Fix the bug in my code"
    confidence: medium
```

**Fields:**

| Field | Required | Description |
| -- | -- | --
| `skill` | yes | Skill name to check for invocation |
| `should_trigger_prompts` | at least one of the two prompt lists | Prompts where the skill should activate |
| `should_not_trigger_prompts` | at least one of the two prompt lists | Prompts where the skill should stay silent |
| `prompt` | yes | The test prompt text |
| `reason` | no | Human-readable explanation (not used in scoring) |
| `confidence` | no | `high` (default) or `medium` — controls scoring weight |

### Confidence Weighting

Each prompt's result is weighted by its confidence level:

- **`high`** (or omitted): weight **1.0** — a clear-cut case where the expected
  behavior is unambiguous.
- **`medium`**: weight **0.5** — an edge case or borderline prompt where the
  expected behavior is less certain.

This lets you include borderline prompts without letting them dominate the score.
For example a "medium" false positive penalizes accuracy half as much as a "high"
one.

### Metrics

Trigger tests produce standard classification metrics:

| Metric        | Description                                            |
| ------------- | ------------------------------------------------------ |
| **Accuracy**  | (TP + TN) / total                                     |
| **Precision** | TP / (TP + FP) — how often activation was correct     |
| **Recall**    | TP / (TP + FN) — how often it activated when it should have |
| **F1**        | Harmonic mean of precision and recall                 |
| **Errors**    | Prompts that failed to execute (counted as incorrect) |

### Using `trigger_accuracy` as a Metric

Add `trigger_accuracy` to the `metrics` section of your `eval.yaml` to set a
pass/fail threshold:

```yaml
metrics:
  - name: trigger_accuracy
    threshold: 0.9
    weight: 30
```

When configured, trigger accuracy is included in the benchmark outcome and the
run fails if accuracy falls below the threshold.

### Error Handling

When a prompt fails to execute (engine error), it counts as an incorrect
classification — a false negative for should-trigger prompts or a false positive
for should-not-trigger prompts. The error count is reported separately so you can
distinguish engine failures from genuine misclassifications.

## Creating Custom Graders

Implement the `Grader` interface in Go and register it in `internal/graders/grader.go`:

```go
// 1. Implement the Grader interface in internal/graders/
type myCustomGrader struct {
    name string
}

func (g *myCustomGrader) Name() string            { return g.name }
func (g *myCustomGrader) Kind() models.GraderKind { return "my_custom" }

func (g *myCustomGrader) Grade(ctx context.Context, gradingContext *Context) (*models.GraderResults, error) {
    // Your logic here
    return &models.GraderResults{
        Name:     g.name,
        Type:     "my_custom",
        Score:    1.0,
        Passed:   true,
        Feedback: "Custom grading complete",
    }, nil
}
```

```go
// 2. Add the new GraderKind constant in internal/models/outcome.go
GraderKindMyCustom GraderKind = "my_custom"
```

```go
// 3. Register in the Create function in internal/graders/grader.go
case models.GraderKindMyCustom:
    // decode params and create grader
```

Then use in eval.yaml:
```yaml
graders:
  - type: my_custom
    name: special_check
```
