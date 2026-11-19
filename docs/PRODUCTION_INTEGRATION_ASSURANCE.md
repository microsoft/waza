# Production integration assurance package

The v0.42.0 package is the versioned source document
[`PRODUCTION_INTEGRATION_ASSURANCE-1.0.json`](PRODUCTION_INTEGRATION_ASSURANCE-1.0.json).
It links the operator conformance matrix, threat model, readiness checklist,
verification limits, migration notes, and the existing #769–#774 evidence views
to the checked-in contracts, tests, and operator guides. The published page is
generated at
[`production-integration-assurance.mdx`](../site/src/content/docs/guides/production-integration-assurance.mdx).

## Rebuild and validate

From a Waza source checkout:

```bash
go run ./cmd/waza runner assurance build > package.json
go run ./cmd/waza runner assurance build --format markdown
```

The JSON output is a normalized `waza.production-integration-assurance` 1.0
package. The Markdown output is the deterministic site page. Both operations
run offline and fail if the package schema is invalid, a capability's state
differs from the version 1.0 conformance matrix, a referenced published contract
schema or evidence file is missing, a referenced schema has no Waza schema ID
or fails offline compilation with its local references, any of the three
conformance states is absent, or a linked Lab guide is not a documentation-site
guide that describes its named panel. `internal/runnerassurance` tests require
the checked-in page to match generated output independent of checkout line
endings.

When a v0.42.0 contract or its evidence changes:

1. Update the package JSON with the verified contract state and limitations.
2. Link every matrix, threat, and checklist claim to local schema, implementation,
   test, workflow, example, or operator-guide evidence.
3. Run `go run ./cmd/waza runner assurance build --format markdown` from the
   repository root and replace the generated site page with its output.
4. Run `go test ./internal/runnerassurance ./cmd/waza` and the site documentation
   tests/build. Review the rendered claim against the actual contract and its
   unavailable states; a successful build validates references, not deployment
   safety.

The package is a candidate until v0.42.0 is released. Its version describes the
assurance document contract, not an assurance level or a Waza software version.
Evidence links are pinned to the `v0.42.0` tag so a released record cannot drift
as `main` advances. While the package is a candidate, those links resolve after
the release tag is published; the offline builder verifies the referenced files
against the local checkout. The candidate qualification workflow separately
binds its run URL, artifact ID, artifact digest and per-file hashes into the
workflow-linked assurance artifact; it does not create a tag or registry image.
Do not silently expand a claim when a host, signer, image, workflow, runtime, or
external scheduler does not produce the listed evidence.

## Release and migration

The assurance record complements the v0.42.0 release and migration notes in
[`RELEASE.md`](RELEASE.md), the
[published runner contracts](../site/src/content/docs/guides/runner-contract.mdx), and the
[site assurance page](../site/src/content/docs/guides/production-integration-assurance.mdx).
Runner contract additions remain opt-in; older runners may reject new fields,
so negotiate capabilities before depending on them. After a Waza or image
upgrade, verify the exact consumed digest, refresh inventory and provenance
evidence, and repeat host capability checks.

Known gaps remain explicit: Waza execution provenance still has no keyless/KMS
signer (GitHub artifact attestations cover build outputs, not execution
outcomes); reference-image inventory publication occurs only in the qualified
tag-release path and does not retroactively attest old images;
telemetry is post-run HTTP/JSON rather than live emission; several OS resource
limits/accounting dimensions are unsupported; isolation proves only the
documented off-host network and remote-tool boundary; and adapters include no
scheduler, while the optional provenance hook runs only when configured.
