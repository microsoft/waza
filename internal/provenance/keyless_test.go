package provenance

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/secure-systems-lab/go-securesystemslib/dsse"
	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	protocommon "github.com/sigstore/protobuf-specs/gen/pb-go/common/v1"
	protorekor "github.com/sigstore/protobuf-specs/gen/pb-go/rekor/v1"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/sign"
	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/tlog"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	testWorkflowIdentity = "https://github.com/example/waza/.github/workflows/sign.yml@refs/heads/main"
	testOIDCIssuer       = "https://token.actions.githubusercontent.com"
)

type fakeKeypair struct{ key *ecdsa.PrivateKey }

func (*fakeKeypair) GetHashAlgorithm() protocommon.HashAlgorithm {
	return protocommon.HashAlgorithm_SHA2_256
}
func (*fakeKeypair) GetSigningAlgorithm() protocommon.PublicKeyDetails {
	return protocommon.PublicKeyDetails_PKIX_ECDSA_P256_SHA_256
}
func (*fakeKeypair) GetHint() []byte                  { return nil }
func (*fakeKeypair) GetKeyAlgorithm() string          { return "ECDSA" }
func (k *fakeKeypair) GetPublicKey() crypto.PublicKey { return &k.key.PublicKey }
func (k *fakeKeypair) GetPublicKeyPem() (string, error) {
	der, err := x509.MarshalPKIXPublicKey(k.GetPublicKey())
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), nil
}
func (k *fakeKeypair) SignData(ctx context.Context, data []byte) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	hash := sha256.Sum256(data)
	sig, err := ecdsa.SignASN1(rand.Reader, k.key, hash[:])
	return sig, hash[:], err
}

type fakeFulcio struct {
	cert  *x509.Certificate
	calls int
}

func (f *fakeFulcio) GetCertificate(_ context.Context, key sign.Keypair, opts *sign.CertificateProviderOptions) ([]byte, error) {
	f.calls++
	publicKey, ok := f.cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || opts.IDToken != "fake-oidc-token" || !publicKey.Equal(key.GetPublicKey()) {
		return nil, errors.New("fake Fulcio rejected token or key")
	}
	return f.cert.Raw, nil
}

type fakeRekor struct {
	ca    *ca.VirtualSigstore
	cert  *x509.Certificate
	calls int
}

func (r *fakeRekor) GetTransparencyLogEntry(_ context.Context, _ []byte, b *protobundle.Bundle) error {
	r.calls++
	e := b.GetDsseEnvelope()
	if e == nil || len(e.Signatures) != 1 {
		return errors.New("missing fake Rekor DSSE input")
	}
	envelope := &dsse.Envelope{
		PayloadType: e.PayloadType, Payload: base64.StdEncoding.EncodeToString(e.Payload),
		Signatures: []dsse.Signature{{Sig: base64.StdEncoding.EncodeToString(e.Signatures[0].Sig)}},
	}
	entry, err := r.ca.GenerateTlogEntry(r.cert, envelope, e.Signatures[0].Sig, time.Now().Unix(), true)
	if err != nil {
		return err
	}
	logEntry := entry.TransparencyLogEntry()
	logEntry.KindVersion = &protorekor.KindVersion{Kind: "dsse", Version: "0.0.1"}
	set, err := r.ca.RekorSignPayload(tlog.RekorPayload{
		Body:           base64.StdEncoding.EncodeToString(logEntry.CanonicalizedBody),
		IntegratedTime: logEntry.IntegratedTime, LogIndex: logEntry.LogIndex,
		LogID: hex.EncodeToString(logEntry.LogId.KeyId),
	})
	if err != nil {
		return err
	}
	logEntry.InclusionPromise = &protorekor.InclusionPromise{SignedEntryTimestamp: set}
	b.VerificationMaterial.TlogEntries = append(b.VerificationMaterial.TlogEntries, logEntry)
	return nil
}

