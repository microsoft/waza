package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/spf13/cobra"
)

type assuranceFlags struct {
	contract  string
	baseline  controlledcomparison.AssuranceSource
	candidate controlledcomparison.AssuranceSource
}

func (a *assuranceFlags) flags(cmd *cobra.Command, sourceFlags bool) {
	if sourceFlags {
		sources := controlledSources{}
		sources.flags(cmd)
	}
	if cmd.Name() != "compare-assurance-plan" {
		cmd.Flags().StringVar(&a.contract, "assurance-contract", "", "Explicit independent offline assurance contract; never upgrades base 1.0")
	}
	for _, side := range []struct {
		name  string
		value *controlledcomparison.AssuranceSource
	}{{"baseline", &a.baseline}, {"candidate", &a.candidate}} {
		cmd.Flags().StringVar(&side.value.ReferencesPath, side.name+"-references", "", "Current evaluator-only reference-label JSON for this arm")
		cmd.Flags().StringVar(&side.value.ReviewPath, side.name+"-review", "", "Current separately supplied review declaration for this arm")
		cmd.Flags().StringVar(&side.value.AcceptReviewSource, side.name+"-accept-review-source", "", "Explicitly accept this current supplied review source; not authenticated human review")
	}
}

func (a *assuranceFlags) sources(cmd *cobra.Command) error {
	for _, side := range []struct {
		name  string
		value *controlledcomparison.AssuranceSource
	}{{"baseline", &a.baseline}, {"candidate", &a.candidate}} {
		for _, suffix := range []string{"eval", "context-dir", "references", "review", "accept-review-source"} {
			name := side.name + "-" + suffix
			value, err := cmd.Flags().GetString(name)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed(name) && value == "" {
				return fmt.Errorf("--%s cannot be empty when explicitly selected", name)
			}
		}
		var err error
		side.value.Source.EvalPath, err = cmd.Flags().GetString(side.name + "-eval")
		if err != nil {
			return err
		}
		side.value.Source.ContextDir, err = cmd.Flags().GetString(side.name + "-context-dir")
		if err != nil {
			return err
		}
		if side.value.Source.EvalPath == "" || side.value.ReferencesPath == "" {
			return fmt.Errorf("--%s-eval and --%s-references are required for selected assurance", side.name, side.name)
		}
	}
	return nil
}

func (a *assuranceFlags) selected(cmd *cobra.Command) bool {
	for _, name := range []string{"assurance-contract", "baseline-references", "candidate-references",
		"baseline-review", "candidate-review", "baseline-accept-review-source", "candidate-accept-review-source"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func (a *assuranceFlags) assessmentSelected(cmd *cobra.Command) bool {
	for _, name := range []string{"baseline-eval", "candidate-eval", "baseline-context-dir", "candidate-context-dir"} {
		if cmd.Flags().Changed(name) {
			return true
		}
	}
	return a.selected(cmd)
}

func newCompareAssurancePlanCommand() *cobra.Command {
	var flags assuranceFlags
	var policyPath, output string
	cmd := &cobra.Command{Use: "compare-assurance-plan", Short: "Bind an additive offline assurance contract before paired collection",
		Long: "Bind current native text declarations, authored reference inputs and separately supplied review sources to an assurance-required base policy. This finite-corpus profile never runs calibration or a paid model. Unreviewed inputs remain nonpassing; original base 1.0 stays unchanged.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if policyPath == "" || output == "" {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("--release-policy and --output are required")}
			}
			if err := flags.sources(cmd); err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			data, err := os.ReadFile(policyPath)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			contract, err := controlledcomparison.PlanAssurance(cmd.Context(), data, flags.baseline, flags.candidate)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			_, writeErr := file.Write(contract)
			if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Offline assurance contract: %s (local binding, not authenticated review)\n", output)
			return err
		}}
	flags.flags(cmd, true)
	cmd.Flags().StringVar(&policyPath, "release-policy", "", "Existing base 1.0 policy with assurance explicitly required")
	cmd.Flags().StringVarP(&output, "output", "o", "", "New assurance contract file; no overwrite")
	return cmd
}

func runAssuredAssessment(cmd *cobra.Command, flags *assuranceFlags, policyPath, directory, format string) error {
	if flags.contract == "" || policyPath == "" || directory == "" {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("--assurance-contract, --release-policy and --collection-dir are all required")}
	}
	if err := flags.sources(cmd); err != nil {
		return &ExitCodeError{Code: 1, Err: err}
	}
	policy, err := os.ReadFile(policyPath)
	if err != nil {
		return &ExitCodeError{Code: 1, Err: err}
	}
	contract, err := os.ReadFile(flags.contract)
	if err != nil {
		return &ExitCodeError{Code: 1, Err: err}
	}
	d, assessmentErr := controlledcomparison.AssessWithAssurance(cmd.Context(), policy, contract, directory,
		flags.baseline, flags.candidate)
	if err := renderAssuredDecision(cmd.OutOrStdout(), d, format); err != nil {
		return &ExitCodeError{Code: 1, Err: err}
	}
	if assessmentErr != nil {
		return &ExitCodeError{Code: 1, Err: assessmentErr}
	}
	if !d.Accepted {
		return &ExitCodeError{Code: 1, Err: fmt.Errorf("selected additive offline assurance policy did not pass")}
	}
	return nil
}

func renderAssuredDecision(out io.Writer, d releasepolicy.AssuredDecision, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(d)
	}
	if format != "human" && format != "markdown" && format != "github-actions" && format != "table" {
		return fmt.Errorf("unsupported assured output format %q", format)
	}
	if err := renderControlledDecision(out, d.BaseDecision, "human"); err != nil {
		return err
	}
	state := "NOT PASSED"
	if d.Accepted {
		state = "PASSED"
	}
	if _, err := fmt.Fprintf(out, "\nAdditive offline assurance decision: %s\nAssurance: %s\nDeterministic regrade: %s\n",
		state, d.Assurance.State, d.Regrade.State); err != nil {
		return err
	}
	for _, reason := range append(append([]string{}, d.Assurance.Reasons...), d.Regrade.Reasons...) {
		if _, err := fmt.Fprintln(out, "  "+reason); err != nil {
			return err
		}
	}
	for _, limitation := range d.Limitations {
		if _, err := fmt.Fprintln(out, "Limitation: "+limitation); err != nil {
			return err
		}
	}
	if format == "github-actions" && !d.Accepted {
		_, err := fmt.Fprintln(out, "::error::Selected additive offline assurance policy did not pass.")
		return err
	}
	return nil
}
