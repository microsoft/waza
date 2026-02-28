# Session: Model Policy Overhaul

**Date:** 2026-02-20
**Requested by:** Scott Boyer

## Summary

Full model assignment overhaul — quality-first policy. Cost is not a constraint.

## Actions

1. Listed current model assignments per team role
2. User directed: Saul, Scribe, diversity reviews → `gemini-3-pro-preview`
3. Researched SWE-bench Verified benchmarks (Feb 2026)
4. Locked in quality-first policy:
   - Code roles (Rusty, Linus, Basher, Livingston) → `claude-opus-4.6`
   - Docs/Scribe/diversity → `gemini-3-pro-preview`
   - Heavy code gen → `gpt-5.2-codex`
5. Merged inbox decisions (model overhaul + release trigger unification)
6. Propagated model update notes to agent histories

## Decisions Merged

- `copilot-directive-model-updates.md` → decisions.md
- `rusty-release-trigger-unification.md` → decisions.md
