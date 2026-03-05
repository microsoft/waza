# History — Livingston

## Project Context
- **Project:** waza — CLI tool for evaluating Agent Skills
- **Stack:** Go (primary), React 19 + Tailwind CSS v4 (web UI)
- **User:** Shayne Boyer (spboyer)
- **Repo:** spboyer/waza
- **Universe:** The Usual Suspects

## Key Learnings

### Documentation Structure
- **Main files:** README.md, docs/, waza-go/README.md
- **Key sections:** Usage, examples, CLI flags, agent integration
- **API docs:** BenchmarkSpec, TestCase, EvaluationOutcome, Validator interface
- **Update requirement:** Must stay in sync with code changes

### Waza Concepts
- Evaluation specs (YAML format)
- Task definitions with fixtures
- Validator registry (extensible grading)
- Agent execution (Go engine, fixture isolation)
- Results and scoring

### CI/CD
- Workflows defined in .github/workflows/
- Branch protection enforces docs stay current
- Changelog tracking for releases

### GUIDE.md Patterns (Issue #253)
- **Structure:** Overview → Installation → Quick Start → Command Reference → Advanced → Dashboard → Troubleshooting
- **Key principle:** All examples use Go CLI only (no Python, no venv, no legacy references)
- **Installation methods:** Binary (recommended), from source, azd extension
- **Quick Start:** 5-step workflow — init → new → define → run → serve
- **Command reference:** Detailed flags and examples for init, new, run, check, serve
- **Advanced sections:** Caching, filtering, parallel execution, multi-model comparison, CI/CD, session logging
- **Dashboard:** Pages are home/dashboard, run details, compare, trends, live view (from web/src/App.tsx routing)
- **Troubleshooting:** Port conflicts, missing results, fixture paths, validation issues
- **File paths:** docs/GUIDE.md is the canonical user guide; links to GETTING-STARTED.md for step-by-step; references examples/ for runnable code

### CLI Command Implementation Details
- **init:** Creates skills/, evals/, .github/workflows/eval.yml, defaults to claude-sonnet-4.6
- **new:** Two modes (project vs standalone), interactive wizard for TTY, non-interactive for CI/CD
- **run:** Accepts eval.yaml OR skill-name OR auto-detect; supports filtering (--task, --tags), parallel (--workers), multi-model (--model), caching (--cache, --cache-dir), output formats (--format), session logging
- **check:** Validates compliance (Low/Medium/Medium-High/High), token count, eval presence; supports auto-detect
- **serve:** HTTP dashboard (default port 3000), can also run JSON-RPC TCP (--tcp :9000) or stdio
- **Exit codes:** 0 = success, 1 = test failed, 2 = configuration/runtime error

### Web Dashboard Routing
- Pages in App.tsx: home (Dashboard), run (RunDetail), compare (CompareView), trends (TrendsPage), live (LiveView)
- Features: live updates, search, filtering by status/tags/date, export, dark mode

📌 Team update (2026-02-19): Documentation maintenance gates established (Saul reviews PRs for doc impact) — decided by Saul (#256)


## 📌 Team update (2026-02-20): Model policy overhaul

All code roles now use `claude-opus-4.6`. Docs/Scribe/diversity use `gemini-3-pro-preview`. Heavy code gen uses `gpt-5.2-codex`. Decided by Scott Boyer. See decisions.md for full details.

## 📌 Issue #89 Completion (2026-03-05): CI/CD Integration Guide

**Completed:** Comprehensive CI/CD integration guide for GitHub Pages docs site

**What I learned about CI/CD patterns in waza:**
- **Real workflows:** .github/workflows/waza-eval.yml shows production patterns — context-dir detection, input-driven dispatch, artifact uploading
- **Installation methods:** Sidebar already had ci-cd entry, file was minimal (GitHub Actions only)
- **Expansion scope:** Task requested GitHub Actions + Azure DevOps + GitLab CI + best practices
- **Sidebar config:** astro.config.mjs sidebar array already includes `{ label: 'CI/CD Integration', slug: 'guides/ci-cd' }` — no changes needed
- **MDX components:** Used Tabs/TabItem for platform-specific secrets management, Aside for warnings

**What the new guide covers:**
1. **Installation:** azd extension, binary, source
2. **GitHub Actions:** Quick start, multi-model matrix, token budget checks, path filtering, caching
3. **Azure DevOps:** Full YAML with matrix strategy, token budget checks
4. **GitLab CI:** Portable example with parallel matrix
5. **Best Practices:** 8 practices covering filters, caching, timeouts, fail-fast, budgets, secrets
6. **Troubleshooting:** PATH, timeouts, token auditing, rate limits
7. **Advanced:** Baseline comparison, multi-stage pipelines

**Key decisions:**
- Based all workflow examples on real patterns from go-ci.yml, waza-eval.yml
- Used actual azd extension install pattern from repo
- Kept examples concise but complete
- Emphasized path filtering to save CI/CD costs
- Cross-platform secrets management with Tabs component
- Focused on waza tokens diff for budget enforcement (issue #81 pattern)

**Site build verified:** All 14 pages built successfully, ci-cd page included

**Branch:** squad/89-ci-integration-guide → PR to microsoft/waza main
