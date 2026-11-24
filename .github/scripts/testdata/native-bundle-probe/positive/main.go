package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	copilot "github.com/microsoft/copilot-sdk/go/v2"
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
	cli, err := bundledCLI(root+"/home/.copilot/cache/copilot-sdk", directory)
	if err != nil {
		return err
	}
	client := copilot.NewClient(&copilot.ClientOptions{
		Mode:    copilot.ModeCodingAgent,
		Runtime: copilot.SpawnedStdioConnection{Path: cli},
		GitHub:  copilot.ClientGitHubOptions{UseLoggedInUser: copilot.Bool(false)},
	})
	if err := client.Start(ctx); err != nil {
		return err
	}
	defer client.Stop()
	workingDir := root + "/canary/tmp/sdk-positive"
	if err := os.MkdirAll(workingDir, 0700); err != nil {
		return err
	}
	providerName := "offline-provider"
	defaultProvider := copilot.String(providerName)
	session, err := client.CreateSession(ctx, &copilot.CreateSessionOptions{
		Model: "offline-model",
		Inference: copilot.SessionInferenceOptions{
			DefaultProvider: &defaultProvider,
			Providers: []copilot.NamedProviderConfig{{
				Name: providerName, Type: "openai", BaseURL: os.Getenv("COPILOT_BASE_URL"),
				APIKey: "offline-synthetic-not-a-credential", WireAPI: "chat",
			}},
		},
		GitHub:                copilot.SessionGitHubOptions{Capi: &copilot.CapiSessionOptions{Enabled: copilot.Bool(false)}},
		WorkingDirectory:      workingDir,
		EnableConfigDiscovery: copilot.Bool(true),
		Hooks:                 copilot.SessionHookOptions{EnableRepositoryHooks: copilot.Bool(true)},
		Instructions: copilot.CreateSessionInstructionOptions{
			SkipCustomInstructions: copilot.Bool(false),
			OnDemandDiscovery:      copilot.Bool(true),
		},
		Tools:       copilot.SessionToolOptions{AvailableTools: []string{}},
		Permissions: copilot.SessionPermissionOptions{OnRequest: copilot.PermissionHandler.ApproveAll},
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

// bundledCLI locates the single version directory the runner extracted, so the
// control follows the embedded bundle without pinning its version.
func bundledCLI(cacheRoot, directory string) (string, error) {
	entries, err := os.ReadDir(cacheRoot)
	if err != nil {
		return "", fmt.Errorf("read bundled CLI cache: %w", err)
	}
	var candidates []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(cacheRoot, entry.Name(), directory+"copilot")
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect bundled CLI candidate: %w", err)
		}
		if info.Mode().IsRegular() {
			candidates = append(candidates, path)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("expected exactly one extracted bundled CLI under %s, found %q", cacheRoot, candidates)
	}
	return candidates[0], nil
}
