package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/artifactmanifest"
	"github.com/microsoft/waza/internal/inventory"
	"github.com/microsoft/waza/internal/provenance"
	"github.com/microsoft/waza/internal/runnercontract"
	"github.com/spf13/cobra"
)

const maxProvenanceKeyBytes = 64 << 10

func newRunnerProvenanceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "provenance",
		Short: "Generate and verify signed execution provenance bundles",
		Long: `Generate a signed in-toto statement (DSSE envelope) binding the canonical
request, runner image digest, Waza version, runner API version, policy,
runtime/model identity, artifact chain and terminal outcome, then verify the
bundle offline against a trusted public key.`,
		SilenceUsage: true,
	}
	cmd.AddCommand(newProvenanceKeygenCommand(), newProvenanceGenerateCommand(), newProvenanceVerifyCommand())
	return cmd
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Close())
}

func newProvenanceKeygenCommand() *cobra.Command {
	var privatePath, publicPath string
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Create a local Ed25519 signing key pair (never overwrites existing files)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			privatePEM, publicPEM, keyID, err := provenance.GenerateKey()
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			if err := writeNewFile(privatePath, privatePEM, 0o600); err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing private key: %w", err)}
			}
			if err := writeNewFile(publicPath, publicPEM, 0o644); err != nil {
				return &ExitCodeError{Code: 2, Err: fmt.Errorf("writing public key: %w", err)}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "{\"key_id\":%q,\"mode\":%q}\n", keyID, provenance.ModeLocalEd25519)
			return err
		},
	}
	cmd.Flags().StringVar(&privatePath, "private-key", "", "Path for the new PKCS#8 PEM private key (created with mode 0600)")
	cmd.Flags().StringVar(&publicPath, "public-key", "", "Path for the new PKIX PEM public key to distribute to verifiers")
	_ = cmd.MarkFlagRequired("private-key")
	_ = cmd.MarkFlagRequired("public-key")
	return cmd
}

func provenanceMediaType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return "application/json"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