func fakeKeyless(t *testing.T) (*keylessSigner, []byte, *fakeFulcio, *fakeRekor) {
	t.Helper()
	virtual, err := ca.NewVirtualSigstore()
	require.NoError(t, err)
	logs := virtual.RekorLogs()
	for id, log := range logs {
		log.ID, err = hex.DecodeString(id)
		require.NoError(t, err)
	}
	trust, err := root.NewTrustedRoot(root.TrustedRootMediaType01,
		virtual.FulcioCertificateAuthorities(), nil, nil, logs)
	require.NoError(t, err)
	trustJSON, err := trust.MarshalJSON()
	require.NoError(t, err)
	created, err := NewKeylessSigner(trustJSON)
	require.NoError(t, err)
	signer, ok := created.(*keylessSigner)
	require.True(t, ok)
	cert, private, err := virtual.GenerateLeafCert(testWorkflowIdentity, testOIDCIssuer)
	require.NoError(t, err)
	key, ok := private.(*ecdsa.PrivateKey)
	require.True(t, ok)
	signer.key = &fakeKeypair{key}
	der, err := x509.MarshalPKIXPublicKey(signer.key.GetPublicKey())
	require.NoError(t, err)
	signer.keyID = "sha256:" + sha256Hex(der)
	fulcio := &fakeFulcio{cert: cert}
	rekor := &fakeRekor{ca: virtual, cert: cert}
	signer.options.CertificateProvider = fulcio
	signer.options.TransparencyLogs = []sign.Transparency{rekor}
	signer.token = func(context.Context) (string, error) { return "fake-oidc-token", nil }
	return signer, trustJSON, fulcio, rekor
}

func TestKeylessGenerateVerifyOffline(t *testing.T) {
	signer, trust, fulcio, rekor := fakeKeyless(t)
	fixture := newFixture(t)
	bundle, err := Generate(t.Context(), fixture.input, signer)
	require.NoError(t, err)
	require.Equal(t, 1, fulcio.calls)
	require.Equal(t, 1, rekor.calls)
	require.NotEmpty(t, bundle.Files[SigstoreEvidencePath])

	verifier, err := NewKeylessVerifier(trust, testWorkflowIdentity, "", testOIDCIssuer)
	require.NoError(t, err)
	// Verification must not consult ambient service configuration or network.
	signer.token = func(context.Context) (string, error) { t.Fatal("verification fetched OIDC"); return "", nil }
	beforeFulcio, beforeRekor := fulcio.calls, rekor.calls
	report := verifyFiles(t, bundle.Files, Options{Verifier: verifier, ExpectImageDigest: testImageDigest})
	require.Equal(t, "verified", report.Status, "%+v", report.Failures())
	require.Equal(t, beforeFulcio, fulcio.calls)
	require.Equal(t, beforeRekor, rekor.calls)
	require.Equal(t, ModeSigstoreKeyless, report.Signer.Mode)

	otherSigner, untrustedRoot, _, _ := fakeKeyless(t)
	require.NotEqual(t, signer.KeyID(), otherSigner.KeyID())
	for name, test := range map[string]struct {
		root, identity, regexp, issuer string
	}{
		"wrong identity": {identity: testWorkflowIdentity + "-wrong", issuer: testOIDCIssuer},
		"wrong issuer":   {identity: testWorkflowIdentity, issuer: "https://wrong.example"},
		"wrong root":     {root: string(untrustedRoot), identity: testWorkflowIdentity, issuer: testOIDCIssuer},
	} {
		t.Run(name, func(t *testing.T) {
			rootData := trust
			if test.root != "" {
				rootData = []byte(test.root)
			}
			v, err := NewKeylessVerifier(rootData, test.identity, test.regexp, test.issuer)
			require.NoError(t, err)
			r := verifyFiles(t, bundle.Files, Options{Verifier: v})
			require.Equal(t, "failed", r.Status)
			require.Equal(t, "signature_invalid", failure(r, "signature").Code)
		})
	}
	for _, name := range []string{"missing evidence", "missing log", "missing proof", "bad checkpoint", "bad SET", "tampered payload", "tampered both payloads", "substituted evidence", "tampered terminal"} {
		t.Run(name, func(t *testing.T) {
			files := maps.Clone(bundle.Files)
			var evidence protobundle.Bundle
			require.NoError(t, protojson.Unmarshal(files[SigstoreEvidencePath], &evidence))
			switch name {
			case "missing evidence":
				delete(files, SigstoreEvidencePath)
			case "missing log":
				evidence.VerificationMaterial.TlogEntries = nil
			case "missing proof":
				evidence.VerificationMaterial.TlogEntries[0].InclusionProof = nil
			case "bad checkpoint":
				evidence.VerificationMaterial.TlogEntries[0].InclusionProof.Checkpoint.Envelope += "wrong"
			case "bad SET":
				evidence.VerificationMaterial.TlogEntries[0].InclusionPromise.SignedEntryTimestamp[0] ^= 1
			case "tampered payload", "tampered both payloads":
				var envelope Envelope
				require.NoError(t, json.Unmarshal(files[EnvelopePath], &envelope))
				payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
				require.NoError(t, err)
				envelope.Payload = base64.StdEncoding.EncodeToString(append(payload, ' '))
				files[EnvelopePath], err = json.Marshal(envelope)
				require.NoError(t, err)
				if name == "tampered both payloads" {
					evidence.GetDsseEnvelope().Payload = append(payload, ' ')
				}
			case "tampered terminal":
				path := bundle.Statement.Predicate.Outcome.Artifact.Path
				files[path] = append(bytes.Clone(files[path]), ' ')
			case "substituted evidence":
				other, _, _, _ := fakeKeyless(t)
				substitute, err := Generate(t.Context(), fixture.input, other)
				require.NoError(t, err)
				files[SigstoreEvidencePath] = substitute.Files[SigstoreEvidencePath]
			}
			if name != "missing evidence" && name != "substituted evidence" {
				files[SigstoreEvidencePath], err = protojson.Marshal(&evidence)
				require.NoError(t, err)
			}
			r := verifyFiles(t, files, Options{Verifier: verifier})
			require.Equal(t, "failed", r.Status, "%+v", r)
			if name == "tampered terminal" {
				require.Equal(t, "passed", failure(r, "signature").Status)
				require.Equal(t, "failed", failure(r, "outcome").Status)
			} else {
				require.Equal(t, "failed", failure(r, "signature").Status)
				require.Nil(t, r.Predicate, "unverified claims must not appear as verified evidence")
			}
		})
	}
	// A fully scoped regex is an alternative-valid policy.
	regex, err := NewKeylessVerifier(trust, "", `^https://github\.com/example/waza/\.github/workflows/sign\.yml@refs/heads/main$`, testOIDCIssuer)
	require.NoError(t, err)
	require.Equal(t, "verified", verifyFiles(t, bundle.Files, Options{Verifier: regex}).Status)
}

