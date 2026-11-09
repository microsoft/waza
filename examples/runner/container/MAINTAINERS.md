# Bounded local runner reference

The canonical user walkthrough is the
[site quickstart](../../../site/src/content/docs/guides/runner-container.mdx).
From the repository root, `node --test examples/runner/container/test.mjs` builds
and exercises the production container boundary with no runtime network or paid
execution. Node.js 24, Docker with BuildKit/Linux containers, network access during
build, and hydrated Git LFS objects are prerequisites.
On networks requiring a public npm mirror, set `NPM_CONFIG_REGISTRY` explicitly
for the test; it forwards the URL as the Dockerfile's standard npm registry build
argument without changing the lockfile or integrity checks. Record that URL with
the evidence. Never put credentials in the argument.

## Source, build and image identity

Record the reviewed source commit, full tracked/untracked diff if any, host OS/CPU,
Docker version, test command/result and emitted local image ID. Use a clean
checkout for reproducible input selection. The root `.dockerignore` is the build
allowlist; inspect it when adding new required embedded resources. This reference
does not modify the root Dockerfile or its cache optimization.

The Node and Go build bases are pinned by multi-platform registry index digest;
Go is fixed at 1.26.4 and `GOTOOLCHAIN=local` prevents toolchain auto-download.
`npm ci` uses the lockfile and Go checks module checksums. The default
`reference` target starts from `scratch`, with the statically linked production
binary, real embedded web assets and a non-root numeric user. It remains
mock/text-only. The explicit `native-reference` target uses a pinned Debian
Bookworm/glibc base with Bash, CA certificates and Git for the bundled Copilot
runtime. It runs as UID/GID `65532:65532`; its default `HOME`,
`COPILOT_HOME` and `TMPDIR` are `/home/waza`, `/home/waza/.copilot` and `/tmp`.
Runtime cache and temporary storage must be writable and executable, including
when the root filesystem is read-only. One GiB was exercised for the current
bundle; it is not a measured minimum. The test target and installed Debian
package versions can change independently of the pinned base digest, so this is
not a bit-for-bit reproducible or hermetic build.

These are repeatable build instructions with pinned bases, **not a demonstrated
bit-for-bit reproducible or hermetic build**: dependency registries are needed,
frontend tooling and archive timestamps can vary, and source/LFS content must be
recorded. `-trimpath` and disabled VCS stamping do not establish whole-image
reproducibility. The test's `sha256:...` image ID is an immutable local identifier;
depending on Docker's image store, it may identify an OCI index rather than the
image configuration. It is not evidence of publication. Do not advertise a local tag or ID as a registry
pin, and do not infer publication from historical issue comments.

<span id="update-and-eventual-publication-policy"></span>

## Update and publication acceptance policy

