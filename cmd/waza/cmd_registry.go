// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"github.com/spf13/cobra"
)

// newRegistryCommand builds the `waza registry` parent command tree.
//
// Phase 1 (issue #17) ships the `search` and `add` subcommands. Full
// end-to-end functionality — actual index HTTP calls and ref
// resolution — depends on issue #15's resolver, which is stubbed here
// with clear TODOs.
func newRegistryCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Discover and add remote grader presets and eval modules",
		Long: `Manage Waza registry sources for composable eval construction.

The registry lets you discover reusable graders, evals, and datasets
published to the waza-evals GitHub org (or any additional registry you
configure) and add them to your eval.yaml with a single command.

See docs/research/waza-eval-registry-design.md for the full design.`,
	}

	cmd.AddCommand(newRegistrySearchCommand())
	cmd.AddCommand(newRegistryAddCommand())

	return cmd
}
