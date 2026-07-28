package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	wazaconfig "github.com/microsoft/waza/internal/config"
	"github.com/microsoft/waza/internal/projectconfig"
	"github.com/spf13/cobra"
)

type registrySearchOptions struct {
	kind     string
	registry string
	format   string
}

type registrySearchResult struct {
	Ref         string `json:"ref"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Stars       int    `json:"stars"`
	Registry    string `json:"registry"`
}

func newRegistrySearchCommand() *cobra.Command {
	opts := registrySearchOptions{format: "table"}

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search configured registry indexes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRegistrySearch(cmd, args[0], opts)
		},
	}

	cmd.Flags().StringVar(&opts.kind, "kind", "", "Filter by artifact kind: grader, eval, or dataset")
	cmd.Flags().StringVar(&opts.registry, "registry", "", "Search only the named registry source")
	cmd.Flags().StringVar(&opts.format, "format", "table", "Output format: table or json")

	return cmd
}

func runRegistrySearch(cmd *cobra.Command, query string, opts registrySearchOptions) error {
	if opts.kind != "" && opts.kind != "grader" && opts.kind != "eval" && opts.kind != "dataset" {
		return fmt.Errorf("invalid --kind %q: expected grader, eval, or dataset", opts.kind)
	}
	if opts.format != "table" && opts.format != "json" {
		return fmt.Errorf("invalid --format %q: expected table or json", opts.format)
	}

	cfg, err := projectconfig.Load(".")
	if err != nil {
		return err
	}
	registries, err := selectRegistrySources(cfg.Registries, opts.registry)
	if err != nil {
		return err
	}

	results := searchRegistryIndexes(query, opts.kind, registries)
	if opts.format == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	renderRegistrySearchTable(cmd, results)
	return nil
}

func selectRegistrySources(sources []wazaconfig.RegistrySource, name string) ([]wazaconfig.RegistrySource, error) {
	if len(sources) == 0 {
		sources = wazaconfig.DefaultRegistrySources()
	}
	sort.SliceStable(sources, func(i, j int) bool {
		return sources[i].Priority > sources[j].Priority
	})
	if name == "" {
		return sources, nil
	}
	for _, source := range sources {
		if source.Name == name {
			return []wazaconfig.RegistrySource{source}, nil
		}
	}
	return nil, fmt.Errorf("registry %q is not configured", name)
}

func searchRegistryIndexes(query, kind string, registries []wazaconfig.RegistrySource) []registrySearchResult {
	// TODO: call registry index API for each configured source.
	catalog := []registrySearchResult{
		{
			Ref:         "github.com/waza-evals/fact#factuality@v1.0.0",
			Kind:        "grader",
			Description: "Prompt grader for factual grounding.",
			Stars:       128,
		},
		{
			Ref:         "github.com/waza-evals/fact#closedqa@v1.0.0",
			Kind:        "grader",
			Description: "Closed-question answer evaluator.",
			Stars:       96,
		},
		{
			Ref:         "github.com/waza-evals/agent-basics#repo-maintainer@v1.0.0",
			Kind:        "eval",
			Description: "Reusable repository-maintainer eval bundle.",
			Stars:       54,
		},
		{
			Ref:         "github.com/waza-evals/fact#factual-answering@v1.0.0",
			Kind:        "dataset",
			Description: "Factual answering dataset.",
			Stars:       38,
		},
	}

	query = strings.ToLower(strings.TrimSpace(query))
	var results []registrySearchResult
	for _, registry := range registries {
		for _, result := range catalog {
			if kind != "" && result.Kind != kind {
				continue
			}
			haystack := strings.ToLower(result.Ref + " " + result.Kind + " " + result.Description)
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			result.Registry = registry.Name
			results = append(results, result)
		}
	}
	return results
}

func renderRegistrySearchTable(cmd *cobra.Command, results []registrySearchResult) {
	w := cmd.OutOrStdout()
	if len(results) == 0 {
		fmt.Fprintln(w, "No registry results found.") //nolint:errcheck
		return
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "REF\tKIND\tDESCRIPTION\tSTARS") //nolint:errcheck
	for _, result := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", result.Ref, result.Kind, result.Description, result.Stars) //nolint:errcheck
	}
	tw.Flush() //nolint:errcheck
}
