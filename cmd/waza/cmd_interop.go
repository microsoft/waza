package main

import (
	"fmt"
	"io"

	"github.com/microsoft/waza/internal/interop"
	"github.com/spf13/cobra"
)

func newInteropCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "interop",
		Short: "Project pinned external assertions offline (not whole eval suites)",
	}
	for _, operation := range []string{"import", "export"} {
		var adapter, testID string
		var assertion int
		child := &cobra.Command{
			Use:   operation,
			Short: operation + " one case-sensitive contains assertion via stdin/stdout",
			Long: `Definition-only, offline assertion conversion. Read YAML from stdin and write
YAML to stdout. No file references, providers, scripts or models are loaded.
Import explicitly selects one assertion from a suite; everything outside that
assertion remains unconverted, including task inputs and sibling graders.
Export accepts one native grader, not an eval or task.
Only the exact --adapter pin is supported. See the external interoperability
guide for limits; this is NOT whole-suite or AgentEvals-standard compatibility.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if adapter != interop.Adapter {
					return fmt.Errorf("--adapter must be %q; unknown formats/versions are unsupported", interop.Adapter)
				}
				data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), interop.MaxBytes+1))
				if err != nil {
					return fmt.Errorf("reading definition: %w", err)
				}
				var output []byte
				if cmd.Name() == "import" {
					output, err = interop.ImportContains(data, testID, assertion)
				} else {
					output, err = interop.ExportContains(data)
				}
				if err != nil {
					return err
				}
				if cmd.Name() == "import" {
					diagnostic := fmt.Sprintf("ASSERTION-ONLY PROJECTION: selected test %q assertion %d; task inputs, providers, fixtures, sibling assertions, thresholds, suite metadata and execution settings are NOT converted or validated. Source provenance is unverified. Output is one native grader, not a runnable eval.\n", testID, assertion)
					n, err := io.WriteString(cmd.ErrOrStderr(), diagnostic)
					if err == nil && n != len(diagnostic) {
						err = io.ErrShortWrite
					}
					if err != nil {
						return fmt.Errorf("writing projection diagnostic: %w", err)
					}
				}
				n, err := cmd.OutOrStdout().Write(output)
				if err == nil && n != len(output) {
					err = io.ErrShortWrite
				}
				if err != nil {
					return fmt.Errorf("writing converted assertion: %w", err)
				}
				return nil
			},
		}
		child.Flags().StringVar(&adapter, "adapter", "", "Required exact version-pinned adapter identifier")
		if operation == "import" {
			child.Flags().StringVar(&testID, "test", "", "Required exact upstream test ID (no globs)")
			child.Flags().IntVar(&assertion, "assertion", 0, "Required 1-based assertion index; no implicit selection")
		}
		command.AddCommand(child)
	}
	return command
}
