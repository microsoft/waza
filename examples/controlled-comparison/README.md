# Offline controlled comparison example

This mock/native-text example exercises base 1.0 planning and paired collection.
It intentionally stays statistically inconclusive: its single deterministic
cluster does not establish the independence or precision needed for a release
claim. It is not real-agent quality evidence.

Run the base workflow in [the controlled comparison guide](../../docs/CONTROLLED-COMPARISONS.md#offline-planning-and-collection).
Do not overwrite an existing policy, adopt historical outcomes, or resume an
interrupted collection.

## Optional independent offline assurance

The bundled example is **not** a ready-to-pass assurance fixture: it has no
authored reference corpus or review declaration. Examples are unreviewed by
default. Test-only reviews elsewhere are explicitly synthetic, not authenticated
human review or production review evidence.

To author a separate qualified example, require assurance in the base policy,
declare every native-text endpoint through task requirement/check mappings, and
provide exact-bound per-arm evaluator-only reference labels and finite authored
output envelopes using the [existing challenge contracts](../../docs/GRADER-CHALLENGES.md#strict-file-content-assurance).
Use `compare-assurance-plan --release-policy ... --baseline-eval ...
--candidate-eval ... --baseline-references ... --candidate-references ...
--output ...` to create a new independent contract. Collection and assessment
must explicitly select `--assurance-contract` plus `--release-policy`, with
current per-arm sources; assessment also needs `--collection-dir`.

Review files and per-arm `--accept-review-source` are separate current inputs.
They do not authenticate a reviewer. Never change an example's unreviewed state
merely to obtain a pass. Fresh exact offline1.0 reports for both arms,
deterministic regrade of retained actual responses, and **all** base
non-assurance pass conditions are necessary. This example's statistical
limitations cannot be erased by grader qualification.

Only mock/no-skills/native-text/finite-authored-output execution is supported.
No live SDK, calibration, paid1.1/billing support or automatic paid validation is
selected. Required-assurance base decisions, the API and dashboard remain
nonpassing; only explicitly selected outer CLI assessment can pass.
See [the profile lifecycle, schemas and limitations](../../docs/CONTROLLED-COMPARISONS.md#independent-offline-assurance-profile).
