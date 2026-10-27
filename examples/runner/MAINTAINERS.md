# Bounded runner image maintenance (#754 / #762)

This is the public-contract **mock** reference, not a general Waza runtime image
or a claim of strong isolation. No scheduler integration or Lab change is needed.

## Rebuild and conformance

Supported host: native Linux x86_64, linux/amd64 Docker Engine with Buildx/BuildKit,
Bash, Git + Git LFS, Python 3.11+, and a Go launcher able to obtain Go **1.26.4**.
Initial builds require network access to Docker Hub, npm and Go module services.
Reserve at least 8 GiB for hydrated runtime assets, frontend dependencies and Go
build/test caches.
Other host architectures and emulation are not covered by this reference.

```bash
git lfs pull
bash examples/runner/build.sh
bash examples/runner/test.sh
```

The script snapshots tracked local files into `.runner-reference/context`; ignored
build products and ambient Go/Node flags are not inputs. Node/npm is pinned by
platform manifest digest in `build.sh`; Go is pinned exactly, dependencies are
verified with `go.sum` and `package-lock.json`. The complete built web assets and
the verified hydrated Linux amd64 glibc/musl LFS payloads remain embedded.
The final base is `scratch`. BuildKit timestamps come from the source commit.
Bitwise rebuild comparison is within one Docker/BuildKit implementation;
cross-builder-version equality is not claimed. Locally validated packaging uses
Engine 29.1.3, Buildx 0.30.1 and BuildKit 0.26.2.
`test.sh` repeats compilation and packaging without Docker layer-cache reuse,
compares image IDs, checks the exact fixture and rejects floating/malformed pins.
It also runs the publisher's offline failure/reconciliation tests.

`smoke.sh --local` is **local CI evidence only**. An image ID is not a published
reference. The fixture requests shard `shard-a`, task `selected`, trial `2`,
attempt `2`; neighboring task globs must not be opened. The only delivered local
artifact is `.runner-reference/output/unit.runner.json`, captured from stdout.
The image's public `runner validate-result` command checks its schema; the smoke
checks the frozen identity, selection, observation and successful terminal result.
Execution uses no network, a read-only root filesystem and an invocation-owned
`/work` tmpfs; no credentials or host project are mounted.

## Publication and pin updates

Normal destination for this bounded OCI example is
`ghcr.io/microsoft/waza-runner-reference`, linked to this repository by OCI label.
No existing repository workflow publishes containers; this separate, manual
workflow uses the repository's pinned checkout/artifact actions and GitHub's
package registry, and does not touch Waza release/tag workflows.

1. Review/merge the source and publication workflow to `main`.
2. Dispatch **Publish bounded runner reference** on `main`, supplying the full
   reviewed commit SHA already merged to main. It builds/smokes that commit, then
   uses `GITHUB_TOKEN` with job-scoped `packages: write`.
3. If organization policy disables package creation or the package is private,
   an authorized maintainer must permit publication / make this package public.
   The workflow intentionally fails until an anonymous digest read succeeds.
4. On retry it reads `sha-<full-commit>` first. Only an authoritative manifest
   404 permits a first push. Authorization/network errors stop; existing tags
   with different image bytes are never overwritten. An uncertain push must be
   reconciled by registry read before another write.
5. Retrieve the **confirmed** digest reference from the successful run's summary
   and `runner-reference-<commit>` artifact. It is independently hashed from the
   registry manifest, checked anonymously, then smoke-tested by digest. Only then
   copy the literal reference into the user walkthrough and release notes.

User-facing invocation must use that literal confirmed reference:

```bash
# Set REFERENCE to the exact ghcr.io/...@sha256:... from the successful run.
bash examples/runner/smoke.sh "$REFERENCE"
```

Tags (including commit tags), a Docker daemon ID and an invented digest are not
acceptable user-facing pins. There is intentionally no claimed published digest
until publication succeeds. Update Node/toolchain/dependency pins only in a
reviewed commit; rerun clean conformance and publication and update the
walkthrough's literal digest together. Never silently move a documented pin.
