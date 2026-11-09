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

Prepare and commit the target `version.txt`, `extension.yaml`, changelog, and
matching dashboard index/favicon before creating the semver tag. Release builds
reject a dashboard rebuild that changes tracked source; publication never repairs
the tagged metadata. Create the tag on the reviewed commit, not a moving default:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

This triggers the full pipeline: exact-source validation → CLI build → extension
build → GitHub Release → extension publish → registry-sync PR → GitHub Pages deploy.

## What the Workflow Does

1. **validation** — Calls the reusable release validation workflow for the tag's
   exact SHA. It requires the four native Linux race/atomic shards, the remaining
   Linux race/atomic suite, native Windows race tests, vet, the checksum-pinned
   lint schema, fail-closed offline integration, exhaustive shard inventories,
   and complete digest/source evidence. Missing, failed, cancelled, skipped,
   stale-SHA, or incomplete evidence fails validation.
2. **setup-version** — Extracts the version from the pushed tag (strips `v`) and validates semver format.
3. **build-cli** — After validation succeeds, installs the validation producer's
   verified dashboard, then matrix-builds 6 platforms (linux,
   darwin, windows × amd64, arm64). Each binary is accompanied by its SHA256
   digest and exact source SHA.
4. **release-cli** — Rejects missing or stale matrix evidence, verifies every
   digest, and creates or updates the **CLI GitHub Release** (`Waza vX.Y.Z`).
5. **release-extension** — After validation succeeds, verifies already-committed
   `version.txt` and `extension.yaml`, builds and packs the azd extension, records the exact source
   SHA and package digests in `release-provenance.txt`, creates the **Extension
   GitHub Release** (`Waza azd Extension vX.Y.Z`), publishes the package-only
   release to the azd registry, uploads public provenance, then opens a PR with
   updated `registry.json`.
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
Extension metadata must already match the detected version before building.
Source lookups, source-record reads, sidecar reads, and binary hashing must each
succeed before their captured outputs are compared. Matching output from a
failed command is not validation evidence.
Workflow regression controls bind Windows tests to `windows-latest`, Linux
validation and publishers to `ubuntu-latest`, and native shards to their reusable
workflow delegation. Changing platform labels cannot substitute for native tests.
Release versions and tags remain bound to their production detector outputs,
including branch creation and CLI/extension consumers; independently injected
test versions do not establish that handoff.
The validation workflow's Linux `build-test` job is the sole dashboard producer
for a release source/run. After its genuine `npm ci && npm run build`, semantic
dashboard and clean-source admission, it packs `web/dist` with the landed
`release-assets-helper`. A same-run artifact named with the exact source SHA and
run attempt contains `manifest.json` and the source-relative `web/dist` tree.
The canonical manifest binds repository, source SHA, run ID/attempt, every file's
size and SHA256; its own digest travels independently through step, job, and
reusable-workflow outputs, not through a trusted value read from the download.

Consumers export their artifact directory from `$RUNNER_TEMP` through
`$GITHUB_ENV` before downloading; runner paths are not valid job-level `env`
expressions. The independent producer manifest digest remains job-level input.

All six CLI consumers and the azd consumer retain success-only dependencies on
the entire validation workflow. They download only that named same-run artifact,
verify the independent digest and observed checkout identity, and install only
missing intended assets. Existing tracked index/favicon must match; conflicting,
extra, truncated, absent or mismatched content fails rather than being repaired.
The manifest stays outside embedded `dist`. Artifact paths are outside the
checkout, preserving clean-source admission. Before extension compilation,
`build.sh` re-verifies the installed inventory against the same manifest and
repeats semantic/source admission instead of running npm again. Ordinary local
builds, and standalone release-admission builds without a supplied handoff, keep
their existing real web build. Supplying only half a handoff fails closed.

