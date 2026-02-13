# Grader Reference

Complete reference for all available grader types in waza.

## Overview

Graders evaluate skill execution and produce scores. Each grader returns:
- `score`: 0.0 to 1.0
- `passed`: boolean
- `message`: human-readable result
- `details`: additional metadata

## Inline vs Script Graders

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
  
  - type: regex
    name: format_check
    config:
      must_match:
        - "deployed to .+"
```

### Script Graders (in graders/ directory)

Best for complex, multi-criteria evaluation logic:

```
my-waza/
├── eval.yaml
├── tasks/
└── graders/
    └── quality_checker.py    # Complex custom logic
```

Reference in eval.yaml:
```yaml
graders:
  - type: script
    name: quality_checker
    config:
      script: graders/quality_checker.py
```

**When to use script graders:**
- Multi-criteria scoring (5+ checks)
- Domain-specific business logic
- Reusable across multiple evals
- Complex pattern matching or analysis
- Integration with external services

See the [code-explainer example](../examples/code-explainer/graders/explanation_quality.py) for a complete script grader implementation.

---

## Code Graders

### `code` - Assertion-Based Grader

Evaluates Python expressions against the execution context.

```yaml
- type: code
  name: my_grader
  config:
    assertions:
      - "len(output) > 0"
      - "'success' in output.lower()"
      - "len(errors) == 0"
```

**Available Context Variables:**
| Variable | Type | Description |
|----------|------|-------------|
| `output` | str | Final skill output |
| `outcome` | dict | Outcome state |
| `transcript` | list | Full execution transcript |
| `tool_calls` | list | Tool calls from transcript |
| `errors` | list | Errors from transcript |
| `duration_ms` | int | Execution duration |

**Available Functions:**
`len`, `any`, `all`, `str`, `int`, `float`, `bool`, `list`, `dict`, `re` (regex module)

**Scoring:** `passed_assertions / total_assertions`

**⚠️ Important:** Do NOT use generator expressions in assertions. They don't work with Python's `eval()` in restricted scope.

```yaml
# ❌ WRONG - generator expressions fail
assertions:
  - "any(kw in output for kw in ['azure', 'deploy'])"

# ✅ CORRECT - use explicit or chains
assertions:
  - "'azure' in output.lower() or 'deploy' in output.lower()"
```

---

### `regex` - Pattern Matching Grader

Matches output against regex patterns.

```yaml
- type: regex
  name: format_checker
  config:
    must_match:
      - "deployed to https?://.+"
      - "Resource group: .+"
    must_not_match:
      - "error|failed|exception"
      - "permission denied"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `must_match` | list[str] | Patterns that MUST appear |
| `must_not_match` | list[str] | Patterns that MUST NOT appear |

**Scoring:** `passed_checks / total_checks`

---

### `tool_calls` - Tool Usage Grader

Validates which tools were called and how.

```yaml
- type: tool_calls
  name: tool_validator
  config:
    required:
      - pattern: "azd up"
      - pattern: "git commit"
    forbidden:
      - pattern: "rm -rf"
      - pattern: "sudo"
    max_calls: 20
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `required` | list | Patterns that MUST appear in tool calls |
| `forbidden` | list | Patterns that MUST NOT appear |
| `max_calls` | int | Maximum allowed tool calls |

---

### `script` - External Script Grader

Runs a custom Python script for complex validation.

```yaml
- type: script
  name: custom_logic
  config:
    script: graders/my_grader.py
```

**Script Format:**
```python
#!/usr/bin/env python3
import json
import sys

def grade(context: dict) -> dict:
    output = context.get("output", "")
    
    # Your custom logic here
    score = 1.0 if "success" in output else 0.0
    
    return {
        "score": score,
        "passed": score >= 0.5,
        "message": "Custom grading complete",
        "details": {"custom_field": "value"}
    }

if __name__ == "__main__":
    context = json.load(sys.stdin)
    print(json.dumps(grade(context)))
```

---

## LLM Graders

### `llm` - LLM-as-Judge Grader

Uses an AI model to evaluate quality.

```yaml
- type: llm
  name: quality_judge
  model: gpt-4o-mini
  rubric: |
    Score the skill execution from 1-5:
    
    1. Correctness: Did it accomplish the task?
    2. Completeness: Were all requirements addressed?
    3. Quality: Was the approach appropriate?
    
    Return JSON: {"score": N, "reasoning": "...", "passed": true/false}
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `model` | str | Model to use (default: gpt-4o-mini) |
| `rubric` | str | Evaluation rubric (inline or file path) |
| `threshold` | float | Pass threshold (default: 0.75) |

**Score Normalization:** Raw scores 1-5 are normalized to 0-1:
- Score 1 → 0.0
- Score 3 → 0.5
- Score 5 → 1.0

