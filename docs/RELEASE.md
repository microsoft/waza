# Release Process

This document describes how Waza validates and publishes releases. The canonical
validation contract is `.github/workflows/release-validation.yml`; every retained
publisher must complete that workflow successfully for the exact source SHA it
publishes.

The tag workflow retains GitHub's success-only dependency conditions for each
builder and publisher. Required validation and evidence-verification steps may
not be skipped or tolerate failures; workflow regression controls exercise those
bypasses on both LF and CRLF checkouts.
Windows tests use the same proven native rejecting runtime guard as ordinary Go
CI, and fail on its launch marker even if a test tolerates a rejected launch.
Release Linux and Windows suites disable Go's test-result cache with `-count=1`
so their successes require fresh execution under the current run's guard.

## Cutting a Release

Create and push a semver tag:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

This triggers the full pipeline: exact-source validation → CLI build → extension
build → GitHub Release → extension publish → version sync → GitHub Pages deploy.

## What the Workflow Does

1. **validation** — Calls the reusable release validation workflow for the tag's
   exact SHA. It requires the four native Linux race/atomic shards, the remaining
   Linux race/atomic suite, native Windows race tests, vet, the checksum-pinned
   lint schema, fail-closed offline integration, exhaustive shard inventories,
   and complete digest/source evidence. Missing, failed, cancelled, skipped,
   stale-SHA, or incomplete evidence fails validation.
2. **setup-version** — Extracts the version from the pushed tag (strips `v`) and validates semver format.
3. **build-cli** — After validation succeeds, matrix-builds 6 platforms (linux,
   darwin, windows × amd64, arm64). Each binary is accompanied by its SHA256
   digest and exact source SHA.
4. **release-cli** — Rejects missing or stale matrix evidence, verifies every
   digest, and creates or updates the **CLI GitHub Release** (`Waza vX.Y.Z`).
5. **release-extension** — After validation succeeds, syncs `version.txt` and
   `extension.yaml`, builds and packs the azd extension, records the exact source
   SHA and package digests in `release-provenance.txt`, creates the **Extension
   GitHub Release** (`Waza azd Extension vX.Y.Z`), publishes to the azd registry,
   then opens a PR with updated `registry.json` and synced version files.
6. **pages.yml** — Deploys the documentation site after the `Release` workflow completes successfully, ensuring release notes and download links published to `site/` are pushed to GitHub Pages.

The retained `Squad Release` (`main`) and `Squad Insider Release` (`insider`)
publishers first perform a lightweight exact-tag check. Untagged branch pushes
stop there and rely on ordinary push CI instead of repeating the full release
suite. A tagged branch head calls the same reusable validation workflow and
publishes only its downloaded, digest-verified tested binary. Stable and insider
release assets include `waza.sha256` and `source-sha.txt`.
Generated `-insider` tags are excluded from candidate detection so retries still
select the single base tag. New insider tags explicitly target the validated SHA.
Tag lookup failures stop admission instead of being interpreted as an untagged
head. Tested-binary sidecars must match both the digest and the `waza` filename
before the binary can execute or publish.

The six CLI matrix binaries and azd packages are release builds from the
validated source, with recorded digests; they are not the Linux binary exercised
by offline integration, nor proof of native execution on all six platforms.
Extension packaging intentionally synchronizes version files before building.
Source lookups, source-record reads, sidecar reads, and binary hashing must each
succeed before their captured outputs are compared. Matching output from a
failed command is not validation evidence.
Workflow regression controls bind Windows tests to `windows-latest`, Linux
validation and publishers to `ubuntu-latest`, and native shards to their reusable
workflow delegation. Changing platform labels cannot substitute for native tests.
Extension provenance enumerates archive globs directly and requires a nonempty
regular-file inventory; directory admission and digest failures stop publication
rather than being hidden by a process-substitution discovery pipeline.

## Retry and Partial-Publication Behavior

Branch candidate checkout pins the event SHA, fetches complete history and tags,
and verifies HEAD before detection. Job outputs bind the named detector's
publication decision and tag; missing history or broken bindings are rejected by
the workflow regression controls rather than silently treated as untagged input.

Validation evidence is scoped to the current workflow run and named with the
exact `${{ github.sha }}`. Publishers do not search for a same-named check or
artifact from another run or SHA. Retrying a failed run therefore repeats any
missing validation/build work and cannot reuse stale evidence.

CLI release upload is retry-safe and replaces assets for the same tag. Extension
release and registry publication remain sequential; if a later step fails after
an earlier external write, rerun the same tag workflow only after inspecting the
existing release/registry state. The workflow never treats a partially published
release as proof that remaining publishers succeeded, and the version-sync PR is
still subject to normal review and CI.
The branch publishers fail closed on an existing release rather than assuming
its tag and assets match validation or silently repairing partial publication.
A maintainer must inspect the existing source and assets before retrying; these
workflows never turn an existing or partially uploaded release into success.
Insider publication also rejects any pre-existing synthesized tag, even when
its GitHub Release is missing, and rejects remote tag lookup failures. Only a
confirmed absent synthesized tag is created with the validated SHA as target;
this check is not an atomic reservation against concurrent tag writers.

The version-sync PR remains open until its normal review requirements and real
CI checks pass. The release workflow does not synthesize successful checks or
merge this PR itself. If enterprise policy prevents the workflow token from
creating the PR, a maintainer must open it from the pushed `release/vX.Y.Z` branch
so the pull-request workflows run. When the workflow token creates or updates
the PR successfully, the resulting CI runs require approval: a maintainer with
write access must select **Approve workflows to run** in the PR's merge box.
Approving workflow execution does not replace the required code review or
passing checks.

## Release Media (Every Release)

Plan a release-media task for **every** release, including patch releases. This
is a manual release-planning checkpoint; no automation creates or runs the task.
Before opening a new issue, search both open and closed issues for an existing
task for the target version. Reuse an existing task when appropriate, or create
one from [`docs/templates/release-media-task.md`](templates/release-media-task.md)
and record its URL in the release plan. Assign AI ownership to Tess
(`squad:tess`); do not assign a GitHub human.

The task is tracked during planning, but capture starts only after Virgil verifies
the release-candidate or shipped scope: record the full source commit SHA (or
shipped tag and resolved SHA), confirm the required checks and usable
deterministic fixture/capture paths, and identify which merged features are
actually in scope. This is a readiness handoff, not a new code-merge or release
blocker. A task noticed by an automated issue monitor, including one labeled
`squad:tess`, is not thereby authorized or ready for capture. If the source SHA
changes, revalidate the scope and recapture any affected assets.

Follow the complete evidence, capture, review, and publication boundaries in
[`docs/RELEASE-MEDIA.md`](RELEASE-MEDIA.md). The release-media deliverables are
review-ready drafts, not an announcement or permission to publish.

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
