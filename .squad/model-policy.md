# Waza Squad Model Policy

Effective 2026-10-15. This project policy takes precedence over the generic model
selection and silent fallback guidance in Squad's managed templates. Earlier model
assignments in histories and decisions are historical, not current instructions.

## Resolve the assignment before selecting its model

1. **Code review:** use `gpt-5.6-terra`, including reviews by a member other than
   Rusty. This review-only assignment takes precedence over the member's authoring
   model. Pass it explicitly to a host that supports per-spawn model selection.
2. **Other assignments:** use `.squad/config.json`'s `agentModelOverrides` by
   lowercase member ID, then `defaultModel`. Charters document the same choices.
3. An explicit user request to change the policy wins. Do not reinterpret a generic
   request for speed or a larger context window as permission to change providers.

| Work | Preferred model | Members |
|------|-----------------|---------|
| Architecture and independent code review | `gpt-5.6-terra` | Rusty |
| Go implementation and evaluation runtime | `gpt-6.1-sol` | Linus |
| Test design and test implementation | `gpt-6.1-sol` | Basher |
| Performance and reliability analysis | `gpt-5.6-terra` | Turk |
| Web design and frontend implementation | `gpt-6.1-sol` | Yen |
| Release and developer infrastructure | `gpt-6.1-sol` | Virgil |
| Reproducible browser/CLI media capture, editing automation, exports | `gpt-6.1-sol` | Tess |
| Documentation, information architecture, developer relations | `gpt-6-luna` | Livingston, Saul, Danny |
| Bounded logging and work monitoring | `gpt-6-luna` | Scribe, Ralph |
| Responsible AI and evidence verification | `gpt-5.6-terra` | Rai, Fact Checker |

These are host model IDs, not benchmark rankings or promises of account access.
Do not replace them with older model names from the bundled catalog just because
that catalog predates the host.

## Release-media task routing

For every release, follow `docs/RELEASE-MEDIA.md`. Select the role first, then
pass the existing host's supported explicit model parameter at dispatch:

| Media task | Accountable role | Explicit model | Role-fit rationale |
|------------|------------------|----------------|--------------------|
| Browser/CLI capture, FFmpeg/editing automation, caption timing, reproducible exports and artifact manifest | Tess | `gpt-6.1-sol` | Tool-driven implementation and reproducible technical work |
| Narrative, storyboard, opening hook, script, caption copy and draft social copy | Danny | `gpt-6-luna` | Audience-aware writing grounded in verified features |
| Independent technical, claim and code review | Rusty | `gpt-5.6-terra` | Independent evidence/contract gate; never author self-approval |
| Bounded editorial documentation check, if needed | Saul | `gpt-6-luna` | Documentation consistency; not an automatic second blocking gate |

These are role-fit selections, not empirically benchmarked "best" models.
Use the supported `task` tool's `model` parameter (or the host's supported
equivalent); config retains only `agentModelOverrides` and `defaultModel`.
Do not invent per-task runtime fields or change another member's default for a
media subtask. Report the actual model used or an enforcement limitation.
Capture remains gated on verified release readiness even if a model is available;
no paid evaluations or paid media services are authorized.

## Availability and fallbacks

- Verify against the current host's advertised models. A syntactically valid ID
  does not prove entitlement, availability, or successful execution.
- For implementation, try advertised GPT alternatives in this order:
  `gpt-6-sol`, `gpt-5.6-sol`, `gpt-5.6-terra`.
- For documentation and mechanical support work, try advertised
  `gpt-5.6-luna`, then `gpt-5.4-mini`. Do not upgrade routine logging into a
  premium implementation assignment.
- Code review remains pinned to `gpt-5.6-terra`. If it is unavailable, report
  the blocker and obtain a different review-model decision; do not silently
  substitute the implementation model.
- Retry only confirmed model-availability failures, not arbitrary task, test,
  permission, or tool errors. Try each candidate at most once.
- Record an actual fallback and briefly surface the changed model. Do not
  silently omit the model parameter or switch to a non-GPT provider. If the
  GPT candidates are exhausted, ask before changing providers.
- On hosts without per-spawn model control, report that limitation. Do not claim
  that a charter or JSON preference changed the host's actual model.

## Maintaining the policy

Keep config, active charters, and the roster aligned. Do not add invented
`taskModelOverrides` or other unsupported runtime configuration keys.
After an upgrade, reapply the small Waza policy hook in `squad.agent.md` and
preserve the repository contributor hook in `.github/copilot-instructions.md`.
See `meet-the-squad.md` for upgrade and verification commands.
