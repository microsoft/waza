// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// GitHub authentication example for ADC Go SDK.
//
// This demonstrates how to use GitHub Device Flow authentication
// instead of API keys for interactive CLI tools.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	adc "github.com/coreai-microsoft/adc-sdk-go"
	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func main() {
	fmt.Println("=== ADC SDK GitHub Authentication Example ===")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 1. Create client without API key
	fmt.Println("1. Creating ADC client (without API key)...")
	client := adc.New(adc.Config{
		// No API key - we'll authenticate with GitHub
	})
	defer client.Close()
	fmt.Println("   Client created (not authenticated yet)")
	fmt.Println()

	// 2. Authenticate with GitHub Device Flow
	fmt.Println("2. Initiating GitHub Device Flow authentication...")
	fmt.Println("   This will display instructions for browser-based authentication.")
	fmt.Println()

	// Option A: Use default callback (prints to stdout)
	err := client.Login(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to authenticate: %v", err)
	}

	// Option B: Use custom callback for more control over the UI
	// err := client.Login(ctx, &adc.LoginOptions{
	// 	Callback: func(userCode, verificationURI string) {
	// 		fmt.Printf("\n")
	// 		fmt.Printf("Please visit: %s\n", verificationURI)
	// 		fmt.Printf("Enter code: %s\n", userCode)
	// 		fmt.Printf("\n")
	// 	},
	// })

	// Option C: Use a custom GitHub OAuth App client ID
	// err := client.Login(ctx, &adc.LoginOptions{
	// 	ClientID: "your-custom-client-id",
	// })

	fmt.Println("   Authentication successful!")
	fmt.Println()

	// 3. Now we can use the API
	fmt.Println("3. Listing disk images (authenticated)...")
	diskImages, err := client.DiskImages.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list disk images: %v", err)
	}
	fmt.Printf("   Found %d disk image(s)\n", len(diskImages))
	for i, img := range diskImages {
		if i >= 3 {
			fmt.Printf("   ... and %d more\n", len(diskImages)-3)
			break
		}
		fmt.Printf("   - %v (%s)\n", img.Labels, img.Status.State)
	}
	fmt.Println()

	// 4. Create a quick sandbox to verify everything works
	fmt.Println("4. Creating a quick test sandbox...")
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{
			Name:     "ubuntu",
			IsPublic: true,
		},
		CPU:    "1000m",
		Memory: "1024Mi",
		Labels: map[string]string{
			"name":   "github-auth-test",
			"source": "github-auth-example",
		},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox: %v", err)
	}
	fmt.Printf("   Sandbox ID: %s\n", sandbox.ID())
	fmt.Printf("   State: %s\n\n", sandbox.State())

	// 5. Execute a command
	fmt.Println("5. Executing command in sandbox...")
	result, err := sandbox.ExecuteShellCommand(ctx, "echo 'Hello from GitHub-authenticated sandbox!'", "", nil, "")
	if err != nil {
		log.Fatalf("Failed to execute command: %v", err)
	}
	fmt.Printf("   Output: %s\n", result.Stdout)

	// 6. Clean up
	fmt.Println("6. Cleaning up...")
	err = sandbox.Delete(ctx)
	if err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   Sandbox deleted")
	fmt.Println()

	fmt.Println("=== Example completed successfully! ===")
	fmt.Println("\nNote: The GitHub token is stored in memory only.")
	fmt.Println("You'll need to re-authenticate in the next session.")
}
