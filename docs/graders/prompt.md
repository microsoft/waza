### `prompt` - LLM-Based Evaluation

Uses a language model to evaluate skill execution quality via an explicit grader prompt.

```yaml
- type: prompt
  name: quality_judge
  config:
    model: gpt-4o-mini
    continue_session: false
    prompt: |
      Evaluate whether the agent correctly completed the task.

      If all checks pass, call set_waza_grade_pass with a description and reason.
      If any check fails, call set_waza_grade_fail with a description and reason.
```

**Options:**
| Option | Type | Description |
|--------|------|-------------|
| `prompt` | string | **Required.** Grader instructions sent to the judge model |
| `model` | string | Model to use for the judge session |
| `continue_session` | bool | Reuse the task session context by session ID (default: `false`) |

**Timeout:**

Each judge send waits up to **120s** for the session to finish (reach `session.idle`). Long multi-turn judge sessions can need more, so the budget is overridable with the `WAZA_PROMPT_GRADER_TIMEOUT` environment variable. It accepts a Go duration (`5m`, `300s`) or a bare number of seconds (`300`); empty, invalid, zero, or negative values keep the 120s default.

```bash
WAZA_PROMPT_GRADER_TIMEOUT=5m waza run eval.yaml --context-dir fixtures
```

**Result semantics:**

- The judge should call `set_waza_grade_pass` and/or `set_waza_grade_fail`.
- Score is `passes / (passes + failures)`.
- `passed` is `true` only when there is at least one pass call and zero fail calls.
- If no pass/fail tool is called, score is `0.0` and the grader fails.

**Tool call contract:**

The prompt grader injects two tools:

- `set_waza_grade_pass`
- `set_waza_grade_fail`

Both accept optional `description` and `reason` fields.

**Example with session continuation:**

```yaml
- type: prompt
  name: judge_with_context
  config:
    model: claude-sonnet-4.5
    continue_session: true
    prompt: |
      Evaluate whether previous work in this session satisfies task requirements.
      Call set_waza_grade_pass if it does, otherwise call set_waza_grade_fail.
```

**Rubric-style prompt patterns:**

Use `config.prompt` as your rubric text. Tell the judge how to score, then tell it exactly when to call pass vs fail.

- Use **one final tool call** if you want a strict binary outcome.
- Use **one tool call per criterion** if you want partial credit (`score = passes / total pass+fail calls`).
- Encode any threshold logic directly in the prompt instructions.

**Example: Multi-criteria rubric (correctness/completeness/clarity)**

```yaml
- type: prompt
  name: comprehensive_quality
  config:
    model: gpt-4o
    prompt: |
      Evaluate the agent response on:
      1) Correctness (1-5)
      2) Completeness (1-5)
      3) Clarity (1-5)

      Compute the average score.
      If average >= 4.0, call set_waza_grade_pass once with:
      - description: "overall quality rubric"
      - reason: include the three scores and average.
      Otherwise call set_waza_grade_fail once with the same detail.
```

**Example: Binary requirements rubric**

```yaml
- type: prompt
  name: security_requirements
  config:
    model: gpt-4o-mini
    prompt: |
      Check these requirements:
      - Contains user authentication
      - Follows security best practices
      - Includes error handling

      If all requirements are satisfied, call set_waza_grade_pass.
      If any requirement is missing, call set_waza_grade_fail and explain what is missing.
```

**Example: Criterion-level partial credit rubric**

```yaml
- type: prompt
  name: style_compliance
  config:
    model: claude-sonnet-4.5
    prompt: |
      Evaluate three criteria independently:
      1) Naming conventions
      2) Documentation completeness
      3) Code organization

      For each criterion, call:
      - set_waza_grade_pass if the criterion is met
      - set_waza_grade_fail if it is not

      Make exactly one call per criterion (3 total calls).
```
