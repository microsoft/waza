# Explicit offline calibration example

The runnable example is the deterministic injected-engine corpus in
[`internal/assurance/calibrate_test.go`](../../../internal/assurance/calibrate_test.go).
It calls the real `assurance.Calibrate` API, invokes native grade callbacks for
good, alternative-valid and critical-bad finite inputs, and exports returned
reports. Review decisions are explicitly synthetic test fixtures, not human
certification. No production engine factory, live credentials or paid calls are
used.

With an absolute rejecting native-runtime guard already verified to exit 64:

```bash
COPILOT_CLI_PATH=/absolute/path/to/verified-rejecting-guard \
  ENABLE_COPILOT_TESTS=false NO_COLOR=1 \
  go test ./internal/assurance \
  -run 'TestCalibrateActualNativePositiveCorpus|TestCalibrateReviewAndNativeBindingsBeforeNotice|TestCalibrateResponseOnlyFailureCannotCertifyGoodOrBadCallback' \
  -count=1 -timeout=120s
```

`TestCalibrateExportActualOfflineReports` additionally writes three actual
returned API reports when `WAZA_CALIBRATION_REPORT_DIR` selects an absolute directory under this worktree's
`.cache/` (for example, `$PWD/.cache/assurance-reports`): positive calibrated `1.1`, operational-failure calibrated
`1.1` retaining nonzero known accounting, and an offline native `Verify` `1.0`
control. The **Offline Grader Assurance** CI workflow runs this export, exact
frozen-reader rejection/acceptance controls and explicit browser inspection.

## Callable shape

An evaluator supplies already admitted native declarations, exact source bytes,
reference documents, current supplied review and both borrowed evidence/rubric
roots. The injected factory implements `assurance.CalibrationEngine`; it must
only construct an owned engine, never initialize or execute inside the callback.
There is no default factory.

```go
request := assurance.CalibrateRequest{
    VerifyRequest: selectedInputs,
    RubricRoot: rubricRoot,
    EngineFactory: injectedOfflineFactory,
    PaidCallNotice: func(plan assurance.CalibrationPlan, uniqueExecutions int) error {
        // Record the unchanged reviewed plan and actual admitted count.
        // Provider follow-up calls and spending are not bounded by this count.
        return recordNotice(plan, uniqueExecutions)
    },
}
request.Calibrate = true
request.Acceptance = assurance.ReviewSourceAcceptance{
    SourceID: explicitlySelectedCurrentSource,
    AcceptCurrentDecision: true,
}
report, err := assurance.Calibrate(ctx, request)
if err != nil {
    // A nonnil report may retain partial execution and known accounting.
    return report, err
}
if report.State != assurance.AssessmentPassed {
    return report, fmt.Errorf("calibration did not pass: %s", report.State)
}
return report, nil
```

Selecting the current review source does not authenticate a reviewer or discover
withheld revocations. Missing factory/notice, refusal, unavailable evidence,
unsupported rubric modes and ineligible review remain non-passing. Golden/
reference-answer field presence is rejected before notice or initialization.
Operational execution failure cannot become correct negative rejection, even
after a native callback. Cleanup uses a cooperative deadline, not forced shutdown.

Default offline `Verify`, `waza assure`, and legacy `waza assure --calibrate`
remain adapter-free and report version `1.0`. The separately selected
`waza assure calibrate` child command produces version `1.1`; it requires
explicit references, current review-source acceptance, a rubric root, and
`--accept-paid-calls` before its production factory can be invoked. It prints
the reviewed plan and admitted execution count before construction. That count
and the cooperative timeout are not a provider-call or spending cap.

Do not run the paid child command to reproduce this offline example. Its CLI
tests inject engines and cover refused consent, notice/output failures, partial
reports, cancellation and strict-pass exits without credentials or paid calls.
Calibrated version-`1.1` inspection requires separate explicit selection.
Tested client policy/guards and finite-corpus agreement do not certify server
isolation, universal correctness or measured live-model confidence.
