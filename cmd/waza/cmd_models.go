package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/microsoft/waza/internal/execution"
	"github.com/spf13/cobra"
)

type modelsCommandOptions struct {
	NewCopilotClient func(clientOptions *copilot.ClientOptions) execution.CopilotClient
}

func newModelsCommand() *cobra.Command {
	return newModelsCommandWithOptions(nil)
}

func newModelsCommandWithOptions(options *modelsCommandOptions) *cobra.Command {
	if options == nil {
		options = &modelsCommandOptions{}
	}

	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "models",
		Short: "List available models",
		Long:  `List models available for evaluation via the Copilot SDK.`,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (finalErr error) {
			engine := execution.NewCopilotEngineBuilder("", &execution.CopilotEngineBuilderOptions{
				NewCopilotClient: options.NewCopilotClient,
			}).Build()

			if err := engine.Initialize(cmd.Context()); err != nil {
				if isAuthError(err) {
					return fmt.Errorf("not authenticated — run \"copilot login\" first")
				}
				return fmt.Errorf("failed to connect to Copilot: %w", err)
			}

			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if shutdownErr := engine.Shutdown(ctx); shutdownErr != nil {
					finalErr = errors.Join(finalErr, shutdownErr)
				}
			}()

			models, err := engine.ListModels(cmd.Context())
			if err != nil {
				return fmt.Errorf("failed to list models: %w", err)
			}

			sort.Slice(models, func(i, j int) bool {
				return models[i].ID < models[j].ID
			})

			if jsonOutput {
				data, err := json.MarshalIndent(models, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal models: %w", err)
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(data)) //nolint:errcheck
				return nil
			}

			return renderModelsTable(cmd, models)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func renderModelsTable(cmd *cobra.Command, models []copilot.ModelInfo) error {
	w := cmd.OutOrStdout()

	if len(models) == 0 {
		fmt.Fprintln(w, "No models available.") //nolint:errcheck
		return nil
	}

	// Compute column widths
	idWidth := len("MODEL ID")
	nameWidth := len("NAME")
	for _, m := range models {
		if len(m.ID) > idWidth {
			idWidth = len(m.ID)
		}
		if len(m.Name) > nameWidth {
			nameWidth = len(m.Name)
		}
	}

	header := fmt.Sprintf("%-*s  %-*s  %-8s  %s", idWidth, "MODEL ID", nameWidth, "NAME", "VISION", "CONTEXT WINDOW")
	fmt.Fprintln(w, header)                             //nolint:errcheck
	fmt.Fprintln(w, strings.Repeat("─", len(header)+4)) //nolint:errcheck

	for _, m := range models {
		vision := "no"
		if m.Capabilities.Supports.Vision {
			vision = "yes"
		}

		contextWindow := "-"
		if m.Capabilities.Limits.MaxContextWindowTokens > 0 {
			contextWindow = formatTokenCount(m.Capabilities.Limits.MaxContextWindowTokens)
		}

		fmt.Fprintf(w, "%-*s  %-*s  %-8s  %s\n", idWidth, m.ID, nameWidth, m.Name, vision, contextWindow) //nolint:errcheck
	}

	fmt.Fprintf(w, "\n%d models available\n", len(models)) //nolint:errcheck
	return nil
}

func formatTokenCount(tokens int) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%dM", tokens/1_000_000)
	}
	if tokens >= 1_000 {
		return fmt.Sprintf("%dk", tokens/1_000)
	}
	return fmt.Sprintf("%d", tokens)
}

func isAuthError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "not authenticated") ||
		strings.Contains(msg, "copilot login") ||
		strings.Contains(msg, "authentication status")
}
