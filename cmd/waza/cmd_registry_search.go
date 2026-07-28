// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License. See LICENSE in the project root for license information.

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/microsoft/waza/internal/registry"
	"github.com/spf13/cobra"
)

type registrySearchFlags struct {
	kind     string
	registry string
	format   string
}

func newRegistrySearchCommand() *cobra.Command {
	f := &registrySearchFlags{}
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search configured registry indexes",
		Long: `Search configured registry indexes for graders, evals, and datasets.

Results are printed as a human-readable table by default, or as JSON with
--format json for automation. Multiple sources are consulted in priority
order; duplicates are collapsed by canonical ref.

Examples:
  waza registry search factual
  waza registry search factual --kind grader
  waza registry search factual --registry public
  waza registry search factual --format json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var query string
			if len(args) == 1 {
				query = args[0]
			}
			return runRegistrySearch(cmd.OutOrStdout(), query, *f)
		},
	}

	cmd.Flags().StringVar(&f.kind, "kind", "", "Filter results by artifact kind (grader|eval|dataset)")
	cmd.Flags().StringVar(&f.registry, "registry", "", "Restrict search to a single configured registry source by name")
	cmd.Flags().StringVar(&f.format, "format", "table", "Output format: table|json")

	return cmd
}

func runRegistrySearch(out io.Writer, query string, f registrySearchFlags) error {
	if err := validateSearchFlags(f); err != nil {
		return err
	}

	// TODO(#15): load user-supplied waza.registry.yaml when present.
	// For Phase 1 we use the default public registry only.
	cfg := registry.DefaultConfig()
	searcher := registry.NewSearcher(cfg)

	opts := registry.SearchOptions{
		Query:    query,
		Kind:     registry.Kind(f.kind),
		Registry: f.registry,
	}
	results, err := searcher.Search(opts)
	if err != nil {
		return fmt.Errorf("registry search: %w", err)
	}

	switch strings.ToLower(f.format) {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "table", "":
		return writeSearchTable(out, results)
	default:
		return fmt.Errorf("unsupported --format %q (want table|json)", f.format)
	}
}

func validateSearchFlags(f registrySearchFlags) error {
	if f.kind != "" {
		switch registry.Kind(f.kind) {
		case registry.KindGrader, registry.KindEval, registry.KindDataset, registry.KindProgramGrader:
		default:
			return fmt.Errorf("unsupported --kind %q (want grader|eval|dataset)", f.kind)
		}
	}
	if f.format != "" {
		switch strings.ToLower(f.format) {
		case "table", "json":
		default:
			return fmt.Errorf("unsupported --format %q (want table|json)", f.format)
		}
	}
	return nil
}

func writeSearchTable(out io.Writer, results []registry.SearchResult) error {
	if len(results) == 0 {
		fmt.Fprintln(out, "No matching results.") //nolint:errcheck
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REF\tKIND\tSTARS\tDESCRIPTION") //nolint:errcheck
	fmt.Fprintln(tw, "---\t----\t-----\t-----------") //nolint:errcheck
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", r.Ref, r.Kind, r.Stars, r.Description) //nolint:errcheck
	}
	return tw.Flush()
}
