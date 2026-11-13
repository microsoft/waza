package provenance

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
)

// Signer modes. Only ModeLocalEd25519 is implemented in this build; keyless
// (Sigstore) and managed (KMS/HSM) signing are represented by the same
// interfaces and report an explicit unavailable state until an integration is
// configured.
const (
	ModeLocalEd25519    = "local-ed25519"
	ModeSigstoreKeyless = "sigstore-keyless"
	ModeManagedKMS      = "managed-kms"
)

var (
	ErrSignerUnavailable = errors.New("signer unavailable")
	ErrUnknownSigner     = errors.New("unknown signer mode")
	ErrKeyUntrusted      = errors.New("key is not trusted")
)

// Signer produces a signature over the DSSE pre-authentication encoding.
// Implementations for keyless or managed signing must not expose key material.
type Signer interface {
	Mode() string
	KeyID() string
	Sign(ctx context.Context, message []byte) ([]byte, error)
}

// SignatureVerifier checks one DSSE signature against configured trust.
type SignatureVerifier interface {
	Mode() string
	Verify(keyID string, message, signature []byte) error
}

// Envelope is a DSSE v1 envelope.
type Envelope struct {
	PayloadType string      `json:"payloadType"`
	Payload     string      `json:"payload"`
	Signatures  []Signature `json:"signatures"`
}

type Signature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// PAE is the DSSE v1 pre-authentication encoding.
func PAE(payloadType string, payload []byte) []byte {
	out := []byte("DSSEv1 " + strconv.Itoa(len(payloadType)) + " " + payloadType + " " + strconv.Itoa(len(payload)) + " ")
	return append(out, payload...)
}

func signEnvelope(ctx context.Context, signer Signer, payload []byte) (Envelope, error) {
	sig, err := signer.Sign(ctx, PAE(PayloadType, payload))
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{
		PayloadType: PayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures:  []Signature{{KeyID: signer.KeyID(), Sig: base64.StdEncoding.EncodeToString(sig)}},
	}, nil
}

// KeyID is "sha256:" plus the hex digest of the PKIX DER public key.
func KeyID(public ed25519.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return "", fmt.Errorf("encoding public key: %w", err)
	}
	return "sha256:" + sha256Hex(der), nil
}

// GenerateKey returns PEM PKCS#8 private and PKIX public Ed25519 keys.
func GenerateKey() (privatePEM, publicPEM []byte, keyID string, err error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", fmt.Errorf("generating ed25519 key: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, "", fmt.Errorf("encoding private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return nil, nil, "", fmt.Errorf("encoding public key: %w", err)
	}
	keyID, err = KeyID(public)
	if err != nil {
		return nil, nil, "", err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}), keyID, nil
}

type localSigner struct {
	key   ed25519.PrivateKey
	keyID string
}

// NewSigner selects a signer mode. Local mode requires a PEM PKCS#8 Ed25519
// private key; keyless and managed modes return ErrSignerUnavailable.
func NewSigner(mode string, privatePEM []byte) (Signer, error) {
	switch mode {
	case ModeLocalEd25519:
		return NewLocalSigner(privatePEM)
	case ModeSigstoreKeyless, ModeManagedKMS:
		return nil, fmt.Errorf("%w: %s signing has no configured integration in this build; use %s", ErrSignerUnavailable, mode, ModeLocalEd25519)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownSigner, mode)
	}
}

func NewLocalSigner(privatePEM []byte) (Signer, error) {
	block, rest := pem.Decode(privatePEM)
	if block == nil || block.Type != "PRIVATE KEY" || len(trimSpace(rest)) != 0 {
		return nil, errors.New("signing key must be one PEM PKCS#8 PRIVATE KEY block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing signing key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("signing key is not Ed25519")
	}
	keyID, err := KeyID(key.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, err
	}
	return &localSigner{key: key, keyID: keyID}, nil
}

func (s *localSigner) Mode() string  { return ModeLocalEd25519 }
func (s *localSigner) KeyID() string { return s.keyID }
func (s *localSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.key.Sign(nil, message, crypto.Hash(0))
}

type localVerifier struct {
	keys map[string]ed25519.PublicKey
}

// NewLocalVerifier trusts exactly the supplied PEM PKIX Ed25519 public keys.
func NewLocalVerifier(publicPEMs ...[]byte) (SignatureVerifier, error) {
	keys := make(map[string]ed25519.PublicKey, len(publicPEMs))
	for _, data := range publicPEMs {
		for len(trimSpace(data)) > 0 {
			block, rest := pem.Decode(data)
			if block == nil || block.Type != "PUBLIC KEY" {
				return nil, errors.New("trusted key must be PEM PKIX PUBLIC KEY blocks")
			}
			parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("parsing trusted key: %w", err)
			}
			key, ok := parsed.(ed25519.PublicKey)
			if !ok {
				return nil, errors.New("trusted key is not Ed25519")
			}
			id, err := KeyID(key)
			if err != nil {
				return nil, err
			}
			keys[id] = key
			data = rest
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("no trusted public key supplied")
	}
	return &localVerifier{keys: keys}, nil
}

func (v *localVerifier) Mode() string { return ModeLocalEd25519 }
func (v *localVerifier) Verify(keyID string, message, signature []byte) error {
	key, ok := v.keys[keyID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyUntrusted, keyID)
	}
	if !ed25519.Verify(key, message, signature) {
		return errors.New("signature does not verify")
	}
	return nil
}

func trimSpace(data []byte) []byte {
	for len(data) > 0 && (data[0] == ' ' || data[0] == '\n' || data[0] == '\r' || data[0] == '\t') {
		data = data[1:]
	}
	return data
}
