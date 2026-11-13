package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

// collectEvidence renders the platform table and fails unless every native
// target has a complete receipt for the exact source and version. Targets
// without a native runner are listed as unavailable, never as passed.
func collectEvidence(dir, sourceSHA, revision, version string) (string, error) {
	expected := map[string]bool{}
	var b strings.Builder
	var problems []string
	b.WriteString("### Native package smoke evidence\n\n")
	b.WriteString("| Target | Runner | Evidence | CLI | Extension |\n|---|---|---|---|---|\n")
	for _, p := range platforms {
		cells := []string{}
		for _, kind := range []string{"cli", "extension"} {
			name := receiptName(kind, p)
			switch {
			case p.Evidence != evidenceNative:
				cells = append(cells, "unavailable")
			case kind == "extension" && !p.Extension:
				cells = append(cells, "not selected")
			default:
				expected[name] = true
				r, err := readReceipt(filepath.Join(dir, name), kind, p, sourceSHA, revision, version)
				if err != nil {
					problems = append(problems, err.Error())
					cells = append(cells, "**missing or invalid**")
					continue
				}
				cells = append(cells, fmt.Sprintf("pass (%.0f s, %d assets)", r.Seconds, r.DashboardAssets))
			}
		}
		evidence := p.Evidence
		if p.Reason != "" {
			evidence += ": " + p.Reason
		}
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", p.target(), p.Runner, evidence, cells[0], cells[1])
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, err.Error())
	}
	for _, entry := range entries {
		if !expected[entry.Name()] {
			problems = append(problems, "unexpected evidence file "+entry.Name())
		}
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

func readReceipt(path, kind string, p platform, sourceSHA, revision, version string) (receipt, error) {
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
