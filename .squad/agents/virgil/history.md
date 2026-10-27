# Virgil — History

## 2026-10-15: Joined Waza

User-approved Release & Developer Infrastructure owner. Initial scope includes
Go/Node CI, installers, embedded Copilot assets, azd packaging, and safe Squad
upgrades. Source: current code at `b8be34296` and the approved team expansion.

## 2026-10-27: Independent #779 / #762 infrastructure revision

Preserved Yen `3c144a71` and Basher `f15eff60` on the existing #754 branch.
Reference builds now snapshot tracked inputs, verify hydrated Linux amd64 LFS
payload hashes, build real web assets with digest-pinned Node/npm, and compile
with exact Go 1.26.4/static baseline settings. Docker config IDs and runnable
manifest IDs must be recorded separately on the containerd image store.
Binary file mtimes and exported layer timestamps must also be normalized;
SOURCE_DATE_EPOCH metadata alone does not make copied layers reproducible.

The required `ubuntu-latest` gate depends on the Linux build job, which now runs
rebuild/negative/container/publication conformance. Existing required contexts
(`Lint`, `ubuntu-latest`, `windows-latest`, `license/cla`) remain unchanged.

Current integration credentials return HTTP 403 both for organization package
enumeration and the authenticated GHCR pull/push token request. Package absence
cannot be inferred; no publication was attempted and no registry digest is
claimed. The manual main-only publisher reconciles commit tags before writes,
refuses conflicting bytes, independently hashes anonymous registry reads and
smokes the digest before recording success. An authorized maintainer must merge,
dispatch with the full reviewed main commit, permit public package access, and
carry the confirmed literal digest into Livingston's walkthrough/release notes.

Local Docker smoke and admission/publisher tests passed; pinned static Go 1.26.4
runner/CLI tests passed. Initial fully containerized dependency builds and
default-configuration Go test attempts exhausted the shared 19 GiB filesystem;
targeted static tests reuse the image compiler settings instead. No release/tag,
dashboard, scheduler code, hydrated binary commit or strong-isolation claim.
