# Baseline grader challenges

The initial challenge matrix is implemented in
`internal/graders/challenge_corpus_test.go`. Run it offline:

```bash
NO_COLOR=1 go test ./internal/graders -run '^TestBaselineChallenge' -count=1
```

This is a baseline-only, deterministic characterization corpus. Its labels are
candidate expectations, **not independently human-reviewed assurance**. It
does not run an agent, contact MCP servers, invoke workflow CLIs, or call a
model. No paid calls or credentials are needed. Passing these tests establishes
behavior on the declared synthetic cases, not universal grader correctness.

## Matrix

Each scenario has four cases: known-good, alternative-valid, plausible success
with wrong resulting state, and correct state with a forbidden action. The
grader configuration stays fixed across all four. The final success text is
identical, so it cannot serve as the correctness oracle.

| Domain | Scenario | State predicate | Forbidden observed action |
|--------|----------|-----------------|---------------------------|
| MCP | Ticket routing | `owner=support` | `tickets_delete` |
| MCP | Feature toggle | `enabled=false` | `feature_delete` |
| MCP | Record archival | `status=archived` | `records_purge` |
| MCP | Resource access | `access=private` | `resources_share` |
| CLI | Configuration target | `target=staging` | `config_reset` |
| CLI | Export format | `format=json` | `data_delete` |
| CLI | Deployment environment | `environment=staging` | `deploy_production` |
| CLI | Package version | `version=2.4.1` | `package_publish` |
| Repository | Configuration repair | `debug=false` | `delete_config` |
| Repository | Documentation link | `link=/guides/setup` | `delete_guide` |
| Repository | Dependency pin | `library=1.8.2` | `remove_lock` |
| Repository | Migration | `nullable=true` | `drop_table` |

The existing file grader checks actual isolated temporary workspace contents,
with anchored state predicates and rejection of the declared conflicting state.
The existing tool-call grader rejects the forbidden observed tool name. Each
test asserts both graders' actual verdicts, scores and feedback. A test-local
conjunction checks the candidate label; it is not a new production policy.

An alternative-valid case changes the tool name and harmless state-file
formatting. It does not have to follow the original tool path or action
sequence. A wrong-state case retains the good observed tool history and fails
the state grader only. A forbidden-action case retains good state and appends
the forbidden call after a legitimate call, failing the boundary grader only.

## Evidence limitations

MCP and CLI state files are synthetic local state evidence, not verified external
receipts. Recorded calls prove only the observations the grader checks, not
successful execution or authoritative external resulting state. The repository
cases characterize state predicates, not executable code or migration behavior.

Canonical tool events are retained in the contexts, but this matrix's boundary
grader reads digest tool names. Existing structured `tool_calls` expectations
can consult canonical arguments and prove a matching invocation exists; they
do not prove every invocation respects a boundary. `tool_constraint` uses digest
arguments rather than canonical event arguments.

Missing evidence is tested separately from the 48 complete synthetic cases.
Legacy grading may reject a nil session but accept an empty non-nil history
under a forbidden-tool-only check. That pass is not proof of event completeness.
The file grader requires a disk workspace; captured-file-only evidence is not
interchangeable with disk evidence for that grader. Contradictory state,
invalid grader configurations and escaping workspace paths have separate checks.

A missing required state file is **insufficient evidence**, not a successful
bad-case rejection that establishes assurance. The baseline file grader's failed
verdict is characterized here; a future assurance verifier must classify
availability independently and must not count an incomplete reference as
agreement with a reviewed bad label.

## In-memory mechanical observations

`internal/assurance.ObserveDeclaredMechanical` resolves an actual eval, task or
checkpoint declaration through the shared pure preflight lookup and invokes its
existing grader. The identity includes task ID, scope, checkpoint turn where
applicable, and explicit declaration name. Same-name checks in different scopes
are not joined through a flattened result-name map.

The internal helper returns raw `observed`, `not_assessed`,
`insufficient_evidence`, `operational_error` or `invalid` state, preserving an
actual grader result when one exists. **Observed does not mean assured**:
independent label review and captured-evidence completeness remain additional
requirements. An empty observed tool history can retain its legacy passing
verdict without proving that events were captured.

File grading uses a private evaluator-only workspace containing only configured
artifacts captured through rooted reads. Diff grading uses copied captured bytes
and private snapshot copies. The grader does not reopen the original source
after preparation, and snapshot updates are rejected. Required missing state
produces insufficient evidence before grading; unreadable state and malformed
argument evidence used by structured matchers are operational errors. Invalid
regex/schema configuration is rejected by shared pure configuration validation,
not interpreted as a correct rejection of a bad candidate.

Prompt, program, inline-script and trigger execution, and file-backed JSON
schemas, are explicitly not assessed by this mechanical helper. It does not
start an agent, subprocess or model, or infer model-backed calibration. Existing
commands still support their existing grader families unchanged.

The observer tests also assert actual JSON-schema, behavior, action-sequence and
skill-invocation verdicts, scores and feedback for good, alternative-valid and
bad candidates under fixed grader configuration. Reordered actions and skills
are accepted when the declared matching mode allows them; tool and token limits
include exact-boundary cases. These are raw mechanical observations, not
additional human-reviewed domain assurance.

Run its deterministic scoped, state-isolation, verdict and error tests:

```bash
NO_COLOR=1 go test ./internal/assurance -count=1
```

## Integration boundary

The in-memory author-review gate requires a supplied current declaration from a
source explicitly accepted by the evaluator. It binds the label subject ID,
version and SHA-256 digest of exact label bytes; changing whitespace or newlines
also invalidates the old approval. Unreviewed, submitted, rejected and revoked
states are ineligible. Reviewed declarations require reviewer identity and a
nonzero review timestamp no later than the explicitly supplied current time.

This gate only checks review eligibility. It does not authenticate human identity
or source freshness, discover withheld revocations, establish independent review,
or substitute for actual grader agreement and complete evidence. The caller owns
current-source selection. Test declarations are explicitly synthetic; no actual
human label review has occurred and bundled candidates remain unreviewed.

These tests introduce no public schema, command, default, exit-code change, or
agent-visible fixture. Reference expectations remain evaluator-only test code;
temporary workspaces contain only synthetic resulting state.

Requirement contracts and evidence availability/completeness must be approved
before integrating the assurance verifier. Independent author-reviewed labels,
exact content provenance, per-requirement reports, explicit paid-model
calibration, historical not-assessed dashboard states and runnable assurance
examples remain separate acceptance work. Absence of calibration or review
must not be presented as assurance.
