// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Public image example for ADC Go SDK.
//
// This demonstrates creating a sandbox from a public disk image
// without needing to create your own disk image first.
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
	fmt.Println("=== ADC SDK Public Image Example ===")
	fmt.Println()

	// Get API key from environment
	apiKey := os.Getenv("ADC_API_KEY")
	if apiKey == "" {
		log.Fatal("ADC_API_KEY environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 1. Create client
	fmt.Println("1. Creating ADC client...")
	client := adc.New(adc.Config{
		APIKey: apiKey,
	})
	defer client.Close()
	fmt.Println("   ✓ Connected")
	fmt.Println()

	// 2. List available public images
	fmt.Println("2. Listing available public images...")
	publicImages, err := client.DiskImages.ListPublic(ctx)
	if err != nil {
		log.Fatalf("Failed to list public images: %v", err)
	}
	fmt.Printf("   ✓ Found %d public image(s):\n", len(publicImages))
	for _, img := range publicImages {
		fmt.Printf("     - %s (%s)\n", img.Name, img.Status.State)
	}
	fmt.Println()

	// 3. Create sandbox from public Ubuntu image
	fmt.Println("3. Creating sandbox from public Ubuntu image...")
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{
			Name:     "ubuntu",
			IsPublic: true,
		},
		CPU:    "1000m",
		Memory: "1024Mi",
		Labels: map[string]string{
			"name": "ubuntu-sandbox",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox ID: %s\n", sandbox.ID())
	fmt.Printf("   ✓ State: %s\n\n", sandbox.State())

	// 4. Install python3
	fmt.Println("4. Installing Python 3...")
	result, err := sandbox.ExecuteShellCommand(ctx, "sudo apt-get update && sudo apt-get install -y python3", "", nil, "")
	if err != nil {
		log.Fatalf("Failed to install Python 3: %v", err)
	}
	fmt.Printf("   ✓ %s\n", result.Stdout)

	// 5. Run a Python script
	fmt.Println("5. Running a Python script...")
	script := `
import sys
print(f"Python {sys.version}")
print("Hello from ADC sandbox!")
for i in range(5):
    print(f"  Count: {i + 1}")
`
	result, err = sandbox.ExecuteShellCommand(ctx, fmt.Sprintf("python3 -c '%s'", script), "", nil, "")
	if err != nil {
		log.Fatalf("Failed to execute Python script: %v", err)
	}
	fmt.Printf("   ✓ Output:\n%s\n", result.Stdout)

	// 6. Clean up
	fmt.Println("6. Cleaning up...")
	err = sandbox.Delete(ctx)
	if err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   ✓ Sandbox deleted")
	fmt.Println()

	fmt.Println("=== Example completed successfully! ===")
}
