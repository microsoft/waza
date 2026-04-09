// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Volume usage example for ADC Go SDK.
//
// This demonstrates the volume workflow:
// 1. Create a volume with labels
// 2. List and retrieve volumes
// 3. Upload and download files in a volume
// 4. Create directories and list files
// 5. Delete files from a volume
// 6. Create a sandbox with a volume mount
// 7. Clean up
//
// Set environment variables:
//
//	ADC_BASE_URL - API endpoint (default: https://management.azuredevcompute.io)
//	ADC_API_KEY  - API key for authentication
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	adc "github.com/coreai-microsoft/adc-sdk-go"
	"github.com/coreai-microsoft/adc-sdk-go/models"
)

func main() {
	fmt.Println("=== ADC SDK Volume Usage Example ===")
	fmt.Println()

	// Get config from environment
	apiKey := os.Getenv("ADC_API_KEY")
	if apiKey == "" {
		log.Fatal("ADC_API_KEY environment variable is required")
	}
	baseURL := os.Getenv("ADC_BASE_URL")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// 1. Configure and create client
	fmt.Println("1. Creating ADC client...")
	if baseURL != "" {
		fmt.Printf("   Endpoint: %s\n", baseURL)
	}
	client := adc.New(adc.Config{
		APIKey:  apiKey,
		APIURL:  baseURL,
		Timeout: 5 * time.Minute,
	})
	defer client.Close()
	fmt.Println("   ✓ Connected")
	fmt.Println()

	// 2. Create a volume (or fetch existing one)
	fmt.Println("2. Creating volume...")
	volume, err := client.Volumes.Create(ctx, "my-data-volume", models.CreateVolumeRequest{
		Type:   models.VolumeTypeAzureBlob,
		Labels: map[string]string{"team": "platform", "purpose": "shared-data"},
	})
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "Conflict") {
			fmt.Println("   Volume already exists, fetching...")
			volume, err = client.Volumes.Get(ctx, "my-data-volume")
			if err != nil {
				log.Fatalf("Failed to get volume: %v", err)
			}
		} else {
			log.Fatalf("Failed to create volume: %v", err)
		}
	}
	fmt.Printf("   ✓ Volume: %s\n", volume.VolumeName)
	fmt.Printf("   ✓ Type: %s\n", volume.Type)
	fmt.Printf("   ✓ Labels: %v\n", volume.Labels)
	if volume.Usage != nil {
		fmt.Printf("   ✓ Usage: %d bytes used, %d items\n", volume.Usage.UsedBytes, volume.Usage.ItemCount)
	}
	fmt.Println()

	// 3. List volumes
	fmt.Println("3. Listing volumes...")
	volumes, err := client.Volumes.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list volumes: %v", err)
	}
	fmt.Printf("   ✓ Found %d volume(s)\n", len(volumes))
	for i, v := range volumes {
		if i >= 5 {
			break
		}
		fmt.Printf("     - %s (%s)\n", v.VolumeName, v.Type)
	}
	fmt.Println()

	// 4. List volumes with label filter
	fmt.Println("4. Filtering volumes by label...")
	filteredVolumes, err := client.Volumes.List(ctx, &adc.ListOptions{
		Labels: map[string]string{"team": "platform"},
	})
	if err != nil {
		log.Fatalf("Failed to filter volumes: %v", err)
	}
	fmt.Printf("   ✓ Found %d volume(s) with label team=platform\n\n", len(filteredVolumes))

	// 5. Get volume by name
	fmt.Println("5. Retrieving volume by name...")
	retrieved, err := client.Volumes.Get(ctx, "my-data-volume")
	if err != nil {
		log.Fatalf("Failed to get volume: %v", err)
	}
	fmt.Printf("   ✓ Retrieved: %s\n\n", retrieved.VolumeName)

	// 6. Create a directory in the volume
	fmt.Println("6. Creating directory in volume...")
	_, err = client.Volumes.CreateDirectory(ctx, "my-data-volume", "data/configs")
	if err != nil {
		log.Fatalf("Failed to create directory: %v", err)
	}
	fmt.Println("   ✓ Created data/configs")
	fmt.Println()

	// 7. Upload files to the volume
	fmt.Println("7. Uploading files...")
	configContent := []byte(`{"setting": "value", "debug": true}`)
	_, err = client.Volumes.UploadFile(ctx, "my-data-volume", "data/configs/app.json", configContent, true)
	if err != nil {
		log.Fatalf("Failed to upload file: %v", err)
	}
	fmt.Println("   ✓ Uploaded data/configs/app.json")

	readmeContent := []byte("# My Project\nShared data volume for the platform team.")
	_, err = client.Volumes.UploadFile(ctx, "my-data-volume", "data/README.md", readmeContent, true)
	if err != nil {
		log.Fatalf("Failed to upload file: %v", err)
	}
	fmt.Println("   ✓ Uploaded data/README.md")
	fmt.Println()

	// 8. List files in volume directory
	fmt.Println("8. Listing files in data/...")
	listing, err := client.Volumes.ListFiles(ctx, "my-data-volume", "data")
	if err != nil {
		log.Fatalf("Failed to list files: %v", err)
	}
	fmt.Printf("   ✓ Found %d item(s) in data/:\n", len(listing.Items))
	for _, item := range listing.Items {
		kind := "file"
		if item.IsDirectory {
			kind = "dir"
		}
		sizeStr := ""
		if item.SizeBytes != nil {
			sizeStr = fmt.Sprintf(", %d bytes", *item.SizeBytes)
		}
		fmt.Printf("     - %s (%s%s)\n", item.ItemName, kind, sizeStr)
	}
	fmt.Println()

	// 9. Download a file from the volume
	fmt.Println("9. Downloading file...")
	downloaded, err := client.Volumes.DownloadFile(ctx, "my-data-volume", "data/configs/app.json")
	if err != nil {
		log.Fatalf("Failed to download file: %v", err)
	}
	fmt.Printf("   ✓ Content of data/configs/app.json: %s\n\n", string(downloaded))

	// 10. Create a sandbox with the volume mounted (using public ubuntu image)
	fmt.Println("10. Creating sandbox with volume mount...")
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{
			Name:     "ubuntu",
			IsPublic: true,
		},
		CPU:        "1000m",
		Memory:     "1024Mi",
		Labels:     map[string]string{"name": "volume-sandbox"},
		Entrypoint: []string{"/bin/sleep"},
		Cmd:        []string{"infinity"},
		Volumes: []models.SandboxVolume{
			{
				VolumeName: "my-data-volume",
				Mountpoint: "/mnt/data",
				ReadOnly:   false,
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox ID: %s\n", sandbox.ID())
	fmt.Printf("   ✓ State: %s\n", sandbox.State())
	fmt.Println("   ✓ Volume 'my-data-volume' mounted at /mnt/data")
	fmt.Println()

	// 11. Verify volume is accessible from the sandbox
	fmt.Println("11. Verifying volume access from sandbox...")
	lsResult, err := sandbox.ExecuteShellCommand(ctx, "ls -la /mnt/data/data", "", nil, "")
	if err != nil {
		log.Fatalf("Failed to execute command: %v", err)
	}
	fmt.Printf("   ✓ Exit Code: %d\n", lsResult.ExitCode)
	fmt.Printf("   ✓ Volume contents visible from sandbox:\n     %s\n\n",
		strings.ReplaceAll(lsResult.Stdout, "\n", "\n     "))

	// 12. Delete files from the volume
	fmt.Println("12. Deleting files from volume...")
	err = client.Volumes.DeleteFile(ctx, "my-data-volume", "data/README.md", false)
	if err != nil {
		log.Fatalf("Failed to delete file: %v", err)
	}
	fmt.Println("   ✓ Deleted data/README.md")

	err = client.Volumes.DeleteFile(ctx, "my-data-volume", "data/configs", true)
	if err != nil {
		log.Fatalf("Failed to delete directory: %v", err)
	}
	fmt.Println("   ✓ Deleted data/configs (recursive)")
	fmt.Println()

	// 13. Clean up
	fmt.Println("13. Cleaning up...")
	err = sandbox.Delete(ctx)
	if err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   ✓ Sandbox deleted")

	err = client.Volumes.Delete(ctx, "my-data-volume")
	if err != nil {
		log.Fatalf("Failed to delete volume: %v", err)
	}
	fmt.Println("   ✓ Volume deleted")
	fmt.Println()

	fmt.Println("=== Volume example completed successfully! ===")
}
