# Release Process

This document describes how the Waza release process works. All releases are handled by the unified workflow at `.github/workflows/release.yml`.

## Cutting a Release

### Tag Push (recommended)

```bash
git tag v1.2.3
git push origin v1.2.3
```

This triggers the full pipeline: version sync → CLI build → extension build → GitHub Release → extension publish → registry + version PR (auto-merged).

### Manual Dispatch

Go to **Actions → Release → Run workflow** and fill in:

| Input | Description | Default |
|-------|-------------|---------|
| `version` | Semver without `v` prefix (e.g. `1.2.3`) | *required* |
| `build_cli` | Build standalone CLI binaries | `true` |
| `build_extension` | Build azd extension binaries | `true` |
| `publish_extension` | Publish extension to azd registry | `false` |

Manual dispatch creates the git tag automatically if it doesn't exist.

## What the Workflow Does

1. **setup-version** — Extracts version from the tag (strips `v`) or manual input. Validates semver format.
2. **build-cli** — Matrix build for 6 platforms (linux, darwin, windows × amd64, arm64). Builds the web UI then produces `waza-{os}-{arch}` binaries. Version is injected via `-ldflags`.
3. **build-extension** — Syncs `version.txt` and `extension.yaml` locally so packaged artifacts contain the correct version. Builds the web UI, then the azd extension via `azd x build` and `azd x pack`.
4. **create-cli-release** — Downloads CLI artifacts, generates SHA256 checksums, creates a **CLI GitHub Release** (`Waza vX.Y.Z`) with standalone binaries attached.
5. **publish-extension** — Runs `azd x release` to create a separate **Extension GitHub Release**, then `azd x publish` to update the registry. Creates a single PR that updates `registry.json`, `version.txt`, and `extension.yaml` together, then auto-merges it with `--admin` to bypass required status checks (since bot-created PRs don't trigger CI).

## Version File Locations

| File | Purpose |
|------|---------|
| `version.txt` | Canonical version string used by build scripts |
| `extension.yaml` | `version:` field for the azd extension manifest |
| `registry.json` | Extension registry with download URLs and checksums (updated by publish step) |

## Why Auto-Merge with `--admin`?

GitHub intentionally does not trigger workflows on PRs created by `GITHUB_TOKEN` (to prevent recursive loops). Since the registry/version PR is created by `github-actions[bot]`, the required CI checks (`Build and Test Go Implementation`, `Lint Go Code`) never run. Using `--admin` bypasses these checks. This is safe because:

- The PR only contains machine-generated changes (checksums, URLs, version strings)
- The release artifacts have already been built and validated earlier in the pipeline
- The content is deterministic and derived from the release that just completed

## Deprecated Workflows

The following workflows are superseded by `release.yml` and kept for reference only:

- `go-release.yml` — Previously handled standalone CLI releases
- `azd-ext-release.yml` — Previously handled azd extension releases
