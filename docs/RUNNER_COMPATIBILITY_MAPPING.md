# Runner compatibility developer mapping reference

## Preparation boundary

The immutable starting contract is #782 commit
`80209b9f2e1bd5d028bca9fe0674f83f4f6d1b62`. This preparation adds only
`internal/runnercompat`, `cmd/waza/cmd_runner_adapt*.go` and dedicated docs.
It does not edit the command root, shared runner service, schemas, README,
changelog, site navigation, workflows, runtime lifecycle or result publication.
No sibling PR or stack metadata belongs to this preparation branch.

The final #761 layer must be based on the actual pushed execution, identity and
terminal predecessor, not this earlier #782 parent. Its acceptance includes
production routing and comparison through the actual runner service.
Constructor tests do not satisfy that missing integration.

## Public Go API

```go
request, err := runnercompat.AdaptLocal(evalBytes, taskBytes, runnercompat.Options{
    EvalPath: evalPath,
    TaskPath: taskPath,
    RunKey: runKey,
    Selection: runnercontract.Selection{
        Shard: shard, Task: taskID, Trial: 1, Attempt: 1,
    },
    TimeoutMS: nil, // Inherit the effective local timeout; pointer asserts equality.
})
```

`AdaptLocal` returns `(*runnercontract.Request, error)`, with nil request on
every error. It uses `models.ParseCapturedEvalSpecOffline`,
`models.ParseCapturedTestCaseOffline`, `graders.ValidateConfig` and
`runnercontract.Parse`. It neither reads the supplied filenames nor creates an
engine, subprocess or runtime client. A rejecting prompt-file reader is passed
to the native task loader. Source bytes remain caller-owned and unchanged.

The library maps **captured sources**, not an entire ambient local invocation.
`EvalPath`/`TaskPath` are lexical provenance used to verify the sole configured
task filename; they are not a confinement or symlink-equivalence guarantee.
The caller must first resolve and reject unsupported local invocation state,
including effective context fixtures and flags. The CLI constructor performs
bounded reads of the two named regular files and rejects the default
eval-relative fixture entry. It makes no filesystem snapshot/transaction or
host-isolation claim.

## Field mapping

| Local source | Request destination or constraint |
|---|---|
| Original eval bytes | `input.eval`, byte-for-byte, not marshaled `EvalSpec`. |
| Original task bytes | `input.task`, byte-for-byte, not marshaled `TestCase`. |
| Caller run key | `run_key`, validated by the pinned runner request schema. |
| Explicit shard | `selection.shard` and `input.shard`, equal. |
| Sole task `id` | Must equal explicit `selection.task`; selected filename must match the one literal eval task entry. |
| Trial and attempt | Exactly 1; eval trials must equal 1 and max attempts must be omitted/0/1. This adapter does not schedule a selected member of a larger batch. |
| Eval timeout + task override | Native precedence; task omitted/null inherits, present positive task value overrides. Convert seconds to milliseconds only after enforcing 1..86400 seconds. |
| Optional timeout assertion | Must equal the resolved timeout. Explicit 0 is an error, not inheritance. |
| Eval schema version | Validated by the existing captured loader, retained unchanged. Runner schema version stays independently 1.0. |
| Task field presence | Original null/empty/omitted/false declarations retained; supported native defaults are checked, unsupported fields rejected even when empty. |
| Runtime capabilities | Exactly `exact-unit`, `inline.mock-text`; optional list is empty, paid consent false. No native or terminal-result capability is invented. |
| Text checks | Both scopes and output expectations are retained, statically validated, collision checked, never flattened or dropped. |
| Historical outputs | No conversion. Existing local writer versions, usage absence/zero, IDs and exit policy must remain with their historical owners in final integration. |

See the [user matrix](RUNNER_COMPATIBILITY_MIGRATION.md) for all intentional
incompatibilities. Admission is conservative: declarations outside the adapter
allowlist fail even when ordinary native loading might ignore them.

## Exact final-layer wiring required

1. In `newRunnerCommand`, add `cmd.AddCommand(newRunnerAdaptCommand())`.
   The prepared constructor is intentionally not registered here; tests invoke
   it directly without pretending it is already reachable from the real root.
2. Encode the returned request once and pass it to the settled
   `runnerunit.Validate([]byte) Report` / `NewService(EngineFactory).Run(ctx,
   []byte) Report` boundary. Keep request parsing/schema and negotiated
   capabilities unchanged. Never interpret `waza.runner-unit` 1.0 as a #760
   terminal envelope.
