// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Sandbox Group usage example for ADC Go SDK.
//
// This demonstrates sandbox group workflow:
// 1. Create a sandbox group
// 2. List sandbox groups
// 3. Create a sandbox within the group
// 4. Get and list sandboxes within the group
// 5. Execute commands in a grouped sandbox
// 6. Clean up
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	adc "github.com/coreai-microsoft/adc-sdk-go"
	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func main() {
	fmt.Println("=== ADC SDK Sandbox Group Example ===")
	fmt.Println()

	// Get API key from environment
	apiKey := os.Getenv("ADC_API_KEY")
	if apiKey == "" {
		log.Fatal("ADC_API_KEY environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. Create client
	fmt.Println("1. Creating ADC client...")
	client := adc.New(adc.Config{APIKey: apiKey})
	fmt.Println("   ✓ Connected")
	fmt.Println()

	// 2. Create a disk image
	fmt.Println("2. Creating disk image...")
	diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
		BaseImage: "ubuntu:22.04",
		Labels:    map[string]string{"purpose": "sandbox-group-example"},
	})
	if err != nil {
		log.Fatalf("Failed to create disk image: %v", err)
	}
	fmt.Printf("   ✓ Disk image created: %s\n\n", diskImage.ID)

	// 3. Create a sandbox group
	fmt.Println("3. Creating sandbox group...")
	group, err := client.SandboxGroups.Create(ctx, models.CreateSandboxGroupRequest{
		Labels:           map[string]string{"team": "platform", "env": "dev"},
		AllowedLocations: []string{"westus2"},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox group: %v", err)
	}
	fmt.Printf("   ✓ Sandbox group created: %s\n", group.ID)
	fmt.Printf("   Labels: %v\n\n", group.Labels)

	// 4. List sandbox groups
	fmt.Println("4. Listing sandbox groups...")
	groups, err := client.SandboxGroups.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list sandbox groups: %v", err)
	}
	fmt.Printf("   ✓ Found %d sandbox group(s)\n\n", len(groups))

	// 5. Get sandbox group details
	fmt.Println("5. Getting sandbox group details...")
	groupDetails, err := client.SandboxGroups.Get(ctx, group.ID)
	if err != nil {
		log.Fatalf("Failed to get sandbox group: %v", err)
	}
	fmt.Printf("   ✓ Group ID: %s\n", groupDetails.ID)
	fmt.Printf("   Allowed locations: %v\n\n", groupDetails.AllowedLocations)

	// 6. Create a sandbox within the group
	fmt.Println("6. Creating sandbox in group...")
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage:      models.SandboxSourceDiskImage{ID: diskImage.ID},
		Labels:         map[string]string{"app": "web-server"},
		SandboxGroupID: group.ID,
		CPU:            "1",
		Memory:         "1024Mi",
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox created: %s\n", sandbox.ID())
	fmt.Printf("   Group ID: %s\n", sandbox.Data.SandboxGroupID)
	fmt.Printf("   State: %s\n\n", sandbox.State())

	// 7. List sandboxes in the group
	fmt.Println("7. Listing sandboxes in group...")
	sandboxes, err := client.SandboxGroups.ListSandboxes(ctx, group.ID, nil)
	if err != nil {
		log.Fatalf("Failed to list sandboxes in group: %v", err)
	}
	fmt.Printf("   ✓ Found %d sandbox(es) in group\n\n", len(sandboxes))

	// 8. Get a specific sandbox from the group
	fmt.Println("8. Getting sandbox from group...")
	retrieved, err := client.SandboxGroups.GetSandbox(ctx, group.ID, sandbox.ID())
	if err != nil {
		log.Fatalf("Failed to get sandbox from group: %v", err)
	}
	fmt.Printf("   ✓ Retrieved sandbox: %s\n", retrieved.ID())
	fmt.Printf("   State: %s\n\n", retrieved.State())

	// 9. Execute a command in the grouped sandbox
	fmt.Println("9. Executing command in grouped sandbox...")
	result, err := sandbox.ExecuteShellCommand(ctx, `echo "Hello from sandbox group!"`, "", nil, "")
	if err != nil {
		log.Fatalf("Failed to execute command: %v", err)
	}
	fmt.Printf("   ✓ Exit code: %d\n", result.ExitCode)
	fmt.Printf("   stdout: %s\n\n", result.Stdout)

	// 10. File operations work through group-scoped paths
	fmt.Println("10. Writing file to grouped sandbox...")
	_, err = sandbox.WriteFile(ctx, "/tmp/group-test.txt", []byte("Created in sandbox group"), nil)
	if err != nil {
		log.Fatalf("Failed to write file: %v", err)
	}
	content, err := sandbox.ReadFile(ctx, "/tmp/group-test.txt")
	if err != nil {
		log.Fatalf("Failed to read file: %v", err)
	}
	fmt.Printf("   ✓ File content: %s\n\n", string(content))

	// 11. Clean up
	fmt.Println("11. Cleaning up...")
	if err := sandbox.Delete(ctx); err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   ✓ Sandbox deleted")
	if err := client.SandboxGroups.Delete(ctx, group.ID); err != nil {
		log.Fatalf("Failed to delete sandbox group: %v", err)
	}
	fmt.Println("   ✓ Sandbox group deleted")
	if err := client.DiskImages.Delete(ctx, diskImage.ID); err != nil {
		log.Fatalf("Failed to delete disk image: %v", err)
	}
	fmt.Println("   ✓ Disk image deleted")
	fmt.Println()
	fmt.Println("=== Example complete! ===")
}
