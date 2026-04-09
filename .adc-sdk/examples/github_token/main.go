// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// GitHub token authentication example for ADC Go SDK.
//
// This demonstrates how to authenticate using a GitHub PAT directly,
// without going through the Device Flow login. The SDK sends the token
// via the "Authorization: GitHub {token}" header.
//
// Requirements:
//   - A GitHub PAT (ghp_...) or OAuth token (gho_...)
//   - The GitHub account must have a verified @microsoft.com or @github.com email
//   - Set the GITHUB_TOKEN environment variable before running
//
// Usage:
//
//	GITHUB_TOKEN=ghp_yourtoken go run examples/github_token/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	adc "github.com/coreai-microsoft/adc-sdk-go"
)

func main() {
	githubToken := os.Getenv("GITHUB_TOKEN")
	if githubToken == "" {
		fmt.Println("Error: GITHUB_TOKEN environment variable is required.")
		fmt.Println("Usage: GITHUB_TOKEN=ghp_yourtoken go run examples/github_token/main.go")
		os.Exit(1)
	}

	fmt.Println("=== ADC SDK GitHub Token Auth Example ===")
	fmt.Println()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Create client with GitHub token
	client := adc.New(adc.Config{
		GitHubToken: githubToken,
	})
	defer client.Close()
	fmt.Println("1. Created ADC client with GitHub token auth")
	fmt.Println()

	// List disk images
	fmt.Println("2. Listing disk images...")
	diskImages, err := client.DiskImages.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list disk images: %v", err)
	}
	fmt.Printf("   Found %d disk image(s)\n", len(diskImages))
	for i, img := range diskImages {
		if i >= 3 {
			break
		}
		fmt.Printf("   - %v (%s)\n", img.Labels, img.Status.State)
	}
	fmt.Println()

	// List sandboxes
	fmt.Println("3. Listing sandboxes...")
	sandboxes, err := client.Sandboxes.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list sandboxes: %v", err)
	}
	fmt.Printf("   Found %d sandbox(es)\n", len(sandboxes))
	for i, sb := range sandboxes {
		if i >= 3 {
			break
		}
		fmt.Printf("   - %s: %s\n", sb.ID(), sb.State())
	}

	fmt.Println()
	fmt.Println("=== Example completed successfully! ===")
}
