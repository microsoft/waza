# Local workflow migration to the bounded runner

The source-build v0.40.0 preview provides `runner adapt` and the explicit
`run --runner-compat` mode, based on runner request version 1.0.
Both preserve original source bytes through `runnercompat.AdaptLocal`.
Local compatible execution and `runner run` use the same `runnerunit.Service`
execution path; the former projects the observed run through the historical
benchmark aggregators and writer rather than emitting a terminal envelope.

Existing local commands remain available. No command, historical schema or
artifact is deprecated. A failed adapter mapping is not
permission to remove the unsupported declaration.

## What can be captured

| Input or behavior | Supported adapter mapping |
|---|---|
| Explicit eval and task filenames | Captures only the caller-named regular files. `eval.tasks` must contain exactly one literal relative filename resolving to the selected task path from the eval directory. The complete resolved task path, including eval parent directories, must contain no glob metacharacters (`*`, `?`, `[`) or Unix backslash escapes; Windows backslash separators remain supported. Absolute task entries are rejected; native path selection is unchanged. Custom filenames and nested directories are supported; no discovery or glob expansion. |
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

These are historical native-loader fixtures, not strict preflight-schema
examples. `preflight --strict` reports `invalid eval.schema` because the strict
schema additionally requires metrics and legacy skill/scenario declarations.
The adapter does not fabricate those declarations. Use `runner adapt` and
`runner validate` for this captured profile, and retain ordinary schema-based
authoring/execution where those broader semantics matter.

**After, preserving local artifacts and exits:**

```bash
waza run custom-suite.yaml --runner-compat --output local-results.json --no-summary
```

Run this mode from a directory without an inherited `.waza.yaml`. It rejects
ambient project defaults/storage, adjacent trigger tests, every default fixture
entry and unsupported flags before execution. Only `--output`, `--no-summary`,
`--debug` and `--no-update-check` can accompany the mode. Explicit unsupported
flags fail even when set to their default or false values. `--no-summary` is
accepted because this single-suite path never creates a combined summary.
No automatic upload or alternate output publication occurs.

The local output remains `EvaluationOutcome` schema 1.7 with historical eval/task
IDs, statistics, grader details and unavailable usage. Generated run/session IDs,
timestamps, workspace paths and timing measurements remain invocation-specific.
Exits remain 0 (passed), 1 (failed checks or a recorded execution-error attempt,
with requested results saved), and 2 (invalid configuration, failure before an
observed attempt, lifecycle or output error). An observed execution error keeps
the native run `error` status and failed-task aggregate, including cancellation
observed during the attempt. The external runner's typed failure and 0-6
transport taxonomy remain separate.

**After, explicitly choosing the external boundary instead:**

```bash
waza runner adapt --eval custom-suite.yaml --task tasks/selected.case.yaml \
  --run-key qualification-42 --shard local --task-id selected \
  --trial 1 --attempt 1 > request.json
waza runner validate < request.json
waza runner run < request.json > unit.json
```

All selection flags are required explicitly. The constructor writes a request
only after source, profile, request-schema and shared offline runner admission; those validation
failures leave stdout empty. Filesystem and writer failures propagate, including
short writes; a writer failure can leave incomplete output, which must be
discarded. Successful adaptation proves neither execution nor model quality.
Mock execution is a harness control, not live qualification evidence.

The emitted request is at most 4,194,304 bytes, including an optional trailing
LF. Exactly-limit JSON omits the LF. A prompt such as `inspect ./file`, which
requires files absent from the inline empty workspace, is rejected before
output; keep ordinary local execution for that workflow.

Unix source admission opens nonblocking and checks/reads the same owned handle,
so writerless FIFOs and nonregular replacements do not hang these callers.
Regular symlink targets retain native path resolution. Windows retains the
shared opener's best-effort precheck and opened-handle recheck, not a race-free
or nonblocking-open guarantee. Neither path promises a stable-content snapshot,
filesystem confinement or hostile-host isolation.

## Intentional incompatibilities

