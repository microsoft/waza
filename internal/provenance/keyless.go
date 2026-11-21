package provenance

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	protobundle "github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	sigbundle "github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/sign"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	SigstoreEvidencePath  = "sigstore.bundle.json"
	MaxTrustedRootBytes   = 1 << 20
	maxOIDCResponseBytes  = 64 << 10
	keylessTimeout        = 90 * time.Second
	keylessRequestTimeout = 30 * time.Second
	keylessMediaType      = "application/vnd.dev.sigstore.bundle.v0.3+json"
)

type keylessSigner struct {
	gate    chan struct{}
	key     sign.Keypair
	keyID   string
	options sign.BundleOptions
	token   func(context.Context) (string, error)
}

// NewKeylessSigner uses only GitHub Actions OIDC and public-good Fulcio/Rekor
// v1. The caller supplies trust material; neither signing nor verification
// discovers trust from the execution bundle or fetches it implicitly.
func NewKeylessSigner(trustedRootJSON []byte) (Signer, error) {
	trust, err := parseTrustedRoot(trustedRootJSON)
	if err != nil {
		return nil, err
	}
	if err := validateSigningTrust(trust); err != nil {
		return nil, err
	}
	key, err := sign.NewEphemeralKeypair(nil)
	if err != nil {
		return nil, fmt.Errorf("%w: creating ephemeral signing key: %v", ErrSignerUnavailable, err)
	}
	der, err := x509.MarshalPKIXPublicKey(key.GetPublicKey())
	if err != nil {
		return nil, fmt.Errorf("encoding ephemeral public key: %w", err)
	}
	return &keylessSigner{
		gate: make(chan struct{}, 1), key: key, keyID: "sha256:" + sha256Hex(der), token: githubOIDCToken,
		options: sign.BundleOptions{
			TrustedRoot:         trust,
			CertificateProvider: &keylessFulcio{baseURL: "https://fulcio.sigstore.dev"},
			TransparencyLogs: []sign.Transparency{sign.NewRekor(&sign.RekorOptions{
				BaseURL: "https://rekor.sigstore.dev", Version: 1, Timeout: keylessRequestTimeout,
			})},
		},
	}, nil
}

func (s *keylessSigner) Mode() string  { return ModeSigstoreKeyless }
func (s *keylessSigner) KeyID() string { return s.keyID }
func (s *keylessSigner) SignEnvelope(ctx context.Context, payload []byte) (Envelope, []byte, error) {
	signature, evidence, err := s.signEvidence(ctx, PAE(PayloadType, payload))
	if err != nil {
		return Envelope{}, nil, err
	}
	return Envelope{
		PayloadType: PayloadType, Payload: base64.StdEncoding.EncodeToString(payload),
		Signatures: []Signature{{KeyID: s.keyID, Sig: base64.StdEncoding.EncodeToString(signature)}},
	}, evidence, nil
}

func (s *keylessSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	signature, _, err := s.signEvidence(ctx, message)
	return signature, err
}

func (s *keylessSigner) signEvidence(ctx context.Context, message []byte) ([]byte, []byte, error) {
	// sigstore-go's Rekor client lazily initializes shared client state.
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return nil, nil, keylessContextError(ctx)
	}
	if err := keylessContextError(ctx); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, keylessTimeout)
	defer cancel()
	if err := validateSigningTrust(s.options.TrustedRoot); err != nil {
		return nil, nil, err
	}
	// The Signer contract receives PAE, whereas sigstore-go accepts its payload.
	prefix := "DSSEv1 " + strconv.Itoa(len(PayloadType)) + " " + PayloadType + " "
	if !bytes.HasPrefix(message, []byte(prefix)) {
		return nil, nil, errors.New("keyless signer requires Waza DSSE PAE")
	}
	length, payload, ok := bytes.Cut(message[len(prefix):], []byte(" "))
	n, err := strconv.Atoi(string(length))
	if !ok || err != nil || n != len(payload) {
		return nil, nil, errors.New("keyless signer received invalid DSSE payload length")
	}
	token, err := s.token(ctx)
	if contextErr := keylessContextError(ctx); contextErr != nil {
		return nil, nil, contextErr
	}
	if err != nil {
		return nil, nil, err
	}
	opts := s.options
	opts.Context = ctx
	opts.CertificateProviderOptions = &sign.CertificateProviderOptions{IDToken: token}
	b, err := sign.Bundle(&sign.DSSEData{Data: payload, PayloadType: PayloadType}, s.key, opts)
	if contextErr := keylessContextError(ctx); contextErr != nil {
		return nil, nil, contextErr
	}
	if err != nil {
		// Provider errors may contain JWTs or HTTP authorization data.
		return nil, nil, fmt.Errorf("%w: Fulcio/Rekor signing or trust validation failed (check OIDC permissions, egress and trusted root)", ErrSignerUnavailable)
	}
	envelope := b.GetDsseEnvelope()
	if envelope == nil || envelope.PayloadType != PayloadType || !bytes.Equal(envelope.Payload, payload) ||
		len(envelope.Signatures) != 1 || len(envelope.Signatures[0].Sig) == 0 {
		return nil, nil, fmt.Errorf("%w: Sigstore returned incomplete or inconsistent signature or transparency evidence", ErrSignerUnavailable)
	}
	if err := validateKeylessEvidence(b); err != nil {
		return nil, nil, fmt.Errorf("%w: Sigstore returned invalid evidence: %v", ErrSignerUnavailable, err)
	}
	evidence, err := protojson.Marshal(b)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encoding Sigstore evidence", ErrSignerUnavailable)
	}
	if len(evidence) > maxEnvelopeBytes {
		return nil, nil, fmt.Errorf("%w: Sigstore evidence exceeds bundle member limit", ErrSignerUnavailable)
	}
	return bytes.Clone(envelope.Signatures[0].Sig), evidence, nil
}

func keylessContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: keyless signing canceled: %w", ErrSignerUnavailable, err)
	}
	return nil
}

func validateSigningTrust(trust root.TrustedMaterial) error {
	if trust == nil || len(trust.FulcioCertificateAuthorities()) == 0 || len(trust.RekorLogs()) == 0 {
		return fmt.Errorf("%w: keyless signing trusted root requires at least one Fulcio CA and one Rekor log", ErrSignerUnavailable)
	}
	return nil
}

type keylessFulcio struct {
	baseURL   string
	transport http.RoundTripper
}

func (f *keylessFulcio) GetCertificate(ctx context.Context, key sign.Keypair, opts *sign.CertificateProviderOptions) ([]byte, error) {
	origin, err := url.Parse(f.baseURL)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil {
		return nil, errors.New("keyless Fulcio endpoint must use an HTTPS origin without credentials")
	}
	port := origin.Port()
	if port == "" {
		port = "443"
	}
	// Fulcio v1.3.0 ignores ctx on HTTP requests. Bind the transport per call,
	// retaining the request timeout through reading the response body.
	ctx, cancel := context.WithTimeout(ctx, keylessRequestTimeout)
	defer cancel()
	transport := f.transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	provider := sign.NewFulcio(&sign.FulcioOptions{
		BaseURL: f.baseURL, Timeout: keylessRequestTimeout,
		Transport: keylessFulcioTransport{context: ctx, base: transport, hostname: origin.Hostname(), port: port},
	})
	return provider.GetCertificate(ctx, key, opts)
}

type keylessFulcioTransport struct {
	context  context.Context
	base     http.RoundTripper
	hostname string
	port     string
}

func (t keylessFulcioTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	port := request.URL.Port()
	if port == "" {
		port = "443"
	}
	// Check every redirect hop before the bearer token reaches the transport.
	if request.URL.Scheme != "https" || request.URL.User != nil ||
		!strings.EqualFold(request.URL.Hostname(), t.hostname) || port != t.port {
		return nil, errors.New("keyless Fulcio request destination must match its HTTPS origin")
	}
	return t.base.RoundTrip(request.Clone(t.context))
}

func githubOIDCToken(ctx context.Context) (string, error) {
	return requestGithubOIDCToken(ctx, os.Getenv, &http.Client{
		Timeout: keylessRequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	})
}

func requestGithubOIDCToken(ctx context.Context, getenv func(string) string, client *http.Client) (token string, retErr error) {
	requestURL, authorization := getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if getenv("GITHUB_ACTIONS") != "true" || requestURL == "" || authorization == "" {
		return "", fmt.Errorf("%w: GitHub Actions OIDC unavailable; require permissions id-token: write and ACTIONS_ID_TOKEN_REQUEST_URL/TOKEN", ErrSignerUnavailable)
	}
	u, err := url.Parse(requestURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return "", fmt.Errorf("%w: GitHub Actions OIDC request URL must be HTTPS", ErrSignerUnavailable)
	}
	if !strings.HasSuffix(strings.ToLower(u.Hostname()), ".actions.githubusercontent.com") ||
		(u.Port() != "" && u.Port() != "443") {
		return "", fmt.Errorf("%w: GitHub Actions OIDC request URL must use a public GitHub Actions host", ErrSignerUnavailable)
	}
	query := u.Query()
	query.Set("audience", "sigstore")
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("%w: could not create OIDC request", ErrSignerUnavailable)
	}
	req.Header.Set("Authorization", "Bearer "+authorization)
	response, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: GitHub Actions OIDC request failed", ErrSignerUnavailable)
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			token = ""
			retErr = errors.Join(retErr, fmt.Errorf("%w: closing GitHub Actions OIDC response failed", ErrSignerUnavailable))
		}
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: GitHub Actions OIDC request returned HTTP %d", ErrSignerUnavailable, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxOIDCResponseBytes+1))
	if err != nil || len(data) > maxOIDCResponseBytes {
		return "", fmt.Errorf("%w: OIDC response unreadable or too large", ErrSignerUnavailable)
	}
	var result struct {
		Value string `json:"value"`
	}
	if json.Unmarshal(data, &result) != nil || strings.TrimSpace(result.Value) == "" {
		return "", fmt.Errorf("%w: OIDC response did not contain a token", ErrSignerUnavailable)
	}
	return result.Value, nil
}

