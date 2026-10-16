# Migrating the old script grader example

`script` is not a native kind. Use [`program`](program.md) with
`config.command` and `config.args`. The program reads output on stdin and gets
`WAZA_WORKSPACE_DIR`; exit zero passes, nonzero fails. The old JSON-returning
script protocol is not the native program protocol.
