# Waza Squad Governance

Effective 2026-10-15. This is the project ownership and review policy; the roster
and dispatch map live in `team.md` and `routing.md`.

## Ownership

Every task has one accountable specialist. Add bounded collaboration only when a
concrete dependency, missing expertise, or selected review gate requires it.
Do not launch the whole team, anticipatory QA, or routine log-only agents by default.

Rusty owns cross-package architecture and scope. Linus owns Go/runtime behavior,
Yen owns frontend/design, Basher owns test strategy, Turk owns measured performance,
Livingston owns technical writing, Saul owns documentation architecture, Danny owns
draft socialization, and Virgil owns release/developer infrastructure.

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

Repository merge requirements remain in `.github/PR_REQUIREMENTS.md`.

## Guardrails

Preserve historical decisions, member names, histories, and user-owned state.
Current team/model policy supersedes earlier model directives without rewriting
their historical claims. Never work in a different checkout or mutate unrelated files.

No paid eval, release publication, social post, outbound message, credential change,
merge, or autonomous watcher is implied by an ordinary implementation/status request.
Report unavailable evidence and actual blockers instead of fabricating successful
checks, benchmark improvements, or host-model control.
