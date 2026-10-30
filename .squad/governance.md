# Waza Squad Governance

Effective 2026-10-15. This is the project ownership and review policy; the roster
and dispatch map live in `team.md` and `routing.md`.
These Squad coordination rules do not apply to ordinary Copilot work.

## Ownership

Squad membership, issue routing, and implementation consultation are AI-only.
Do not assign issues or implementation handoffs to humans, including as an
overflow fallback. Linus covers Copilot SDK/backend work and Rusty covers
architecture/independent review; report uncovered expertise to the coordinator.
This does not change the project owner's authority, repository collaborators,
CODEOWNERS, or required independent native approvals. Existing reviewer/author
lockouts survive roster changes; do not rename or add identities to evade them.

Every task has one accountable specialist. Add bounded collaboration only when a
concrete dependency, missing expertise, or selected review gate requires it.
Do not launch the whole team, anticipatory QA, or routine log-only agents by default.

Rusty owns cross-package architecture and scope. Linus owns Go/runtime behavior,
Yen owns frontend/design, Basher owns test strategy, Turk owns measured performance,
Livingston owns technical writing, Saul owns documentation architecture, Danny owns
draft socialization, Tess owns release-media capture/edit/export, and Virgil owns
release/developer infrastructure and release-readiness verification. Tess's
membership does not clear rejected-author lockouts or grant Go revision ownership.

Scribe, Ralph, Rai, and Fact Checker are support identities, not specialist routing
destinations. Retain their charters and activate only for necessary bounded work.
The inactive Scribe casting record is a legacy historical binding, not a retired
support agent; new support work uses the fixed identity outside the specialist registry.

## Gates

- Ordinary implementation work gets one independent, qualified review gate.
  Rusty normally owns code review; Saul normally owns documentation review.
- If the gate authored the work, select another qualified member, never self-approve.
  All code-review assignments use Terra under `model-policy.md`, regardless of authoring model.
- High-risk contract, evidence, privacy, or publication changes may have one primary
  gate plus one specifically justified orthogonal gate, not a routine review fan-out.
- A documentation-impact check is part of acceptance, not automatically another
  mandatory reviewer. Resolve the selected gate and required docs before dispatch.
- After rejection, assign a different qualified revision owner. The rejecting gate
  does not implement, pair on, or advise the immediate revision; it performs a
  focused rereview of blockers, the delta, and regressions.
  The rejection includes concrete counter-evidence, the violated requirement,
  affected scope, and minimum condition for approval.
  After successful focused rereview of the independently corrected revision, the
  selected gate may issue approval/PASS. This is a review verdict, not native
  repository approval, merge or publishing authority; author lockout persists
  throughout the revision cycle.
- Rai and Fact Checker return findings in the current review response.
  Charter output/persistence restrictions take precedence over generic history,
  audit-trail or decision-inbox instructions. Only an authorized coordinator or
  Scribe handles required persistence under the current state contract; no
  routine review logging is required.

Repository merge requirements remain in `.github/PR_REQUIREMENTS.md`.

## Guardrails

Preserve historical decisions, member names, histories, and user-owned state.
Current team/model policy supersedes earlier model directives without rewriting
their historical claims. Never work in a different checkout or mutate unrelated files.

No paid eval, release publication, social post, outbound message, credential change,
merge, or autonomous watcher is implied by an ordinary implementation/status request.
Report unavailable evidence and actual blockers instead of fabricating successful
checks, benchmark improvements, or host-model control.
