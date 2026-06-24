# Release Process

This document describes how the Waza release process works. CLI and azd extension releases are handled by the unified workflow at `.github/workflows/release.yml`; container image verification and ADC runner image publishing are handled by `.github/workflows/docker-ci.yml`.

## Cutting a Release

Create and push a semver tag:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

This triggers the full pipeline: CLI build -> extension build -> GitHub Release -> extension publish -> ADC runner image publish -> version sync -> GitHub Pages deploy.

## What the Workflow Does

1. **setup-version** - Extracts the version from the pushed tag (strips `v`) and validates semver format.
2. **build-cli** - Matrix build for 6 platforms (linux, darwin, windows x amd64, arm64). Builds the web UI then produces `waza-{os}-{arch}` binaries.
3. **release-cli** - Downloads CLI artifacts, generates SHA256 checksums, and creates the **CLI GitHub Release** (`Waza vX.Y.Z`) with standalone binaries attached.
4. **release-extension** - Syncs `version.txt` and `extension.yaml`, builds the web UI, builds and packs the azd extension, creates the **Extension GitHub Release** (`Waza azd Extension vX.Y.Z`), publishes to the azd registry, then opens a PR with updated `registry.json` and synced version files.
5. **docker-ci.yml** - Builds and verifies container images. For `Dockerfile.adc-runner`, publishes `ghcr.io/microsoft/waza/adc-runner` on `main`, `vX.Y.Z` tags, and manual dispatch.
6. **pages.yml** - Deploys the documentation site after the `Release` workflow completes successfully, ensuring release notes and download links published to `site/` are pushed to GitHub Pages.

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
