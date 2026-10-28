# Local workflow migration to the bounded runner

This is the #761 **preparation contract**, based on runner request version 1.0.
The adapter package and isolated command constructor are implemented here.
The shared command tree and execution service are deliberately not changed:
`runner adapt` is **not registered in the shipping CLI at this preparation
head**. Registration, local execution routing and end-to-end equivalence belong
to the final sequential layer after #758, #759 and #760.

Existing local commands remain available. No command, historical schema or
artifact is deprecated by this preparation. A failed adapter mapping is not
permission to remove the unsupported declaration.

## What can be captured

| Input or behavior | Supported adapter mapping |
|---|---|
| Explicit eval and task filenames | Captures only the caller-named regular files. `eval.tasks` must contain exactly one literal filename resolving to the selected task path, relative to the eval directory. Custom filenames and nested directories are supported; no discovery or glob expansion. |
| Eval schema | Versionless and native-supported same-major 1.x; scenario evals require explicit `schemaVersion: "2.0"` and a valid `scenario`. Original version declarations are retained, never upgraded. |
| Task schema | Native unversioned task YAML; declaring a task `schemaVersion` is rejected by this adapter, even though the ordinary local loader warns and ignores unknown fields. |
| Execution | Explicit `config.executor: mock` and nonblank `config.model`; one task, one trial, one attempt. `max_attempts` omitted/0/1 retains the local effective single-attempt default. |
| Skills | `skill` must be empty/omitted. Legacy suites must disable ambient skills with `disabled_skills: ["*"]`. Scenario suites can use native scenario no-skill resolution. Empty/null/omitted skill-directory lists are retained; populated paths are rejected. `inject_skill_body: true` is rejected. |
| Task availability | Omitted/null `enabled` inherits true; explicit true is supported. False is rejected because the local command would skip the task rather than execute it. |
| Deadline | Effective task `timeout_seconds` overrides eval timeout; omitted/null task timeout inherits. Effective timeout must be 1..86400 seconds. The optional `--timeout-ms` asserts the same deadline, not a new shorter timeout. |
| Assertions | Builtin text graders at eval and task scopes and `expected.output_contains`, `output_not_contains`, `output_contains_any`. Names must be unique across both scopes without the reserved `_` prefix. At least one check is required; invalid regexes and blank expected-output assertions fail before execution. |
| Source fidelity | UTF-8 bytes, including LF/CRLF, comments, explicit values and ordering, are preserved inside `input.eval` and `input.task`. No YAML reserialization or source rewriting. |
| Runner transport | Request 1.0 JSON, required capabilities `exact-unit` and `inline.mock-text`, no optional capabilities and `allow_paid_execution: false`. Invocation-local #758 reports are not terminal #760 envelopes. |

This is intentionally narrower than the runtime profile and the ordinary local
loaders. The source allowlist is part of the adapter contract, not an expansion
of historical compatibility guarantees.

## Before and after

**Before: executable existing local command.** Keep this when a workflow needs
fixtures, multiple tasks, retries, native execution or other unsupported
semantics:

```bash
waza run custom-suite.yaml --output local-results.json --no-summary
```

The frozen example inputs are
[`eval-1.4.yaml`](../internal/runnercompat/testdata/eval-1.4.yaml) and
[`task.yaml`](../internal/runnercompat/testdata/task.yaml). To use them as a
local suite, copy the eval to `custom-suite.yaml` and the task to
`tasks/selected.case.yaml` alongside it. Do not copy a populated `fixtures/`
directory into this bounded example.

**After: intended final-layer invocation, NOT executable at this preparation
head.** These commands require registration of the constructor and the later
execution/terminal layers. They are an integration target, not shipped examples:

```bash
waza runner adapt --eval custom-suite.yaml --task tasks/selected.case.yaml \
  --run-key qualification-42 --shard local --task-id selected \
  --trial 1 --attempt 1 > request.json
waza runner validate < request.json
waza runner run < request.json > unit.json
```

All selection flags are required explicitly. The constructor writes a request
only after source, profile and request-schema validation; those validation
failures leave stdout empty. Filesystem and writer failures propagate, including
short writes; a writer failure can leave incomplete output, which must be
discarded. Successful adaptation proves neither execution nor model quality.
Mock execution is a harness control, not live qualification evidence.

## Intentional incompatibilities

| Not mapped | Reason and migration |
|---|---|
| Multiple tasks, glob/discovery/CSV, multiple trials or retries | They imply adjacent work or retry policy. Keep `waza run`; author a distinct one-unit suite only if that is the intended behavior. Selecting trial 2 from a three-trial local workflow is not preservation of that workflow. |
| Native/paid executors | No native adapter is available. Do not add a native capability or consent automatically. Retain the original command and its documented usage implications. |
| Fixtures, file/resources, repos, `prompt_file`, instructions, context/workdir/environment | The request cannot preserve their resolution or side effects in this bounded profile. Keep the local command or explicitly author a standalone inline prompt. The constructor rejects an existing eval-relative default `fixtures` entry, even an empty directory or symlink; it does not pretend to capture it. |
| Skills, tool policy/MCP servers, required skills, dependency mocks, hooks | Their discovery, policy and execution semantics cannot be dropped. Keep local execution. Explicit empty/null/false declarations of unsupported fields also fail, rather than being treated as omitted. |
| Non-text graders, remote grader refs, authored grader weights | They are not represented by this adapter. In particular numeric declarations are rejected rather than rounded to fit the captured identity profile. Keep historical grading. |
| Multi-turn/responder/checkpoints, requirements, metrics, baseline/adversarial behavior, golden policy | Not one bounded inline text attempt. Preserve the existing command and artifacts. |
| Local run flags for cache, snapshots, model overrides, parallelism, telemetry, reporters or other side effects | They are not `runner adapt` flags. A future local routing caller must reject or retain legacy handling for these semantics before invoking the adapter; it must never ignore them. |
| Unknown fields, aliases/anchors/merge keys, duplicate/nonstring keys, extra documents, invalid known types | Captured native parsing rejects these rather than silently making a different identity. Ordinary local loader compatibility is unchanged. |
| Historical results, snapshots and IDs | No conversion to runner results or logical IDs. Continue using their native readers and `grade`, `gate`, `compare`, `replay` and other artifact commands. |

An adapter error begins `cannot preserve local semantics in runner 1.0` and
identifies the offending declaration or constraint. Preserve the original
inputs and keep `waza run` when a mapping is unsupported; refactoring a workflow
is a deliberate product choice, not an automatic migration.

## Release-note facts for later shared publication

| Category | Accurate statement at this preparation head |
|---|---|
| Preserved | Existing local commands and historical reader/writer families are not modified; the frozen local mock/text observation remains checked through the real `run` command. |
| Additive preparation | Offline byte-preserving `runnercompat.AdaptLocal` and an isolated `newRunnerAdaptCommand` constructor; not registered and not a replacement execution path yet. |
| Incompatible mapping | Every unsupported declaration is a hard error with local-command guidance; no silent feature dropping, native execution or automatic schema conversion. |
| Deprecated | None. |
| Still required | Register the command, wire eligible supported local paths through the actual runner service while preserving local output/exit semantics, prove equivalence, publish site navigation/examples and final release notes. |
| Lab | **No Lab dashboard change is required for #761.** Commands, source/schema diagnostics and frozen fixtures expose compatibility behavior; this issue adds no runtime-state surface. |

Developer integration and oracle provenance are in
[the mapping reference](RUNNER_COMPATIBILITY_MAPPING.md). Existing workflow
compatibility remains governed by [COMPATIBILITY.md](COMPATIBILITY.md).
