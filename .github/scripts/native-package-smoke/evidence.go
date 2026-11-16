package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

type receiptArtifact struct {
	kind    string
	runID   string
	attempt int
	index   int
}

type collectedReceipt struct {
	status   receiptStatus
	receipt  receipt
	valid    bool
	problems []string
}

type jobAttempt struct {
	Kind             string `json:"kind"`
	Target           string `json:"target"`
	RunAttempt       int    `json:"run_attempt"`
	Conclusion       string `json:"conclusion"`
	UploadConclusion string `json:"upload_conclusion"`
}

func parseReceiptArtifactName(name string) (receiptArtifact, error) {
	var artifact receiptArtifact
	parts := strings.Split(name, "-")
	if len(parts) != 6 || parts[0] != "native" || parts[1] != "smoke" ||
		parts[2] != "cli" && parts[2] != "extension" {
		return artifact, fmt.Errorf("unexpected receipt artifact %q", name)
	}
	if _, err := strconv.ParseUint(parts[3], 10, 64); err != nil || parts[3] == "0" {
		return artifact, fmt.Errorf("receipt artifact %q has an invalid run ID", name)
	}
	attempt, err := strconv.Atoi(parts[4])
	if err != nil || attempt < 1 {
		return artifact, fmt.Errorf("receipt artifact %q has an invalid attempt", name)
	}
	index, err := strconv.Atoi(parts[5])
	if err != nil || index < 0 {
		return artifact, fmt.Errorf("receipt artifact %q has an invalid matrix index", name)
	}
	return receiptArtifact{kind: parts[2], runID: parts[3], attempt: attempt, index: index}, nil
}

func readJobAttempts(path string) ([]jobAttempt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read authoritative native job attempts: %w", err)
	}
	var jobs []jobAttempt
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&jobs); err != nil {
		return nil, fmt.Errorf("decode authoritative native job attempts: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode authoritative native job attempts: trailing data")
	}
	if len(jobs) == 0 {
		return nil, errors.New("authoritative native job attempts are empty")
	}
	return jobs, nil
}

func collectEvidence(dir, sourceSHA, revision, version, runID string, runAttempt int, jobAttempts []jobAttempt) (string, error) {
	latest := map[string]collectedReceipt{}
	latestJobs := map[string]jobAttempt{}
	var problems []string
	for _, job := range jobAttempts {
		if job.Kind != "cli" && job.Kind != "extension" {
			problems = append(problems, fmt.Sprintf("authoritative job has invalid kind %q", job.Kind))
			continue
		}
		p, err := findPlatform(job.Target)
		if err != nil || p.Evidence != evidenceNative || job.Kind == "extension" && !p.Extension {
			problems = append(problems, fmt.Sprintf("authoritative job names an unselected target %q", job.Target))
			continue
		}
		if job.RunAttempt < 1 || job.RunAttempt > runAttempt {
			problems = append(problems, fmt.Sprintf("%s %s has invalid authoritative attempt %d", job.Kind, job.Target, job.RunAttempt))
			continue
		}
		if job.Conclusion == "" || job.UploadConclusion == "" {
			problems = append(problems, fmt.Sprintf("%s %s attempt %d has incomplete authoritative job status", job.Kind, job.Target, job.RunAttempt))
			continue
		}
		key := job.Kind + "/" + job.Target
		prior, ok := latestJobs[key]
		switch {
		case !ok || job.RunAttempt > prior.RunAttempt:
			latestJobs[key] = job
		case job.RunAttempt == prior.RunAttempt:
			problems = append(problems, fmt.Sprintf("%s has multiple authoritative jobs for attempt %d", key, job.RunAttempt))
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err.Error())
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			problems = append(problems, "unexpected evidence file "+entry.Name())
			continue
		}
		artifact, err := parseReceiptArtifactName(entry.Name())
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if artifact.runID != runID || artifact.attempt > runAttempt {
			problems = append(problems, fmt.Sprintf("receipt artifact %q is outside run %s attempt %d", entry.Name(), runID, runAttempt))
			continue
		}
		record, err := readReceiptArtifact(filepath.Join(dir, entry.Name()), artifact, sourceSHA, revision, version)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		key := record.status.Kind + "/" + record.status.Target
		prior, ok := latest[key]
		switch {
		case !ok || record.status.RunAttempt > prior.status.RunAttempt:
			latest[key] = record
		case record.status.RunAttempt == prior.status.RunAttempt:
			problems = append(problems, fmt.Sprintf("%s has multiple receipt artifacts for attempt %d", key, record.status.RunAttempt))
		}
	}
	for _, record := range latest {
		problems = append(problems, record.problems...)
	}

	var b strings.Builder
	b.WriteString("### Native package smoke evidence\n\n")
	b.WriteString("| Target | Runner | Evidence | CLI | Extension |\n|---|---|---|---|---|\n")
	for _, p := range platforms {
		cells := []string{}
		for _, kind := range []string{"cli", "extension"} {
			key := kind + "/" + p.target()
			switch {
			case p.Evidence != evidenceNative:
				cells = append(cells, "unavailable")
			case kind == "extension" && !p.Extension:
				cells = append(cells, "not selected")
			default:
				job, jobOK := latestJobs[key]
				if !jobOK {
					problems = append(problems, fmt.Sprintf("%s %s: no authoritative job result in run %s", kind, p.target(), runID))
					cells = append(cells, "**missing or invalid**")
					continue
				}
				if job.Conclusion != "success" || job.UploadConclusion != "success" {
					problems = append(problems, fmt.Sprintf("%s %s: latest job attempt %d concluded %s with receipt upload %s", kind, p.target(), job.RunAttempt, job.Conclusion, job.UploadConclusion))
					cells = append(cells, "**missing or invalid**")
					continue
				}
				record, ok := latest[key]
				switch {
				case !ok:
					problems = append(problems, fmt.Sprintf("%s %s: no receipt artifact in run %s", kind, p.target(), runID))
					cells = append(cells, "**missing or invalid**")
				case record.status.RunAttempt != job.RunAttempt:
					problems = append(problems, fmt.Sprintf("%s %s: latest receipt attempt %d does not match authoritative job attempt %d", kind, p.target(), record.status.RunAttempt, job.RunAttempt))
					cells = append(cells, "**missing or invalid**")
				case record.status.Status != "success":
					problems = append(problems, fmt.Sprintf("%s %s: latest attempt %d ended with status %s", kind, p.target(), record.status.RunAttempt, record.status.Status))
					cells = append(cells, "**missing or invalid**")
				case !record.valid:
					cells = append(cells, "**missing or invalid**")
				default:
					cells = append(cells, fmt.Sprintf("pass (%.0f s, %d assets)", record.receipt.Seconds, record.receipt.DashboardAssets))
				}
			}
		}
		evidence := p.Evidence
		if p.Reason != "" {
			evidence += ": " + p.Reason
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", p.target(), p.Runner, evidence, cells[0], cells[1])
	}
	b.WriteString("\n")
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(&b, "- %s\n", problem)
		}
		b.WriteString("\n")
		return b.String(), fmt.Errorf("native package evidence is incomplete: %s", strings.Join(problems, "; "))
	}
	return b.String(), nil
}

