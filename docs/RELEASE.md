# Release Process

This document describes how the Waza release process works. All releases are handled by the unified workflow at `.github/workflows/release.yml`.

## Cutting a Release

### Tag Push (recommended)

```bash
git tag v1.2.3
git push origin v1.2.3
```

This triggers the full pipeline: CLI build → extension build → GitHub Release → extension publish → version sync.

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
2. **build-cli** — Matrix build for 6 platforms (linux, darwin, windows × amd64, arm64). Produces `waza-{os}-{arch}` binaries.
3. **build-extension** — Builds the azd extension via `azd x build` and `azd x pack`. Produces platform-specific archives.
4. **create-release** — Downloads all artifacts, generates SHA256 checksums, creates a GitHub Release with all binaries attached.
5. **publish-extension** — Runs `azd x publish` to update the registry, creates a PR with the updated `registry.json`, and auto-merges it.
6. **sync-versions** — Updates `version.txt` and `extension.yaml` to match the released version, commits to `main` if changed.

## Version File Locations

| File | Purpose |
|------|---------|
| `version.txt` | Canonical version string used by build scripts |
| `extension.yaml` | `version:` field for the azd extension manifest |
| `registry.json` | Extension registry with download URLs and checksums (updated by publish step) |

## Deprecated Workflows

The following workflows are superseded by `release.yml` and kept for reference only:

- `go-release.yml` — Previously handled standalone CLI releases
- `azd-ext-release.yml` — Previously handled azd extension releases
