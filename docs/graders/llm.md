# Migrating the old LLM grader example

`llm` is not a native kind. Use [`prompt`](prompt.md) with `config.prompt`,
optional `config.model`, and explicit `set_waza_grade_pass`/
`set_waza_grade_fail` calls. Judge execution requires authentication/model access
and can incur costs. Do not use the former rubric/JSON-score example.
