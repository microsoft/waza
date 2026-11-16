package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var repositoryIDPattern = regexp.MustCompile(`^[0-9]+$`)

type verificationReport []struct {
	VerificationResult struct {
		Signature struct {
			Certificate struct {
				SourceRepositoryIdentifier json.RawMessage `json:"sourceRepositoryIdentifier"`
			} `json:"certificate"`
		} `json:"signature"`
	} `json:"verificationResult"`
}

func validateTrustedRoot(repositoryID, expectedDigest string, trustedRoot []byte) error {
	if !repositoryIDPattern.MatchString(repositoryID) {
		return errors.New("expected repository ID must be supplied as a numeric trusted-policy value")
	}
	if len(expectedDigest) != sha256.Size*2 {
		return errors.New("expected trusted-root SHA-256 must be supplied as a 64-character out-of-band value")
	}
	if _, err := hex.DecodeString(expectedDigest); err != nil {
		return errors.New("expected trusted-root SHA-256 is invalid")
	}
	if len(trustedRoot) == 0 {
		return errors.New("trusted root is empty")
	}
	actual := sha256.Sum256(trustedRoot)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expectedDigest) {
		return errors.New("trusted-root SHA-256 does not match trusted policy")
	}
	return nil
}

func verifyRepositoryIDs(repositoryID string, reports [][]byte) error {
	if !repositoryIDPattern.MatchString(repositoryID) {
		return errors.New("expected repository ID must be supplied as a numeric trusted-policy value")
	}
	if len(reports) == 0 {
		return errors.New("at least one verified attestation report is required")
	}
	for reportIndex, report := range reports {
		var results verificationReport
		if err := json.Unmarshal(report, &results); err != nil {
			return fmt.Errorf("verified attestation report %d is invalid JSON: %w", reportIndex+1, err)
		}
		if len(results) == 0 {
			return fmt.Errorf("verified attestation report %d contains no verification results", reportIndex+1)
		}
		for resultIndex, result := range results {
			actual, err := sourceRepositoryID(result.VerificationResult.Signature.Certificate.SourceRepositoryIdentifier)
			if err != nil {
				return fmt.Errorf("verified attestation report %d result %d: %w", reportIndex+1, resultIndex+1, err)
			}
			if actual != repositoryID {
				return fmt.Errorf("verified attestation report %d result %d repository ID %q does not match trusted policy", reportIndex+1, resultIndex+1, actual)
			}
		}
	}
	return nil
}

func verifyAttestations(repositoryID, expectedDigest string, trustedRoot []byte, reports [][]byte) error {
	if err := validateTrustedRoot(repositoryID, expectedDigest, trustedRoot); err != nil {
		return err
	}
	return verifyRepositoryIDs(repositoryID, reports)
}

func sourceRepositoryID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("sourceRepositoryIdentifier is missing")
	}
	var value string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", errors.New("sourceRepositoryIdentifier is invalid")
		}
	} else {
		value = string(raw)
	}
	if !repositoryIDPattern.MatchString(value) {
		return "", errors.New("sourceRepositoryIdentifier is not a numeric repository ID")
	}
	return value, nil
}

func run(args []string) error {
	if len(args) < 1 {
		return errors.New("usage: attestation-policy root <repository-id> <trusted-root-sha256> <trusted-root-file> | verify <repository-id> <trusted-root-sha256> <trusted-root-file> <report-file>...")
	}
	switch args[0] {
	case "root":
		if len(args) != 4 {
			return errors.New("usage: attestation-policy root <repository-id> <trusted-root-sha256> <trusted-root-file>")
		}
		root, err := os.ReadFile(args[3])
		if err != nil {
			return fmt.Errorf("read trusted root: %w", err)
		}
		return validateTrustedRoot(args[1], args[2], root)
	case "verify":
		if len(args) < 5 {
			return errors.New("usage: attestation-policy verify <repository-id> <trusted-root-sha256> <trusted-root-file> <report-file>...")
		}
		root, err := os.ReadFile(args[3])
		if err != nil {
			return fmt.Errorf("read trusted root: %w", err)
		}
		reports := make([][]byte, 0, len(args)-4)
		for _, path := range args[4:] {
			report, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read verified attestation report %q: %w", path, err)
			}
			reports = append(reports, report)
		}
		return verifyAttestations(args[1], args[2], root, reports)
	default:
		return errors.New("usage: attestation-policy root <repository-id> <trusted-root-sha256> <trusted-root-file> | verify <repository-id> <trusted-root-sha256> <trusted-root-file> <report-file>...")
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
