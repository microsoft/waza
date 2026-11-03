# External evaluation interoperability (source-build preview)

Waza does **not** import whole AgentEvals, AgentV, Harbor or ASSERT suites.
The first bounded adapter projects one explicitly selected AgentV
case-sensitive `contains` assertion into one native text grader and exports the
same native subset back into an AgentV assertion. This is partial work toward
[#789](https://github.com/microsoft/waza/issues/789), not completion of that issue.
It is targeted for v0.40.0, not a claim about a published release.

## Verified sources and distinct roles

The following pins identify the reviewed artifacts, not compatibility with
every format in the associated repository. Licenses describe upstream source;
individual datasets and dependencies can have separate terms.

| Ecosystem / pin | Authoritative contract and real fixture | Role and current support |
|---|---|---|
| [AgentEvals `61ec10dc`](https://github.com/agentevals/agentevals/tree/61ec10dcb5b4653bbebc5ee8a55c134b7666c3a5), MIT | [Specification README and example](https://github.com/agentevals/agentevals/blob/61ec10dcb5b4653bbebc5ee8a55c134b7666c3a5/README.md) | Specification/documentation repository, identifying AgentV as its canonical implementation. No executable oracle established here. Its `execution.evaluators`, `input`, `rubrics` and underscore type names are **not** the pinned AgentV assertion dialect. No direct import. |
| [AgentV `68df6d22`](https://github.com/EntityProcess/agentv/tree/68df6d2206cea61f721d0445b405efcb5e94aff0), MIT | [Real assertion suite](https://github.com/EntityProcess/agentv/blob/68df6d2206cea61f721d0445b405efcb5e94aff0/examples/features/assert/evals/suite.yaml), [executable assertions](https://github.com/EntityProcess/agentv/blob/68df6d2206cea61f721d0445b405efcb5e94aff0/packages/core/src/evaluation/graders/assertions.ts), [upstream decision tests](https://github.com/EntityProcess/agentv/blob/68df6d2206cea61f721d0445b405efcb5e94aff0/packages/core/test/evaluation/graders/assertions.test.ts) | Supported **selected assertion only**, not the enclosing task, execution configuration or entire suite. Retained fixture/oracle/license bytes are checksum-pinned. Do not confuse this project with `langchain-ai/agentevals`. |
| [Harbor `f6cf2e2e`](https://github.com/harbor-framework/harbor/tree/f6cf2e2e86a102a9d2980ce8888f4bda251aa998), Apache-2.0 | [Task model](https://github.com/harbor-framework/harbor/blob/f6cf2e2e86a102a9d2980ce8888f4bda251aa998/src/harbor/models/task/config.py), [real hello-world task, schema 1.4](https://github.com/harbor-framework/harbor/blob/f6cf2e2e86a102a9d2980ce8888f4bda251aa998/examples/tasks/hello-world/task.toml), [verifier script](https://github.com/harbor-framework/harbor/blob/f6cf2e2e86a102a9d2980ce8888f4bda251aa998/examples/tasks/hello-world/tests/test.sh) | Container/task framework: task success depends on executable verifiers and environment state. The hello-world script installs packages and downloads a runtime installer. Not executed or imported. ATIF trajectory structural validation is not benchmark success evidence. No support. |
| [ASSERT `45da4c56`](https://github.com/responsibleai/ASSERT/tree/45da4c56768436dd255cd0a0fe8bb7cf7d7e466c), MIT | [Versioned arm-outcomes fixture](https://github.com/responsibleai/ASSERT/blob/45da4c56768436dd255cd0a0fe8bb7cf7d7e466c/examples/bank_manager_agent_control/fixtures/coercion_powered_120_arm_outcomes.json), [calibration artifact](https://github.com/responsibleai/ASSERT/blob/45da4c56768436dd255cd0a0fe8bb7cf7d7e466c/examples/bank_manager_agent_control/runtime/coercion_calibration.json), [model-mocked systematization test](https://github.com/responsibleai/ASSERT/blob/45da4c56768436dd255cd0a0fe8bb7cf7d7e466c/tests/test_systematization_convert_stage.py) | Separate scoring/systematization project, **not** the Agent Governance Toolkit's policy fixture schema. Deterministic calibration artifacts exist, but an interchangeable evaluation-definition schema and same-version offline scoring oracle have not been established. No support. |

Direct reuse was considered first. AgentV substring assertion semantics map
exactly to native `text.config.contains_cs` with **one** check: binary score
0/1, higher is better, case-sensitive, no trimming. Native `contains` is
case-insensitive and would be an incorrect substitution. Multiple checks in one
native grader produce fractional scores, so export refuses them. Regex dialects,
trimmed `equals`, LLM rubrics, scripts, thresholds and weighting do not have a
proven mapping in this adapter. Harbor execution and ASSERT scoring are not
replaced by schema-validation success.

## Explicit selection and diagnostics

The required adapter ID pins both upstream semantics and the Waza mapping:

```text
agentv-68df6d2206cea61f721d0445b405efcb5e94aff0-contains-v1
```

```bash
# From the repository root, using a binary built from this source.
adapter=agentv-68df6d2206cea61f721d0445b405efcb5e94aff0-contains-v1
waza interop import --adapter "$adapter" --test contains-check --assertion 1 \
  < internal/interop/testdata/agentv/suite.yaml
```

Stdout is one native grader mapping, suitable for a native eval's `graders`
list. It is **not** a task or runnable eval. Import requires an exact test ID
and 1-based assertion index; no implicit selection or globs. Stderr explicitly
reports the assertion-only projection and every excluded semantic category:
task inputs, providers, fixtures, sibling assertions, thresholds, suite metadata
and execution settings are **not converted or validated**. Those excluded
fields may contain unsupported/executable content; selection never authorizes
their execution. Copying an assertion does not copy an upstream test's decision.

The selected mapping accepts only `type: contains`, nonempty string `value`,
and optional nonblank string `metric`. Every other selected field fails closed,
including `required`, `negate`, `weight`, `threshold`, `flags`, `command`, `name`
and nested configuration. A missing metric produces the native name
`agentv-contains`; export writes this name as an explicit `metric`. Round trips
are semantic, not byte-identical.

```bash
waza interop export --adapter "$adapter" < one-native-grader.yaml
```

Export accepts exactly `name`, `type: text` and
`config: {contains_cs: [one-nonempty-string]}`. Extra fields, weights, other
checks or multiple checks are errors. Output is **one AgentV assertion**, not a
suite; place it in an upstream test's `assert` list yourself.

Both operations read stdin and write stdout; they never open referenced paths,
load ambient config, execute hooks/scripts/providers or contact a model.
Inputs are UTF-8, at most 4 MiB and one YAML mapping document. Duplicate/merge
keys, anchors, aliases, custom tags and excessive nesting fail before projection.
Selected null/non-string/empty values are rejected, not defaulted.
Selected strings containing `${`, `{{` or `}}` are rejected: environment or
template interpolation must be resolved outside this literal-only adapter.
`file://../../secret` in a `contains` value is literal text, never a read.
Unknown adapter IDs/versions fail; there is no auto-detection or latest fallback.
Failure is exit 2 with no converted stdout. Output/diagnostic delivery errors
propagate; shell redirection may still create a file before the command fails.

Source SHA256 and adapter comments are **unverified advisory provenance** only.
They do not certify origin, execution, evidence, billing or scores, and native
loaders may discard comments. No new fields enter saved results or Lab.

## Runnable example and evidence

Run the [assertion projection example](../examples/interop/run.sh) from the
repository root:

```bash
go build -o /tmp/waza-interop ./cmd/waza
bash examples/interop/run.sh /tmp/waza-interop
```

The example imports the real pinned upstream assertion, assembles a native mock
eval and runs positive and negative controls. It expects one pass and one
failure (normal `waza run` exit 1), with binary scores in saved JSON. These are
**mock/supplied-output conformance controls**, not real agent quality evidence.

```bash
NO_COLOR=1 go test ./internal/interop ./cmd/waza \
  -run 'TestPinnedAgentV|TestAgentV|TestInterop' -count=1
```

The public CLI test executes the retained, reviewed upstream TypeScript
`runContainsAssertion` implementation with Node type stripping, compares
positive and negative decisions **and scores** through production
`waza interop import` and `waza grade`, and rejects unsupported selections.
It needs Node 22.18+ or 24+; no npm packages or inference. Original upstream
files and license are retained with [exact provenance](../internal/interop/testdata/agentv/PROVENANCE.md).
The checkout regression covers checksum preservation with both Git CRLF modes.

## Remaining #789 acceptance gaps

- Whole-task/suite import, including task inputs, fixture materialization,
  model/tool requirements and combined grading thresholds/direction.
- Whole-suite native/external equivalent decisions against an upstream runner,
  rather than the deliberately selected assertion oracle.
- Direct AgentEvals specification, Harbor and ASSERT adapters; ASSERT's
  interchangeable definition schema and runnable scoring oracle remain unverified.
- Full definition/provenance/evidence round trips. The shipped round trip
  covers only the supported individual grader mapping.

Keep #789 open. This preview must not be described as three-format support or
completion of external evaluation interoperability.

## v0.40.0 release-note contribution (unreleased)

Added opt-in, version-pinned `waza interop import/export` for a **single selected
AgentV case-sensitive contains assertion**. Conversion is offline, has explicit
projection/loss boundaries, and preserves the supported binary decision/score
mapping. The real upstream fixture, executable assertion oracle and license are
retained for reproducible conformance. Whole eval/task imports, AgentEvals-spec,
Harbor and ASSERT adapters remain unsupported; #789 is still open. No default
native execution, artifact schema, evidence or Lab behavior changes.
