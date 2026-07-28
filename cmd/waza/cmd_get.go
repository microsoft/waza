package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/microsoft/waza/internal/models"
	"github.com/microsoft/waza/internal/registry"
	"github.com/spf13/cobra"
)

func newGetCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [eval.yaml | ref]",
		Short: "Resolve remote grader refs and update waza.lock",
		Long: `Resolve remote grader refs and update waza.lock.

When given an eval YAML file, resolves every graders[].ref entry, downloads the
module source into the Waza module cache, and writes a lockfile next to the eval.
When given a single ref, resolves that ref and writes waza.lock in the current
directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "eval.yaml"
			if len(args) > 0 {
				target = args[0]
			}
			resolver, err := registry.NewResolver()
			if err != nil {
				return err
			}
			return runGet(cmd.OutOrStdout(), cmd.Context(), resolver, target)
		},
	}
	return cmd
}

type getResolver interface {
	ResolveEvalLock(ctx context.Context, evalPath string) (*models.Lockfile, []models.LockfileGrader, error)
	ResolveRefs(ctx context.Context, refs []string) ([]models.LockfileGrader, error)
}

func runGet(out io.Writer, ctx context.Context, resolver getResolver, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		target = "eval.yaml"
	}
	if isEvalYAMLTarget(target) {
		lock, entries, err := resolver.ResolveEvalLock(ctx, target)
		if err != nil {
			return err
		}
		lockPath := filepath.Join(filepath.Dir(target), models.LockfileName)
		if err := models.WriteLockfile(lockPath, lock); err != nil {
			return fmt.Errorf("writing %s: %w", lockPath, err)
		}
		_, err = fmt.Fprintf(out, "Resolved %d remote grader ref(s); wrote %s\n", len(entries), lockPath)
		return err
	}

	entries, err := resolver.ResolveRefs(ctx, []string{target})
	if err != nil {
		return err
	}
	lock := models.NewLockfile()
	for _, entry := range entries {
		lock.UpsertGrader(entry)
	}
	lockPath := models.LockfileName
	if err := models.WriteLockfile(lockPath, lock); err != nil {
		return fmt.Errorf("writing %s: %w", lockPath, err)
	}
	_, err = fmt.Fprintf(out, "Resolved %d remote grader ref(s); wrote %s\n", len(entries), lockPath)
	return err
}

func isEvalYAMLTarget(target string) bool {
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return true
	}
	ext := strings.ToLower(filepath.Ext(target))
	return ext == ".yaml" || ext == ".yml"
}