func TestKeylessTrustPolicyRequired(t *testing.T) {
	_, trust, _, _ := fakeKeyless(t)
	for _, test := range []struct{ identity, regexp, issuer string }{
		{"", "", testOIDCIssuer}, {testWorkflowIdentity, "", ""},
		{testWorkflowIdentity, "^.*$", testOIDCIssuer}, {"", ".*", testOIDCIssuer},
		{"", "^[invalid$", testOIDCIssuer},
	} {
		_, err := NewKeylessVerifier(trust, test.identity, test.regexp, test.issuer)
		require.Error(t, err)
	}
	_, err := NewKeylessVerifier(nil, testWorkflowIdentity, "", testOIDCIssuer)
	require.Error(t, err)
}

func TestGitHubOIDCTokenRequest(t *testing.T) {
	for _, name := range []string{"success", "denied", "empty token", "oversized", "redirect"} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				require.Equal(t, "sigstore", r.URL.Query().Get("audience"))
				require.Equal(t, "existing", r.URL.Query().Get("query"))
				require.Equal(t, "Bearer fake-request-secret", r.Header.Get("Authorization"))
				switch name {
				case "denied":
					http.Error(w, "fake-request-secret", http.StatusForbidden)
				case "empty token":
					_, _ = fmt.Fprint(w, `{"value":""}`)
				case "oversized":
					_, _ = fmt.Fprint(w, strings.Repeat("x", maxOIDCResponseBytes+1))
				case "redirect":
					w.Header().Set("Location", "/redirected")
					w.WriteHeader(http.StatusFound)
				default:
					_, _ = fmt.Fprint(w, `{"value":"fake-oidc-token"}`)
				}
			}))
			defer server.Close()
			client := server.Client()
			baseTransport := client.Transport
			target, err := url.Parse(server.URL)
			require.NoError(t, err)
			client.Transport = tokenTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "oidc.actions.githubusercontent.com", r.URL.Hostname())
				redirected := r.Clone(r.Context())
				redirected.URL.Host = target.Host
				return baseTransport.RoundTrip(redirected)
			})
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			env := map[string]string{"GITHUB_ACTIONS": "true", "ACTIONS_ID_TOKEN_REQUEST_URL": "https://oidc.actions.githubusercontent.com?query=existing", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "fake-request-secret"}
			token, err := requestGithubOIDCToken(t.Context(), func(key string) string { return env[key] }, client)
			require.Equal(t, 1, requests)
			if name == "success" {
				require.NoError(t, err)
				require.Equal(t, "fake-oidc-token", token)
			} else {
				require.ErrorIs(t, err, ErrSignerUnavailable)
				require.NotContains(t, err.Error(), "fake-request-secret")
				require.Empty(t, token)
			}
		})
	}
}

