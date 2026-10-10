package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/microsoft/waza/internal/releasepolicy"
)

func runControlledAssessment(out io.Writer, policyPath, directory, format string) error {
	d := releasepolicy.InitialDecision()
	var assessmentError error
	if policyPath == "" || directory == "" {
		assessmentError = fmt.Errorf("--release-policy and --collection-dir must both be explicitly selected")
	} else {
		data, err := os.ReadFile(policyPath)
		if err != nil {
			assessmentError = fmt.Errorf("reading selected release policy: %w", err)
		} else {
			selected, err := releasepolicy.DecodePolicy(data)
			if err != nil {
				assessmentError = err
			} else {
				d, assessmentError = releasepolicy.ReadSelectedDecision(directory, selected.Digest)
			}
		}
	}
	if assessmentError != nil {
		d.Accepted = false
		if d.Compatibility.State == "not_assessed" {
			d.Compatibility.State = "invalid"
		}
		d.Compatibility.Reasons = append(d.Compatibility.Reasons, assessmentError.Error())
	}
	if err := renderControlledDecision(out, d, format); err != nil {
		return &ExitCodeError{Code: 1, Err: err}
	}
	if !d.Accepted {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("selected release policy did not pass")}
	}
	return nil
}

func renderControlledDecision(out io.Writer, d releasepolicy.Decision, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(d)
	}
	if format != "human" && format != "markdown" && format != "github-actions" {
		return fmt.Errorf("unsupported selected release output format %q", format)
	}
	var report strings.Builder
	result := "NOT PASSED"
	if d.Accepted {
		result = "PASSED"
	}
	fmt.Fprintf(&report, "Selected release policy: %s\n", result)
	for _, dimension := range []struct {
		name  string
		value releasepolicy.Dimension
	}{
		{"Compatibility", d.Compatibility}, {"Completeness", d.Completeness},
		{"Assurance", d.Assurance}, {"Golden", d.Golden}, {"Billing", d.Billing},
		{"Statistics", d.Statistics}, {"Operations", d.Operations},
	} {
		fmt.Fprintf(&report, "%s: %s\n", dimension.name, dimension.value.State)
		for _, reason := range dimension.value.Reasons {
			fmt.Fprintf(&report, "  %s\n", reason)
		}
	}
	for _, arm := range []releasepolicy.Arm{releasepolicy.Baseline, releasepolicy.Candidate} {
		if counts, ok := d.Accounting[arm]; ok {
			fmt.Fprintf(&report, "%s: %d/%d trials started, %d complete, %d attempts; first passes %d, retry-policy passes %d, recovered %d\n",
				arm, counts.StartedTrials, counts.PlannedTrials, counts.CompleteTrials, counts.Attempts,
				counts.FirstPasses, counts.RetryPasses, counts.RecoveredTrials)
		}
		for _, usage := range d.Usage[arm] {
			value := "unavailable"
			if usage.Value != nil {
				value = usage.Value.String()
			}
			fmt.Fprintf(&report, "%s %s %s: %s (%s)\n", arm, usage.Axis, usage.Currency, value, usage.Observation)
		}
	}
	if d.Estimate != nil {
		fmt.Fprintf(&report, "Fixed-suite paired estimate %.6g, interval [%.6g, %.6g], half-width %.6g; %d planned clusters (not iid retries)\n",
			*d.Estimate, *d.Lower, *d.Upper, *d.HalfWidth, d.PlannedClusters)
	}
	for _, limitation := range d.Limitations {
		fmt.Fprintf(&report, "Limitation: %s\n", limitation)
	}
	if format == "github-actions" && !d.Accepted {
		report.WriteString("::error::Selected release policy did not pass; inspect the distinct decision dimensions above.\n")
	}
	_, err := io.WriteString(out, report.String())
	return err
}