func readReceiptArtifact(dir string, artifact receiptArtifact, sourceSHA, revision, version string) (collectedReceipt, error) {
	var result collectedReceipt
	statusPath := filepath.Join(dir, "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return result, fmt.Errorf("%s: no native job status", filepath.Base(dir))
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result.status); err != nil {
		return result, fmt.Errorf("%s: malformed native job status: %v", filepath.Base(dir), err)
	}
	if result.status.Schema != statusSchema || result.status.Kind != artifact.kind ||
		result.status.RunID != artifact.runID || result.status.RunAttempt != artifact.attempt ||
		result.status.Status != "success" && result.status.Status != "failure" && result.status.Status != "cancelled" {
		return result, fmt.Errorf("%s: native job status does not match artifact identity", filepath.Base(dir))
	}
	p, err := findPlatform(result.status.Target)
	if err != nil || p.Evidence != evidenceNative || artifact.kind == "extension" && !p.Extension {
		return result, fmt.Errorf("%s: native job status names an unselected target %q", filepath.Base(dir), result.status.Target)
	}
	expectedReceipt := receiptName(artifact.kind, p)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	foundReceipt := false
	for _, entry := range entries {
		switch entry.Name() {
		case "status.json":
		case expectedReceipt:
			foundReceipt = true
		default:
			result.problems = append(result.problems, fmt.Sprintf("%s: unexpected evidence file %s", filepath.Base(dir), entry.Name()))
		}
	}
	if foundReceipt {
		result.receipt, err = readReceipt(filepath.Join(dir, expectedReceipt), artifact.kind, p, sourceSHA, revision, version, artifact.runID, artifact.attempt)
		if err != nil {
			result.problems = append(result.problems, err.Error())
		} else {
			result.valid = true
		}
	}
	if result.status.Status == "success" && !foundReceipt {
		result.problems = append(result.problems, fmt.Sprintf("%s %s: successful smoke job has no receipt", artifact.kind, p.target()))
	}
	return result, nil
}

func readReceipt(path, kind string, p platform, sourceSHA, revision, version, runID string, runAttempt int) (receipt, error) {
	var r receipt
	data, err := os.ReadFile(path)
	if err != nil {
		return r, fmt.Errorf("%s %s: no native receipt", kind, p.target())
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, fmt.Errorf("%s %s: malformed receipt: %v", kind, p.target(), err)
	}
	artifact := cliBinaryName(p)
	if kind == "extension" {
		artifact = extensionArchiveName(p)
	}
	switch {
	case r.Schema != receiptSchema || r.Kind != kind || r.Target != p.target() || r.Artifact != artifact:
		return r, fmt.Errorf("%s %s: receipt names a different package", kind, p.target())
	case r.RunID != runID || r.RunAttempt != runAttempt:
		return r, fmt.Errorf("%s %s: receipt identity does not match artifact run %s attempt %d", kind, p.target(), runID, runAttempt)
	case r.SourceSHA != sourceSHA || r.Revision != revision || r.Version != version:
		return r, fmt.Errorf("%s %s: receipt is for %s@%s %s, not %s@%s %s", kind, p.target(), r.SourceSHA, r.Revision, r.Version, sourceSHA, revision, version)
	case !sha256Pattern.MatchString(r.SHA256):
		return r, fmt.Errorf("%s %s: receipt has no package digest", kind, p.target())
	case !reflect.DeepEqual(r.Checks, requiredChecks):
		return r, fmt.Errorf("%s %s: checks %v, want %v", kind, p.target(), r.Checks, requiredChecks)
	case r.DashboardAssets < 1:
		return r, fmt.Errorf("%s %s: no dashboard assets were fetched", kind, p.target())
	}
	return r, nil
}
