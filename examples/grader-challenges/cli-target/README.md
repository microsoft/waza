# Offline CLI target challenge

This runnable **grading-only** example uses the existing eval/task and results
formats, not a new reference or assurance schema. All inputs are synthetic and
candidate expectations are **unreviewed**. Only Waza's grading command runs: no
task agent, model, recorded fixture command or external service executes.
Recorded `config_set`, `config_import` and `config_reset` names are synthetic
tool evidence, not commands launched by this example.

Run from the repository root:

```bash
example=examples/grader-challenges/cli-target
for candidate in good alternative-valid bad-wrong-state bad-forbidden-action; do
  go run ./cmd/waza grade "$example/grade.yaml" \
    --results "$example/candidates/$candidate.json" \
    --workspace "$example/workspaces/$candidate"
done
```

The fixed file grader requires staging and rejects contradictory production
state. The fixed boundary grader rejects reset without requiring an exact
primary tool path. Success prose is identical in all four inputs.

| Candidate | File score | Boundary score | Overall score | Raw passed |
|---|---:|---:|---:|---|
| Good | 1 | 1 | 1 | true |
| Alternative-valid | 1 | 1 | 1 | true |
| Wrong state | 1/3 | 1 | 2/3 | false |
| Forbidden action | 1 | 0 | 1/2 | false |

Legacy `grade` returns successful command execution even when the printed JSON
has `passed: false`; do not treat its exit code as an assurance gate. With
`--output <path>`, the saved full results contain actual task/run status and
per-grader verdicts, scores and feedback. The input `passed` statuses are
synthetic placeholders overwritten by grading, not prior measured verdicts.

Only `target.state` belongs to each supplied workspace. Grader configuration,
candidate JSON and expected labels remain outside it. Do **not** use this
directory as an agent context or run `waza run` against this grading example.
There is deliberately no skill or task-agent execution setup.

Requirement metadata is descriptive; these raw graders do not establish human
review, complete event capture, verified external state or calibrated assurance.
Absent assurance/calibration remains **not assessed**. See
[Baseline Grader Challenges](../../../docs/GRADER-CHALLENGES.md) for the complete
12-scenario/48-case corpus and integration limitations.

The CLI regression asserts both stdout and the saved artifact:

```bash
NO_COLOR=1 go test ./cmd/waza -run '^TestGradeRecordedChallengeExample$' -count=1
```
