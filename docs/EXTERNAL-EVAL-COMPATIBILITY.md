# External evaluation definition compatibility

## Status and scope

This branch contains **package-only preparation** for issue #789:
`internal/externalformat`. It does not yet register an external-format CLI
command. Public CLI registration, explicit native execution binding, runnable
native eval assembly, site guidance and final release gates belong to the
production integration after #761. This preparation does not close #789.

The first supported definition profile is explicitly selected as
`agentv / 5.3.3 / contains-v1`. **5.3.3 identifies a pinned upstream
implementation and dialect, not an official `schema_version`.** The adapter
does not change native Waza loaders, schemas or result formats.

Compatibility means that the supported definitions preserve literal inputs,
task identities/order and deterministic grading decisions on the same recorded
output. It does **not** mean upstream provider execution, interchangeable
models/tools, equivalent generated responses, benchmark execution, or a claim
that all definitions from a named project work.

## Verified compatibility matrix

The source investigation and reference execution were performed on 2026-10-28.
Pins, rather than a project's current default branch, identify this evidence.

| Project / source | Pinned contract and license | Assessment |
|---|---|---|
| [Canonical AgentV](https://github.com/EntityProcess/agentv/tree/68df6d2206cea61f721d0445b405efcb5e94aff0) | Tag v5.3.3, commit `68df6d2206cea61f721d0445b405efcb5e94aff0`; MIT | Bidirectional definition conversion for the narrow `contains-v1` profile below. Actual pinned parser/schema and pure grader/verdict code executed on complete, explicitly Waza-authored conformance suites. No provider/agent execution. |
| [AgentEvals documentation/specification](https://github.com/agentevals/agentevals/tree/61ec10dcb5b4653bbebc5ee8a55c134b7666c3a5) | Documentation spec 1.0, commit `61ec10dcb5b4653bbebc5ee8a55c134b7666c3a5`; MIT | Not an importer. Its historical dialect differs from current AgentV. A real `contains` assertion fragment is not a runnable complete suite. The advertised `https://agentevals.io/schema/eval.schema.json` returned HTTP 404; no machine-readable schema was found in the pinned tree. |
| [Harbor benchmark](https://github.com/harbor-framework/harbor/tree/e1edda49b70b800d9b755ac784bd6be4d00fa0ea) | Commit `e1edda49b70b800d9b755ac784bd6be4d00fa0ea`; Apache-2.0 | Not supported. Actual task config defaults to schema 1.4 while the pinned configuration documentation describes 1.3. Tasks include an environment and executable verifier producing reward files. Extracting only the instruction would discard benchmark semantics. |
| [Requested toolkit / ASSERT identity](https://github.com/microsoft/agent-governance-toolkit/tree/1b6805366554cc0ed8d8b8453ed745fff98fd755) | Toolkit commit `1b6805366554cc0ed8d8b8453ed745fff98fd755`; MIT | No ASSERT-named format/component was identified in the complete pinned tree. The actual `tests/redteam/benchmark/contract.schema.json` is an AGT deterministic red-team benchmark contract, not verified as ASSERT. No compatibility claim or importer. |
| [Separate ASSERT project](https://github.com/responsibleai/ASSERT/tree/5b5710738e5f76957156440e899d6c0de6609ae9) | Commit `5b5710738e5f76957156440e899d6c0de6609ae9`, package assert-ai 0.3.0; MIT | Not established as the requested toolkit component. Its systematize/test-set/inference/judge pipeline is not a pre-existing deterministic task suite. No importer. The separate v0.3.0 tag points to `04b713fe73879208369750b3bd5ad53544f3eecc`; these pins are not interchangeable. |

Harbor's authoritative [task overview](https://docs.harborframework.com/tasks/overview)
and [pinned hello-world task](https://github.com/harbor-framework/harbor/tree/e1edda49b70b800d9b755ac784bd6be4d00fa0ea/examples/tasks/hello-world)
identify the intended benchmark, not an assumed `harbor.ai` hostname.

Direct reuse was assessed first. Current AgentV's authoritative schema is
[executable Zod source](https://github.com/EntityProcess/agentv/blob/68df6d2206cea61f721d0445b405efcb5e94aff0/packages/core/src/evaluation/validation/eval-file.schema.ts),
not a drop-in native Waza schema. Executing a JavaScript schema at admission
would violate runtime-free admission. Its default unknown-field stripping also
cannot establish lossless conversion. The Go adapter therefore checks an
explicit original-YAML-node profile and uses the exact upstream sources only as
an independently executed test reference.

## Exact supported profile

The complete accepted field set is:

```yaml
name: contains-conformance
requires:
  agentv: "5.3.3"
prompts:
  - "{{ input }}"
tests:
  - id: world
    vars:
      input: Say hello to the world.
    assert:
      - type: contains
        value: world
```

Every shown field is required. There are no additional supported optional
fields. Suite names contain 1-128 lowercase ASCII letters, digits or hyphens;
task IDs contain 1-128 ASCII letters, digits, underscores or hyphens and must be
unique. The template is exactly `{{ input }}`. Inputs are nonblank literal
strings and assertions use nonempty literal strings. Template expressions,
template comments and `file:` references are not accepted as literals.

There must be 1-1000 ordered tests and exactly one assertion per test. One
case-sensitive substring condition maps to native text `contains_cs`, producing
the same binary score 0/1 and pass/fail decision as the pinned pure upstream
grader. No weight, threshold, required gate or negation is synthesized.
With one assertion, omitted default weighting and threshold cannot alter its
binary decision. Even explicit upstream default-valued fields are rejected
rather than normalized away.

Provider/model/tool/fixture requirements, criteria, natural-language or
model-backed judges, extra assertions, transforms, extensions, hooks, scripts,
includes, resource paths, extra variables and unknown fields reject the **whole
suite**. Do not remove these fields merely to make admission pass. Such a suite
requires another demonstrated-compatible adapter.

Nulls, numbers and booleans are not coerced into strings or defaults.
Quoted `"0"` and `"false"` remain strings. Empty output is a valid, explicitly
supplied grading control; it is not evidence that an output was observed.
Missing execution evidence is outside conversion and is never fabricated.
Unicode normalization, case folding, whitespace stripping and line-ending
normalization of string values are not performed.

Input, combined native task content and canonical external serialization are
bounded to 1 MiB. Documents have a maximum nesting depth of 32 and a 100,000
value-node budget. Invalid UTF-8, multiple documents, YAML anchors, aliases,
merge keys, duplicate/nonstring keys and custom tags fail with diagnostics.
After structural admission, independent invalid task diagnostics are reported
together; no partial suite is returned.

## Conversion API and reverse export

`externalformat.Import(bytes, Selection{Format: "agentv", Version: "5.3.3",
Profile: "contains-v1"})` returns a suite identity and native task files.
The files have deterministic `tasks/0001.yaml`-style names independent of
external IDs. Returned files are **data**, not filesystem writes. The package
does not open files, resolve references, discover ambient configuration,
construct an engine, launch a process or call a model/network.

Each imported task is admitted through the production captured/offline native
loader in the tests. For example, the first task contains:

```yaml
id: world
name: world
inputs:
  prompt: Say hello to the world.
graders:
  - name: contains
    type: text
    config:
      contains_cs:
        - world
```

`externalformat.Export(suite, selection)` reads the actual native task bytes,
not a retained original external document. Supported edits to IDs, literal
prompts or substring values are exported. Display name must equal ID. The
exact native field set shown above is required; any other field, including
explicit empty or default-valued fields, is rejected by presence. Extra native
graders/rules, prompt/resource paths, execution settings and skill requirements
cannot be silently discarded. Generated task paths and order are validated.

The round-trip contract is semantic: task identity/order, literal input,
substring, score and decision are preserved. Comments, YAML key order,
quoting style and original source bytes are not reproduced. Execution policy
is deliberately not part of this definition-only bundle. Production integration
must request an explicit native executor/model, timeout and trial count rather
than pretend that an unspecified upstream provider was preserved. Conversion
does not generate result provenance, billing, scores or assurance evidence.

## Reproducible independent evidence

The complete three-case fixture is
`internal/externalformat/testdata/contains.eval.yaml`: it is **Waza-authored**,
not an unchanged upstream positive suite. The exact AgentV parser
`parseYamlValue`, `EvalFileSchema.parse`, `runContainsAssertion` and
`scoreToVerdict` admit/evaluate it. The unmodified upstream
`upstream-deterministic-suite.yaml` is a whole-suite rejection control; its
providers, structured messages and additional judges are not extracted away.

`testdata/reference/` retains the pinned unmodified parser/schema/grader/verdict
sources, upstream MIT license, dependency rights, public-registry lockfile,
source URLs/checksums and the frozen `observed.json`. These are reference-only
test artifacts, not runtime dependencies of Waza.

With the existing Bun runtime (the recorded run used Bun 1.3.14):

```bash
cd internal/externalformat/testdata/reference
npm ci --ignore-scripts --omit=optional --no-audit --fund=false \
  --registry=https://registry.npmjs.org
bun run check.ts --verify
```

Installation requires the public registry or a populated package cache.
Reference grading itself does not use a provider or network. `check.ts` checks
source digests and asserts its fresh observations equal the frozen oracle.
The two locked reference-only dependencies are `yaml` 2.8.3 (ISC) and
`zod` 3.25.76 (MIT). `npm audit` failed with advisory endpoint
`read ENOTCONN`; **no clean vulnerability-audit result is claimed**.

From the repository root:

```bash
NO_COLOR=1 ENABLE_COPILOT_TESTS=false go test ./internal/externalformat -count=1
```

The Go regression sends the **actual converter-emitted bytes** through
`models.ParseCapturedTestCaseOffline` and production `graders.RunAll`, then
compares all 12 observations to the frozen independently executed upstream
oracle. Controls include positive/negative case sensitivity, composed versus
decomposed Unicode, explicit empty output and CRLF versus LF. Reverse conversion
is also tested after real native edits. The independently authored
`native-world.yaml` is a separate native control, not a substitute for testing
converted output.

Offline tests arm and verify a rejecting configured CLI process, a rejecting
default HTTP transport, a captured-source file-read callback and poisoned
ambient native configuration. These sentinels cover those named paths; they
are not a general operating-system sandbox or a claim of provider execution.
