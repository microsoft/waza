# Writing Skill Evals - Tutorial

Skills and custom agents remain supported alongside workflow scenarios. Start
with [Concepts](../site/src/content/docs/concepts.mdx) and the
[offline workflow Quick Start](../site/src/content/docs/quick-start.mdx) if you
need repository, CLI or MCP evaluation without a `SKILL.md`.

This tutorial starts with **mock harness checks**, not agent-quality claims.
Preflight and new evidence features require upcoming main; published standalone
`v0.38.10` (October 9, 2026) is separate. Follow the
[complete source-build procedure](../README.md#install-from-source), including
Git LFS, Go 1.26+, Make, Node.js, npm and generated dashboard assets.

## 1. Create a project and skill

```bash
waza init my-evals --no-skill
cd my-evals
waza new skill my-awesome-skill
```

The skill is `skills/my-awesome-skill/SKILL.md`. Its eval is
`evals/my-awesome-skill/eval.yaml`, with sibling `tasks/` and `fixtures/`.
There is no `init --interactive`, `init --path` or nested skill `skill.yaml`.

## 2. Write a task file

Replace `evals/my-awesome-skill/tasks/basic-usage.yaml`:

```yaml
id: explain-function
name: Explain a local function
inputs:
  prompt: "Read sample.py and explain the function and its parameter."
  files:
    - path: sample.py
requirements:
  - id: explanation
    category: quality
    description: Explain the function and parameter.
    checks:
      - scope: eval
        grader: explains-concepts
```

Keep the generated `fixtures/sample.py`, or replace it with your own sanitized
function. `inputs.files` selects files copied from the fixture lookup root into
a fresh task workspace; their contents are not automatically prompt text.
Original fixtures are not modified. Isolation is not a host/network sandbox.

`expected.outcomes` is descriptive metadata, not a runtime assertion.
For final file state or forbidden tool observations, use native `file`,
`tool_calls` or `action_sequence` graders and name them in `requirements.checks`.
See the [alternative-valid and rejection controls](../site/src/content/docs/reference/schema.mdx#outcomes).

## 3. Configure the suite

Replace `evals/my-awesome-skill/eval.yaml`:

```yaml
name: tutorial-eval
skill: my-awesome-skill
config:
  executor: mock
  model: mock
  trials_per_task: 1
  timeout_seconds: 30
metrics:
  - name: task_completion
    weight: 1
    threshold: 1
graders:
  - type: text
    name: explains-concepts
    config:
      regex_match: ["(?i)(function|parameter)"]
tasks:
  - tasks/basic-usage.yaml
```

Task entries are file paths, not inline objects. A text grader uses
`regex_match: [...]`, not `pattern:`. Regex and keyword checks are not separate
grader kinds.

## 4. Run offline and challenge the predicate

```bash
waza preflight evals/my-awesome-skill/eval.yaml
waza run evals/my-awesome-skill/eval.yaml --output results.json
```

Expect exit **0**: the mock echoes the task prompt, so the text predicate sees
those words. It has not read/explained Python or demonstrated skill quality.
Change the regex to `["^IMPOSSIBLE_TUTORIAL_SENTINEL$"]` and run again: expect
exit **1** and a missing-pattern failure. Restore the original before continuing.

Preflight inventories local configuration and explicit references without
running agents, graders, subprocesses or services. Warnings are not verified
runtime readiness; `--strict` makes unresolved prerequisites fail.

Inspect `tasks[].runs[].validations` and `requirement_explanations` in
`results.json`, not only its aggregate score. Add `--snapshot snapshots` to
capture local recorded evidence. [Replay](../site/src/content/docs/guides/snapshot-replay.mdx)
checks recorded consistency, not fresh agent/grader execution.

Other supported run controls:

```bash
waza run evals/my-awesome-skill/eval.yaml --task explain-function -v
waza run evals/my-awesome-skill/eval.yaml --trials 3 --output repeated.json
waza run evals/my-awesome-skill/eval.yaml --transcript-dir transcripts
```

## 5. Choose native graders

There are **13 native kinds**: `code`, `prompt`, `text`, `file`, `json_schema`,
`program`, `behavior`, `action_sequence`, `skill_invocation`, `trigger`, `diff`,
`tool_constraint`, and `tool_calls`. See the [catalog](GRADERS.md).
`trigger_tests` is suite configuration, not a grader kind.

### Prompt grader: authenticated judge, possible model cost

Insert this under `graders:` only when deliberately selecting a real
`config.executor: copilot-sdk` run and judge:

```yaml
- type: prompt
  name: quality-assessment
  config:
    model: gpt-4o-mini
    prompt: |
      Check whether the explanation is correct, complete and clear.
      Call set_waza_grade_pass with a reason only if every check passes.
      Otherwise call set_waza_grade_fail with a reason.
```

`llm` is not the native kind. `config.prompt` supplies judge instructions;
the judge must call the explicit grading tools, not merely print a JSON score.
This branch is preflight-checked, **not paid/live-tested** by the docs suite.

### Program grader: runnable local Node.js check

Insert this under `graders:` with the existing mock executor:

```yaml
- type: program
  name: custom-validation
  config:
    command: node
    args:
      - -e
      - |
        let output = '';
        process.stdin.setEncoding('utf8');
        process.stdin.on('data', chunk => output += chunk);
        process.stdin.on('end', () => process.exit(output.trim() ? 0 : 1));
```

This checks nonempty output: exit zero passes, nonzero fails. Replace the final
exit expression with `process.exit(1)` to exercise the rejection path.
`program` replaces the obsolete `script` kind and JSON-returning script protocol.
A program can inspect the actual `WAZA_WORKSPACE_DIR` supplied by the runner.

## 6. Evaluate a real agent, then gate regressions

Before changing the suite to `config.executor: copilot-sdk`, complete the
[SDK installation/authentication, model-access, permissions and usage prerequisites](../site/src/content/docs/getting-started.mdx#authenticate-before-real-execution).
Select a model available to your account. Waza's bundled runtime does not put
`copilot` on `PATH`; use the supported standalone CLI login route.
Real runs and prompt judges can incur usage costs. Worker counts, cooperative
timeouts and token-content limits are not provider spending caps.

There is no `run --executor` or `run --engine` override: edit the YAML executor.
After a separately authorized run, inspect actual output, workspace and
finalized usage. Record a reviewed baseline before using
[CI regression gates](../site/src/content/docs/guides/ci-cd.mdx).
Mock outcomes, metadata and passing summaries do not establish live service
safety, external state, production readiness or release authority.
