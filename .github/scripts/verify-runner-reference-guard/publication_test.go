package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPublication(t *testing.T) {
	const invocation = "https://github.com/microsoft/waza/actions/runs/42/attempts/2"
	const endpoint = "repos/microsoft/waza/actions/runs/42/attempts/2"
	for _, name := range []string{
		"valid", "failed finalization", "canceled run", "incomplete run", "later successful attempt",
		"wrong source", "wrong repository", "wrong workflow", "wrong branch", "wrong event",
		"missing publisher job", "skipped publisher job", "missing final guard", "failed final guard",
		"skipped attestation", "skipped summary", "duplicate publisher job",
		"wrong digest", "wrong subject", "wrong predicate", "missing invocation", "foreign invocation",
		"invalid JSON", "no attestations", "API failure", "invalid run JSON", "invalid jobs JSON",
	} {
		t.Run(name, func(t *testing.T) {
			candidate := validVerification()
			run := publicationRun{
				ID: "42", RunAttempt: "2", HeadSHA: candidate.SourceSHA, HeadBranch: "main",
				Path: publisherPath, Event: "workflow_dispatch", Status: "completed", Conclusion: "success",
			}
			run.Repository.FullName = "microsoft/waza"
			jobsJSON := `[
				{"jobs": [{"name": "Validate protected main candidate", "status": "completed", "conclusion": "success"}]},
				{"jobs": [{"name": "Publish reviewed Linux AMD64 image", "status": "completed", "conclusion": "success",
					"steps": [
						{"name": "Attest published image digest", "status": "completed", "conclusion": "success"},
						{"name": "Revalidate protected main after push and attestation", "status": "completed", "conclusion": "success"},
						{"name": "Record pushed candidate digest", "status": "completed", "conclusion": "success"}
					]}]}
			]`
			provenance := []verifiedProvenance{{}}
			statement := &provenance[0].VerificationResult.Statement
			statement.PredicateType = "https://slsa.dev/provenance/v1"
			statement.Predicate.RunDetails.Metadata.InvocationID = invocation
			statement.Subject = append(statement.Subject, struct {
				Name   string            `json:"name"`
				Digest map[string]string `json:"digest"`
			}{
				Name:   "ghcr.io/microsoft/waza-runner-reference",
				Digest: map[string]string{"sha256": strings.TrimPrefix(candidate.ImageDigest, "sha256:")},
			})
			switch name {
			case "failed finalization":
				run.Conclusion = "failure"
			case "canceled run":
				run.Conclusion = "cancelled" //nolint:misspell // GitHub's exact run conclusion.
			case "incomplete run":
				run.Status = "in_progress"
			case "later successful attempt":
				run.RunAttempt = "3"
			case "wrong source":
				run.HeadSHA = strings.Repeat("a", 40)
			case "wrong repository":
				run.Repository.FullName = "fork/waza"
			case "wrong workflow":
				run.Path = ".github/workflows/other.yml"
			case "wrong branch":
				run.HeadBranch = "feature"
			case "wrong event":
				run.Event = "push"
			case "missing publisher job":
				jobsJSON = `[{"jobs":[]}]`
			case "skipped publisher job":
				jobsJSON = strings.ReplaceAll(jobsJSON, `"conclusion": "success"`, `"conclusion": "skipped"`)
			case "missing final guard":
				jobsJSON = strings.ReplaceAll(jobsJSON, "Revalidate protected main after push and attestation", "other step")
			case "failed final guard", "skipped attestation", "skipped summary":
				var pages []publicationJobs
				if err := json.Unmarshal([]byte(jobsJSON), &pages); err != nil {
					t.Fatal(err)
				}
				index := map[string]int{"failed final guard": 1, "skipped attestation": 0, "skipped summary": 2}[name]
				pages[1].Jobs[0].Steps[index].Conclusion = "failure"
				encoded, err := json.Marshal(pages)
				if err != nil {
					t.Fatal(err)
				}
				jobsJSON = string(encoded)
			case "duplicate publisher job":
				var pages []publicationJobs
				if err := json.Unmarshal([]byte(jobsJSON), &pages); err != nil {
					t.Fatal(err)
				}
				pages = append(pages, pages[1])
				encoded, err := json.Marshal(pages)
				if err != nil {
					t.Fatal(err)
				}
				jobsJSON = string(encoded)
			case "wrong digest":
				statement.Subject[0].Digest["sha256"] = strings.Repeat("a", 64)
			case "wrong subject":
				statement.Subject[0].Name = "ghcr.io/fork/other"
			case "wrong predicate":
				statement.PredicateType = "https://example.com/other"
			case "missing invocation":
				statement.Predicate.RunDetails.Metadata.InvocationID = ""
			case "foreign invocation":
				statement.Predicate.RunDetails.Metadata.InvocationID = "https://github.com/fork/waza/actions/runs/42/attempts/2"
			}
			encoded, err := json.Marshal(provenance)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "invalid JSON":
				encoded = []byte("{")
			case "no attestations":
				encoded = []byte("[]")
			}
			path := filepath.Join(t.TempDir(), "verified.json")
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			var calls []string
			err = runPublication(path, candidate.SourceSHA, candidate.ImageDigest, func(request string) ([]byte, error) {
				calls = append(calls, request)
				if name == "API failure" {
					return nil, errors.New("publication evidence unavailable")
				}
				switch request {
				case endpoint:
					if name == "invalid run JSON" {
						return []byte("{"), nil
					}
					return json.Marshal([]publicationRun{run})
				case endpoint + "/jobs?per_page=100":
					if name == "invalid jobs JSON" {
						return []byte("{"), nil
					}
					return []byte(jobsJSON), nil
				default:
					t.Fatalf("unexpected API endpoint: %s", request)
					return nil, nil
				}
			})
			if name == "valid" {
				if err != nil || len(calls) != 2 {
					t.Fatalf("valid finalized run rejected: %v; API calls: %v", err, calls)
				}
			} else if err == nil {
				t.Fatal("invalid publication accepted")
			}
			if name == "failed finalization" && !strings.Contains(err.Error(), "did not successfully complete") {
				t.Fatalf("signed digest must be rejected specifically for failed publisher finalization: %v", err)
			}
		})
	}
}
