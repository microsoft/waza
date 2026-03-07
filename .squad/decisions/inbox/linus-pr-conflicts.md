# Linus: PR conflict resolution decision

For PR conflict fixes against `main`, use an isolated worktree and merge `origin/main` into the PR branch instead of rebasing when the branch is already published. When `main` already contains newer token-limit warning behavior and fixture coverage, keep the newer `main` implementation and preserve branch-specific intent with targeted docs/examples or assertions rather than reintroducing older warning text.
