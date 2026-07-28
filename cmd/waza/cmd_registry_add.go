// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/registry"
	"github.com/spf13/cobra"
)

type registryAddFlags struct {
	evalPath  string
	name      string
	sets      []string
	weight    float64
	allowExec bool
	dryRun    bool
	yes       bool
}

func newRegistryAddCommand() *cobra.Command {
	f := &registryAddFlags{}
	cmd := &cobra.Command{
		Use:   "add <ref>",
		Short: "Add a registry artifact to eval.yaml and update waza.lock",
		Long: `Resolve a registry ref, append it to eval.yaml as a "ref:" grader
entry, and update waza.lock with the resolved commit and digest.

Program graders (executable artifacts) are refused unless the caller
passes --allow-exec or confirms the interactive prompt.

Examples:
  waza registry add github.com/waza-evals/fact#factuality@v1.0.0
  waza registry add github.com/waza-evals/fact#factuality@v1.0.0 --eval eval.yaml
  waza registry add github.com/waza-evals/fact#factuality@v1.0.0 \
      --name factuality_strict --set config.threshold=0.9`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistryAdd(cmd.OutOrStdout(), cmd.InOrStdin(), args[0], *f)
		},
	}

	cmd.Flags().StringVar(&f.evalPath, "eval", "eval.yaml", "Path to the eval file to modify")
	cmd.Flags().StringVar(&f.name, "name", "", "Local alias for the grader (overrides remote default)")
	cmd.Flags().StringSliceVar(&f.sets, "set", nil, "Config overrides, key.path=value (repeatable)")
	cmd.Flags().Float64Var(&f.weight, "weight", 0, "Grader weight override (0 keeps default)")
	cmd.Flags().BoolVar(&f.allowExec, "allow-exec", false, "Allow adding program-grader artifacts without prompting")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "Print planned changes without writing files")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "Assume yes to interactive prompts")

	return cmd
}

func runRegistryAdd(out io.Writer, in io.Reader, refStr string, f registryAddFlags) error {
	if !registry.IsRemote(refStr) {
		return fmt.Errorf("%q is not a registry ref (expected host/owner/repo#export@version)", refStr)
	}
	ref, err := registry.ParseRef(refStr)
	if err != nil {
		return err
	}

	config, err := registry.ParseSetFlag(f.sets)
	if err != nil {
		return err
	}

	// TODO(#15): call the real resolver. For now the stub returns
	// syntax-derived metadata so we can still write the ref entry.
	resolver := registry.StubResolver{}
	resolution, err := resolver.Resolve(ref)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", ref, err)
	}

	if resolution.Kind == registry.KindProgramGrader {
		if !f.allowExec && !f.yes {
			ok, err := confirmProgramGrader(out, in, ref.String())
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("aborted by user; re-run with --allow-exec to skip the prompt")
			}
		}
		resolution.Trusted = true
	}

	entry := registry.GraderRefEntry{
		Ref:    ref.String(),
		Name:   f.name,
		Weight: f.weight,
		Config: config,
	}

	if f.dryRun {
		return printAddDryRun(out, f.evalPath, entry, resolution)
	}

	evalPath, err := filepath.Abs(f.evalPath)
	if err != nil {
		return fmt.Errorf("resolving eval path: %w", err)
	}
	if err := registry.AppendGraderRef(evalPath, entry); err != nil {
		return err
	}

	lockPath := filepath.Join(filepath.Dir(evalPath), registry.LockFileName)
	lf, err := registry.LoadLockFile(lockPath)
	if err != nil {
		return err
	}
	lockEntry := registry.EntryFromResolution(resolution)
	lf.Upsert(lockEntry)
	if err := lf.Save(lockPath); err != nil {
		return err
	}

	fmt.Fprintf(out, "Added grader %s to %s\n", ref.String(), f.evalPath) //nolint:errcheck
	fmt.Fprintf(out, "Updated %s\n", registry.LockFileName)               //nolint:errcheck
	// TODO(#15): print resolved commit + digest once the real resolver
	// returns them.
	return nil
}

func confirmProgramGrader(out io.Writer, in io.Reader, ref string) (bool, error) {
	fmt.Fprintf(out, "%s is a program grader (executable). Trust and add? [y/N]: ", ref) //nolint:errcheck
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes", nil
}

func printAddDryRun(out io.Writer, evalPath string, entry registry.GraderRefEntry, res registry.Resolution) error {
	fmt.Fprintf(out, "DRY RUN: would add grader to %s:\n", evalPath) //nolint:errcheck
	fmt.Fprintf(out, "  ref: %s\n", entry.Ref)                       //nolint:errcheck
	if entry.Name != "" {
		fmt.Fprintf(out, "  name: %s\n", entry.Name) //nolint:errcheck
	}
	if entry.Weight != 0 {
		fmt.Fprintf(out, "  weight: %g\n", entry.Weight) //nolint:errcheck
	}
	if len(entry.Config) > 0 {
		fmt.Fprintln(out, "  config:") //nolint:errcheck
		for k, v := range entry.Config {
			fmt.Fprintf(out, "    %s: %v\n", k, v) //nolint:errcheck
		}
	}
	fmt.Fprintf(out, "DRY RUN: would update %s with module %s@%s\n", registry.LockFileName, res.Module, res.Version) //nolint:errcheck
	return nil
}
