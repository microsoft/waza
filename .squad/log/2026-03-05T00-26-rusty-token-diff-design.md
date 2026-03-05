# Session: 2026-03-05 — Token Diff Distribution Strategy (Issue #81)

**Agent:** Rusty (Lead / Architect)
**Context:** Analyzed distribution options for GitHub Action token budget PR comment feature
**Scope:** Issue #81 — waza tokens diff CLI + lightweight wrapper

## Outcome

**APPROVED:** CLI-first architecture.

**Recommendation:** Implement `waza tokens diff` as a core CLI command with optional thin GitHub Action wrapper. This serves all audiences (standalone binary users, GitHub Action users, azd extension, non-GitHub CI) without vendor lock-in or semantic inversion.

## Key Design Decisions

1. **Primary:** `waza tokens diff [ref1] [ref2]` CLI command
   - Outputs JSON or formatted table
   - Reuses `compare` command logic (~100–150 LOC)
   - Works on any Git-aware CI platform

2. **Secondary:** `.github/actions/token-diff/action.yml` wrapper (~20 LOC)
   - Calls CLI command, posts PR comment
   - Optional convenience layer for GitHub users
   - No lock-in; users can call CLI directly

3. **Distribution:** Users choose their path
   - **GitHub:** Use action, or call CLI in workflow
   - **Non-GitHub CI:** Use CLI directly
   - **azd users:** `azd waza tokens diff` (auto-wrapped)

## Alternatives Rejected

- **Action-only:** Vendor lock-in; semantically wrong
- **CLI-only:** Ignores users wanting PR automation
- **azd extension:** Redundant; doesn't solve root problem
- **Template:** No central maintenance; manual sync burden

## Implementation Notes

- Add `diff.go` to `cmd/waza/tokens/`
- Reuse score/limit logic from `compare.go`
- Git ref parsing (default: `origin/main..HEAD`)
- Exit code 1 if `--strict` and limits exceeded
- Unit tests + e2e action test

## Related

- Issue #81
- Decisions logged to `.squad/decisions.md` (2026-03-05 entry)
