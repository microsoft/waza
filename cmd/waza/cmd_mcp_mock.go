package main

import (
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
	var attemptConfigFile string
	cmd := &cobra.Command{
		Use:    "__mcp-mock",
		Short:  "Run an internal MCP mock server",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readMCPMockConfig(configBase64, configFile, attemptConfigFile)
			if err != nil {
				return err
			}
			var cfg mcpmock.Config
			if err := json.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("parse mock config: %w", err)
			}
			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
			// Retain default signal termination: inherited stdio may not unblock
			// on Close, so graceful root cancellation cannot safely join it.
			if err := mcpmock.ServeCancelableStdio(cmd.Context(), &cfg, os.Stdin, os.Stdout, logger); err != nil {
				return fmt.Errorf("serve MCP mock: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&configBase64, "config-base64", "", "base64-encoded MCP mock config")
	cmd.Flags().StringVar(&configFile, "config-file", "", "path to MCP mock config JSON")
	cmd.Flags().StringVar(&attemptConfigFile, "attempt-config-file", "", "path to retained task-attempt MCP mock config JSON")
	return cmd
}

func readMCPMockConfig(configBase64, configFile, attemptConfigFile string) ([]byte, error) {
	if attemptConfigFile != "" && (configBase64 != "" || configFile != "") {
		return nil, fmt.Errorf("--attempt-config-file cannot be combined with --config-file or --config-base64")
	}
	switch {
	case attemptConfigFile != "":
		// The task attempt owns this file so restarted servers can reuse it.
		data, err := os.ReadFile(attemptConfigFile)
		if err != nil {
			return nil, fmt.Errorf("read attempt mock config file: %w", err)
		}
		return data, nil
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
		return nil, fmt.Errorf("--config-file, --attempt-config-file, or --config-base64 is required")
	}
}
