package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

type candidateContext struct {
	EventName    string
	Repository   string
	Ref          string
	EventSHA     string
	CandidateSHA string
	CheckoutSHA  string
	MainSHA      string
}

var fullCommitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

func validateCandidate(candidate candidateContext) error {
	if candidate.EventName != "workflow_dispatch" {
		return fmt.Errorf("event %q is not an allowed manual dispatch", candidate.EventName)
	}
	if candidate.Repository != "microsoft/waza" {
		return fmt.Errorf("repository %q is not the publication source", candidate.Repository)
	}
	if candidate.Ref != "refs/heads/main" {
		return fmt.Errorf("ref %q is not the protected main branch", candidate.Ref)
	}
	if !fullCommitSHA.MatchString(candidate.CandidateSHA) {
		return errors.New("candidate SHA must be a full lowercase 40-character commit SHA")
	}
	for name, value := range map[string]string{
		"workflow event": candidate.EventSHA,
		"checkout":       candidate.CheckoutSHA,
		"current main":   candidate.MainSHA,
	} {
		if !fullCommitSHA.MatchString(value) {
			return fmt.Errorf("%s SHA is not a full commit SHA", name)
		}
		if value != candidate.CandidateSHA {
			return fmt.Errorf("%s SHA %q does not match candidate %q", name, value, candidate.CandidateSHA)
		}
	}
	return nil
}

func gitRevision(ref string) (string, error) {
	output, err := exec.Command("git", "rev-parse", "--verify", ref).Output()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", ref, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func run() error {
	candidate := candidateContext{
		EventName:    os.Getenv("EVENT_NAME"),
		Repository:   os.Getenv("REPOSITORY"),
		Ref:          os.Getenv("REF"),
		EventSHA:     os.Getenv("EVENT_SHA"),
		CandidateSHA: os.Getenv("CANDIDATE_SHA"),
	}
	var err error
	if candidate.CheckoutSHA, err = gitRevision("HEAD"); err != nil {
		return err
	}
	if candidate.MainSHA, err = gitRevision("FETCH_HEAD"); err != nil {
		return err
	}
	if err := validateCandidate(candidate); err != nil {
		return err
	}

	outputPath := os.Getenv("GITHUB_OUTPUT")
	if outputPath == "" {
		return errors.New("GITHUB_OUTPUT is required")
	}
	output, err := os.OpenFile(outputPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open GitHub output: %w", err)
	}
	_, writeErr := fmt.Fprintf(output, "candidate_sha=%s\n", candidate.CandidateSHA)
	closeErr := output.Close()
	if writeErr != nil {
		return fmt.Errorf("write candidate output: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close candidate output: %w", closeErr)
	}
	fmt.Printf("Verified current protected main candidate %s\n", candidate.CandidateSHA)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "runner image candidate validation failed: %v\n", err)
		os.Exit(1)
	}
}
