package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/waza/internal/provenance"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	"github.com/sigstore/protobuf-specs/gen/pb-go/dsse"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

const cliWorkflowIdentity = "https://github.com/example/waza/.github/workflows/sign.yml@refs/heads/main"
const cliOIDCIssuer = "https://token.actions.githubusercontent.com"

// The CLI fixture substitutes only the network signer. Certificates, DSSE
// signatures, checkpoints and SETs are real; verification is not substituted.
type fixtureKeylessSigner struct {
	ca       *ca.VirtualSigstore
	keyID    string
	evidence []byte
}

func (*fixtureKeylessSigner) Mode() string       { return provenance.ModeSigstoreKeyless }
func (s *fixtureKeylessSigner) KeyID() string    { return s.keyID }
func (s *fixtureKeylessSigner) Evidence() []byte { return bytes.Clone(s.evidence) }
func (s *fixtureKeylessSigner) SignEnvelope(ctx context.Context, payload []byte) (provenance.Envelope, []byte, error) {
	sig, err := s.Sign(ctx, provenance.PAE(provenance.PayloadType, payload))
	if err != nil {
		return provenance.Envelope{}, nil, err
	}
	return provenance.Envelope{
		PayloadType: provenance.PayloadType, Payload: base64.StdEncoding.EncodeToString(payload),
		Signatures: []provenance.Signature{{KeyID: s.keyID, Sig: base64.StdEncoding.EncodeToString(sig)}},
	}, s.Evidence(), nil
}
func (s *fixtureKeylessSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("DSSEv1 %d %s ", len(provenance.PayloadType), provenance.PayloadType)
	remainder, ok := bytes.CutPrefix(message, []byte(prefix))
	if !ok {
		return nil, fmt.Errorf("fixture signer received invalid PAE")
	}
	length, payload, ok := bytes.Cut(remainder, []byte(" "))
	n, err := strconv.Atoi(string(length))
	if !ok || err != nil || n != len(payload) {
		return nil, fmt.Errorf("fixture signer received invalid payload length")
	}
	entity, err := s.ca.AttestAtTime(cliWorkflowIdentity, cliOIDCIssuer, payload, time.Now().Add(time.Second), true)
	if err != nil {
		return nil, err
	}
	content, err := entity.SignatureContent()
	if err != nil {
		return nil, err
	}
	envelope := content.EnvelopeContent().RawEnvelope()
	sig, err := base64.StdEncoding.DecodeString(envelope.Signatures[0].Sig)
	if err != nil {
		return nil, err
	}
	verification, err := entity.VerificationContent()
	if err != nil {
		return nil, err
	}
	cert := verification.Certificate()
	der, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(der)
	s.keyID = "sha256:" + hex.EncodeToString(digest[:])
	entries, err := entity.TlogEntries()
	if err != nil {
		return nil, err
	}
	entry := entries[0].TransparencyLogEntry()
	entry.KindVersion = &protorekor.KindVersion{Kind: "dsse", Version: "0.0.1"}
	set, err := s.ca.RekorSignPayload(tlog.RekorPayload{
		Body:           base64.StdEncoding.EncodeToString(entry.CanonicalizedBody),
		IntegratedTime: entry.IntegratedTime, LogIndex: entry.LogIndex,
		LogID: hex.EncodeToString(entry.LogId.KeyId),
	})
	if err != nil {
		return nil, err
	}
	entry.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	b := &protobundle.Bundle{
		MediaType: "application/vnd.dev.sigstore.bundle.v0.3+json",
		Content: &protobundle.Bundle_DsseEnvelope{DsseEnvelope: &dsse.Envelope{
			PayloadType: provenance.PayloadType, Payload: payload,
			Signatures: []*dsse.Signature{{Sig: sig}},
		}},
		VerificationMaterial: &protobundle.VerificationMaterial{
			Content:     &protobundle.VerificationMaterial_Certificate{Certificate: &protocommon.X509Certificate{RawBytes: cert.Raw}},
			TlogEntries: []*protorekor.TransparencyLogEntry{entry},
		},
	}
	s.evidence, err = protojson.Marshal(b)
	return sig, err
}

func TestRunnerProvenanceKeylessCLI(t *testing.T) {
	virtual, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	logs := virtual.RekorLogs()
	for id, log := range logs {
		log.ID, err = hex.DecodeString(id)
		require.NoError(t, err)
	}
	trust, err := root.NewTrustedRoot(root.TrustedRootMediaType01, virtual.FulcioCertificateAuthorities(), nil, nil, logs)
	require.NoError(t, err)
	trustJSON, err := trust.MarshalJSON()
	require.NoError(t, err)
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "trusted-root.json")
	require.NoError(t, os.WriteFile(rootPath, trustJSON, 0o600))
	artifacts := filepath.Join(dir, "objects")
	require.NoError(t, os.Mkdir(artifacts, 0o755))
	terminal, err := os.ReadFile("../../internal/artifactmanifest/testdata/publication-terminal.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(artifacts, "terminal.json"), terminal, 0o600))
	policy := filepath.Join(dir, "policy.json")
	require.NoError(t, os.WriteFile(policy, []byte(`{"policy":"test"}`), 0o600))
	bundle := filepath.Join(dir, "bundle")
	args := []string{"generate", "--signer", "sigstore-keyless", "--trusted-root", rootPath,
		"--request", "../../examples/runner/unit.json", "--manifest", "../../internal/artifactmanifest/testdata/publication-manifest.json",
		"--artifacts", artifacts, "--policy", policy, "--image-reference", "ghcr.io/example/runner:test",
		"--image-digest", "sha256:" + strings.Repeat("a", 64), "--waza-version", "0.42.0", "--out", bundle}

	t.Setenv("GITHUB_ACTIONS", "")
	output, code := runProvenanceCLI(t, args...)
	require.Equal(t, 5, code, "%s", output)
	_, err = os.Stat(bundle)
	require.ErrorIs(t, err, os.ErrNotExist, "no bundle may be emitted when OIDC is unavailable")
	old := provenanceKeylessSigner
	t.Cleanup(func() { provenanceKeylessSigner = old })
	provenanceKeylessSigner = func([]byte) (provenance.Signer, error) { return &fixtureKeylessSigner{ca: virtual}, nil }
	output, code = runProvenanceCLI(t, args...)
	require.Zero(t, code, "%s", output)
	require.Contains(t, output, `"signer":"sigstore-keyless"`)

	verify := []string{"verify", "--bundle", bundle, "--trusted-root", rootPath,
		"--certificate-oidc-issuer", cliOIDCIssuer, "--certificate-identity", cliWorkflowIdentity}
	output, code = runProvenanceCLI(t, verify...)
	require.Zero(t, code, "%s", output)
	var report provenance.Report
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	require.Equal(t, "verified", report.Status)
	require.True(t, report.Signer.Trusted)
	wrongIdentity := append([]string(nil), verify...)
	wrongIdentity[len(wrongIdentity)-1] += "-wrong"
	output, code = runProvenanceCLI(t, wrongIdentity...)
	require.Equal(t, 1, code, "%s", output)
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	require.Equal(t, "failed", report.Status)
	require.False(t, report.Signer.Trusted)
	_, code = runProvenanceCLI(t, verify[:len(verify)-2]...)
	require.Equal(t, 2, code, "missing certificate identity is invalid input")
	_, code = runProvenanceCLI(t, append(verify, "--public-key", rootPath)...)
	require.Equal(t, 2, code, "local and keyless trust flags cannot be mixed")
}