---

### `llm_comparison` - Reference Comparison Grader

Compares output against a reference using LLM.

```yaml
- type: llm_comparison
  name: reference_check
  model: gpt-4o-mini
  config:
    reference: |
      Expected output should include:
      - Confirmation of deployment
      - URL of deployed resource
      - Next steps for the user
```

---

## Human Graders

### `human` - Manual Review Grader

Marks tasks for human review.

```yaml
- type: human
  name: expert_review
  config:
    instructions: "Review for security best practices"
    criteria:
      - "Uses managed identity"
      - "No hardcoded secrets"
      - "Follows least privilege"
```

**Output:** Returns `pending` status until human submits review.

---

### `human_calibration` - Calibration Grader

Collects human labels to calibrate LLM graders.

```yaml
- type: human_calibration
  name: calibrate_quality
  config:
    calibrate_grader: quality_judge
```

---

## Behavior Graders

### `behavior` - Agent Behavior Validation

Validates agent behavior patterns like tool call counts, token usage, and execution duration.

```yaml
- type: behavior
  name: efficiency_check
  config:
    max_tool_calls: 20
    max_tokens: 10000
    max_duration_ms: 300000
    required_tools:
      - "bash"
      - "view"
    forbidden_tools:
      - "rm -rf"
      - "sudo"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `max_tool_calls` | int | Maximum allowed tool calls |
| `max_tokens` | int | Maximum token usage allowed |
| `max_duration_ms` | int64 | Maximum execution time in milliseconds |
| `required_tools` | list[str] | Tool names (exact matches) that MUST be called |
| `forbidden_tools` | list[str] | Tool names (exact matches) that MUST NOT be called |

**Note:** `required_tools` and `forbidden_tools` use exact string matching on tool names; patterns, wildcards, or regular expressions are not supported.

**Scoring:** Composite score based on all behavior constraints passed/failed.

---

### `action_sequence` - Tool Call Sequence Validation

Validates that the agent's tool calls match an expected action sequence. Supports three matching modes and calculates precision, recall, and F1 scores.

```yaml
- type: action_sequence
  name: deployment_workflow
  config:
    matching_mode: in_order_match
    expected_actions:
      - "bash"
      - "edit"
      - "bash"
      - "report_progress"
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `matching_mode` | string | How to match sequences (see modes below) |
| `expected_actions` | list[str] | List of expected tool names in sequence |

**Matching Modes:**

1. **`exact_match`** - Perfect match required
   - Actual sequence must match expected sequence exactly
   - Same length, same order, same tools
   - Example: Expected `["bash", "edit"]` only matches actual `["bash", "edit"]`

2. **`in_order_match`** - Actions must appear in order
   - All expected actions must appear in actual sequence
   - Can have extra actions between expected ones
   - Order must be preserved
   - Example: Expected `["bash", "edit"]` matches actual `["bash", "view", "edit", "report_progress"]`

3. **`any_order_match`** - All actions present regardless of order
   - All expected actions must appear in actual sequence
   - Order doesn't matter
   - Frequency must match (if expected has 2x "bash", actual must have at least 2x "bash")
   - Example: Expected `["edit", "bash"]` matches actual `["bash", "view", "edit"]`

**Scoring:**

The grader calculates three metrics:
- **Precision**: `true_positives / len(actual_actions)` - What fraction of actual actions were expected?
- **Recall**: `true_positives / len(expected_actions)` - What fraction of expected actions were performed?
- **F1 Score**: `2 * precision * recall / (precision + recall)` - Harmonic mean (used as the final score)

The `passed` field is based on the matching mode constraint, while the `score` field always uses F1.

**Example Use Cases:**

```yaml
# Ensure exact workflow for reproducible demos
- type: action_sequence
  name: demo_script
  config:
    matching_mode: exact_match
    expected_actions: ["bash", "view", "edit", "bash", "report_progress"]

# Verify key steps happen in order (allows flexibility)
- type: action_sequence
  name: deployment_flow
  config:
    matching_mode: in_order_match
    expected_actions: ["bash", "edit", "report_progress"]

# Check that required tools were used (any order)
- type: action_sequence
  name: required_tools
  config:
    matching_mode: any_order_match
    expected_actions: ["bash", "view", "edit"]
```

---

## LLM-as-Judge Graders

### `prompt` - LLM-Based Evaluation

Uses a language model to evaluate skill execution quality based on a rubric. This grader follows the Azure ML evaluator pattern for LLM-as-judge evaluation.

> **Note:** This grader requires implementation and is currently planned for a future release.

