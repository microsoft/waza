# Offline preserved-native mechanical example

The runnable corpus is
[`internal/assurance/preserved_mechanical_test.go`](../../../internal/assurance/preserved_mechanical_test.go);
the integrated returned-report example is
[`internal/assurance/verify_preserved_test.go`](../../../internal/assurance/verify_preserved_test.go).
They invoke existing native graders over complete synthetic selected artifacts.
Review declarations are synthetic, not independent human certification. No
engine factory, runtime credentials, task execution or paid calibration is used.

```bash
NO_COLOR=1 go test ./internal/assurance -run 'Preserved' -count=1
```

The raw corpus includes good, alternative-valid, wrong-state and forbidden-action
cases across the 12 domains/scenarios. The tool path can vary where the native
declaration permits it. Tests assert actual grader verdicts, scores and feedback,
not completeness metadata as a substitute for results.

## Separate operation

An evaluator supplies an admitted eval/task inventory, original reference and
snapshot bytes, a selected current review source and a caller-owned evidence
root through the existing `assurance.VerifyRequest`.

```go
report, err := assurance.VerifyPreserved(ctx, selectedInputs)
if err != nil {
    return report, err
}
if report.State != assurance.AssessmentPassed {
    return report, fmt.Errorf("preserved assurance did not pass: %s", report.State)
}
return report, nil
```

Only `assurance.ParsePreservedReport` admits this standalone report `1.2` with
operation `preserved_native_mechanical_assurance`. Frozen offline `1.0` and
calibrated `1.1` readers reject it. Default CLI and dashboard dispatch are
unchanged; this example does not enable a new command or consumer import.

The adapter validates original raw JSON, required field presence, native
origin/manifest joins, whole artifact locators and selected content hashes.
Its native-declaration binding identifies the current selected eval/task grader,
not the historical grader or source version that produced the snapshot. This
operation does not certify historical grader correspondence.
Explicit complete unredacted empty tool tapes are specific to this operation;
the existing nonempty-tape importer remains unchanged. Missing/null consumed
arguments are insufficient; explicit `{}` retains its native grading behavior.
Canonical tool-call argument checks and normalized digest-based constraints are
not interchangeable.

Only tool calls, tool constraints, action sequences and required-file
existence/content are supported. File subsets cannot prove absence. Raw snapshot
admission is capped at 16 MiB, decoded selected files at 4 MiB each/8 MiB total,
and selected paths at 256. Historical unknown completeness remains unknown.
Selecting calibration remains nonpass/not assessed with null billing and no
execution ledger.

Pre-observation unavailable/invalid states have no grader result. A late
operational failure such as cleanup may retain an actual native result, but
agreement stays null and the report remains nonpass.

## Actual returned-report proof

With an absolute rejecting runtime guard already verified to exit 64:

```bash
COPILOT_CLI_PATH=/absolute/path/to/verified-rejecting-guard \
  ENABLE_COPILOT_TESTS=false NO_COLOR=1 \
  WAZA_PRESERVED_REPORT_DIR="$PWD/.cache/preserved-reports" \
  go test ./internal/assurance \
  -run '^TestVerifyPreservedActualNativeResultsAndExport$' -count=1
```

The test exports actual positive and nonpass reports. These are finite synthetic
controls, not authenticated historical execution or human review. Exact frozen
reader proofs must use those unchanged returned bytes plus actual offline `1.0`
and calibrated `1.1` positive controls, never version-renamed conversions.
