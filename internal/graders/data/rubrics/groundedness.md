---
name: groundedness
version: 1.0.0
scale: pass-fail
description: Whether the candidate response is fully supported by the provided source context, with no invented or contradictory claims.
goldens:
  - name: grounded-answer-passes
    input: What year did Apollo 11 land on the moon?
    output: According to the provided NASA article, Apollo 11 landed on the moon on July 20, 1969.
    context: "NASA article: Apollo 11 landed on July 20, 1969."
    expected: pass
  - name: unsupported-claim-fails
    input: What year did Apollo 11 land?
    output: Apollo 11 landed in 1972 and the astronauts brought back 500kg of rocks.
    context: "NASA article: Apollo 11 landed on July 20, 1969."
    expected: fail
  - name: partial-answer-still-grounded
    input: When did Apollo 11 land?
    output: Apollo 11 landed in July 1969.
    context: "NASA article: Apollo 11 landed on July 20, 1969."
    expected: pass
---

# Groundedness

You are evaluating whether a candidate response is **grounded** in the provided source context.

## What to check
- Every factual claim in the response must be traceable to the source context.
- The response must not invent details that are not present in the source.
- The response may be less detailed than the source, but it must not contradict the source.
- Stylistic differences, paraphrasing, and summarization are fine as long as facts are preserved.

## Scoring
- **Pass** — every factual claim is grounded in the source context.
- **Fail** — one or more claims are unsupported by the source or directly contradict it.

When the response passes, call `set_waza_grade_pass` with a short reason.
When the response fails, call `set_waza_grade_fail` and name the unsupported or contradictory claim in the `reason` argument.
