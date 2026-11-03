package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var (
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type verificationContext struct {
	EventName      string
	Repository     string
	Ref            string
	EventSHA       string
	SourceSHA      string
	ImageDigest    string
	CheckoutSHA    string
	CurrentMainSHA string
	SourceOnMain   bool
}

func validateVerification(candidate verificationContext) error {
	if candidate.EventName != "workflow_dispatch" {
		return fmt.Errorf("event %q is not an allowed manual verification", candidate.EventName)
	}
	if candidate.Repository != "microsoft/waza" {
		return fmt.Errorf("repository %q is not the image source", candidate.Repository)
	}
	if candidate.Ref != "refs/heads/main" {
		return fmt.Errorf("ref %q is not the protected main branch", candidate.Ref)
	}
	for name, value := range map[string]string{
		"workflow event":    candidate.EventSHA,
		"workflow checkout": candidate.CheckoutSHA,
		"source":            candidate.SourceSHA,
		"current main":      candidate.CurrentMainSHA,
	} {
		if !fullSHA.MatchString(value) {
			return fmt.Errorf("%s SHA is not a full lowercase 40-character commit SHA", name)
		}
	}
	if candidate.EventSHA != candidate.CheckoutSHA {
		return errors.New("workflow checkout does not match the dispatch event SHA")
	}
	if candidate.EventSHA != candidate.CurrentMainSHA {
		return errors.New("verification workflow is not running from the current main tip")
	}
	if !candidate.SourceOnMain {
		return fmt.Errorf("image source %q is not an ancestor of current main %q",
			candidate.SourceSHA, candidate.CurrentMainSHA)
	}
	if !imageDigest.MatchString(candidate.ImageDigest) {
		return errors.New("image digest must be sha256 followed by 64 lowercase hexadecimal characters")
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

func isAncestor(revision, main string) (bool, error) {
	err := exec.Command("git", "merge-base", "--is-ancestor", revision, main).Run()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether image source is on current main: %w", err)
}

func run() error {
	candidate := verificationContext{
		EventName:   os.Getenv("EVENT_NAME"),
		Repository:  os.Getenv("REPOSITORY"),
		Ref:         os.Getenv("REF"),
		EventSHA:    os.Getenv("EVENT_SHA"),
		SourceSHA:   os.Getenv("SOURCE_SHA"),
		ImageDigest: os.Getenv("IMAGE_DIGEST"),
	}
	if !fullSHA.MatchString(candidate.SourceSHA) {
		return errors.New("source SHA must be a full lowercase 40-character commit SHA")
	}
	if !imageDigest.MatchString(candidate.ImageDigest) {
		return errors.New("image digest must be sha256 followed by 64 lowercase hexadecimal characters")
	}
	var err error
	if candidate.CheckoutSHA, err = gitRevision("HEAD"); err != nil {
		return err
	}
	if candidate.CurrentMainSHA, err = gitRevision("FETCH_HEAD"); err != nil {
		return err
	}
	if candidate.SourceOnMain, err = isAncestor(candidate.SourceSHA, "FETCH_HEAD"); err != nil {
		return err
	}
	if err := validateVerification(candidate); err != nil {
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
	_, writeErr := fmt.Fprintf(output, "source_sha=%s\nimage_digest=%s\n",
		candidate.SourceSHA, candidate.ImageDigest)
	closeErr := output.Close()
	if writeErr != nil {
		return fmt.Errorf("write verification inputs: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close verification inputs: %w", closeErr)
	}
	fmt.Printf("Verified public-image test inputs for source %s and digest %s\n",
		candidate.SourceSHA, candidate.ImageDigest)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "runner reference verification input rejected: %v\n", err)
		os.Exit(1)
	}
}