3. Implement an explicit eligible local `run` routing path using the same
   adapter. Resolve real local configuration first. Do not accept cache,
   snapshot, skip-grader, parallel/baseline, override, fixture, policy, skill or
   output-side-effect flags merely because source YAML passes. Unsupported
   adapter mode must error; unchanged historical commands can retain their
   original implementation, but must not be relabeled adapted.
4. Preserve local `EvaluationOutcome` serialization, source/task identities,
   billing availability, existing run exits (0/1/2), grading failure policy and
   requested side effects. An invocation-local runner report is not a local
   evaluation artifact. Do not fabricate an aggregate from incomplete evidence.
5. Add actual root CLI and API/production-service execution comparisons against
   the frozen observation below, including failed checks and alternative-valid
   configurations. Assert source selection and no adjacent execution. Extend
   coverage to timeout/error handling, explicit null/empty/disabled paths and
   every supported flag. Rerun compatibility and current-head checks after
   the last integration change.
6. Publish the user/developer material on the documentation site with navigation,
   affected command links, executable final-head examples and browser checks.
   Integrate the release-note facts into shared release docs only once true.
   **No Lab UI change is required for #761.**

## Frozen historical control and provenance

Observed 2026-10-28 on macOS arm64, Go 1.26.4. The oracle was produced by
building the independent pre-adapter tree
`80209b9f2e1bd5d028bca9fe0674f83f4f6d1b62` from `git archive` outside project
ancestry, then invoking that binary against the fixed eval and task. It was not
produced by the new adapter, inferred from #779's proposed oracle, or certified
by comparing two new implementations.

```bash
git archive 80209b9f2e1bd5d028bca9fe0674f83f4f6d1b62 | tar -x -C "$BASELINE"
cd "$BASELINE"
go build -o "$BINARY" ./cmd/waza
ENABLE_COPILOT_TESTS=false COPILOT_CLI_PATH="$REJECTING_RUNTIME" \
  WAZA_NO_UPDATE_CHECK=1 "$BINARY" run "$INPUT/custom-suite.yaml" \
  --output "$OUTPUT/baseline-result.json" --no-summary
```

`testdata/local-observation.original` retains the complete raw run observation
except the specifically volatile `duration_ms`, `workspace_dir`,
`session_digest.session_id` and each validation `duration_ms`. Its assertions
retain full prompt/output, selection cardinality, attempts, all five verdicts,
scores, weights, details, feedback, unavailable detail fields and tool inventory.
Command tests separately assert writer version 1.7, eval/task identity and one
task/run. They preserve raw null-versus-empty in the oracle comparison and
mutation-test prompt, score precision, passed state, optional detail shape,
attempt and output drift.

| Frozen file | SHA-256 |
|---|---|
| `testdata/eval-1.4.yaml` | `baefad0b21ccd27037174fa05ab8389673e91bc7e916bac53827399ac0a42ec4` |
| `testdata/task.yaml` | `1c007c8d56b3642bf9bf0fc97891ea64be9684c28dd7a53e49c655ff723f9654` |
| `testdata/local-observation.original` | `68d40e6e2b6bd5a408f2c3070efb9e51e06f564c6e26fae4234b2b2a7765a372` |

Paths in this table are relative to `internal/runnercompat`. Local
`.gitattributes` disables text conversion for these originals/inputs and tests
pin their checksums. Existing compatibility and native-snapshot frozen
fixtures/checksums are untouched.

The verified reuse candidate was #779 head
`4fdbaf62e9dcf4ded2055c1919544973bc42761d` against actual source base
`6c100c73786c6440f564f6df114ca2fbd473f167`, fetched into
`refs/pr-stack/source/779` from the matching `microsoft/waza` remote. Its
byte-capture approach informed this isolated adapter; runtime/archive/native
logic, proposed release/blog claims, histories and its oracle were not imported.

## Focused checks

With an absolute verified executable that rejects Copilot runtime launch:

```bash
ENABLE_COPILOT_TESTS=false COPILOT_CLI_PATH="$REJECTING_RUNTIME" NO_COLOR=1 \
  go test ./internal/runnercompat ./cmd/waza \
  -run 'TestAdapter|TestRunnerAdapt' -count=1
```

This selects adapter schema/presence/mapping tests, offline HTTP/ambient-config
sentinels, constructor I/O/failure tests, historical local-command fixtures,
LF/CRLF controls and oracle mutation checks. It is not production
adapted-execution equivalence. Repository vet, full Go tests, actual lint and
`NO_COLOR=1 make test-compat` remain required; unavailable/failed families must
be reported, not relabeled passing.
