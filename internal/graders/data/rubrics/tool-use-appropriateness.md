---
name: tool-use-appropriateness
version: 1.0.0
scale: pass-fail
description: Whether the agent used tools appropriately — invoked the right tools at the right time, with sensible arguments, and avoided unnecessary calls.
goldens:
  - name: appropriate-tool-passes
    input: What files are in the current directory?
    output: |
      I'll use the list_files tool.
      tool: list_files({"path": "."})
      Result: ["README.md", "main.go"]
      The directory contains: README.md, main.go
    expected: pass
  - name: missing-tool-call-fails
    input: What files are in the current directory?
    output: |
      The directory probably contains some source files and a README, based on what most projects look like.
    expected: fail
  - name: wrong-tool-fails
    input: What files are in the current directory?
    output: |
      I'll use the web_search tool.
      tool: web_search({"query": "what files are in the current directory"})
    expected: fail
  - name: extraneous-call-fails
    input: What is 2 + 2?
    output: |
      I'll use the list_files tool to check.
      tool: list_files({"path": "."})
      The answer is 4.
    expected: fail
---

# Tool Use Appropriateness

You are evaluating whether the agent used its tools **appropriately**.

## What to check
- The agent invoked a tool when one was actually needed (e.g. to read the filesystem, query an API, run a command).
- The tool chosen was the right one for the job, not a tangentially related tool.
- Tool arguments are sensible — not empty, not malformed, not obviously wrong.
- The agent did **not** call tools that were unnecessary for the request (extraneous or speculative calls).
- The agent did **not** make up tool results without actually calling the tool.

## Scoring
- **Pass** — tool usage matched the task: necessary tools were called with sensible args, no extraneous calls, no hallucinated results.
- **Fail** — missing required tool call, wrong tool, malformed args, extraneous call, or fabricated result.

When the response passes, call `set_waza_grade_pass`.
When the response fails, call `set_waza_grade_fail` and explain which problem occurred ("missing tool call", "wrong tool", "extraneous call", "fabricated result") in the `reason` argument.