| Not mapped | Reason and migration |
|---|---|
| Multiple tasks, glob/discovery/CSV, multiple trials or retries | They imply adjacent work or retry policy. Keep `waza run`; author a distinct one-unit suite only if that is the intended behavior. Selecting trial 2 from a three-trial local workflow is not preservation of that workflow. |
| Native/paid executors | No native adapter is available. Do not add a native capability or consent automatically. Retain the original command and its documented usage implications. |
| Fixtures, file/resources, repos, `prompt_file`, instructions, context/workdir/environment | The request cannot preserve their resolution or side effects in this bounded profile. Keep the local command or explicitly author a standalone inline prompt. The constructor rejects an existing eval-relative default `fixtures` entry, even an empty directory or symlink; it does not pretend to capture it. |
| Skills, tool policy/MCP servers, required skills, dependency mocks, hooks | Their discovery, policy and execution semantics cannot be dropped. Keep local execution. Explicit empty/null/false declarations of unsupported fields also fail, rather than being treated as omitted. |
| Non-text graders, remote grader refs, authored grader weights | They are not represented by this adapter. In particular numeric declarations are rejected rather than rounded to fit the captured identity profile. Keep historical grading. |
| Multi-turn/responder/checkpoints, requirements, metrics, baseline/adversarial behavior, golden policy | Not one bounded inline text attempt. Preserve the existing command and artifacts. |
| Local run flags for cache, snapshots, model overrides, parallelism, telemetry, reporters or other side effects | `runner adapt` and `run --runner-compat` reject these flags, including explicit defaults. Remove `--runner-compat` to keep ordinary execution and its side effects. |
| Ambient `.waza.yaml` configuration or adjacent trigger tests | The local adapted mode rejects existing entries without opening or parsing them, including directories, symlinks and named pipes. Configuration discovery checks the current directory and at most nine ancestors, matching the native limit. Keep ordinary `run`, or deliberately author an isolated standalone suite. Offline `runner adapt` never loads project configuration; it captures only its explicit files. |
| Unknown fields, aliases/anchors/merge keys, duplicate/nonstring keys, extra documents, invalid known types | Captured native parsing rejects these rather than silently making a different identity. Ordinary local loader compatibility is unchanged. |
| Historical results, snapshots and IDs | No conversion to runner results or logical IDs. Continue using their native readers and `grade`, `gate`, `compare`, `replay` and other artifact commands. |

Source selection preserves native path handling: the eval is opened using the
original argument, while task selection uses the cleaned eval-relative path.
`runner adapt --task` opens the same cleaned absolute task path that it validates
against the eval declaration. These can differ from an OS walk through a symlink
followed by `..`; the adapter does not substitute bytes from that other task.
CLI command registration does not read ambient model defaults for unrelated
`quality` or `suggest` commands. Those commands resolve project model defaults
when executed, and an explicit `--model` still takes precedence.

An adapter error begins `cannot preserve local semantics in runner 1.0` and
identifies the offending declaration or constraint. Preserve the original
inputs and keep `waza run` when a mapping is unsupported; refactoring a workflow
is a deliberate product choice, not an automatic migration.

## Release-note compatibility matrix

| Category | Source-build preview behavior |
|---|---|
| Preserved | Ordinary local commands/defaults and historical reader/writer families remain available; the adapted mock/text mode retains result 1.7 and local 0/1/2 exits, checked through the public command against an independent frozen observation. |
| Additive | Offline byte-preserving `runner adapt` and opt-in `run --runner-compat`, routed through the shared runner service. |
| Incompatible mapping | Every unsupported declaration is a hard error with local-command guidance; no silent feature dropping, native execution or automatic schema conversion. |
| Deprecated | None. |
| Migration | Choose the bounded mode only for the documented profile; remove the mode flag to retain unsupported local semantics. Choose `runner run` explicitly for terminal JSON instead of legacy artifacts. |
| Lab | **No Lab dashboard change is required for #761.** Commands, source/schema diagnostics and frozen fixtures expose compatibility behavior; this issue adds no runtime-state surface. |

Developer integration and oracle provenance are in
[the mapping reference](RUNNER_COMPATIBILITY_MAPPING.md). Existing workflow
compatibility remains governed by [COMPATIBILITY.md](COMPATIBILITY.md).
