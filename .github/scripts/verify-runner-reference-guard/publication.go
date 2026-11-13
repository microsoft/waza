package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const publisherPath = ".github/workflows/publish-runner-reference.yml"

var publisherInvocation = regexp.MustCompile(`^https://github\.com/microsoft/waza/actions/runs/([1-9][0-9]*)/attempts/([1-9][0-9]*)$`)

type verifiedProvenance struct {
	VerificationResult struct {
		Statement struct {
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
			Predicate struct {
				RunDetails struct {
					Metadata struct {
						InvocationID string `json:"invocationId"`
					} `json:"metadata"`
				} `json:"runDetails"`
			} `json:"predicate"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

type publicationRun struct {
	ID         json.Number `json:"id"`
	RunAttempt json.Number `json:"run_attempt"`
	HeadSHA    string      `json:"head_sha"`
	HeadBranch string      `json:"head_branch"`
	Path       string      `json:"path"`
	Event      string      `json:"event"`
	Status     string      `json:"status"`
	Conclusion string      `json:"conclusion"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type publicationJobs struct {
	Jobs []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Steps      []struct {
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"steps"`
	} `json:"jobs"`
}

func githubAPI(endpoint string) ([]byte, error) {
	output, err := exec.Command("gh", "api", "--paginate", "--slurp", endpoint).Output()
	if err != nil {
		return nil, fmt.Errorf("read publication evidence from %s: %w", endpoint, err)
	}
	return output, nil
}

func requireFinalizedRun(invocation, source string, api func(string) ([]byte, error)) error {
	match := publisherInvocation.FindStringSubmatch(invocation)
	if match == nil {
		return fmt.Errorf("invalid signed publisher invocation %q", invocation)
	}
	endpoint := "repos/microsoft/waza/actions/runs/" + match[1] + "/attempts/" + match[2]
	data, err := api(endpoint)
	if err != nil {
		return err
	}
	var runs []publicationRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return fmt.Errorf("decode publisher attempt: %w", err)
	}
	if len(runs) != 1 {
		return errors.New("publication evidence must identify exactly one run attempt")
	}
	run := runs[0]
	if run.ID.String() != match[1] || run.RunAttempt.String() != match[2] ||
		run.Repository.FullName != "microsoft/waza" || run.Path != publisherPath ||
		run.HeadSHA != source || run.HeadBranch != "main" || run.Event != "workflow_dispatch" ||
		run.Status != "completed" || run.Conclusion != "success" {
		return fmt.Errorf("signed publisher invocation %s did not successfully complete for source %s", invocation, source)
	}
	data, err = api(endpoint + "/jobs?per_page=100")
	if err != nil {
		return err
	}
	var pages []publicationJobs
	if err := json.Unmarshal(data, &pages); err != nil {
		return fmt.Errorf("decode publisher jobs: %w", err)
	}
	found := false
	for _, page := range pages {
		for _, job := range page.Jobs {
			if job.Name != "Publish reviewed Linux AMD64 image" {
				continue
			}
			if found || job.Status != "completed" || job.Conclusion != "success" {
				return errors.New("publisher job must complete successfully exactly once")
			}
			found = true
			for _, required := range []string{
				"Attest published image digest",
				"Attest runner image component inventory",
				"Revalidate protected main after push and attestation",
				"Record pushed candidate digest",
			} {
				passed := false
				for _, step := range job.Steps {
					if step.Name == required && step.Status == "completed" && step.Conclusion == "success" {
						passed = true
					}
				}
				if !passed {
					return fmt.Errorf("publisher finalization step %q did not succeed", required)
				}
			}
		}
	}
	if !found {
		return errors.New("publisher job evidence is missing")
	}
	return nil
}

// Input must be the successful JSON output of gh attestation verify, never an unverified bundle.
func runPublication(path, source, digest string, api func(string) ([]byte, error)) error {
	if !fullSHA.MatchString(source) || !imageDigest.MatchString(digest) {
		return errors.New("publication verification requires a full source SHA and immutable digest")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read verified provenance: %w", err)
	}
	var results []verifiedProvenance
	if err := json.Unmarshal(data, &results); err != nil {
		return fmt.Errorf("decode verified provenance: %w", err)
	}
	failures := []error{errors.New("no finalized publisher attestation for the requested image digest")}
	for _, result := range results {
		statement := result.VerificationResult.Statement
		if statement.PredicateType != "https://slsa.dev/provenance/v1" {
			continue
		}
		matched := false
		for _, subject := range statement.Subject {
			if subject.Name == "ghcr.io/microsoft/waza-runner-reference" &&
				subject.Digest["sha256"] == strings.TrimPrefix(digest, "sha256:") {
				matched = true
			}
		}
		if !matched {
			continue
		}
		invocation := statement.Predicate.RunDetails.Metadata.InvocationID
		if err := requireFinalizedRun(invocation, source, api); err != nil {
			failures = append(failures, err)
			continue
		}
		fmt.Printf("Verified successful publisher finalization: %s for %s\n", invocation, digest)
		return nil
	}
	return errors.Join(failures...)
}
