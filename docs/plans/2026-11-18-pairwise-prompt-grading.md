# Pairwise Prompt Grading Design

## Goal

Make `mode: pairwise` compare the skills-enabled output with its corresponding
skills-disabled baseline output, without silently falling back to independent
grading when either observation is unavailable.

## Design

When a baseline evaluation includes pairwise graders, run the skills-disabled
pass first. Capture each successful baseline output in memory by `TestID` and
`RunNumber`, replacing it on each retry so the final attempt is selected.
Track output presence separately from its contents so a successful empty
output remains a valid observation.

Before starting the skills-enabled pass, verify that every selected task/trial
with pairwise grading has a successful baseline output. Supply that output and
its presence bit to the treatment grader context. Pairwise graders are omitted
from the baseline pass; all other baseline behavior is unchanged. Baseline
evaluations without pairwise graders retain their current phase order.

The prompt grader rejects missing candidate or baseline observations rather
than using independent grading. Pairwise checkpoint graders and saved-result
regrading are explicitly unsupported and rejected before judge calls.
Existing position-swapped judgments and grader-session usage accounting remain
the execution path. Pairwise partial outcomes identify the baseline phase and
include its completed result during treatment execution.

## Verification

- Offline grader tests cover empty-but-present and missing outputs.
- Orchestration tests cover task/trial pairing, final-attempt selection,
  baseline-phase grader suppression, missing-baseline rejection, and partial
  outcomes.
- CLI tests verify pairwise saved-result regrading is rejected before
  workspace or judge work.
- Update grader, CLI, and eval YAML documentation for phase order and limits.
