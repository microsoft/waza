# Bounded OCI runner reference: preparation

This directory prepares [#762](https://github.com/microsoft/waza/issues/762)
for the [v0.40.0 runner contract](https://github.com/microsoft/waza/issues/754).
It is **not the final integration or a published image**. The starting commit
`80209b9f2e1bd5d028bca9fe0674f83f4f6d1b62` supports public capability
negotiation, not `runner run`. Exact execution (#758), independently checked
identity (#759), and terminal schema/exit verification (#760) must be integrated
before this reference can satisfy acceptance. `conformance.mjs` deliberately
cannot pass until its final verifier is implemented against those committed
contracts. It does not invent an envelope or silently skip unavailable gates.

The bounded scenario is mock/text only: one invocation requests shard `shard-a`,
task `selected`, trial `2`, attempt `2`. All inputs are in one public JSON
document on stdin. The task glob names nonexistent neighboring work to expose
accidental discovery. No scheduler, queue, fleet, remote storage, Lab feature,
credentials, host project mount, or live/paid model is needed.
**No Lab dashboard change is required for #762.** Native SDK execution remains
unavailable; a mock result is not native execution or isolation evidence.

## Host and build assumptions

The build is scoped to native **Linux x86_64** with a native Linux/amd64 Docker
daemon, Buildx/BuildKit supporting `rewrite-timestamp`, Git, Git LFS, tar, and
Node.js 24.15.0 or later. macOS/ARM, emulation, Windows, rootless daemon variants,
and cross-builder bitwise equality are not validated by this preparation.
Initial builds need access to Docker Hub, the npm registry and Go module services.
Allow substantial disk space for hydrated runtime bundles and build caches.

The Dockerfile pins the complete Node 24.15.0 and Go 1.26.4 build images by
Linux/amd64 manifest digest. The final image uses `scratch`, a static Waza binary,
and UID/GID 65532. It preserves the real embedded dashboard build and Linux amd64
glibc/musl runtime assets; it does not overwrite the checkout's `web/dist`.
The presence of SDK assets is not a claim that native execution is supported.

`build.mjs` requires clean **tracked** source, archives exactly `HEAD`, disables
automatic LFS smudging, then hydrates only the Linux amd64 payloads from the local
checkout after checking committed LFS hashes and sizes. Ignored build output,
untracked files, host compiler flags and ambient Node dependencies are not inputs.
No `.env` files are opened. The container build uses lockfiles and readonly Go
module mode. Build timestamps derive from the source commit.
This is a deterministic-build recipe, not yet measured reproducibility evidence.

## Copyable local checks

From the repository root, offline infrastructure tests use only Node, Git, and
Git LFS; they do not require a Docker daemon:

```bash
node --test examples/runner-reference/test/*.test.mjs
```

These are transport/build-input tests, including test doubles for Docker cleanup.
They are **not container acceptance evidence**. To validate the clean fixture
through real public negotiation, first build a real binary into an existing
scratch directory outside the checkout, then run:

```bash
# ARTIFACTS must name an existing absolute scratch directory.
go build -o "$ARTIFACTS/waza" ./cmd/waza
WAZA_REFERENCE_BINARY="$ARTIFACTS/waza" \
  node --test examples/runner-reference/test/public-contract.mjs
```

The test rejects SDK launch through an executable sentinel. On the preparation
base it expects `unsupported_capability`/exit 5 for this well-formed execution
request, not successful execution. Malformed/trailing-document input must return
`invalid_request`/exit 6. This is #757 input validation only.

On a supported Docker-capable host, with reviewed changes already committed:

```bash
git lfs pull
# Each destination must have an existing parent and must not already exist.
node examples/runner-reference/build.mjs /tmp/waza-reference-build-a
node examples/runner-reference/build.mjs /tmp/waza-reference-build-b
node --input-type=module - /tmp/waza-reference-build-a/build.json /tmp/waza-reference-build-b/build.json <<'JS'
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
const [a, b] = await Promise.all(process.argv.slice(2).map(async p => JSON.parse(await readFile(p, 'utf8'))));
for (const key of ['revision', 'epoch', 'imageId', 'buildId']) assert.equal(a[key], b[key], key);
console.log('Two uncached builds agree within this builder implementation.');
JS
IMAGE="$(node -p 'JSON.parse(require("fs").readFileSync("/tmp/waza-reference-build-a/build.json")).imageId')"
node examples/runner-reference/smoke.mjs --local "$IMAGE"
node examples/runner-reference/conformance.mjs --local "$IMAGE" /tmp/waza-reference-captures
```

There are no conditional-success skips. A missing daemon fails. Missing public
capabilities fail smoke. Even after those capabilities are integrated, conformance
fails with `BLOCKED` until final result verification is implemented.
Do not treat the commands above as already-passing integration instructions.
No build, run, or test command here publishes an image.

## Transport and local outputs

Each container gets a fresh read-only root filesystem, a 32 MiB invocation-local
`/work` tmpfs, no network, no extra capabilities, and bounded memory/PIDs. The
host sends only stdin and captures stdout/stderr. Docker create is bounded to
30 seconds; each start/attach to 90 seconds; combined captured output to 8 MiB;
exact-ID cleanup to 15 seconds. Local process groups receive TERM then KILL.
The cleanup removes only the returned full container ID, never a global prune
or name-based sweep. If creation returns no valid ID, the error identifies the
unique invocation name for inspection; it does not delete an unverified resource.

`build.json` is a **local build receipt**, with revision, timestamp, daemon image
ID, Buildx-reported image ID, local tag and verified payload inventory. The receipt is
created exclusively after successful packaging. Local IDs are not registry
manifest digests or publicly pullable image references. The interpretation of the
Buildx ID depends on the exporter/image store; no independent OCI config digest
verification is claimed here.
Build failure can leave an empty destination directory, build cache or a loaded
task-owned image; no whole-build transaction or automatic image cleanup is claimed.

After smoke admits the capabilities, the conformance collector writes eight raw
transport records, exclusively in a newly created directory:
`selected.json`, `invalid-shard.json`, `invalid-task.json`, `invalid-trial.json`,
`invalid-attempt.json`, `evaluation-failure.json`, `malformed.json`, and
`extra-document.json`. Records contain the exact sent request, stdout, stderr,
process status and signal; they are **not terminal envelopes or accepted results**.
A failed/partial collector retains its already-written records for diagnosis.
The final integration must define the verified public output artifact inventory
separately. Do not copy raw records into product result storage as verified data.

Input/output directories and the local Docker host are trusted, invocation-owned
scratch, not hostile shared filesystem boundaries. Concurrent parent replacement,
transactional publication, durable writes and strong isolation are not claimed.
Build cancellation bounds the local Docker client but does not prove a remote
BuildKit job was cancelled; inspect its reported build before starting another.

## Published invocation: still blocked

There is deliberately no fabricated registry digest to paste here.
Once an authorized publication run has independently confirmed a **public** image,
use its literal reference as `REFERENCE`:

```bash
node examples/runner-reference/smoke.mjs "$REFERENCE"
node examples/runner-reference/conformance.mjs "$REFERENCE" /tmp/waza-published-reference-captures
```

Both entrypoints accept only
`ghcr.io/microsoft/waza-runner-reference@sha256:` followed by 64 lowercase hex
digits. Tags, commit tags, short hashes and bare local IDs are rejected unless
the explicit `--local` transport-test mode is selected. Syntax validation alone
does not prove an image exists, belongs to Waza, or is safe.
Local-ID invocations use `--pull=never`; an absent local image must not fall back
to pulling an unrelated repository/tag.

## Troubleshooting

| Failure | Meaning and action |
|---|---|
| Docker socket/daemon unavailable | Use a supported Docker-capable validation environment. Do not start or reconfigure a shared daemon as part of this preparation. |
| Native Linux x86_64 requirement | This reference has no macOS/ARM/emulation proof; move the build to the documented host. |
| Dirty tracked source | Commit the intended source changes; do not reset or discard someone else's work. |
| Unhydrated/corrupt LFS payload | Hydrate the reviewed commit with `git lfs pull`, then retry into a new output directory. |
| Existing output directory | Choose a new invocation-owned destination. The scripts do not overwrite old proof. |
| Missing exact-unit/mock-text/terminal capabilities | The selected image lacks committed dependencies; negotiation is not execution proof. |
| `BLOCKED` final verifier | Integrate #758-#760 and implement the gate in `conformance.mjs` before acceptance. |
| Rebuild IDs differ | Retain both receipts and builder versions; investigate, do not relabel the build reproducible. |
| Registry authorization/404/pull failure | Publication or public access is unproven; do not substitute a local ID. |
| Container cleanup failure | The error names the exact full ID; inspect only that resource and retain the error. |

See [MAINTAINERS.md](MAINTAINERS.md) for publication and final integration gates.