```yaml
- type: prompt
  name: quality_judge
  config:
    model: gpt-4o-mini
    rubric: |
      Evaluate the code explanation on these criteria:
      
      1. **Correctness** (1-5): Is the explanation technically accurate?
      2. **Completeness** (1-5): Are all key concepts covered?
      3. **Clarity** (1-5): Is it easy to understand?
      
      Provide:
      - A score for each criterion (1-5)
      - Overall assessment (1-5)
      - Brief reasoning
      
      Return JSON:
      {
        "correctness": <1-5>,
        "completeness": <1-5>,
        "clarity": <1-5>,
        "overall_score": <1-5>,
        "reasoning": "...",
        "passed": <true/false>
      }
    threshold: 0.75
    score_type: normalized
    response_format: json
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `model` | string | Model to use for evaluation (default: gpt-4o-mini) |
| `rubric` | string | Evaluation rubric (inline text or file path) |
| `threshold` | float | Pass threshold (default: 0.75) |
| `score_type` | string | How to interpret scores: `normalized` (1-5 → 0-1) or `raw` |
| `response_format` | string | Expected response format: `json` or `text` |

**How Rubrics Work:**

A rubric is a structured evaluation criteria that guides the LLM's assessment:

1. **Define Criteria**: List specific aspects to evaluate (correctness, completeness, clarity, etc.)
2. **Rating Scale**: Specify the scale (typically 1-5 or 1-10)
3. **Guidelines**: Describe what each rating level means
4. **Output Format**: Request structured output (JSON preferred) with scores and reasoning

**Example: Multi-Criteria Rubric**

```yaml
- type: prompt
  name: comprehensive_quality
  config:
    model: gpt-4o
    rubric: |
      Evaluate the agent's performance:
      
      **Criteria:**
      1. Task Completion (1-5): Did the agent accomplish the stated goal?
         - 1: Failed completely
         - 3: Partially completed
         - 5: Fully completed with excellence
      
      2. Approach Quality (1-5): Was the approach appropriate and efficient?
         - 1: Poor approach with significant issues
         - 3: Adequate but could be improved
         - 5: Excellent, optimal approach
      
      3. Code Quality (1-5): Is the code well-structured and maintainable?
         - 1: Poor structure, hard to maintain
         - 3: Acceptable quality
         - 5: Excellent quality, follows best practices
      
      **Output Format (JSON):**
      {
        "task_completion": <score>,
        "approach_quality": <score>,
        "code_quality": <score>,
        "overall_score": <average of above>,
        "reasoning": "<2-3 sentence explanation>",
        "passed": <true if overall_score >= 3.5>
      }
      
      Think step-by-step and provide honest, critical evaluation.
    threshold: 0.7
    score_type: normalized
```

**Writing Custom Rubrics:**

Follow these best practices:

1. **Be Specific**: Define exactly what you're evaluating
2. **Use Clear Scales**: Explain what each rating means
3. **Request Reasoning**: Ask for chain-of-thought explanation
4. **Structured Output**: Use JSON for reliable parsing
5. **Include Pass Threshold**: Make the LLM decide pass/fail based on criteria
6. **Avoid Ambiguity**: Be explicit about edge cases

**Common Rubric Patterns:**

```yaml
# Binary pass/fail evaluation
rubric: |
  Does the output meet these requirements?
  1. Contains user authentication
  2. Follows security best practices
  3. Includes error handling
  
  Return JSON: {"score": 1 or 0, "passed": true/false, "reasoning": "..."}

# Comparative evaluation
rubric: |
  Compare the agent's solution to this reference approach:
  [reference description]
  
  Rate similarity (1-5) and quality improvement (1-5).
  Return JSON with scores and reasoning.

# Style compliance check
rubric: |
  Evaluate code style compliance:
  - Naming conventions
  - Documentation completeness
  - Code organization
  
  Return JSON with per-criterion scores and overall assessment.
