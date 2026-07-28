package main

import "github.com/spf13/cobra"

func newRegistryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Discover and add shared eval registry artifacts",
		Long: `Discover shared Waza eval registry artifacts and add remote grader
presets to eval.yaml.

Registry search uses configured index sources for discovery. Registry add writes
minimal ref entries and a lock file scaffold; full module resolution is wired in
when the shared resolver from issue #15 lands.`,
	}

	cmd.AddCommand(newRegistrySearchCommand())
	cmd.AddCommand(newRegistryAddCommand())

	return cmd
}