type keylessVerifier struct {
	verifier *sigverify.Verifier
	identity sigverify.CertificateIdentity
}

func validateKeylessEvidence(b *protobundle.Bundle) error {
	if b.GetMediaType() != keylessMediaType {
		return errors.New("sigstore evidence must use bundle media type v0.3")
	}
	entries := b.GetVerificationMaterial().GetTlogEntries()
	if len(entries) == 0 {
		return errors.New("sigstore evidence has no transparency log entries")
	}
	for i, entry := range entries {
		if entry.GetInclusionProof() == nil || entry.GetInclusionProof().GetCheckpoint().GetEnvelope() == "" ||
			len(entry.GetInclusionPromise().GetSignedEntryTimestamp()) == 0 {
			return fmt.Errorf("sigstore transparency log entry %d requires an inclusion proof, checkpoint and signed entry timestamp", i)
		}
	}
	return nil
}

func parseTrustedRoot(data []byte) (*root.TrustedRoot, error) {
	if len(data) == 0 || len(data) > MaxTrustedRootBytes {
		return nil, errors.New("keyless signing/verification requires a verifier-owned Sigstore trusted-root JSON file (at most 1 MiB)")
	}
	trust, err := root.NewTrustedRootFromJSON(data)
	if err != nil {
		return nil, fmt.Errorf("invalid Sigstore trusted root: %w", err)
	}
	return trust, nil
}

// NewKeylessVerifier creates a strictly offline verifier. The root and identity
// policy must be supplied independently of the untrusted execution bundle.
func NewKeylessVerifier(trustedRootJSON []byte, identity, identityRegexp, issuer string) (SignatureVerifier, error) {
	if issuer == "" || (identity == "") == (identityRegexp == "") {
		return nil, errors.New("keyless verification requires --certificate-oidc-issuer and exactly one of --certificate-identity or --certificate-identity-regexp")
	}
	if identityRegexp != "" && (!strings.HasPrefix(identityRegexp, "^") || !strings.HasSuffix(identityRegexp, "$")) {
		return nil, errors.New("--certificate-identity-regexp must be anchored with ^ and $")
	}
	if identityRegexp != "" {
		// Anchor the complete expression, including all top-level alternatives.
		identityRegexp = "^(?:" + identityRegexp + ")$"
	}
	trust, err := parseTrustedRoot(trustedRootJSON)
	if err != nil {
		return nil, err
	}
	certIdentity, err := sigverify.NewShortCertificateIdentity(issuer, "", identity, identityRegexp)
	if err != nil {
		return nil, fmt.Errorf("invalid certificate identity policy: %w", err)
	}
	verifier, err := sigverify.NewVerifier(trust,
		sigverify.WithTransparencyLog(1), sigverify.WithIntegratedTimestamps(1))
	if err != nil {
		return nil, fmt.Errorf("configuring Sigstore verification: %w", err)
	}
	return &keylessVerifier{verifier: verifier, identity: certIdentity}, nil
}

func (*keylessVerifier) Mode() string { return ModeSigstoreKeyless }
func (*keylessVerifier) Verify(string, []byte, []byte) error {
	return errors.New("keyless verification requires Sigstore evidence")
}

func (v *keylessVerifier) VerifyEvidence(keyID string, message, signature, evidence []byte) error {
	var b sigbundle.Bundle
	if err := b.UnmarshalJSON(evidence); err != nil {
		return fmt.Errorf("invalid Sigstore evidence: %w", err)
	}
	if err := validateKeylessEvidence(b.Bundle); err != nil {
		return err
	}
	envelope := b.GetDsseEnvelope()
	if envelope == nil || len(envelope.Signatures) != 1 || envelope.PayloadType != PayloadType ||
		!bytes.Equal(PAE(envelope.PayloadType, envelope.Payload), message) ||
		!bytes.Equal(envelope.Signatures[0].Sig, signature) {
		return errors.New("sigstore evidence does not match the Waza DSSE envelope")
	}
	content, err := b.VerificationContent()
	if err != nil || content.Certificate() == nil {
		return errors.New("sigstore evidence has no Fulcio certificate")
	}
	der, err := x509.MarshalPKIXPublicKey(content.Certificate().PublicKey)
	if err != nil || keyID != "sha256:"+sha256Hex(der) {
		return errors.New("sigstore certificate key does not match bundle key ID")
	}
	// Artifact checks are performed by Waza for ALL signed members after this
	// exact DSSE comparison. Sigstore's artifact policy checks in-toto subjects,
	// not the envelope bytes, and would check only one of those members.
	_, err = v.verifier.Verify(&b, sigverify.NewPolicy(
		sigverify.WithoutArtifactUnsafe(), sigverify.WithCertificateIdentity(v.identity)))
	if err != nil {
		return fmt.Errorf("sigstore certificate, identity or transparency verification failed: %w", err)
	}
	return nil
}
