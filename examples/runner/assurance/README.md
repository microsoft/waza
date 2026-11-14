# Using the production integration assurance package

The [v0.42.0 assurance package](../../../docs/PRODUCTION_INTEGRATION_ASSURANCE-1.0.json)
is a versioned operator guide, not a deployment certificate. Use the rows
marked `supported`, `unsupported`, and `unavailable` as separate outcomes.
Verify evidence for the exact runner build, host, image digest, and external
scheduler you operate.

## Example review sequence

1. Check what this binary and host actually expose:

   ```bash
   waza runner capabilities
   waza runner budgets
   ```

   Do not treat a configured but unsupported budget as enforced. Wall time,
   model turns, and tool calls are the enforced budget controls; CPU and memory
   observations are process-scoped where available, and disk/process-count
   accounting is unavailable.

2. Verify inventory against the immutable digest you pulled, not a tag:

   ```bash
   waza runner inventory verify --inventory runner.cdx.json \
     --image-digest sha256:<64-hex-digest> \
     --image-reference ghcr.io/microsoft/waza-runner-reference
   ```

   Check the separate registry attestation as well. The reference image
   publication workflow is manually triggered; older images do not gain an
   inventory attestation retroactively.

3. Verify completed-run provenance with an operator-managed public key:

   ```bash
   waza runner provenance verify --bundle provenance/ \
     --public-key signing.pub.pem \
     --expect-image-digest sha256:<64-hex-digest>
   ```

   Local Ed25519 verification is available. Keyless and managed-KMS signing are
   not. Distribute the trusted public key out of band.

4. Run isolation preflight inside the final worker boundary. Treat
   filesystem/environment/subprocess results as unavailable unless an external
   provider proves them. Only off-host network and remote-tool reachability can
   be evidenced by the host provider under the documented no-non-loopback
   interface condition; loopback remains reachable.

5. After execution, derive telemetry from matching completed documents:

   ```bash
   waza runner telemetry --terminal terminal.json --manifest manifest.json \
     --out-dir telemetry/
   ```

   This is post-run derivation and HTTP/JSON export, not live hot-path emission.
   The default payload policy excludes payloads.

6. Keep scheduling outside Waza. An adapter translates one descriptor to one
   public runner call; the external scheduler owns delivery, retry, placement,
   and leases. The optional provenance hook is not configured in the CLI.

For detailed requirements and existing Lab evidence views, use the
[production integration assurance guide](https://microsoft.github.io/waza/guides/production-integration-assurance/).
Reassess after each runner or image upgrade; this example is not a substitute
for environment-specific threat analysis or release acceptance.
