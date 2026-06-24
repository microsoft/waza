# ADC Runner Disk Image (`Dockerfile.adc-runner`)

> Pre-baked disk image for the Waza Platform's ADC (Azure Dev Compute) sandboxes.
>
> Tracks: [#177](https://github.com/microsoft/waza/issues/177) (parent epic [#168](https://github.com/microsoft/waza/issues/168))

## What it is

A multi-stage Docker image that bundles everything the hosted Waza Platform
needs to execute an eval inside an ADC sandbox **without** a cold-start cost:

- **`waza` CLI** — built from this repo at image build time
- **GitHub Copilot CLI** — pre-extracted from the binary's embedded bundle and
  written to `$XDG_CACHE_HOME/copilot-sdk/` so the first invocation skips the
  ~50 MB cache rehydration that normally happens on a fresh machine
- **Skill runtime essentials** — `bash`, `git`, `curl`, `ca-certificates`,
  `jq`, `nodejs`, `npm`, `openssh-client`, `tini`, `unzip`, `xz-utils`

The image is published to **`ghcr.io/microsoft/waza/adc-runner`** by
[`.github/workflows/docker-adc-runner.yml`](../.github/workflows/docker-adc-runner.yml).

## Image contract

The hosted platform creates sandboxes via the ADC SDK. The image is designed
around that contract:

| Aspect | Value |
|---|---|
| Registry / repo | `ghcr.io/microsoft/waza/adc-runner` |
| Architectures | `linux/amd64`, `linux/arm64` |
| Default entrypoint | `tini -- /bin/sleep infinity` (sandbox stays alive; `ExecuteShellCommand` does the work) |
| `waza` location | `/usr/local/bin/waza` |
| `HOME` | `/opt/waza` |
| `XDG_CACHE_HOME` | `/opt/waza/cache` (pre-populated with `copilot-sdk/`) |
| Working directory | `/workspace` (mount your eval workspace here) |
| Marker env var | `WAZA_ADC_RUNNER=1` |
| Required runtime env | `GITHUB_TOKEN` for the Copilot SDK |

### How the platform consumes it

```go
// internal/platform/execution (conceptual)
img, _ := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
    Labels:    map[string]string{"name": "waza-adc-runner", "version": "0.37.0"},
    BaseImage: "ghcr.io/microsoft/waza/adc-runner:0.37.0",
})

sb, _ := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
    DiskImage:  models.SandboxSourceDiskImage{ID: img.ID},
    CPU:        "2",
    Memory:     "4096Mi",
    Entrypoint: []string{"/bin/sleep"},
    Cmd:        []string{"infinity"},
    Labels:     map[string]string{"name": "waza-run-" + runID},
})

res, _ := sb.ExecuteShellCommand(ctx,
    "cd /workspace && waza run eval.yaml --context-dir fixtures -o /tmp/results.json",
    "/workspace",
    map[string]string{"GITHUB_TOKEN": token},
    "")
```

## Tagging strategy

| Trigger | Tags pushed |
|---|---|
| Tag push `v1.2.3` | `:latest`, `:1.2.3`, `:1.2`, `:1`, `:sha-<short>` |
| Push to `main` | `:main`, `:sha-<short>` |
| Manual dispatch | `:sha-<short>` (+ optional `tag` input) |
| PR | Built (single-arch) and smoke-tested but **not pushed** |

`:latest` is only ever moved by a real release tag — never by a `main` push.

## Build & test locally

```bash
# Build
docker build \
  -f Dockerfile.adc-runner \
  --build-arg WAZA_VERSION=$(cat version.txt) \
  -t waza-adc-runner:local .

# Verify the binary
docker run --rm waza-adc-runner:local /usr/local/bin/waza --version

# Verify the embedded Copilot CLI is pre-extracted
docker run --rm --entrypoint /bin/bash waza-adc-runner:local \
  -c 'ls -lh "$XDG_CACHE_HOME/copilot-sdk"'

# Run a one-shot eval against a workspace on the host
docker run --rm \
  -v "$PWD/examples/code-explainer:/workspace" \
  -e GITHUB_TOKEN \
  --entrypoint /usr/local/bin/waza \
  waza-adc-runner:local \
    run /workspace/eval.yaml --context-dir /workspace/fixtures -v
```

## Pulling a published image

```bash
docker pull ghcr.io/microsoft/waza/adc-runner:latest
# or a pinned release
docker pull ghcr.io/microsoft/waza/adc-runner:0.37.0
```

The package is published under the `microsoft/waza` org and inherits the
repo's visibility (public). No PAT is needed to pull.

## Security & provenance

- The publish workflow emits **SLSA provenance** and an **SPDX SBOM**
  (`provenance: true`, `sbom: true` on `docker/build-push-action`).
- Image runs as `root` today to keep things compatible with skills that need
  to install packages mid-eval. Hardening to a non-root user is tracked as a
  follow-up — see "Open follow-ups" below.

## Open follow-ups (intentional TODOs)

These were *not* in scope for #177's first cut and should be split into their
own issues when prioritized:

- [ ] **Cosign signing.** Add `cosign sign --yes $IMAGE@$DIGEST` after the
      push step once the platform team confirms the keyless OIDC trust root.
- [ ] **Trivy / Grype scan gate.** Run a vuln scan on every PR and fail the
      build on `HIGH+` findings (currently informational only).
- [ ] **Non-root runtime user.** Switch `USER` to a dedicated `waza` UID once
      the eval execution layer is audited for permission assumptions.
- [ ] **Slim base.** Evaluate `gcr.io/distroless/base-nossl-debian12` once
      we know the full skill-runtime surface; Ubuntu was chosen for parity
      with the ADC reference base image.
- [ ] **Image size budget.** Add a CI assertion that the image stays under a
      fixed size (suggested cap: 800 MB compressed) so additions don't slip in.
- [ ] **Platform wiring.** `internal/platform/execution` will pick up this
      image in [#176](https://github.com/microsoft/waza/issues/176).
