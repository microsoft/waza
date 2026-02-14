package main

import (
	"fmt"
	"path/filepath"

	"github.com/spboyer/waza/internal/cache"
	"github.com/spf13/cobra"
)

var cacheDir string

func newCacheCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage evaluation result cache",
		Long: `Manage the evaluation result cache.

The cache stores test outcomes to speed up repeated evaluations with the same
inputs. Cached results are keyed by spec configuration, task definition, model,
and fixture file contents.`,
	}

	cmd.AddCommand(newCacheClearCommand())

	return cmd
}

func newCacheClearCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear the evaluation result cache",
		Long: `Clear all cached evaluation results.

This removes all cached test outcomes. The next evaluation run will re-execute
all tests from scratch.`,
		RunE: cacheClearE,
	}

	cmd.Flags().StringVar(&cacheDir, "cache-dir", ".waza-cache", "Cache directory to clear")

	return cmd
}

func cacheClearE(cmd *cobra.Command, args []string) error {
	// Resolve to absolute path
	absDir, err := filepath.Abs(cacheDir)
	if err != nil {
		return fmt.Errorf("resolving cache directory: %w", err)
	}

	c := cache.New(absDir)
	if err := c.Clear(); err != nil {
		return fmt.Errorf("clearing cache: %w", err)
	}

	fmt.Printf("Cache cleared: %s\n", absDir)
	return nil
}