func newProvenanceGenerateCommand() *cobra.Command {
	var requestPath, manifestPath, artifactsDir, policyPath, policyMediaType string
	var imageReference, imageDigest, signerMode, signingKey, outDir, wazaVersion string
	var binds []string
	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Assemble and sign a provenance bundle for one completed execution",
		Long: `Assemble a provenance bundle directory and sign its statement. Every claim is
required: generation fails rather than emit a bundle with an unavailable image
digest, model identity, policy or artifact. Exit 5 means the selected signer
mode is unavailable in this build.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fail := func(code int, format string, args ...any) error {
				return &ExitCodeError{Code: code, Err: fmt.Errorf(format, args...)}
			}
			var signingPEM []byte
			if signerMode == provenance.ModeLocalEd25519 {
				data, err := readArtifactFile(signingKey, maxProvenanceKeyBytes)
				if err != nil {
					return fail(2, "reading signing key: %w", err)
				}
				signingPEM = data
			}
			signer, err := provenance.NewSigner(signerMode, signingPEM)
			if errors.Is(err, provenance.ErrSignerUnavailable) {
				return fail(5, "%w", err)
			}
			if err != nil {
				return fail(2, "%w", err)
			}
			in := provenance.GenerateInput{
				PolicyName: filepath.Base(policyPath), PolicyMediaType: policyMediaType,
				WazaVersion: wazaVersion, ImageReference: imageReference, ImageDigest: imageDigest,
				Objects: map[string][]byte{},
			}
			if in.PolicyMediaType == "" {
				in.PolicyMediaType = provenanceMediaType(policyPath)
			}
			if in.Request, err = readArtifactFile(requestPath, runnercontract.MaxRequestBytes); err != nil {
				return fail(2, "reading request: %w", err)
			}
			if in.Manifest, err = readArtifactFile(manifestPath, artifactmanifest.MaxManifestBytes+1); err != nil {
				return fail(2, "reading manifest: %w", err)
			}
			manifest, err := artifactmanifest.Parse(in.Manifest)
			if err != nil {
				return fail(2, "parsing manifest: %w", err)
			}
			for _, a := range manifest.Artifacts {
				if in.Objects[a.Name], err = readArtifactFile(filepath.Join(artifactsDir, a.Name), a.SizeBytes); err != nil {
					return fail(2, "reading artifact %s: %w", a.Name, err)
				}
			}
			if in.Policy, err = readArtifactFile(policyPath, 1<<20); err != nil {
				return fail(2, "reading policy: %w", err)
			}
			for _, bind := range binds {
				kind, path, ok := strings.Cut(bind, "=")
				if !ok || kind == "" || path == "" {
					return fail(2, "--bind must be kind=path, got %q", bind)
				}
				data, err := readArtifactFile(path, 16<<20)
				if err != nil {
					return fail(2, "reading %s binding: %w", kind, err)
				}
				in.Bindings = append(in.Bindings, provenance.BindingInput{Kind: kind, Name: filepath.Base(path), MediaType: provenanceMediaType(path), Data: data})
			}
			bundle, err := provenance.Generate(cmd.Context(), in, signer)
			if err != nil {
				return fail(2, "generating provenance: %w", err)
			}
			if err := bundle.WriteDir(outDir); err != nil {
				return fail(2, "writing bundle: %w", err)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "{\"status\":\"signed\",\"bundle\":%q,\"signer\":%q,\"key_id\":%q,\"canonical_digest\":%q,\"outcome\":%q}\n",
				outDir, bundle.Mode, bundle.KeyID, bundle.Statement.Predicate.Inputs.Identity.CanonicalDigest, bundle.Statement.Predicate.Outcome.Outcome)
			return err
		},
	}
	f := cmd.Flags()
	f.StringVar(&requestPath, "request", "", "Canonical runner request JSON that was executed")
	f.StringVar(&manifestPath, "manifest", "", "Canonical artifact manifest for the execution")
	f.StringVar(&artifactsDir, "artifacts", "", "Directory containing every manifest-declared artifact by name")
	f.StringVar(&policyPath, "policy", "", "Policy document governing the execution")
	f.StringVar(&policyMediaType, "policy-media-type", "", "Policy media type (inferred from the extension when empty)")
	f.StringVar(&imageReference, "image-reference", "", "Runner image reference (for example ghcr.io/org/runner:tag)")
	f.StringVar(&imageDigest, "image-digest", "", "Runner image digest, sha256:<64 hex>")
	f.StringVar(&wazaVersion, "waza-version", version, "Waza version claim (defaults to this binary's version)")
	f.StringVar(&signerMode, "signer", provenance.ModeLocalEd25519, "Signer mode: local-ed25519, sigstore-keyless or managed-kms")
	f.StringVar(&signingKey, "signing-key", "", "PKCS#8 PEM Ed25519 private key for local-ed25519")
	f.StringVar(&outDir, "out", "", "New or empty directory for the bundle")
	f.StringArrayVar(&binds, "bind", nil, "Additional member bound by digest as kind=path (kind must have a registered verifier)")
	for _, name := range []string{"request", "manifest", "artifacts", "policy", "image-reference", "image-digest", "out"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}

func newProvenanceVerifyCommand() *cobra.Command {
	var bundleDir string
	var publicKeys []string
	var opts provenance.Options
	var requireInventory bool
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a provenance bundle offline and print a verification report",
		Long: `Verify a provenance bundle offline against trusted public keys. Prints a
waza.provenance-verification report; exit 0 when verified, 1 when any check
failed, 2 for invalid flags or unreadable keys. Pins such as --expect-image-digest
bind declared claims to what the verifier consumed.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var pems [][]byte
			for _, path := range publicKeys {
				data, err := readArtifactFile(path, maxProvenanceKeyBytes)
				if err != nil {
					return &ExitCodeError{Code: 2, Err: fmt.Errorf("reading public key: %w", err)}
				}
				pems = append(pems, data)
			}
			if len(pems) > 0 {
				verifier, err := provenance.NewLocalVerifier(pems...)
				if err != nil {
					return &ExitCodeError{Code: 2, Err: err}
				}
				opts.Verifier = verifier
			}
			if requireInventory {
				opts.RequireBindingKinds = []string{inventory.BindingKind}
			}
			src, closeSrc, err := provenance.OpenDir(bundleDir)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			report := provenance.Verify(src, opts)
			if err := closeSrc(); err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			data, err := provenance.EncodeReport(report)
			if err != nil {
				return &ExitCodeError{Code: 2, Err: err}
			}
			return emitRunnerOperationJSON(cmd, data, report.ExitCode, report.Status)
		},
	}
	f := cmd.Flags()
	f.StringVar(&bundleDir, "bundle", "", "Provenance bundle directory")
	f.StringArrayVar(&publicKeys, "public-key", nil, "Trusted PKIX PEM Ed25519 public key (repeatable); never read from the bundle")
	f.StringVar(&opts.ExpectImageDigest, "expect-image-digest", "", "Require the signed runner image digest to equal this consumed digest")
	f.StringVar(&opts.ExpectWazaVersion, "expect-waza-version", "", "Require the signed Waza version to equal this value")
	f.StringVar(&opts.ExpectPolicySHA256, "expect-policy-sha256", "", "Require the signed policy digest to equal this hex sha256")
	f.BoolVar(&requireInventory, "require-inventory", false, "Fail unless the bundle binds a component inventory for the signed runner image")
	_ = cmd.MarkFlagRequired("bundle")
	return cmd
}
