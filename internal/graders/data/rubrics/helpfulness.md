---
name: helpfulness
version: 1.0.0
scale: pass-fail
description: Whether the response actually addresses the user's request with useful, actionable content.
goldens:
  - name: actionable-answer-passes
    input: How do I list files in a directory in Python?
    output: |
      Use `os.listdir(path)` to get a list of names, or `pathlib.Path(path).iterdir()` for Path objects:

      ```python
      import os
      for name in os.listdir("."):
          print(name)
      ```
    expected: pass
  - name: vague-non-answer-fails
    input: How do I list files in a directory in Python?
    output: There are several ways to work with files in Python. It depends on what you're trying to do.
    expected: fail
  - name: off-topic-fails
    input: How do I list files in a directory in Python?
    output: JavaScript has a great filesystem API in Node.js — try `fs.readdirSync`.
    expected: fail
---

# Helpfulness

You are evaluating whether the candidate response is **helpful** — does it actually address the user's request with useful, actionable content?

## What to check
- The response addresses what the user asked about (not a tangential topic).
- The response gives the user enough to act on (an answer, a concrete next step, code, or a clear explanation).
- Caveats and disclaimers are fine, but they must not be the entirety of the response.
- Empty hedging ("it depends," "there are many ways") without any concrete information fails.

## Scoring
- **Pass** — the response is on-topic and gives the user something actionable.
- **Fail** — the response is off-topic, evasive, or contains only hedging with no useful content.

When the response passes, call `set_waza_grade_pass`.
When the response fails, call `set_waza_grade_fail` with a short `reason` (e.g. "off-topic", "evasive", "no actionable content").
