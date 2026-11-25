# Release Process

This document describes how Waza qualifies and publishes releases. The reusable
validation contract is `.github/workflows/release-validation.yml`; the reusable
qualification contract is `.github/workflows/release-qualification.yml`.
Every publisher must use the exact source SHA that completed qualification.

## v0.42.1 dogfood hotfix

The [hotfix article](../site/src/content/docs/releases/v0-42-1.mdx) tracks the
candidate fixes and migration guidance. Keep its publication status, the
changelog, README and site download reference consistent with verified release
state. Existing generated CI files are not automatically rewritten by an
upgrade. Qualify the final merged source before tagging, then validate the
actual published binaries and extension archives; a same-source rebuild does
not establish that the distributed packages ran.

The tag workflow retains GitHub's success-only dependency conditions for each
builder and publisher. Required validation and evidence-verification steps may
not be skipped or tolerate failures; workflow regression controls exercise those
bypasses on both LF and CRLF checkouts.
Windows tests use the same proven native rejecting runtime guard as ordinary Go
CI, and fail on its launch marker even if a test tolerates a rejected launch.
Release Linux and Windows suites disable Go's test-result cache with `-count=1`
so their successes require fresh execution under the current run's guard.

## Nonpublishing Dashboard Handoff Acceptance

`release-dashboard-acceptance.yml` is a manual-only, two-job control for the
accepted source `76fe63e71fcf488ce55ef1d8cbe5077a187c197f`, not a publisher or
general source selector. Its independently reviewed control workflow must first
land on the default branch. Dispatch and its hosted artifact upload require
separate authorization; adding the workflow grants neither.

Both jobs check out that immutable source and log the actual workflow-control
and event SHAs separately. Only original source-admission/handoff subprocesses
receive the accepted source through `GITHUB_SHA`; repository, hosted run ID and
attempt remain authentic. One genuine dashboard build is packed and uploaded,
then a separate runner downloads the exact same-run/source/attempt artifact.
The manifest digest arrives independently through the producer job output.
Original install, verification, semantic and clean-source checks must succeed.
Summaries retain observed tree sizes/digests; the upload action log records
transport archive bytes separately. Neither is a measured speedup.

The consumer then exercises the original configured azd registry bootstrap and
requires selected Extensions Kit 0.14.0. No registry override, direct-Kit
substitute, cloud login, product compilation, packaging or publication occurs.
Bootstrap failure fails the job/run; an earlier successful transfer step is
only partial evidence, not full acceptance. The two jobs have 15-minute limits,
read-only repository permissions and three-day dashboard artifact retention.
This control does not replace release validation, establish native Windows
execution, or repeat the retained twelve-product/six-package acceptance.

## Candidate qualification without publication

Before creating a version tag, dispatch **Release Qualification** from the
reviewed `main` commit and provide that exact full commit SHA. The workflow
rechecks that the selected commit is still the current `main` head, runs the
release validation, builds the release binaries and runner image, generates and
verifies the image inventory, creates GitHub artifact attestations, verifies
them offline, and uploads the evidence and workflow-linked assurance package
only to that workflow run. This candidate path has read-only repository
permissions plus attestation permissions; it creates no version tag, GitHub
Release assets, or public registry image.

Candidate admission, tag admission and runner-image publication use the same
version check (`.github/scripts/release_version_check.sh`). The supported
profile is `major.minor.patch` (unsigned 64-bit components, no leading zeroes)
with an optional dot-separated alphanumeric prerelease such as `0.42.1-rc.1`;
numeric prerelease identifiers also cannot have leading zeroes. Build metadata
(`+build.2`) and internal prerelease hyphens are not supported by this shared
candidate/tag profile. Versions are limited to 127 ASCII bytes: the publisher
uses the unchanged `v`-prefixed version as an OCI tag, which forbids `+` and is
limited to 128 bytes. Qualification does not admit a broader version syntax
than publication.