A manual-only GHCR publisher is provided in
`.github/workflows/publish-runner-reference.yml`.
There is no push, pull-request, or scheduled publication trigger. Do not publish
an image or Waza release without separate authorization. Publication and
public-image acceptance are separate gates. The
[published image record](../../../site/src/content/docs/guides/runner-container.mdx#published-image-and-acceptance-evidence)
contains the immutable digest, source, declared platform and publisher/verifier
links. Publisher attempt 1 succeeded and GHCR reports the package public;
anonymous-pull/provenance/production-caller acceptance passed on Linux AMD64
in verifier run `41826111839` (1/1 test, 0 skipped). Independent infrastructure-owner
review was waived for this one publication, not performed. This record is not
live-provider, billing or inference-quality evidence.

The workflow accepts a full candidate SHA and fails unless the dispatch is from
`microsoft/waza` on protected `main`, the input matches the workflow event and
checked-out commit, and the commit is still the exact current `main` tip. It
requires a clean checkout, rechecks `main` immediately before pushing and again
after push/attestation, and
uses `packages: write` only in the publishing job. The protected-main ruleset is
the gate for required reviews and status checks; operators must additionally
confirm the native Linux AMD64 container check and the independent infrastructure
review are complete before dispatching, or record an explicitly authorized
one-publication review waiver without presenting it as completed review.
The workflow builds only `linux/amd64`
from the clean, LFS-hydrated checkout at the validated candidate SHA, sets source
and revision labels, and attaches BuildKit provenance and an SBOM. A signed
GitHub provenance attestation binds the published image digest to the workflow
source revision; the candidate guard requires that revision to equal the
requested SHA and current `main`. Its per-run tag is only a convenience;
consumers must pin the immutable `@sha256:` digest instead.

Artifact Metadata Storage Records are explicitly disabled to preserve the
publisher's existing permission boundary when upgrading the provenance action.
Signed provenance is still pushed to the registry; no `artifact-metadata: write`
permission is granted.

The `GITHUB_TOKEN` package-write and attestation permissions are declarations,
not proof that repository, organization or package policy allows publication.
Record the actual publisher outcome rather than inferring success from a
workflow dispatch. A first GHCR publication may be private.
A successful push/attestation is not public-image acceptance; a failed
attestation or final-main check can leave a pushed candidate behind. The
publisher summary marks such a digest unaccepted, makes no rollback claim, and
must not be treated as permission to use it.

After an authorized push, an operator with package-admin authority must verify
the package's actual visibility and make it public if required. Then run the
manual-only `.github/workflows/verify-runner-reference.yml` workflow with the
source revision and immutable digest from the publisher summary. Its verifier
has an isolated provenance job with `contents: read`, `attestations: read` and
`actions: read`;
the production container-test job has only `contents: read`. Neither has
package permission. The test job creates an empty Docker configuration, strips
auth environment variables and pulls
`ghcr.io/microsoft/waza-runner-reference@sha256:...` anonymously. It
requires dispatch from the current `main` tip and the image source revision to
be reachable from current `main`, checks the
pulled image's source/revision labels and `linux/amd64` platform, and verifies
the signed provenance for the exact digest and publisher workflow. The signed
invocation identifies the exact publisher run and attempt: the verifier requires
that attempt and its publication/finalization steps to have succeeded. A valid
attestation left by a failed, cancelled or incomplete publisher is not accepted,
even if a later attempt or a different run succeeds for the same source.

The verifier then runs the mock exact-unit caller and all six bundled-native
synthetic-provider controls against that same digest on a native Linux AMD64
runner. It mounts only read-only probe binaries and the fixture from the
validated source checkout, plus the test's evidence directory; it never
replaces the digest's `/waza`. Only a successful verifier run demonstrates
anonymous visibility and production-caller behavior for that immutable digest.
Record its exact source, digest, platform, attestation and test results. A local
image ID, tag, or authenticated push is not proof of public availability.

For any proposed update, review base-image release/security changes and resolve
new index digests explicitly, update the Dockerfile and applicable lockfiles,
then rerun the real container test on each claimed native Linux architecture.
Record exact commands, platform and image ID; a build on one architecture does
not prove another. Run the site build and Chromium suites for guide/navigation
changes. Docker CI runs the container test whenever its source/build inputs
change; no path-filtered success substitutes for execution evidence.
Both push and pull-request filters explicitly retain the existing
`examples/runner/unit.json` input and all nested `examples/runner/container/**`
inputs, guarded by the script and documentation regression tests.
The native control additionally selects `examples/runner/copilot-unit.json` and
`.github/scripts/testdata/runner-runtime/**` and
`.github/scripts/testdata/native-bundle-probe/**` on push and pull request.
Docker push and pull-request checks select only `main`, where native Linux AMD64
controls run against the integrated source.
Run
`node --test examples/runner/container/native-test.mjs` as well as the unchanged
mock harness. The Dockerfile's default final target remains the scratch reference;
`synthetic-test` and `native-smoke` are explicit test targets, not published
native deployment images. The former adds only the deterministic protocol peer;
the latter uses pinned Alpine and the existing `internal/embedded` no-Send smoke.
The build context admits only the peer's `main.go`/`main_test.go` and the specific embedded
smoke test in addition to the existing production allowlist.

Also run `node --test examples/runner/container/native-bundle-test.mjs`. It
builds both `native-reference` and the separate `native-bundle-test` target,
checks the mock profile in the former, and exercises the actual bundled native
CLI in the latter against an in-container synthetic HTTP provider while Docker
networking is disabled. A direct SDK positive control must discover synthetic
home/ancestor instructions and run both hook sentinels; the production runner
must suppress those same canaries. The suite also verifies exact selection,
one request per invocation, success/timeout/SIGTERM outcomes, forced cleanup of
a stopped native child, and cross-UID readability of the two evidence files.
The test rejects a daemon/image architecture mismatch. Docker CI uses
`ubuntu-24.04` and explicitly requires native Linux AMD64; local ARM64 and
emulated runs do not satisfy that platform gate. No synthetic test makes a
claim about live provider behavior, paid execution, billing or model quality.
The CI-generated image ID is local to that run, not a published registry pin.

All runtime controls disable networking and mount no credentials or source.
Synthetic controls mount only a new empty RPC-log output directory; native
smoke mounts nothing. Test-only logs are readable across container/host UIDs
without granting other UIDs write access; no credentials or environment values
are recorded. Run `node --test examples/runner/container/native-lifecycle.test.mjs`
for deterministic pre-creation rejection, timeout and cleanup-failure controls.
The harness preserves original Docker errors and reports cleanup failures
separately. `docker create` returns an immutable ID directly through the owned
subprocess stdout; the harness captures it once in memory and starts, inspects
and removes only that ID, never its reusable name or a mutable cidfile.
Unproven ownership reports potential
residue for manual inspection rather than deleting a competing container.
There is no ownership-directory or cidfile cleanup to race with filesystem
replacement. Original execution and container-removal errors are aggregated;
creation-time failure without a captured ID leaves potential residue explicitly
unverified rather than deleting by name.
Deterministic collision/replacement controls and an actual Docker name collision
verify that existing containers survive failed admission.
The peer's authentication, text and zero-usage responses
are synthetic, not provider evidence. Assert all explicit context opt-outs,
the required `session.options.update` before sending, and update rejection
producing `infrastructure_failure`/`execute/create_failed` without `session.send`.
Metrics remain identity-bound uncomputed metadata; success does not assert
aggregate metric thresholds. Never relabel synthetic success as native runtime
or live-provider assurance.

The scratch reference must fail native startup with zero engine calls. The
Alpine smoke must actually start the bundled CLI and pass its local-provider
lifecycle with zero provider requests; it sends no inference prompt. This
distinguishes missing native loader/dependencies from SDK protocol simulation.
Runtime asset extraction also needs writable executable cache storage: the
mock harness's 32 MiB `noexec` tmpfs is unsuitable; 256 MiB proved insufficient
for the current bundle and the native test uses 1 GiB (not an exact minimum).
Record the actual architecture, image IDs and outcomes; no other platform is
proved by cross-compilation or emulation.

The site workflow's main-push paths and PR `site` filter also select
`.github/workflows/docker-ci.yml`, so workflow-only changes execute the prebuild
guard. Discovery mutation controls reject removing either site selector.
Dependency rollup #820 is part of the v0.40.0 target. Rebuild and rerun acceptance
against its final integrated source before release; older SDK/runtime evidence
does not validate the updated bundles or raised Node minimum.

Before any separately authorized publication, verify the integrated native
runner's actual capabilities and acceptance, choose an authorized registry and
update policy, build from the reviewed commit, verify the pushed manifest
anonymously by digest, and rerun clean-input acceptance using
`registry/repository@sha256:<manifest-digest>`. Record that literal published
reference, supported platforms, source commit, envelope and output evidence in
the walkthrough and release notes. Do not substitute `latest` or a local image
ID for that manifest digest. Local foundation checks do not establish any of
those publication results.

## Output and security boundary

The existing public CLI retains its exact input/path admission and terminal
semantics; there is no wrapper that repairs inputs, grants native consent or
maps failures to success. Runtime input/output is stdin/stdout/stderr, not a host
volume. Temporary workspaces use the bounded `/tmp` tmpfs. The host owns any
redirection files; output delivery is not transactional, durable or guaranteed
on process/host loss. The security flags are hardening, not strong isolation.
No scheduler/platform-specific logic or Lab change is required.
