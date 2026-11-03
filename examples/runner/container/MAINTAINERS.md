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
