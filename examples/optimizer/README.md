# Optimizer walkthrough

An offline suite for `waza optimize run`. The mock executor echoes the
`style-guide.md` context file, so a better candidate guide changes the score
deterministically. There are no model calls unless you choose the
`copilot-reflect` adapter.

- `eval.yaml` and `tasks/`: three `optimization` tasks and two held-out
  `qualification` tasks.
- `seed/style-guide.md`: the seed candidate, identical to
  `fixtures/style-guide.md`.
- `fake-script.json`: scripted proposals for the offline `fake` adapter.
- `adapters/checklist_adapter.py`: a standard-library `command` adapter that
  reads optimization feedback from stdin.

Run these from the repository root:

```bash
# Offline, deterministic: accepted, 0.5 -> 1.0 on both splits
waza optimize run examples/optimizer/eval.yaml \
  --target context-overlay --seed examples/optimizer/seed \
  --optimization-tag optimization --qualification-tag qualification \
  --adapter fake --fake-script examples/optimizer/fake-script.json \
  --output-dir optimize-out

# External command adapter (consent is always required for command adapters)
waza optimize run examples/optimizer/eval.yaml \
  --target context-overlay --seed examples/optimizer/seed \
  --optimization-tag optimization --qualification-tag qualification \
  --adapter command --adapter-command python3 \
  --adapter-arg examples/optimizer/adapters/checklist_adapter.py \
  --allow-paid-calls --output-dir optimize-command
```

See the [Optimizer Adapters guide](../../site/src/content/docs/guides/optimizer-adapters.mdx)
for the paid `copilot-reflect` walkthrough, the contract and the failure
categories.
