package main

import (
	"fmt"
	"os"

	"github.com/microsoft/waza/internal/usagetelemetry"
	"github.com/spf13/cobra"
)

// newTelemetryCommand manages product usage telemetry. Every subcommand is
// local-only: none sends data, and none is itself collected.
func newTelemetryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Manage product usage telemetry (local-only)",
		Long: `Manage Waza product usage telemetry.

Product usage telemetry is separate from OpenTelemetry tracing (--otel-*) and
execution telemetry exports. Released builds send a small set of allowlisted,
pseudonymous usage events from developer machines; CI environments are never
collected. These commands only change local settings in ~/.waza/telemetry.json
and never send data. See https://github.com/microsoft/waza/blob/main/docs/USAGE-TELEMETRY.md.

Opt out at any time with WAZA_TELEMETRY_OPTOUT=1, --no-telemetry, DO_NOT_TRACK=1,
or 'waza telemetry disable'. Any opt-out wins over every other setting.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show whether product usage telemetry is active and why",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			policy := usagetelemetry.ResolveProcess(os.Args[1:], true)
			settings, settingsErr := usagetelemetry.LoadSettings(usagetelemetry.DefaultSettingsPath())
			out := cmd.OutOrStdout()
			state := "inactive"
			if policy.Enabled {
				state = "active"
			}
			persisted := "default"
			if settingsErr != nil {
				persisted = "invalid (collection disabled)"
			} else if settings.Collection != "" {
				persisted = settings.Collection
			}
			installID := "absent"
			if settingsErr == nil && settings.InstallationID != "" {
				installID = "present"
			}
			ci := "no"
			if policy.CI {
				ci = "yes"
			}
			if _, err := fmt.Fprintf(out, "Product usage telemetry: %s\nPersistent setting: %s\nInstallation ID: %s\nCI detected: %s\n",
				state, persisted, installID, ci); err != nil {
				return err
			}
			if len(policy.Reasons) > 0 {
				if _, err := fmt.Fprintln(out, "Reasons:"); err != nil {
					return err
				}
			}
			for _, reason := range policy.Reasons {
				if _, err := fmt.Fprintf(out, "  - %s\n", reason); err != nil {
					return err
				}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "disable",
		Short: "Persistently disable product usage telemetry and delete the installation ID",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := usagetelemetry.Disable(usagetelemetry.DefaultSettingsPath()); err != nil {
				return fmt.Errorf("disabling telemetry: %w", err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Product usage telemetry disabled; the installation ID was deleted.")
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "enable",
		Short: "Remove a persistent disable choice",
		Long: `Record that product usage telemetry may be collected on this machine.

Any opt-out (WAZA_TELEMETRY_OPTOUT, --no-telemetry, DO_NOT_TRACK, project
denial, or a hosting tool's denial) continues to win, and CI is never collected.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := usagetelemetry.Enable(usagetelemetry.DefaultSettingsPath()); err != nil {
				return fmt.Errorf("enabling telemetry: %w", err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Product usage telemetry enabled. Run 'waza telemetry status' to see whether it is active.")
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "reset-id",
		Short: "Delete the installation ID so a new random one is used",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := usagetelemetry.ResetID(usagetelemetry.DefaultSettingsPath()); err != nil {
				return fmt.Errorf("resetting telemetry installation ID: %w", err)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "Installation ID deleted.")
			return err
		},
	})
	return cmd
}
