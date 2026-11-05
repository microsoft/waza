# Waza Pull Request Requirements

- Use the repository PR template and reference the related issue when there is one.
- Keep the change scoped; preserve compatibility and all existing passing tests.
- Run relevant targeted checks during development. Before pushing Go code, run the
  full Go tests and required formatting/lint checks documented in `AGENTS.md`.
- For dashboard changes, run the build, lint, and Chromium Playwright suite; update
  affected screenshots. Build public docs when they change.
- Record exact validation commands and outcomes, including anything not run.
  Mock or offline success is not evidence of live model quality.
- Document changed CLI flags, YAML/results contracts, user-visible behavior, and
  private/opt-in limitations in the affected README, guides, reference, and examples.
- Select independent review; never self-approve. For Squad assignments, follow
  `.squad/governance.md` and the code-review models in `.squad/model-policy.md`.
- Resolve merge requirements from the active repository rulesets, as documented
  in [AGENTS.md](../AGENTS.md#cicd):
  `gh api repos/microsoft/waza/rules/branches/main`.
  Apply all current rules, including required statuses, approvals, code-owner
  review, last-push approval, and conversation resolution. Match required status
  contexts and enforced integration IDs to actual check runs, not workflow titles
  or a copied list of job names.
- Require passing applicable checks on the current PR head. After any source or
  base update, refresh the head, checks, and review evidence; stale, skipped, or
  unexecuted checks are not passing evidence. Do not merge or bypass protection
  without explicit user authorization. An approval override never waives failing
  checks or unresolved work.
- Do not publish releases, social announcements, or captured evidence as a PR side effect.