Qualification consumes the exact-source dashboard artifact produced by release
validation and rechecks its manifest and installed output before building. It
stamps the candidate version into the runner image and checks the image's
`--version` output. Evidence, assurance-package, and native-smoke package
artifacts include the workflow run ID and producer attempt. The release
publisher receives the qualification artifact name and its recorded producer
attempt, rather than assuming it matches the publisher job's attempt. Native
receipt collection downloads only artifacts from the exact workflow run and
keeps each attempt in a separate directory. Each status and passing receipt is
bound to its producing run and attempt. The collector also reads each native
matrix job's attempt and receipt-upload step result from the Actions API. It
requires the newest job attempt for each target to have a matching receipt
artifact and successful upload, so a failed upload or an absent rerun artifact
cannot silently reuse an earlier pass. Successful receipts from targets not
rerun remain valid.

Release validation's tested-binary and complete-validation artifacts include
the workflow run attempt in their names. Reusable-workflow outputs carry each
exact artifact name and producer attempt to consumers; release publishers
download the reported artifact and check that its embedded source and attempt
match those producer outputs. This avoids immutable artifact-name collisions
when a workflow run is rerun while allowing a downstream retry to use the exact
earlier validation artifact when only publication is rerun.

Offline GitHub attestation verification requires two independently trusted
policy values: the repository's immutable numeric ID and the SHA-256 digest of
the Sigstore trusted-root file. The qualification workflow gets the repository
ID from `github.repository_id` and requires the root digest in the
`WAZA_SIGSTORE_TRUSTED_ROOT_SHA256` repository Actions variable. It checks the
root digest before using that root and compares the repository ID in every
verification result; `--repo` owner/name matching alone is not sufficient for
locally supplied bundles. See GitHub's [offline verification
guidance](https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/verify-attestations-offline)
and the [`upload-artifact` immutable-artifact
behavior](https://github.com/actions/upload-artifact#not-uploading-to-the-same-artifact).

To prepare policy values for an offline consumer, use a trusted online machine:

```bash
gh api repos/microsoft/waza --jq .id
gh attestation trusted-root > trusted_root.jsonl
sha256sum trusted_root.jsonl
```

Transfer the repository ID, trusted-root file, and its SHA-256 through a
trusted policy channel separate from the artifact and attestation bundle.
Record the expected digest there; do not calculate it from an untrusted root
delivered beside the artifact. Refresh the root and its policy digest when
Sigstore trust material is updated. Before invoking `gh attestation verify`
offline, compare the root file to that policy digest, then verify every
attestation with JSON output and require each
`verificationResult.signature.certificate.sourceRepositoryIdentifier` to equal
the independently obtained repository ID. The `-R`/`--repo` owner/name check
does not replace this ID comparison.

The repository's fail-closed policy checker can enforce both checks for
locally supplied verification reports:

```bash
go run ./.github/scripts/attestation-policy verify \
  "$EXPECTED_REPOSITORY_ID" "$EXPECTED_TRUSTED_ROOT_SHA256" \
  trusted_root.jsonl verification/*.json
```

Review the candidate artifacts and verifier output before approving a release.
The package identifies the workflow run, source commit, artifact ID and digest,
and hashes every qualification file. A successful candidate run alone does not
authorize publication.

## Cutting a Release

Prepare and commit the target `version.txt`, `extension.yaml`, changelog, and
matching dashboard index/favicon before creating the semver tag. Review a
successful candidate qualification for the exact release commit first. Release
builds reject a dashboard rebuild that changes tracked source; publication never
repairs the tagged metadata. Create the tag on the reviewed commit, not a moving default:

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

This triggers tag-release qualification, then publishes the qualified runner
image and inventory, exact qualified CLI binaries and qualification bundle,
builds the extension, opens its registry-sync PR, and deploys the GitHub Pages
release notes. The GHCR publisher is reusable only from this tag-release path;
it has no candidate `workflow_dispatch` route and refuses to replace an existing
versioned image tag. A retry may continue only if the existing versioned tag
already resolves to the exact digest from qualification; it then skips the
image copy and repeats provenance/inventory attestation and verification.
Different or unprovable registry state fails closed.

## What the Workflow Does

1. **qualification** — Calls the reusable release qualification workflow for the tag's
   exact SHA. It requires the five native Linux race/atomic shards, the remaining
   Linux race/atomic suite, native Windows race tests, vet, the checksum-pinned
   lint schema, fail-closed offline integration, exhaustive shard inventories,
  and complete digest/source evidence. It also builds the six CLI binaries and
  an OCI runner image, generates an inventory for the OCI manifest digest,
  creates provenance/SBOM attestations for that exact digest, downloads the
  Sigstore trusted root, and verifies the binaries, manifest, archive and SBOM
  offline. Missing, failed, cancelled,
   skipped, stale-SHA, or incomplete evidence fails qualification.
2. **setup-version** — Extracts the version from the pushed tag (strips `v`) and validates semver format.
3. **runner-reference** — After qualification succeeds, downloads the exact OCI
   image archive and inventory from that run, verifies the source-bound digest
   attestations offline, and copies that archive to GHCR with digest preservation.
   It fails if the registry digest differs, attaches the inventory attestation to
   the same digest, verifies it offline, and uploads publication evidence.
4. **release-cli** — Downloads the exact qualification artifact, verifies the
   binary source and digest sidecars, and creates or updates the **CLI GitHub
   Release** (`Waza vX.Y.Z`) with the binaries, assurance package and a
   qualification evidence archive that includes the runner image, inventory,
   signatures, trusted root and verification reports.
5. **release-extension** — After qualification and runner-image publication succeed, verifies already-committed
   `version.txt` and `extension.yaml`, builds and packs the azd extension, records the exact source
   SHA and package digests in `release-provenance.txt`, creates the **Extension
   GitHub Release** (`Waza azd Extension vX.Y.Z`), publishes the package-only
   release to the azd registry, uploads public provenance, then opens a PR with
   updated `registry.json`.
6. **native-smoke** — After both publishers' builds, calls
   `native-package-smoke.yml` to execute the exact uploaded CLI binaries and
   representative extension archives on native runners. See
   [Native Platform Evidence](#native-platform-evidence); it is reported
   evidence, not yet a publication gate. It runs with `report_only: true`, and
   the extension-package upload may fail without stopping publication, so a
   smoke failure shows on its jobs without failing the `Release` conclusion
   that Pages deployment waits on.
7. **pages.yml** — Deploys the documentation site after the `Release` workflow completes successfully, ensuring release notes and download links published to `site/` are pushed to GitHub Pages.

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
by offline integration. Native execution evidence for them comes only from the
separate native smoke described in
[Native Platform Evidence](#native-platform-evidence).
The packaged-dashboard integration restores its original source assets even if
the server exits between the cleanup liveness check and termination. A failed
termination still fails the check explicitly; it does not skip restoration or
prove that a server whose termination was denied has stopped.
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

The handoff helper and filesystem checker share one HTML/URL validator.
Inventory verification therefore accepts the same relative URLs and root-preserving
base elements, ignores comments and inert template/noscript content, and checks
resolved references without normalizing or rewriting the packaged bytes.

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
The isolated distribution fixture resolves its checker dependency versions from
the root module graph using offline, read-only module lookup. Its empty-cache
negative control must still fail explicitly rather than fetch missing modules.
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

## Native Platform Evidence

Checksums establish byte integrity, not behavioral compatibility. Platform
claims are limited to what the table below actually executes.

| Target | Runner | Source tests | Package evidence |
|---|---|---|---|
| linux/amd64 | `ubuntu-latest` | Native: full Go CI suite and shards | Native: CLI and extension archive smoke |
| linux/arm64 | `ubuntu-24.04-arm` | Cross-build only | Native: CLI smoke |
| darwin/arm64 | `macos-latest` | Native: pinned judge runtime (Go CI) and Darwin production callers | Native: CLI and extension archive smoke |
| darwin/amd64 | `macos-15-intel` | Native: Darwin production callers | Native: CLI smoke |
| windows/amd64 | `windows-latest` | Native: full Go CI race suite | Native: CLI and extension archive smoke |
| windows/arm64 | `windows-11-arm` | Cross-build only | Native: CLI smoke |
| Other OS/architectures | — | Unsupported | Unsupported: not built or distributed |

Evidence classes:

- **Native** — executed on a hosted runner whose OS and architecture equal the
  target. The checker also requires the runner-reported platform to match and,
  on macOS, `sysctl.proc_translated=0`, so Rosetta translation fails.
- **Cross-build only** — compiled and header/build-info checked on Linux; never
  reported as executed.
- **Emulated** — not used. QEMU or Rosetta execution is rejected rather than
  counted as native.
- **Unavailable** — a supported target whose native runner is not available.
  It is moved to `evidenceUnavailable` with a reason in
  `.github/scripts/native-package-smoke/platforms.go` and reported as
  `unavailable`, never as passed.
- **Unsupported** — not built or distributed.

### Package smoke

`.github/workflows/native-package-smoke.yml` is reusable. It never rebuilds. Each
job checks out the exact source, installs the rejecting Copilot runtime guard,
downloads one package and runs `.github/scripts/native-package-smoke`:

1. native host proof (above);
2. CLI `.sha256` and `.source-sha.txt` sidecars, or extension
   `release-provenance.txt`, must name the downloaded bytes and revision;
3. the executable's object header and Go build info must name the target,
   the revision and `vcs.modified=false`;
4. `--version` must print exactly `waza version X.Y.Z`; `--help` must list
   `run` and `serve`;
5. the shared `release_integration_test.sh` offline mock run must produce a
   complete error-free outcome without launching the Copilot runtime;
6. `serve` from an empty directory must return the production dashboard index
   and every referenced `/assets` file with a script or stylesheet media type,
   proving the dashboard is embedded rather than read from disk.

Extension archives must contain exactly `extension.yaml` (byte-equal to the
source manifest and declaring the version) and the platform entry point as
regular files, executable on Linux. The extension is not installed into azd
and no credentials are used. The extension layout is checked on one
representative target per OS: linux/amd64, darwin/arm64 and windows/amd64.

Each job writes a receipt. The evidence job runs even when smoke jobs fail and
fails unless every native target has a receipt for the same source, revision
and version with every check, a package digest and at least one fetched
dashboard asset. Missing runners, artifacts or receipts, digest mismatches,
wrong architectures, emulation and runtime launches therefore fail instead of
producing success-shaped evidence. The table is written to the job summary.

### Darwin production callers

`TestScenarioDarwinSystemOutputAlias`, `TestSystemRootAliasPlatform`,
`TestWorkspaceRootExplicitSystemAliases` and
`TestPrivateWriterPhysicalAndCleanedRoots` cover the Darwin `/tmp` and `/var`
system-alias callers in `cmd/waza` and `internal/snapshot`. The
`native-platform.yml` job runs them with `go test -json` on both macOS
architectures and requires each to be selected and passed. A skip, such as the
non-Darwin skips in the first and third tests, fails the job.

### Cadence

| When | What runs | Gate |
|---|---|---|
| Every PR | Checker unit, mutation and workflow-contract tests in `./.github/scripts` | Required (Go CI) |
| PR changing the smoke workflows or checker | `native-platform.yml` in full | Reported on the PR |
| Push to `main` touching platform code, weekly, or manual dispatch | `native-platform.yml`: Darwin production callers, plus candidate packages built with the release commands and smoked on every native runner | Reported; failures are visible on `main` |
| Release tag | `release.yml` `native-smoke` on the exact published packages | Reported; not yet a publication gate |

A candidate built from a commit whose tracked `web/dist/index.html` predates the
current dashboard records the rebuilt index in a local commit. The packages
then name that commit as `package_revision`, while fixtures come from the
pushed `source_sha`.

Measured cost on the first candidate run is recorded below. Making the release
smoke a publication gate is a separate decision once its runner availability
and latency are established across releases.

Measured on candidate run
[43506097460](https://github.com/microsoft/waza/actions/runs/43506097460)
(2026-11-13): 11m43s wall time, about 24 runner-minutes, of which about 7.4
were macOS.

| Job | Wall time |
|-----|-----------|
| Candidate packages (web UI, six CLIs, extension pack) | 9m23s |
| Darwin production callers (macos-latest / macos-15-intel) | 2m18s / 1m41s |
| CLI smoke (windows/amd64, darwin/amd64, linux/amd64, windows/arm64, darwin/arm64, linux/arm64) | 1m36s, 1m33s, 28s, 1m08s, 42s, 14s |
| Extension smoke (windows/amd64, linux/amd64, darwin/arm64) | 1m55s, 1m22s, 1m08s |
| Evidence aggregation | 13s |

The Darwin callers run in parallel with candidate packaging, so the smoke and
evidence jobs add about 2m10s after packaging. In `release.yml` the packages
already exist, so that tail is the release's added latency.

## Retry and Partial-Publication Behavior

Branch candidate checkout pins the event SHA, fetches complete history and tags,
and verifies HEAD before detection. Job outputs bind the named detector's
publication decision and tag; missing history or broken bindings are rejected by
the workflow regression controls rather than silently treated as untagged input.

Validation evidence is scoped to the current workflow run and named with the
exact `${{ github.sha }}`. Publishers do not search for a same-named check or
artifact from another run or SHA. Retrying a failed run therefore repeats any
missing validation/build work and cannot reuse stale evidence.
Dashboard identity also includes the producer's `github.run_attempt`: a partial
rerun without the matching producer artifact fails closed. Validation carries the exact
artifact name and producer attempt through reusable-workflow outputs, and each
consumer installs using that recorded producer attempt rather than its own
rerun number. This permits a failed downstream job to reuse the successful
dashboard producer from the same workflow run without relabeling an artifact;
missing or mismatched producer outputs still fail closed.

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
The `release/vX.Y.Z` identity is immutable per version. Before pushing, the job
looks up the remote branch: a lookup failure stops the job, and an existing
branch is never pushed to. A re-dispatch reuses an existing branch only when its
tip is the same registry sync (single parent at the validated release commit
and an identical tree); it then creates the registry-sync PR if none exists, or
reports the existing PR. Any other existing branch fails the job with the
branch left unchanged; publish a new version or inspect it manually.
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

Pages normally requires a successful Release run for the checked-out version's
tag. Its fallback requires a completed tag-push Release run for the exact tag
SHA and name. In the latest attempt, all jobs must be successful or skipped
except `Release Extension`, whose only failed step must be `Create Release PR`;
both `Publish Extension` and `Publish Extension Provenance` must have succeeded.
The matching GitHub Release must also be non-draft and include `checksums.txt`.
The fallback is recorded in the Pages job summary; it does not synchronize the
registry. The Release workflow's downstream `Native package smoke` job is
skipped in this case, so record native smoke evidence separately and distinguish
a same-source rebuild from tests of the published bytes.

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

## Production Integration Assurance (v0.42.0)

Before tagging v0.42.0, validate the versioned production-integration package
and regenerate its documentation page from the same commit:

```bash
go run ./cmd/waza runner assurance build
go run ./cmd/waza runner assurance build --format markdown
```

The package records contract evidence and unsupported/unavailable states; it is
not a blanket deployment approval. Release and migration notes must retain the
documented gaps for keyless/KMS signing, inventory publication restricted to the
qualified tag-release path and unqualified previously published images, post-run
telemetry, OS resource accounting, host-enforced isolation, and external scheduler/provenance-hook
responsibilities. Operators reassess against the exact released Waza version
and image digest after upgrades. See
[`docs/PRODUCTION_INTEGRATION_ASSURANCE.md`](PRODUCTION_INTEGRATION_ASSURANCE.md).

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
