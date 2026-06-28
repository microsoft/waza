package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/microsoft/waza/internal/mcpmock"
	"github.com/spf13/cobra"
)

func newMCPMockCommand() *cobra.Command {
	var configBase64 string
	cmd := &cobra.Command{
		Use:    "__mcp-mock",
		Short:  "Run an internal MCP mock server",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if configBase64 == "" {
				return fmt.Errorf("--config-base64 is required")
			}
			data, err := base64.StdEncoding.DecodeString(configBase64)
			if err != nil {
				return fmt.Errorf("decode mock config: %w", err)
			}
			var cfg mcpmock.Config
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("parse mock config: %w", err)
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			mcpmock.ServeStdio(context.Background(), &cfg, os.Stdin, os.Stdout, logger)
			return nil
		},
	}
	cmd.Flags().StringVar(&configBase64, "config-base64", "", "base64-encoded MCP mock config")
	return cmd
}
