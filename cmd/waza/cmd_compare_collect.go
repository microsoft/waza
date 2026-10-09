package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/microsoft/waza/internal/controlledcomparison"
	"github.com/microsoft/waza/internal/releasepolicy"
	"github.com/spf13/cobra"
)

type controlledSources struct {
	baseline  controlledcomparison.Source
	candidate controlledcomparison.Source
}

func (sources *controlledSources) flags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&sources.baseline.EvalPath, "baseline-eval", "", "Baseline eval YAML (required)")
	cmd.Flags().StringVar(&sources.candidate.EvalPath, "candidate-eval", "", "Candidate eval YAML (required)")
	cmd.Flags().StringVar(&sources.baseline.ContextDir, "baseline-context-dir", "", "Baseline fixtures, relative to cwd (default: eval directory/fixtures)")
	cmd.Flags().StringVar(&sources.candidate.ContextDir, "candidate-context-dir", "", "Candidate fixtures, relative to cwd (default: eval directory/fixtures)")
}

func (sources controlledSources) validate() error {
	if sources.baseline.EvalPath == "" || sources.candidate.EvalPath == "" {
		return fmt.Errorf("--baseline-eval and --candidate-eval are required")
	}
	return nil
}

func newComparePlanCommand() *cobra.Command {
	var sources controlledSources
	var designPath, requirementsPath, output string
	var allowed []string
	cmd := &cobra.Command{
		Use: "compare-plan", Short: "Bind an explicit fixed design to inspected offline comparison sources",
		Long: "Create a private release-policy 1.0 artifact before collecting outcomes. Currently supports mock/no-skills execution with native text checks only. Static planning is not assurance, runtime readiness, agent quality, or authenticated provenance.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := sources.validate(); err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			if designPath == "" || requirementsPath == "" || output == "" {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("--design, --requirements and --output are required")}
			}
			designData, err := os.ReadFile(designPath)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			design, err := releasepolicy.DecodeDesignInput(designData)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			requirementsData, err := os.ReadFile(requirementsPath)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			requirements, err := releasepolicy.DecodeRequirementsInput(requirementsData)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			data, err := controlledcomparison.Plan(sources.baseline, sources.candidate, design, requirements, allowed)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("creating new policy (no overwrite): %w", err)}
			}
			_, writeErr := file.Write(data)
			syncErr := file.Sync()
			closeErr := file.Close()
			if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("persisting policy: %w", err)}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Precollection policy: %s (local consistency, not assurance)\n", output)
			return err
		},
	}
	sources.flags(cmd)
	cmd.Flags().StringVar(&designPath, "design", "", "Explicit fixed-design JSON; allocation must be empty")
	cmd.Flags().StringVar(&requirementsPath, "requirements", "", "Explicit selected identity/runtime/assurance/billing requirements JSON")
	cmd.Flags().StringSliceVar(&allowed, "allow-change", nil, "Declared changed fields: engine, model, reasoning_effort")
	cmd.Flags().StringVarP(&output, "output", "o", "", "New policy file; never overwrites an existing artifact")
	return cmd
}

func newCompareCollectCommand() *cobra.Command {
	var sources controlledSources
	var policyPath, directory string
	cmd := &cobra.Command{
		Use: "compare-collect", Short: "Collect fresh paired offline attempts under a predeclared policy",
		Long: "Reinspect and bind both sources before BEGIN, then each attempt before executing its frozen inputs. Durable starts precede fresh mock-engine initialization. New directory only; no cached outcomes, historical adoption, resume, adaptive stopping, or live/paid execution.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := sources.validate(); err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			if policyPath == "" || directory == "" {
				return &ExitCodeError{Code: 1, Err: fmt.Errorf("--release-policy and --collection-dir are required")}
			}
			data, err := os.ReadFile(policyPath)
			if err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			if err := controlledcomparison.Collect(cmd.Context(), data, sources.baseline, sources.candidate, directory); err != nil {
				return &ExitCodeError{Code: 1, Err: err}
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Collection persisted: %s; assess with compare/gate --release-policy --collection-dir.\n", directory)
			return err
		},
	}
	sources.flags(cmd)
	cmd.Flags().StringVar(&policyPath, "release-policy", "", "Explicit precollection policy JSON (required)")
	cmd.Flags().StringVar(&directory, "collection-dir", "", "New private collection directory (required)")
	return cmd
}
