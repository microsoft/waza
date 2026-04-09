// Copyright (c) Microsoft Corporation. All rights reserved.
// Licensed under the MIT License.

// Basic usage example for ADC Go SDK.
//
// This demonstrates the full workflow:
// 1. Create a disk image
// 2. Create a sandbox from the disk image
// 3. Execute commands in the sandbox
// 4. Create a snapshot
// 5. Add and remove port mappings
// 6. File operations (read, write, list, stat, delete)
// 7. Stop and start the sandbox
// 8. Clean up
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
	fmt.Println("=== ADC SDK Basic Usage Example ===")
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

	// 2. Create a disk image
	fmt.Println("2. Creating disk image...")
	diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
		Labels:    map[string]string{"name": "hello-ubuntu", "version": "latest"},
		BaseImage: "docker.io/library/ubuntu:latest",
	})
	if err != nil {
		log.Fatalf("Failed to create disk image: %v", err)
	}
	fmt.Printf("   ✓ Disk Image ID: %s\n", diskImage.ID)
	fmt.Printf("   ✓ Labels: %v\n", diskImage.Labels)
	fmt.Printf("   ✓ Base Image: %s\n", diskImage.Image.Base)
	fmt.Printf("   ✓ Status: %s\n\n", diskImage.Status.State)

	// 3. List disk images
	fmt.Println("3. Listing disk images...")
	diskImages, err := client.DiskImages.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list disk images: %v", err)
	}
	fmt.Printf("   ✓ Found %d disk image(s)\n", len(diskImages))
	for i, img := range diskImages {
		if i >= 3 {
			break
		}
		fmt.Printf("     - %v (%s): %s\n", img.Labels, img.ID, img.Status.State)
	}
	fmt.Println()

	// 3b. Filter disk images by label
	fmt.Println("3b. Filtering disk images by label...")
	filteredImages, err := client.DiskImages.List(ctx, &adc.ListOptions{
		Labels: map[string]string{"name": "hello-ubuntu"},
	})
	if err != nil {
		log.Fatalf("Failed to filter disk images: %v", err)
	}
	fmt.Printf("   ✓ Found %d disk image(s) with label name=hello-ubuntu\n\n", len(filteredImages))

	// 4. Get disk image by ID
	fmt.Println("4. Retrieving disk image by ID...")
	retrievedImage, err := client.DiskImages.Get(ctx, diskImage.ID)
	if err != nil {
		log.Fatalf("Failed to get disk image: %v", err)
	}
	fmt.Printf("   ✓ Retrieved: %v\n\n", retrievedImage.Labels)

	// 5. Create a sandbox from the disk image
	fmt.Println("5. Creating sandbox from disk image...")
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage: models.SandboxSourceDiskImage{ID: diskImage.ID},
		CPU:       "1000m",
		Memory:    "1024Mi",
		Labels: map[string]string{
			"name":        "my-ubuntu-sandbox",
			"environment": "development",
		},
		Entrypoint: []string{"/bin/sleep"},
		Cmd:        []string{"infinity"},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox ID: %s\n", sandbox.ID())
	fmt.Printf("   ✓ VMM Type: %s\n", sandbox.VmmType())
	fmt.Printf("   ✓ Labels: %v\n", sandbox.Labels())
	fmt.Printf("   ✓ State: %s\n\n", sandbox.State())

	// 6. Get sandbox by ID
	fmt.Println("6. Retrieving sandbox by ID...")
	retrievedSandbox, err := client.Sandboxes.Get(ctx, sandbox.ID())
	if err != nil {
		log.Fatalf("Failed to get sandbox: %v", err)
	}
	fmt.Printf("   ✓ Retrieved: %s\n\n", retrievedSandbox.ID())

	// 7. Count sandboxes
	fmt.Println("7. Counting sandboxes...")
	count, err := client.Sandboxes.Count(ctx)
	if err != nil {
		log.Fatalf("Failed to count sandboxes: %v", err)
	}
	fmt.Printf("   ✓ Total sandboxes: %d\n\n", count)

	// 8. Execute command in sandbox
	fmt.Println("8. Executing command in sandbox...")
	commandResult, err := sandbox.ExecuteCommand(ctx, "ps", []string{"aux"}, nil, "")
	if err != nil {
		log.Fatalf("Failed to execute command: %v", err)
	}
	fmt.Printf("   ✓ Exit Code: %d\n", commandResult.ExitCode)
	output := commandResult.Stdout
	if len(output) > 200 {
		output = output[:200] + "..."
	}
	fmt.Printf("   ✓ Output (first 200 chars):\n     %s\n\n", output)

	// 9. Execute shell command in sandbox
	fmt.Println("9. Executing shell command in sandbox...")
	shellResult, err := sandbox.ExecuteShellCommand(ctx, "ls -alh", "", map[string]string{"MY_VAR": "my_value"}, "")
	if err != nil {
		log.Fatalf("Failed to execute shell command: %v", err)
	}
	fmt.Printf("   ✓ Exit Code: %d\n", shellResult.ExitCode)
	output = shellResult.Stdout
	if len(output) > 200 {
		output = output[:200] + "..."
	}
	fmt.Printf("   ✓ Output:\n     %s\n\n", output)

	// 10. Create a snapshot
	fmt.Println("10. Creating snapshot from sandbox...")
	snapshot, err := sandbox.Snapshot(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to create snapshot: %v", err)
	}
	fmt.Printf("   ✓ Snapshot ID: %s\n\n", snapshot.ID)

	// 11. List snapshots
	fmt.Println("11. Listing snapshots...")
	snapshots, err := client.Snapshots.List(ctx, nil)
	if err != nil {
		log.Fatalf("Failed to list snapshots: %v", err)
	}
	fmt.Printf("   ✓ Found %d snapshot(s)\n\n", len(snapshots))

	// 12. Get snapshot by ID
	fmt.Println("12. Retrieving snapshot by ID...")
	retrievedSnapshot, err := client.Snapshots.Get(ctx, snapshot.ID)
	if err != nil {
		log.Fatalf("Failed to get snapshot: %v", err)
	}
	fmt.Printf("   ✓ Retrieved: %s\n\n", retrievedSnapshot.ID)

	// 13. Add port mappings
	fmt.Println("13. Adding port mappings...")
	webPort, err := sandbox.AddPort(ctx, 8080, &models.AddPortOptions{Name: "web"})
	if err != nil {
		log.Fatalf("Failed to add port: %v", err)
	}
	fmt.Println("   ✓ Added port 8080 with name 'web'")
	fmt.Printf("   ✓ URL: %s\n", webPort.URL)

	apiPort, err := sandbox.AddPort(ctx, 3000, nil)
	if err != nil {
		log.Fatalf("Failed to add port: %v", err)
	}
	fmt.Println("   ✓ Added port 3000 (unnamed)")
	fmt.Printf("   ✓ URL: %s\n\n", apiPort.URL)

	// 14. List current ports
	fmt.Println("14. Listing sandbox ports...")
	fmt.Printf("   ✓ Current ports: %d\n", len(sandbox.Ports()))
	for _, p := range sandbox.Ports() {
		nameStr := ""
		if p.Name != "" {
			nameStr = fmt.Sprintf(" (%s)", p.Name)
		}
		fmt.Printf("     - Port %d%s: %s\n", p.Port, nameStr, p.URL)
	}
	fmt.Println()

	// 15. Remove port mappings
	fmt.Println("15. Removing port mappings...")
	err = sandbox.RemovePort(ctx, models.RemovePortRequest{Name: "web"})
	if err != nil {
		log.Fatalf("Failed to remove port: %v", err)
	}
	fmt.Println("   ✓ Removed port by name 'web'")

	err = sandbox.RemovePort(ctx, models.RemovePortRequest{Port: 3000})
	if err != nil {
		log.Fatalf("Failed to remove port: %v", err)
	}
	fmt.Println("   ✓ Removed port 3000 by number")
	fmt.Printf("   ✓ Remaining ports: %d\n\n", len(sandbox.Ports()))

	// 16. File operations - Write files
	fmt.Println("16. Writing files to sandbox...")
	_, err = sandbox.WriteFileText(ctx, "/tmp/hello.txt", "Hello, World!", nil)
	if err != nil {
		log.Fatalf("Failed to write file: %v", err)
	}
	fmt.Println("   ✓ Created /tmp/hello.txt")

	_, err = sandbox.WriteFileText(ctx, "/tmp/scripts/setup.sh", "#!/bin/bash\necho \"Setup complete\"", &models.WriteFileOptions{
		CreateDirs: true,
		Mode:       0755,
	})
	if err != nil {
		log.Fatalf("Failed to write file: %v", err)
	}
	fmt.Println("   ✓ Created /tmp/scripts/setup.sh with execute permissions")
	fmt.Println()

	// 17. File operations - Read files
	fmt.Println("17. Reading files from sandbox...")
	content, err := sandbox.ReadFileText(ctx, "/tmp/hello.txt")
	if err != nil {
		log.Fatalf("Failed to read file: %v", err)
	}
	fmt.Printf("   ✓ Content of /tmp/hello.txt: %s\n\n", content)

	// 18. File operations - List directory
	fmt.Println("18. Listing directory contents...")
	files, err := sandbox.ListFiles(ctx, "/tmp")
	if err != nil {
		log.Fatalf("Failed to list files: %v", err)
	}
	fmt.Printf("   ✓ Found %d entries in /tmp:\n", len(files.Entries))
	for i, f := range files.Entries {
		if i >= 5 {
			break
		}
		fileType := "file"
		if f.IsDir {
			fileType = "dir"
		}
		fmt.Printf("     - %s (%s): %d bytes\n", f.Name, fileType, f.Size)
	}
	fmt.Println()

	// 19. File operations - Get file stat
	fmt.Println("19. Getting file metadata...")
	stat, err := sandbox.StatFile(ctx, "/tmp/hello.txt")
	if err != nil {
		log.Fatalf("Failed to stat file: %v", err)
	}
	fmt.Printf("   ✓ Name: %s\n", stat.Name)
	fmt.Printf("   ✓ Size: %d bytes\n", stat.Size)
	fmt.Printf("   ✓ Mode: %o\n", stat.Mode)
	fmt.Printf("   ✓ Is directory: %v\n\n", stat.IsDir)

	// 20. File operations - Create directory
	fmt.Println("20. Creating directory...")
	_, err = sandbox.Mkdir(ctx, "/tmp/new-folder", nil)
	if err != nil {
		log.Fatalf("Failed to create directory: %v", err)
	}
	fmt.Println("   ✓ Created /tmp/new-folder")
	fmt.Println()

	// 21. File operations - Delete files
	fmt.Println("21. Deleting files and directories...")
	_, err = sandbox.DeleteFile(ctx, "/tmp/hello.txt", false)
	if err != nil {
		log.Fatalf("Failed to delete file: %v", err)
	}
	fmt.Println("   ✓ Deleted /tmp/hello.txt")

	_, err = sandbox.DeleteFile(ctx, "/tmp/new-folder", true)
	if err != nil {
		log.Fatalf("Failed to delete directory: %v", err)
	}
	fmt.Println("   ✓ Deleted /tmp/new-folder")

	_, err = sandbox.DeleteFile(ctx, "/tmp/scripts", true)
	if err != nil {
		log.Fatalf("Failed to delete directory: %v", err)
	}
	fmt.Println("   ✓ Deleted /tmp/scripts")
	fmt.Println()

	// 22. Stop sandbox
	fmt.Println("22. Stopping sandbox...")
	err = sandbox.Stop(ctx)
	if err != nil {
		log.Fatalf("Failed to stop sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox stopped, state: %s\n\n", sandbox.State())

	// 23. Resume sandbox
	fmt.Println("23. Resuming sandbox...")
	err = sandbox.Resume(ctx)
	if err != nil {
		log.Fatalf("Failed to resume sandbox: %v", err)
	}
	fmt.Printf("   ✓ Sandbox resumed, state: %s\n\n", sandbox.State())

	// 24. Create sandbox from snapshot
	fmt.Println("24. Creating sandbox from snapshot...")
	sandboxFromSnapshot, err := client.Sandboxes.CreateFromSnapshot(ctx, models.CreateFromSnapshotOptions{
		SnapshotID: snapshot.ID,
		Labels:     map[string]string{"name": "snapshot-sandbox"},
	})
	if err != nil {
		log.Fatalf("Failed to create sandbox from snapshot: %v", err)
	}
	fmt.Printf("   ✓ Sandbox from snapshot ID: %s\n\n", sandboxFromSnapshot.ID())

	// 25. Clean up
	fmt.Println("25. Cleaning up...")
	err = sandbox.Delete(ctx)
	if err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   ✓ Original sandbox deleted")

	err = sandboxFromSnapshot.Delete(ctx)
	if err != nil {
		log.Fatalf("Failed to delete sandbox: %v", err)
	}
	fmt.Println("   ✓ Snapshot sandbox deleted")
	fmt.Println()

	fmt.Println("=== Example completed successfully! ===")
}
