# Agent-free preflight example

```bash
waza preflight examples/preflight/eval.yaml --format json
```

The default exit is 0. References and local paths resolve, but
`boundary.post_run` is unresolved: detecting absence after a run does not prevent
effects. `--strict` deliberately exits 1 for that unresolved prerequisite.
Inspection makes zero model/service/subprocess calls and leaves fixtures
unchanged.

This explicitly selected scenario eval uses exact `schemaVersion: "2.0"` to
disable ambient skills; optional task requirement metadata remains descriptive
v1 data. Existing v1 suites need no relabeling or migration.

The `mock` executor is harness-only, not agent quality evidence. File graders
observe captured local state and permit equivalent valid implementation paths;
they do not prove external state, unobserved effects, or preventive enforcement.
Success text is not a substitute for resulting-state evidence.
