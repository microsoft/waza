// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"fmt"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	var strictLock bool

	cmd := &cobra.Command{
		Use:   "get [eval.yaml]",
		Short: "Resolve remote grader refs and write waza.lock",
		Long: `Resolve every 'ref:' grader entry in eval.yaml against its remote Git
source, download the pinned content into the module cache, and write
(or update) waza.lock beside eval.yaml.

Phase 1 supports Go-module-style refs of the form:

  github.com/<owner>/<repo>[/path][#export]@<version>

where <version> must be an exact semver tag (vX.Y.Z) or a full 40-character
commit SHA. Floating selectors (branches, ranges, "latest") are rejected
for reproducibility.

Examples:

  waza get                              # resolves ./eval.yaml
  waza get evals/factuality/eval.yaml   # explicit spec path
  waza get --verify                     # do not modify the lock; verify only
`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			specPath := "eval.yaml"
			if len(args) == 1 {
				specPath = args[0]
			}
			spec, err := models.LoadEvalSpec(specPath)
			if err != nil {
				return fmt.Errorf("loading %s: %w", specPath, err)
			}

			refCount := 0
			for _, g := range spec.Graders {
				if g.Ref != "" {
					refCount++
				}
			}
			if refCount == 0 {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "No remote grader refs found in %s. Nothing to do.\n", specPath)
				return nil
			}

			resolved, lockChanged, err := registry.ResolveSpec(cmd.Context(), spec, specPath, !strictLock)
			if err != nil {
				return err
			}
			lockPath := registry.LockfilePath(specPath)
			switch {
			case strictLock:
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Verified %d remote grader ref(s) against %s.\n", resolved, lockPath)
			case lockChanged:
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resolved %d remote grader ref(s); wrote %s.\n", resolved, lockPath)
			default:
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Resolved %d remote grader ref(s); %s already up to date.\n", resolved, lockPath)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&strictLock, "verify", false, "Verify refs against existing waza.lock without modifying it (fails if any ref is unlocked or digest-mismatched)")
	return cmd
}
