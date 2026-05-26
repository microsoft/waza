# Repo Resources Example

Demonstrates `inputs.repos` — materialize a clean copy of a local git
repository into each task's per-run workspace via `git worktree add`.
Useful when you're developing a skill inside the same repo the skill is
intended to operate on.

## Setup

Edit `tasks/explain-repo.yaml` and replace `SOURCE_REPO_PATH` with the
absolute path to a local git clone you want the agent to reason about.

## Run

```bash
waza run examples/repo-resources/eval.yaml -v
```

Each task run gets a fresh worktree under `<workspace>/my-repo`, and the
agent starts in that directory because of `inputs.workdir: my-repo`. The
worktree is removed with `git worktree remove --force` when the engine
shuts down, so the source repo's `.git/worktrees/` stays clean.

See the `Git Repository Resources` section of the project `README.md`
for the full field reference and notes on what's out of scope today
(HTTPS / SSH clone, submodules, LFS, auto-detection).
