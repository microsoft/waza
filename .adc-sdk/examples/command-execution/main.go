// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Example: Command execution in ADC sandbox (HTTP and WebSocket streaming)
//
// Demonstrates HTTP and WebSocket streaming command execution.
//
// Usage:
//
//	go run main.go <apiUrl> <apiKey>
package main

import (
	"context"
	"fmt"
	"os"

	adc "github.com/coreai-microsoft/adc-sdk-go"
	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run main.go <apiUrl> <apiKey>")
		os.Exit(1)
	}

	apiURL := os.Args[1]
	apiKey := os.Args[2]

	fmt.Printf("Using API: %s\n", apiURL)

	client := adc.New(adc.Config{
		APIURL: apiURL,
		APIKey: apiKey,
	})
	defer client.Close()

	ctx := context.Background()

	// Create a disk image first, then create sandbox from it
	fmt.Println("Creating disk image from ubuntu...")
	diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
		Labels:    map[string]string{"name": "command-exec-example"},
		BaseImage: "mcr.microsoft.com/devcontainers/base:ubuntu",
	})
	if err != nil {
		fmt.Printf("Failed to create disk image: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Disk image created: %s\n", diskImage.ID)

	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{ID: diskImage.ID},
	})
	if err != nil {
		fmt.Printf("Failed to create sandbox: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Created sandbox: %s\n", sandbox.ID())

	defer func() {
		if err := sandbox.Delete(ctx); err != nil {
			fmt.Printf("Failed to delete sandbox: %v\n", err)
		} else {
			fmt.Println("\nSandbox deleted")
		}
		if err := client.DiskImages.Delete(ctx, diskImage.ID); err != nil {
			fmt.Printf("Failed to delete disk image: %v\n", err)
		} else {
			fmt.Println("Disk image deleted")
		}
	}()

	// ==================== Test 1: ExecuteCommand (HTTP) ====================
	fmt.Println("\n--- Test 1: ExecuteCommand (HTTP) ---")
	fmt.Println("Getting server time with single HTTP request...")
	fmt.Println()

	result, err := sandbox.ExecuteCommand(ctx, "date", []string{"+%Y-%m-%d %H:%M:%S.%3N %Z"}, nil, "")
	if err != nil {
		fmt.Printf("ExecuteCommand failed: %v\n", err)
	} else {
		fmt.Printf("Server time: %s", result.Stdout)
		fmt.Printf("Exit code: %d\n", result.ExitCode)
	}

	// ==================== Test 2: StartExecStream (WebSocket) ====================
	fmt.Println("\n--- Test 2: StartExecStream (Channel-based) ---")
	fmt.Println("Streaming server time every second...")
	fmt.Println()

	session, err := sandbox.StartExecStream(ctx, &models.ExecStreamStartRequest{
		Command: "bash",
		Args: []string{"-c", `
			echo "Server timezone: $(cat /etc/timezone 2>/dev/null || echo 'UTC')"
			echo "Server uptime: $(uptime -p 2>/dev/null || uptime)"
			echo ""
			echo "Real-time clock:"
			for i in 1 2 3 4 5; do
				echo "  [$(date +%H:%M:%S.%3N)] Tick $i"
				sleep 1
			done
		`},
	})
	if err != nil {
		fmt.Printf("StartExecStream failed: %v\n", err)
	} else {
		fmt.Printf("Session ID: %s\n", session.SessionID())
		for msg := range session.ReadOutput() {
			switch msg.Type {
			case models.ExecStreamMessageTypeStdout:
				fmt.Print(string(msg.Data))
			case models.ExecStreamMessageTypeStderr:
				fmt.Fprintf(os.Stderr, "[stderr] %s", string(msg.Data))
			case models.ExecStreamMessageTypeExitCode:
				fmt.Printf("\nExit code: %d\n", *msg.ExitCode)
			case models.ExecStreamMessageTypeError:
				fmt.Printf("[error] %s\n", msg.Error)
			}
		}
		session.Close()
	}
}
