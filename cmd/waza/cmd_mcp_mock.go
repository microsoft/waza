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
	var configFile string
	cmd := &cobra.Command{
		Use:    "__mcp-mock",
		Short:  "Run an internal MCP mock server",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readMCPMockConfig(configBase64, configFile)
			if err != nil {
				return err
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
	cmd.Flags().StringVar(&configFile, "config-file", "", "path to MCP mock config JSON")
	return cmd
}

func readMCPMockConfig(configBase64, configFile string) ([]byte, error) {
	switch {
	case configFile != "":
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("read mock config file: %w", err)
		}
		if err := os.Remove(configFile); err != nil {
			return nil, fmt.Errorf("remove mock config file: %w", err)
		}
		return data, nil
	case configBase64 != "":
		data, err := base64.StdEncoding.DecodeString(configBase64)
		if err != nil {
			return nil, fmt.Errorf("decode mock config: %w", err)
		}
		return data, nil
	default:
		return nil, fmt.Errorf("--config-file or --config-base64 is required")
	}
}
