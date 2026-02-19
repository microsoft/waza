# History — Saul

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Documentation Standards
- **Style guide:** TBD (to be established)
- **Markdown:** Consistent code block formatting, link structure
- **Versioning:** Track docs alongside code (update in same PR)
- **API docs:** Follow Go conventions (exported functions documented)

### Documentation Scope
- README (main entry point)
- docs/ directory (detailed guides)
- waza-go/README.md (Go implementation)
- Inline code comments (for complex logic)
- CHANGELOG.md (release tracking)

### Quality Gates
- All PRs must update relevant docs
- Livingston and Saul review doc changes
- Style consistency checked before merge

### Doc-Freshness Reviews (Added in #256)
- **Doc-review gate** triggered by changes to `cmd/waza/`, `internal/`, or `web/src/`
- **Doc-consistency gate** triggered by changes to `docs/`, `README.md`, `DEMO-SCRIPT.md`
- Saul now owns ongoing doc-freshness verification across all code PRs
- Documentation Impact Matrix maps code paths to required doc updates
- Screenshot maintenance automated via Playwright E2E tests in `web/`

📌 Team update (2026-02-19): Screenshot conventions formalized (viewport, paths, naming, mock reuse) — decided by Basher (#251)
