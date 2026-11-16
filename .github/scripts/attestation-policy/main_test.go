package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestValidateTrustedRootRequiresOutOfBandPolicy(t *testing.T) {
	root := []byte("trusted root bundle")
	digest := sha256.Sum256(root)
	expectedDigest := hex.EncodeToString(digest[:])

	for _, test := range []struct {
		name          string
		repositoryID  string
		trustedDigest string
		trustedRoot   []byte
		wantErr       bool
	}{
		{name: "missing repository ID", trustedDigest: expectedDigest, trustedRoot: root, wantErr: true},
		{name: "missing root digest", repositoryID: "12345", trustedRoot: root, wantErr: true},
		{name: "invalid root digest", repositoryID: "12345", trustedDigest: "not-a-digest", trustedRoot: root, wantErr: true},
		{name: "root digest mismatch", repositoryID: "12345", trustedDigest: hex.EncodeToString(make([]byte, sha256.Size)), trustedRoot: root, wantErr: true},
		{name: "matching out-of-band policy", repositoryID: "12345", trustedDigest: expectedDigest, trustedRoot: root},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateTrustedRoot(test.repositoryID, test.trustedDigest, test.trustedRoot)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateTrustedRoot() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestVerifyRepositoryIDsChecksEveryAttestation(t *testing.T) {
	matching := []byte(`[{"verificationResult":{"signature":{"certificate":{"sourceRepositoryIdentifier":"12345"}}}}]`)
	mismatch := []byte(`[{"verificationResult":{"signature":{"certificate":{"sourceRepositoryIdentifier":"54321"}}}}]`)
	missing := []byte(`[{"verificationResult":{"signature":{"certificate":{}}}}]`)

	for _, test := range []struct {
		name         string
		repositoryID string
		reports      [][]byte
		wantErr      bool
	}{
		{name: "missing repository ID", reports: [][]byte{matching}, wantErr: true},
		{name: "no reports", repositoryID: "12345", wantErr: true},
		{name: "first attestation mismatch", repositoryID: "12345", reports: [][]byte{mismatch}, wantErr: true},
		{name: "later attestation mismatch", repositoryID: "12345", reports: [][]byte{matching, mismatch}, wantErr: true},
		{name: "missing attestation identifier", repositoryID: "12345", reports: [][]byte{missing}, wantErr: true},
		{name: "all attestations match", repositoryID: "12345", reports: [][]byte{matching, matching}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyRepositoryIDs(test.repositoryID, test.reports)
			if (err != nil) != test.wantErr {
				t.Fatalf("verifyRepositoryIDs() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestVerifyAttestationsRequiresBothTrustedPolicyValues(t *testing.T) {
	root := []byte("trusted root bundle")
	digest := sha256.Sum256(root)
	expectedDigest := hex.EncodeToString(digest[:])
	report := []byte(`[{"verificationResult":{"signature":{"certificate":{"sourceRepositoryIdentifier":"12345"}}}}]`)

	for _, test := range []struct {
		name          string
		repositoryID  string
		trustedDigest string
		wantErr       bool
	}{
		{name: "missing repository ID", trustedDigest: expectedDigest, wantErr: true},
		{name: "missing root digest", repositoryID: "12345", wantErr: true},
		{name: "both policies supplied", repositoryID: "12345", trustedDigest: expectedDigest},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := verifyAttestations(test.repositoryID, test.trustedDigest, root, [][]byte{report})
			if (err != nil) != test.wantErr {
				t.Fatalf("verifyAttestations() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}