type tokenTransport func(*http.Request) (*http.Response, error)

func (f tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type tokenBody struct{ *strings.Reader }

func (tokenBody) Close() error { return errors.New("private-close-detail") }

func TestGitHubOIDCResponseCloseFailure(t *testing.T) {
	client := &http.Client{Transport: tokenTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: tokenBody{strings.NewReader(`{"value":"fake-oidc-token"}`)}}, nil
	})}
	env := map[string]string{"GITHUB_ACTIONS": "true", "ACTIONS_ID_TOKEN_REQUEST_URL": "https://oidc.actions.githubusercontent.com", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "fake-secret"}
	token, err := requestGithubOIDCToken(t.Context(), func(key string) string { return env[key] }, client)
	require.ErrorIs(t, err, ErrSignerUnavailable)
	require.Empty(t, token)
	require.NotContains(t, err.Error(), "private-close-detail")
}

func TestGitHubOIDCRejectsUntrustedEndpoints(t *testing.T) {
	client := &http.Client{Transport: tokenTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("OIDC credentials sent to an untrusted endpoint")
		return nil, errors.New("unexpected network")
	})}
	for _, endpoint := range []string{
		"http://oidc.actions.githubusercontent.com",
		"https://attacker.example",
		"https://oidc.actions.githubusercontent.com.attacker.example",
		"https://oidc.actions.githubusercontent.com:444",
		"https://secret@oidc.actions.githubusercontent.com",
	} {
		env := map[string]string{"GITHUB_ACTIONS": "true", "ACTIONS_ID_TOKEN_REQUEST_URL": endpoint, "ACTIONS_ID_TOKEN_REQUEST_TOKEN": "fake-secret"}
		_, err := requestGithubOIDCToken(t.Context(), func(key string) string { return env[key] }, client)
		require.ErrorIs(t, err, ErrSignerUnavailable)
		require.NotContains(t, err.Error(), "fake-secret")
	}
}

func TestKeylessSignerConcurrentReuse(t *testing.T) {
	signer, trust, _, _ := fakeKeyless(t)
	verifier, err := NewKeylessVerifier(trust, testWorkflowIdentity, "", testOIDCIssuer)
	require.NoError(t, err)
	f := newFixture(t)
	type result struct {
		bundle *Bundle
		err    error
	}
	results := make(chan result, 3)
	var group sync.WaitGroup
	for i := range 3 {
		group.Go(func() {
			input := f.input
			input.WazaVersion = fmt.Sprintf("0.42.%d", i)
			bundle, err := Generate(t.Context(), input, signer)
			results <- result{bundle, err}
		})
	}
	group.Wait()
	close(results)
	seen := map[string]bool{}
	for result := range results {
		require.NoError(t, result.err)
		report := verifyFiles(t, result.bundle.Files, Options{Verifier: verifier})
		require.Equal(t, "verified", report.Status, "%+v", report.Failures())
		require.False(t, seen[report.Predicate.Waza.Version])
		seen[report.Predicate.Waza.Version] = true
	}
	require.Len(t, seen, 3)
}

func TestKeylessUnavailableBeforeSigning(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	token, err := githubOIDCToken(t.Context())
	require.Empty(t, token)
	require.ErrorIs(t, err, ErrSignerUnavailable)
	signer, _, _, _ := fakeKeyless(t)
	fixture := newFixture(t)
	signer.token = githubOIDCToken
	_, err = Generate(t.Context(), fixture.input, signer)
	require.ErrorIs(t, err, ErrSignerUnavailable)
	// Invalid execution inputs must be rejected before requesting OIDC.
	signer.token = func(context.Context) (string, error) { t.Fatal("invalid input requested OIDC"); return "", nil }
	fixture.input.Request = bytes.Repeat([]byte("invalid"), 2)
	_, err = Generate(t.Context(), fixture.input, signer)
	require.Error(t, err)
}
