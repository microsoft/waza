# Finite authored-output assurance example

Run from the repository root with a built `waza` binary:

```bash
example=$(mktemp -d)
go run ./examples/grader-challenges/authored-output \
  --binary ./waza --output-dir "$example"
./waza assure "$example/eval.yaml" --references "$example/labels.json" \
  --output "$example/assurance.json"
```

**Expected exit: 1, assessment `not_assessed`.** The real native text grader
observes one good, one alternative-valid and two bad outputs under fixed
configuration. All four agree with their candidate labels, but no human review
is supplied or fabricated. The report separately shows actual verdicts, scores,
feedback, evidence references and verified executable/source/config bindings.

The alternative changes its explanation, not the required outcome; no original
tool path is required. The two bad outputs separately have wrong state and a
forbidden marker. These are synthetic finite text inputs, not external MCP/CLI
state or historical agent execution.

The generator uses the existing native task loader, grader declarations and
approved immutable authored-output producer. It does not construct a rival
evidence schema, make paid calls, run an agent or write a reviewed declaration.
It refuses a nonempty output directory. Keep all generated material evaluator-only.

Open the resulting JSON in the dashboard's **Assurance** view to inspect it
locally; import neither uploads it nor associates it with historical runs.
Delete only this temporary example directory after preserving any report you need.
