// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// App usage example for ADC Go SDK.
//
// This demonstrates the app workflow:
// 1. Create an app with a container image
// 2. Add and manage ports
// 3. Stop and resume the app
// 4. Update the app
// 5. Clean up
//
// Apps differ from sandboxes:
// - Apps use container images directly (no disk image creation needed)
// - Apps have persistent identity (stop/resume preserves state via snapshots)
// - Apps are ideal for long-running development environments
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
	fmt.Println("=== ADC SDK App Usage Example ===")
	fmt.Println()

	// Get API key from environment
	apiKey := os.Getenv("ADC_API_KEY")
	if apiKey == "" {
		log.Fatal("ADC_API_KEY environment variable is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. Configure and create client
	fmt.Println("1. Creating ADC client...")
	client := adc.New(adc.Config{
		APIKey:  apiKey,
		Timeout: 5 * time.Minute,
	})
	defer client.Close()
	fmt.Println("   ✓ Connected")
	fmt.Println()

	// 2. Create an app with ports
	fmt.Println("2. Creating app from container image...")
	app, err := client.Apps.Create(ctx, models.AppRequest{
		ContainerImage: "mcr.microsoft.com/devcontainers/base:ubuntu",
		Resources: &models.AppResources{
			CPU:    "1000m",
			Memory: "1024Mi",
		},
		Name: "my-dev-environment",
		Labels: map[string]string{
			"team":    "platform",
			"purpose": "development",
		},
		Ports: []models.AddAppPortRequest{
			{Port: 8080},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create app: %v", err)
	}
	fmt.Printf("   ✓ App ID: %s\n", app.ID())
	fmt.Printf("   ✓ Name: %s\n", app.Name())
	fmt.Printf("   ✓ Container Image: %s\n", app.ContainerImage())
	fmt.Printf("   ✓ State: %s\n", app.State())
	fmt.Printf("   ✓ App URL: %s\n", app.AppURL())
	fmt.Printf("   ✓ Ports: %d\n", len(app.Ports()))
	for _, p := range app.Ports() {
		fmt.Printf("     - Port %d\n", p.Port)
	}
	fmt.Println()

	// 3. List apps
	fmt.Println("3. Listing apps...")
	apps, err := client.Apps.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list apps: %v", err)
	}
	fmt.Printf("   ✓ Found %d app(s)\n", len(apps))
	for i, a := range apps {
		if i >= 3 {
			break
		}
		name := a.Name()
		if name == "" {
			name = a.ID()
		}
		fmt.Printf("     - %s: %s (%s)\n", name, a.State(), a.ContainerImage())
	}
	fmt.Println()

	// 4. Get app by ID
	fmt.Println("4. Retrieving app by ID...")
	retrievedApp, err := client.Apps.Get(ctx, app.ID())
	if err != nil {
		log.Fatalf("Failed to get app: %v", err)
	}
	fmt.Printf("   ✓ Retrieved: %s (%s)\n\n", retrievedApp.Name(), retrievedApp.ID())

	// 5. Count apps
	fmt.Println("5. Counting apps...")
	count, err := client.Apps.Count(ctx)
	if err != nil {
		log.Fatalf("Failed to count apps: %v", err)
	}
	fmt.Printf("   ✓ Total apps: %d\n\n", count)

	// 6. Add another port
	fmt.Println("6. Adding port to app...")
	_, err = app.AddPort(ctx, 9000, "")
	if err != nil {
		log.Fatalf("Failed to add port: %v", err)
	}
	fmt.Println("   ✓ Added port 9000")

	// 7. List current ports
	fmt.Println("7. Listing app ports...")
	ports, err := app.ListPorts(ctx)
	if err != nil {
		log.Fatalf("Failed to list ports: %v", err)
	}
	fmt.Printf("   ✓ Current ports: %d\n", len(ports))
	for _, p := range ports {
		fmt.Printf("     - Port %d\n", p.Port)
	}
	fmt.Println()

	// 8. Remove a port
	fmt.Println("8. Removing port from app...")
	if err := app.RemovePort(ctx, 9000); err != nil {
		log.Fatalf("Failed to remove port: %v", err)
	}
	fmt.Println("   ✓ Removed port 9000")
	remainingPorts, _ := app.ListPorts(ctx)
	fmt.Printf("   ✓ Remaining ports: %d\n\n", len(remainingPorts))

	// 9. Stop app (creates snapshot, preserves state)
	fmt.Println("9. Stopping app...")
	if err := app.Stop(ctx); err != nil {
		log.Fatalf("Failed to stop app: %v", err)
	}
	fmt.Println("   ✓ App stopped")
	fmt.Printf("   ✓ State: %s\n\n", app.State())

	// 10. Resume app (restores from snapshot)
	fmt.Println("10. Resuming app...")
	if err := app.Resume(ctx); err != nil {
		log.Fatalf("Failed to resume app: %v", err)
	}
	fmt.Println("   ✓ App resumed")
	fmt.Printf("   ✓ State: %s\n\n", app.State())

	// 11. Update app (change resources)
	fmt.Println("11. Updating app...")
	updatedApp, err := client.Apps.Update(ctx, app.ID(), models.AppRequest{
		ContainerImage: "mcr.microsoft.com/devcontainers/base:ubuntu",
		Resources: &models.AppResources{
			CPU:    "2000m",
			Memory: "2048Mi",
		},
		Name: "my-dev-environment",
		Labels: map[string]string{
			"team":    "platform",
			"purpose": "development",
			"updated": "true",
		},
	})
	if err != nil {
		log.Fatalf("Failed to update app: %v", err)
	}
	fmt.Println("   ✓ App updated")
	if updatedApp.Resources() != nil {
		fmt.Printf("   ✓ CPU: %s\n", updatedApp.Resources().CPU)
		fmt.Printf("   ✓ Memory: %s\n\n", updatedApp.Resources().Memory)
	}

	// 12. Set egress policy
	fmt.Println("12. Setting egress policy...")
	if err := app.SetEgressPolicy(ctx, models.AppEgressPolicy{
		BlockByDefault: true,
		AllowedHosts:   []string{"github.com", "*.githubusercontent.com", "api.nuget.org"},
	}); err != nil {
		log.Fatalf("Failed to set egress policy: %v", err)
	}
	fmt.Println("   ✓ Egress policy set (allow GitHub, NuGet)")
	fmt.Println()

	// 13. Clean up
	fmt.Println("13. Cleaning up...")
	if err := app.Delete(ctx); err != nil {
		log.Fatalf("Failed to delete app: %v", err)
	}
	fmt.Println("   ✓ App deleted")
	fmt.Println()

	fmt.Println("=== App example completed successfully! ===")
}
