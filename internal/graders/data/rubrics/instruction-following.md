---
name: instruction-following
version: 1.0.0
scale: pass-fail
description: Whether the response follows the explicit instructions in the task — including format, constraints, and required elements.
goldens:
  - name: follows-format-passes
    input: List three primary colors. Respond with a JSON array of strings and nothing else.
    output: '["red", "green", "blue"]'
    expected: pass
  - name: ignores-format-fails
    input: List three primary colors. Respond with a JSON array of strings and nothing else.
    output: |
      Sure! The three primary colors are red, green, and blue.
    expected: fail
  - name: ignores-constraint-fails
    input: Summarize the article in one sentence.
    output: |
      The article is about the history of pasta. It describes how pasta originated in ancient times.
      It then talks about how pasta spread across Europe. Finally it describes modern pasta production.
    expected: fail
---

# Instruction Following

You are evaluating whether the candidate response **follows the explicit instructions** in the task.

## What to check
- Format requirements (JSON, bullet list, single sentence, code only, etc.) are respected.
- Hard constraints (length limits, "do not include X", "only respond with Y") are honored.
- All required elements requested by the instructions are present.
- Quality of the answer is **out of scope** for this rubric — only compliance with the instructions matters.

## Scoring
- **Pass** — every explicit instruction was followed.
- **Fail** — at least one explicit instruction was not followed (wrong format, violated constraint, missing required element).

When the response passes, call `set_waza_grade_pass`.
When the response fails, call `set_waza_grade_fail` and name the violated instruction in the `reason` argument.