The Linux tested-binary, six CLI matrix builds, and extension builder retain
the shared semantic output check before compiling.
It requires a nonempty regular index/favicon, JavaScript/CSS bundles, and every
index-referenced asset, rejecting missing, partial, nonregular, or symlink output.
Root-relative and relative asset URLs are checked locally; malformed or nonlocal
script/stylesheet/preload references fail without fetching external content.
Every preload destination, including fonts/images and unspecified destinations,
uses the same local regular-file admission; preloads never satisfy entry/style roles.
The host-native checker uses the pinned public Go HTML parser, with module
preparation separate from its offline execution. Ambient cross-compilation
targets are cleared for the checker, not for the published binary.
DOM roles require an active executable HTML script and enabled non-alternate
stylesheet with omitted/empty type or `text/css`. Template/noscript descendants,
comments, raw text, and foreign elements cannot satisfy those roles.
HTML role tokens and MIME hints use only space, tab, LF, CR and form feed as
whitespace; entity-decoded Unicode spaces cannot promote inert content to a role.
Role and MIME case folding also changes only ASCII A-Z; Unicode lookalikes do
not become supported executable types.
Script defaults use the original field presence and literal empty values:
`type` always overrides `language`. ASCII padding is accepted only for supported
JavaScript MIME hints in a present `type` attribute. Module identifiers and
language-derived MIME hints are matched without trimming; whitespace-only
`type` values do not become defaults.
Classic scripts with both legacy `for` and `event` attributes count only when
their ASCII-trimmed, ASCII-folded values are `window` and `onload` or `onload()`.
Present empty values do not qualify. Module scripts and scripts with only one
of these attributes are unaffected; all asset references are still checked.
Release admission additionally requires at least one unconditional stylesheet:
its `media` must be omitted, ASCII-empty, or an ASCII-trimmed, ASCII-folded `all`.
Conditional stylesheets may coexist with that fallback, and their files are
still checked. Conditional-only stylesheets, including `screen` and query lists,
do not establish styling under this intentionally tighter static release profile.
They are not invalid HTML; the checker does not evaluate media queries or claim
viewport-specific applicability.
Static stylesheet-set selection follows active HTML declarations in document
order. The first eligible non-alternate, non-disabled titled stylesheet chooses
the preferred name; inline styles participate but do not replace the required
external fallback. Nonempty `default-style` metadata can change that name,
including after earlier links. Candidate titles and names are entity-decoded,
case-sensitive, and not trimmed. Untitled persistent stylesheets remain enabled;
titled candidates must match the final preferred name. Inline style types use
raw HTML keywords, unlike link MIME hints. This does not execute scripts, model
HTTP headers/CSP or asynchronous loading, or establish browser-rendering evidence.
Duplicate attributes and unfinished tokens
are rejected before DOM normalization; this is a strict built-index profile, not
a claim to execute arbitrary HTML. The first active base href must resolve to
the synthetic `https://dashboard.invalid/` origin and root path; `/`, `./`, and
an empty local base are supported, while supplied hosts/schemes and nested bases
are rejected. Local asset paths are percent-decoded without changing case;
query strings and fragments do not become filesystem names.
The final dashboard handoff inside `build.sh` repeats release-only admission before
extension compilation. Clean tracked and untracked source is checked separately
from each published binary's actual `vcs.revision` and `vcs.modified=false`
build metadata. Metadata inspection does not execute cross-platform binaries or
prove native checkpoint execution.
The Linux suite also runs the embedded asset tests. Compile-only Windows/lint
stubs are not release assets or proof that those asset tests executed.
Extension provenance enumerates archive globs directly and requires a nonempty
regular-file inventory; directory admission and digest failures stop publication
rather than being hidden by a process-substitution discovery pipeline.
The installed Extensions Kit remains an unversioned selection, so its actual
`azd x version` output and exit are retained and checked against the verified
0.14.0 contract. Any other or unavailable version stops publication pending
review; prior installation logs do not establish the next selected version.
That tagged contract consumes every GitHub release asset when publishing with
`--repo`. Nonpackage assets therefore stop registry admission, and
`release-provenance.txt` is uploaded only after successful registry publication.
The workflow atomically creates the extension tag at the validated SHA through
GitHub's create-ref endpoint before invoking Kit. Existing or concurrently
created tags are rejected without replacement. CLI publication verifies its
existing remote tag (including annotated tags), passes the exact target SHA,
and forbids default-branch tag creation.
The stable branch publisher checks out only the validated source before
downloading its tested binary and uses the same remote tag guard immediately
before publication. A moved, missing or failed-to-read candidate tag cannot
redirect publication or be recreated from the default branch. This checkout
provides admission scripts; it does not rebuild or replace the tested binary.
Target metadata admission explicitly selects Bash with pipeline failure
propagation; matching output from a failed manifest read cannot pass.

## Retry and Partial-Publication Behavior

Branch candidate checkout pins the event SHA, fetches complete history and tags,
and verifies HEAD before detection. Job outputs bind the named detector's
publication decision and tag; missing history or broken bindings are rejected by
the workflow regression controls rather than silently treated as untagged input.

Validation evidence is scoped to the current workflow run and named with the
exact `${{ github.sha }}`. Publishers do not search for a same-named check or
artifact from another run or SHA. Retrying a failed run therefore repeats any
missing validation/build work and cannot reuse stale evidence.
Dashboard identity also includes `github.run_attempt`: a partial rerun without
the matching producer artifact fails closed. Rerun the producer/validation and
consumers together rather than relabeling a prior-attempt artifact.

This removes repeated dashboard compilation, not a measured release-speed claim.
Observe cold and warm runs before reporting outcomes: compare summed runner work
separately from end-to-end release latency, and include producer packing/upload
and all seven download/verification/install costs. Parallel builds mean less
summed work need not shorten the release's critical path. No before/after
performance result is established by the workflow graph or local cross-builds.

CLI release upload replaces assets only after tag and artifact admission. Extension
release and registry publication remain sequential; if a later step fails after
an earlier external write, rerun the same tag workflow only after inspecting the
existing release/registry state. The workflow never treats a partially published
release as proof that remaining publishers succeeded, and the registry-sync PR is
still subject to normal review and CI.
The branch publishers fail closed on an existing release rather than assuming
its tag and assets match validation or silently repairing partial publication.
A maintainer must inspect the existing source and assets before retrying; these
workflows never turn an existing or partially uploaded release into success.
Insider publication also rejects any pre-existing synthesized tag, even when
its GitHub Release is missing, and rejects remote tag lookup failures. Only a
confirmed absent synthesized tag is created with the validated SHA as target;
this check is not an atomic reservation against concurrent tag writers.

An existing extension tag, failed package publication, failed registry update,
or failed provenance upload remains explicit partial state requiring inspection;
rerunning cannot overwrite the reserved tag or existing provenance. Ref creation
and subsequent publication are not a transaction against other remote writers.
The generated registry-sync branch uses an empty-expected-value ref lease:
creation only, never force-update or deletion of an existing branch. A collision
or permission failure leaves the competing ref unchanged and stops the job.
The checked push receipt must confirm a new ref; an already-up-to-date identical
competing branch is not accepted as an invocation-owned creation.
Generated PRs preserve the repository template and Copilot commit trailer.
PR-creation failure reports the created branch and fails rather than claiming
the complete release succeeded.

The registry-sync PR remains open until its normal review requirements and real
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