```

**Shipped Rubric Templates:**

> **Note:** Rubric templates will be provided in a future release. These will include pre-built rubrics for:
> - Code quality assessment
> - Security best practices
> - Documentation completeness
> - API design quality
> - Test coverage evaluation

---

## Azure ML Evaluator Integration

Waza's grader system is designed to be compatible with [Azure AI Evaluation SDK](https://learn.microsoft.com/azure/ai-studio/how-to/develop/evaluate-sdk) patterns. The `prompt` grader specifically implements the LLM-as-judge pattern used by Azure ML evaluators.

### Supported Azure ML Evaluator Patterns

The following Azure ML evaluator types map to Waza graders:

| Azure ML Evaluator | Waza Grader | Description |
|-------------------|-------------|-------------|
| `RelevanceEvaluator` | `prompt` | Uses LLM to judge response relevance |
| `CoherenceEvaluator` | `prompt` | Evaluates logical flow and coherence |
| `FluencyEvaluator` | `prompt` | Assesses natural language quality |
| `GroundednessEvaluator` | `prompt` | Checks factual grounding |
| `ContentSafetyEvaluator` | `prompt` | Validates content safety |
| Custom evaluators | `prompt` | Any custom LLM-based evaluator |

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
    rubric: |
      Evaluate how relevant the agent's response is to the user's query.
      
      Query: [Available in the test case prompt; the agent's response is provided as 'output']
      
      Rate relevance (1-5):
      1 - Completely irrelevant
      2 - Slightly relevant
      3 - Moderately relevant
      4 - Very relevant
      5 - Perfectly relevant and comprehensive
      
      Return JSON: {
        "relevance_score": <1-5>,
        "reasoning": "<explanation>",
        "passed": <true if score >= 4>
      }
    threshold: 0.75
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
    rubric: |
      Evaluate the code for security vulnerabilities:
      
      Criteria:
      1. Input Validation: Are inputs properly validated?
      2. Authentication: Is auth implemented correctly?
      3. Data Protection: Is sensitive data protected?
      4. Error Handling: Are errors handled securely?
      
      For each criterion, rate 1-5 and provide specific findings.
      
      Return JSON: {
        "input_validation": <score>,
        "authentication": <score>,
        "data_protection": <score>,
        "error_handling": <score>,
        "overall_score": <average>,
        "findings": ["<issue 1>", "<issue 2>", ...],
        "passed": <true if overall >= 4>
      }
    threshold: 0.8
```

### Using Azure ML Prompt Flow Templates

Azure ML Prompt Flow `.prompty` files can be adapted to Waza rubrics:

1. **Extract the system prompt** from the `.prompty` file
2. **Convert to YAML rubric** in your grader config
3. **Map inputs** (context variables are available: `output`, `transcript`, `tool_calls`, etc.)
4. **Preserve scoring logic** from the original evaluator

**Example Conversion:**

```prompty
---
name: CodeQualityEvaluator
description: Evaluates code quality
inputs:
  code: string
outputs:
  score: integer
  reasoning: string
---
system:
Evaluate the code quality on a scale of 1-5...
[evaluation criteria]
```

Becomes:

```yaml
- type: prompt
  name: code_quality
  config:
    rubric: |
      Evaluate the code quality on a scale of 1-5...
      [evaluation criteria - copied from .prompty]
      
      Return JSON: {"score": <1-5>, "reasoning": "..."}
```

### Creating Custom LLM-as-Judge Graders

To create graders that match Azure ML evaluator patterns:

1. **Define clear criteria**: What aspects are you evaluating?
2. **Use consistent scales**: 1-5 or 1-10 (Waza normalizes to 0-1)
3. **Request chain-of-thought**: Ask the LLM to explain its reasoning
4. **Structure output**: Use JSON for reliable parsing
5. **Set appropriate thresholds**: Define what "passing" means for your criteria

**Template:**

```yaml
- type: prompt
  name: my_custom_evaluator
  config:
    model: gpt-4o-mini
    rubric: |
      Evaluate [what you're assessing] based on:
      
      1. [Criterion 1] (1-5): [Description]
      2. [Criterion 2] (1-5): [Description]
      3. [Criterion 3] (1-5): [Description]
      
      For each criterion:
      - Consider [specific aspects]
      - Rate honestly and critically
      - Provide specific examples
      
      Return JSON: {
        "criterion1_score": <1-5>,
        "criterion2_score": <1-5>,
        "criterion3_score": <1-5>,
        "overall_score": <average>,
        "reasoning": "<detailed explanation>",
        "passed": <true/false based on threshold>
      }
      
      Context available:
      - output: The agent's final response
      - tool_calls: List of tools the agent used
      - duration_ms: Execution time
    threshold: 0.7
    score_type: normalized
```

---

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

---

## Grader Weights

When multiple graders are used, results are combined:

```yaml
graders:
  - type: code
    name: basic_check
    # Default weight: 1.0
  
  - type: llm
    name: quality_check
    # Default weight: 1.0
```

**Final Score:** Average of all grader scores (weighted if specified)

---

## Creating Custom Graders

Extend the `Grader` base class:

```python
from waza.graders.base import Grader, GraderContext, GraderType, GraderRegistry
from waza.schemas.results import GraderResult

@GraderRegistry.register("my_custom")
class MyCustomGrader(Grader):
    @property
    def grader_type(self) -> GraderType:
        return GraderType.CODE
    
    def grade(self, context: GraderContext) -> GraderResult:
        # Your logic here
        return GraderResult(
            name=self.name,
            type=self.grader_type.value,
            score=1.0,
            passed=True,
            message="Custom grading complete",
        )
```

Then use in eval.yaml:
```yaml
graders:
  - type: my_custom
    name: special_check
```
