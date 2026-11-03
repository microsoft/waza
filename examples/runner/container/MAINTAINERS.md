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

Both build bases are pinned by multi-platform registry index digest; Go is fixed
at 1.26.4 and `GOTOOLCHAIN=local` prevents toolchain auto-download. `npm ci` uses
the lockfile and Go checks module checksums. The runtime starts from `scratch`,
with the statically linked production binary, real embedded web assets and a
non-root numeric user. No CA bundle, shell or runtime package manager is supplied.
This is sufficient for the tested offline mock/text profile, not a promise of
native/provider support.

These are repeatable build instructions with pinned bases, **not a demonstrated
bit-for-bit reproducible or hermetic build**: dependency registries are needed,
frontend tooling and archive timestamps can vary, and source/LFS content must be
recorded. `-trimpath` and disabled VCS stamping do not establish whole-image
reproducibility. The test's `sha256:...` image ID is an immutable local identifier;
depending on Docker's image store, it may identify an OCI index rather than the
image configuration. It is not evidence of publication. Do not advertise a local tag or ID as a registry
pin, and do not infer publication from historical issue comments.

## Update and eventual publication policy

No automatic publication or update job is added. Do not publish an image or a
Waza release as part of this foundation. #762 stays open; its published pinned
image and native-layer acceptance are not satisfied.

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
`.github/scripts/testdata/runner-runtime/**` on push and pull request.
Docker push and pull-request checks select `main`. The temporary stacked-parent
selector was removed after the native runner merged; final-head native Linux
AMD64 evidence must be refreshed after reconciliation.
Run
`node --test examples/runner/container/native-test.mjs` as well as the unchanged
mock harness. The Dockerfile's default final target remains the scratch reference;
`synthetic-test` and `native-smoke` are explicit test targets, not published
native deployment images. The former adds only the deterministic protocol peer;
the latter uses pinned Alpine and the existing `internal/embedded` no-Send smoke.
The build context admits only the peer's `main.go`/`main_test.go` and the specific embedded
smoke test in addition to the existing production allowlist.

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
ID for that manifest digest. None of those publication checks has been performed
by this local foundation.

## Output and security boundary

The existing public CLI retains its exact input/path admission and terminal
semantics; there is no wrapper that repairs inputs, grants native consent or
maps failures to success. Runtime input/output is stdin/stdout/stderr, not a host
volume. Temporary workspaces use the bounded `/tmp` tmpfs. The host owns any
redirection files; output delivery is not transactional, durable or guaranteed
on process/host loss. The security flags are hardening, not strong isolation.
No scheduler/platform-specific logic or Lab change is required.
