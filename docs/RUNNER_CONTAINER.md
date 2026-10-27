# Bounded runner container quickstart and maintenance

This is a mock-only proof of concept for the public single-unit contract. It
demonstrates local inputs and outputs for one exact unit. It is not a general
runtime image, production deployment recommendation, remote service, scheduler
integration or strong-isolation claim. It does not add scheduler-specific code
to Waza.

**No Lab UI change is required for #762.** The reference uses the existing
public request/result contract; it introduces no new operational status.

## What is currently available

The scripts build and test a local image, but the registry image has not yet
been published. There is no confirmed public image digest to copy into a
user-facing command. Do not treat an OCI config ID, Docker image ID, tag,
example value or placeholder as a published pin.

Once an authorized maintainer completes the publication procedure below, a
user-facing registry invocation must supply only the full immutable digest
reference:

```bash
# Replace this explanatory placeholder only with the literal verified value
# from a successful publication run; no published digest currently exists.
REFERENCE='ghcr.io/microsoft/waza-runner-reference@sha256:<confirmed-64-hex-digest>'
bash examples/runner/smoke.sh "$REFERENCE"
```

The placeholder is deliberately non-runnable and is not an assertion that
publication has completed. The smoke script rejects tags, short hashes, local
IDs and malformed references. Until a confirmed digest exists, use only the
local build/test workflow below; do not copy this template as an image pin.

## Local bounded quickstart

### Host assumptions

The maintained reference supports a **native Linux x86_64** host and native
`linux/amd64` container engine with Buildx/BuildKit. It also requires Bash,
`sha256sum`, Git and Git LFS, Python 3.11+, and a Go launcher able to obtain
Go 1.26.4.
Other host architectures and emulation are not covered. Initial builds require
network access to container base-image, npm and Go module services. Reserve at
least 8 GiB for hydrated runtime assets, frontend dependencies and Go caches.

From a clean checkout with dashboard assets and embedded LFS runtime assets
hydrated:

```bash
git lfs pull
bash examples/runner/build.sh
bash examples/runner/smoke.sh --local
```

To run the repeated clean-build/conformance check:

```bash
bash examples/runner/test.sh
```

`build.sh` accepts tracked checkout files only. It snapshots them into the
ignored `.runner-reference/context`, excludes ignored build products and
ambient compiler flags, verifies hydrated Linux amd64 glibc/musl LFS payloads
against committed pointers, builds the embedded dashboard assets with the
pinned Node/npm distribution, verifies Go module checksums and builds a static
Linux/amd64 binary with Go 1.26.4. The resulting OCI image uses `scratch`.
Build inputs include the source commit timestamp; bitwise comparison is tested
within one Docker/BuildKit implementation, not across builder versions.

The local image ID is written to `.runner-reference/image-id`; the OCI config
ID is tracked separately in `.runner-reference/image-config-id`. These are
local build evidence, not registry references or substitutes for a public
manifest digest.

### Input, isolation boundary and expected output

The container reads the checked-in
[`request.json`](../examples/runner/request.json) from stdin. It receives no
host project mount, credentials, network access, queue or scheduling
integration. The invocation uses a read-only root filesystem, a non-root
identity and a private invocation-owned `/work` tmpfs. Those are bounded
container settings, **not** a claim of strong isolation from a hostile host or
all kernel/runtime threats.

The request selects exactly `shard-a / selected / trial 2 / attempt 2`. The
captured eval declares three trials and attempts and includes a neighboring
task glob as a conformance trap; that glob is never opened or executed. The
only persistent smoke output is:

```text
.runner-reference/output/unit.runner.json
```

The smoke validates that file using `waza runner validate-result` and asserts:

- one `success` result with `exit_code: 0` and `schema_version: "1.0"`;
- resolved tuple `{shard: "shard-a", task: "selected", trial: 2, attempt: 2}`;
- `adjacent_work_selected: false`;
- frozen identity values from
  [`identity.json`](../internal/runnercontract/testdata/identity.json);
- observation `run_number: 2`, `attempts: 2`, and final output containing
  `hello`; and
- an invocation-owned workspace below `/work/`.

The mock demonstrates command wiring and grading, not model quality, live
provider behavior, production reliability or security guarantees.

## Troubleshooting

| Symptom | Check |
|---|---|
| Host or daemon architecture rejected | Use native Linux x86_64 and a native `linux/amd64` engine. Other architectures/emulation are outside this reference. |
| LFS payload is missing or corrupt | Run `git lfs pull`; the build verifies each hydrated payload against the committed pointer and fails closed on a mismatch. |
| Docker Buildx/BuildKit unavailable | Install a Buildx-capable Docker Engine and verify the daemon reports `linux/x86_64`. |
| Node, Go or module fetch fails | The build pins Node/npm and Go and requires first-build access to the named dependency services; verify network access and available disk space. |
| Local smoke says no image ID | Run `bash examples/runner/build.sh` successfully first. |
| Smoke rejects a tag or local hash | That path accepts only a full `ghcr.io/microsoft/waza-runner-reference@sha256:<64 lowercase hex>` reference. For a local test, pass `--local`. |
| Schema or frozen identity assertion fails | Preserve the committed request/vector pair; inspect the result with `waza runner validate-result` and rerun the complete `test.sh` conformance gate. Do not update the expected vector merely to make a changed build pass. |
| No output file exists | Check that `smoke.sh` completed. It captures runner stdout at `.runner-reference/output/unit.runner.json`; a nonzero runner outcome stops the script. |
| Publication cannot authenticate or verify public read | This is an external publication blocker, not evidence that a package exists or that a digest is available. See the current publication status and maintainer steps below. |

## Maintainer build, publication and update policy

The maintained procedure is also recorded in
[`examples/runner/MAINTAINERS.md`](../examples/runner/MAINTAINERS.md). Update
toolchain/dependency pins only in a reviewed commit, then repeat the clean
conformance test and publication workflow. Never silently move a documented
user pin.

The destination is `ghcr.io/microsoft/waza-runner-reference`. The manual
workflow `.github/workflows/runner-reference-publish.yml` is named **Publish
bounded runner reference** and does not publish a Waza CLI release or tag.
After the source/workflow are merged to `main`, required checks and independent
review are complete, and authorized package access/public visibility is
available, dispatch it on `main` with the full reviewed commit SHA already
merged to `main`:

```bash
gh workflow run runner-reference-publish.yml --ref main \
  -f commit="$FULL_REVIEWED_MAIN_SHA"
```

The workflow builds and smokes that exact commit, reconciles the
`sha-<full-commit>` reference with the registry before writing, refuses
authorization/network ambiguity or conflicting bytes, independently hashes
the registry manifest, verifies anonymous digest access and smoke-tests the
confirmed digest. A successful run retains the literal reference in its
summary and `runner-reference-<commit>` artifact. Only that confirmed value may
replace the explanatory placeholder above or appear in release notes.

The latest publication handoff reports that registry token authorization and
organization package enumeration were denied with HTTP 403. No registry write
occurred; package existence and a published digest are unknown. The remaining
external steps are to run required CI on the exact merged head, complete normal
independent review/merge, then dispatch the named workflow on `main` with the
full reviewed main SHA and permitted package access/public visibility. This is
a future workflow step, not completed publication.
