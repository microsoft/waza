package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	copilot "github.com/github/copilot-sdk/go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := os.Getenv("PROBE_ROOT")
	if root == "" {
		return fmt.Errorf("PROBE_ROOT is required")
	}
	libc := os.Getenv("PROBE_LIBC")
	if libc == "" {
		libc = "linuxmusl"
	}
	directory := libc + "/"
	if libc == "linux" {
		directory = ""
	}
	client := copilot.NewClient(&copilot.ClientOptions{
		Connection:      copilot.StdioConnection{Path: root + "/home/.copilot/cache/copilot-sdk/1.0.101/" + directory + "copilot"},
		UseLoggedInUser: copilot.Bool(false),
	})
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer client.Stop()
	workingDir := root + "/canary/tmp/sdk-positive"
	if err := os.MkdirAll(workingDir, 0700); err != nil {
		return err
	}
	session, err := client.CreateSession(ctx, &copilot.SessionConfig{
		Model:                              "offline-model",
		Provider:                           &copilot.ProviderConfig{Type: "openai", BaseURL: os.Getenv("COPILOT_BASE_URL"), APIKey: "offline-synthetic-not-a-credential", WireAPI: "chat"},
		WorkingDirectory:                   workingDir,
		EnableConfigDiscovery:              copilot.Bool(true),
		EnableFileHooks:                    copilot.Bool(true),
		SkipCustomInstructions:             copilot.Bool(false),
		EnableOnDemandInstructionDiscovery: copilot.Bool(true),
		AvailableTools:                     []string{},
		OnPermissionRequest:                copilot.PermissionHandler.ApproveAll,
	})
	if err != nil {
		return err
	}
	defer session.Disconnect()
	response, err := session.SendAndWait(ctx, copilot.MessageOptions{Prompt: "hello"})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}
