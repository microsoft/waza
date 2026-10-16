# Migrating the old reference-comparison example

`llm_comparison` is not a native kind. Use [`prompt`](prompt.md) and include the
reference criteria in `config.prompt`; request explicit pass/fail tool calls.
Do not imply an automatic reference-comparison kind or unconfigured score
threshold exists. Judge runs require model access and may incur costs.
