<!-- markdownlint-disable MD010 -->

# ADC Go SDK

Go SDK for ADC (Azure Dev Compute) sandboxes.

## Installation

```bash
go get github.com/coreai-microsoft/adc-sdk-go
```

## Quick Start

```go
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
	ctx := context.Background()

	// Recommended: Configure client with API key
	client := adc.New(adc.Config{
		APIKey: "your-api-key",
		APIURL: "https://management.azuredevcompute.io",
	})
	defer client.Close()

	// Alternative: Use a bearer token (e.g., from Azure AD)
	// client := adc.New(adc.Config{
	// 	Token: "your-bearer-token",
	// 	APIURL: "https://management.azuredevcompute.io",
	// })

	// Create a disk image
	diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
		Labels:    map[string]string{"name": "ubuntu-sandbox", "version": "latest"},
		BaseImage: "docker.io/library/ubuntu:latest",
	})
	if err != nil {
		log.Fatal(err)
	}

	// Create sandbox from disk image
	sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
		DiskImage:  models.SandboxSourceDiskImage{ID: diskImage.ID},
		CPU:        "1",
		Memory:     "1024Mi",
		Labels:     map[string]string{"name": "my-sandbox"},
		Entrypoint: []string{"/bin/sleep"},
		Cmd:        []string{"infinity"},
	})
	if err != nil {
		log.Fatal(err)
	}

	// Execute commands
	result, err := sandbox.ExecuteCommand(ctx, "ps", []string{"aux"}, nil, "")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Stdout)

	// Execute shell commands
	shellResult, err := sandbox.ExecuteShellCommand(ctx, "ls -alh /", "", nil, "")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(shellResult.Stdout)

	// Expose a port and get a public URL
	port, err := sandbox.AddPort(ctx, 8080, &models.AddPortOptions{Name: "web"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Access at: %s\n", port.URL)

	// Create a snapshot
	snapshot, err := sandbox.Snapshot(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Snapshot ID: %s\n", snapshot.ID)

	// Stop and resume sandbox
	sandbox.Stop(ctx)
	sandbox.Resume(ctx)

	// Clean up
	sandbox.Delete(ctx)
}
```

## Features

- **Disk Image Management**: Create and manage disk images from Docker images
- **Sandbox Management**: Create, stop, resume, and delete sandboxes
- **Command Execution**: Execute commands and shell commands in sandboxes
- **Streaming Execution**: Real-time output streaming via WebSocket for long-running commands
- **File Operations**: Read, write, list, stat, and delete files in sandboxes
- **GitHub Authentication**: Authenticate with GitHub Device Flow
- **Port Management**: Expose ports and get public URLs to access your services
- **Snapshot Support**: Create and manage snapshots of sandboxes
- **Resource Configuration**: Configure CPU and memory resources
- **No External Dependencies**: Uses only Go standard library (plus gorilla/websocket for streaming)

## API Overview

### Client

```go
import adc "github.com/coreai-microsoft/adc-sdk-go"

config := adc.Config{
	APIKey:         "...",                                    // Your ADC API key (mutually exclusive with Token)
	Token:          "...",                                    // Bearer token (mutually exclusive with APIKey)
	APIURL:         "https://management.azuredevcompute.io",  // API base URL (default)
	Timeout:        5 * time.Minute,                          // Request timeout (default: 5 minutes)
	SandboxSpaceID: "/subscriptions/.../...",                 // Optional space ID for scoping
}

client := adc.New(config)
defer client.Close()
```

#### Scoping Operations with Space ID

You can scope all operations to a specific Azure sandbox space by providing a `SandboxSpaceID` in the config:

```go
client := adc.New(adc.Config{
	APIKey: "your-api-key",
	SandboxSpaceID: "/subscriptions/1642eb12-ffc3-4f5c-a0dc-50b4ec6e208c/resourceGroups/myRG/providers/Microsoft.App/sandboxes/myspace",
})

// All operations are now scoped to the sandbox space
diskImages, _ := client.DiskImages.List(ctx, nil)  // Only shows images in this space
sandboxes, _ := client.Sandboxes.List(ctx, nil)    // Only shows sandboxes in this space
snapshots, _ := client.Snapshots.List(ctx, nil)    // Only shows snapshots in this space
```

### Disk Images

```go
import "github.com/coreai-microsoft/adc-sdk-go/models"

// Create a disk image
diskImage, err := client.DiskImages.Create(ctx, models.CreateDiskImageOptions{
	Labels:     map[string]string{"name": "my-image", "env": "dev"},
	BaseImage:  "docker.io/library/python:3.11",
	Entrypoint: []string{"/bin/bash"},  // Optional
})

// Get a disk image
diskImage, err := client.DiskImages.Get(ctx, diskImageID)

// List disk images
diskImages, err := client.DiskImages.List(ctx, nil)

// Filter disk images by labels
filteredImages, err := client.DiskImages.List(ctx, &adc.ListOptions{
	Labels: map[string]string{"name": "my-image"},
})

// List public disk images
publicImages, err := client.DiskImages.ListPublic(ctx)

// Delete a disk image
err := client.DiskImages.Delete(ctx, diskImageID)
```

### Sandboxes

```go
// Create sandbox from disk image
sandbox, err := client.Sandboxes.CreateFromDiskImage(ctx, models.CreateFromDiskImageOptions{
	DiskImage:  models.SandboxSourceDiskImage{ID: diskImageID},  // Or {Name: "python:3.12", IsPublic: true}
	CPU:        "1",              // CPU cores (default: "1000m")
	Memory:     "1024Mi",         // Memory (default: "1024Mi")
	Labels:     map[string]string{"name": "my-sandbox"},
	Entrypoint: []string{"/bin/bash"},
	Cmd:        []string{"-c", "sleep infinity"},
	VmmType:    models.VmmTypeCloudhypervisor,  // VMM type
})

// Create sandbox from snapshot
// Note: CPU, memory, and VmmType are inherited from the snapshot
sandbox, err := client.Sandboxes.CreateFromSnapshot(ctx, models.CreateFromSnapshotOptions{
	SnapshotID: "snapshot-id",
	Labels:     map[string]string{"name": "restored-sandbox"},
	Entrypoint: []string{"/bin/bash"},
	Cmd:        []string{"-c", "sleep infinity"},
})

// Get a sandbox
sandbox, err := client.Sandboxes.Get(ctx, sandboxID)

// List sandboxes
sandboxes, err := client.Sandboxes.List(ctx, nil)

// Filter sandboxes by labels
filteredSandboxes, err := client.Sandboxes.List(ctx, &adc.ListOptions{
	Labels: map[string]string{"env": "production"},
})

// Count sandboxes
count, err := client.Sandboxes.Count(ctx)
```

### Sandbox Operations

```go
// Execute command (HTTP)
result, err := sandbox.ExecuteCommand(ctx, "ls", []string{"-alh", "/app"},
	map[string]string{"MY_ENV": "value"},  // Environment variables
	"/working/dir",                         // Working directory
)
fmt.Println(result.ExitCode)
fmt.Println(result.Stdout)
fmt.Println(result.Stderr)

// Execute shell command (HTTP)
result, err := sandbox.ExecuteShellCommand(ctx,
	"echo 'Hello, World!'",
	"/bin/bash",                            // Optional shell (defaults to /bin/sh)
	map[string]string{"MY_ENV": "value"},   // Environment variables
	"/working/dir",                          // Working directory
)

// Start interactive exec stream (WebSocket) for real-time output
session, err := sandbox.StartExecStream(ctx, &models.ExecStreamStartRequest{
	Command: "bash",
	Args:    []string{"-c", "for i in 1 2 3; do echo $i; sleep 1; done"},
})
for msg := range session.ReadOutput() {
	if msg.Type == models.ExecStreamMessageTypeStdout {
		fmt.Print(string(msg.Data))
	} else if msg.Type == models.ExecStreamMessageTypeExitCode {
		fmt.Printf("Exit code: %d\n", *msg.ExitCode)
	}
}
session.Close()

// Interactive TTY session with stdin
ttySession, _ := sandbox.StartExecStream(ctx, &models.ExecStreamStartRequest{
	Command: "/bin/bash",
	Tty:     true,
	Stdin:   true,
})
ttySession.SendStdinString("ls -la\n")
ttySession.SendStdinString("exit\n")
exitCode, _ := ttySession.WaitForExit()

// Lifecycle management
sandbox.Stop(ctx)
sandbox.Resume(ctx)
sandbox.Refresh(ctx)  // Update state from API

// Create snapshot
snapshot, err := sandbox.Snapshot(ctx, nil)
snapshotWithLabels, err := sandbox.Snapshot(ctx, map[string]string{"project": "my-app"})

// Delete sandbox
sandbox.Delete(ctx)

// Access sandbox properties
fmt.Println(sandbox.ID())
fmt.Println(sandbox.State())
fmt.Println(sandbox.Labels())
fmt.Println(sandbox.VmmType())
```

### Port Management

Expose ports from your sandbox and get public URLs to access them.

```go
// Add a port with a name
port, err := sandbox.AddPort(ctx, 8080, &models.AddPortOptions{Name: "web"})
fmt.Printf("Access your app at: %s\n", port.URL)

// Add a port without a name
apiPort, err := sandbox.AddPort(ctx, 3000, nil)
fmt.Printf("API available at: %s\n", apiPort.URL)

// List all exposed ports
for _, port := range sandbox.Ports() {
	name := ""
	if port.Name != "" {
		name = fmt.Sprintf(" (%s)", port.Name)
	}
	fmt.Printf("Port %d%s: %s\n", port.Port, name, port.URL)
}

// Remove a port by name
sandbox.RemovePort(ctx, models.RemovePortRequest{Name: "web"})

// Remove a port by number
sandbox.RemovePort(ctx, models.RemovePortRequest{Port: 3000})
```

### Snapshots

```go
// List snapshots
snapshots, err := client.Snapshots.List(ctx, nil)

// Filter snapshots by labels
filteredSnapshots, err := client.Snapshots.List(ctx, &adc.ListOptions{
	Labels: map[string]string{"project": "my-app"},
})

// Get a snapshot
snapshot, err := client.Snapshots.Get(ctx, snapshotID)

// Count snapshots
count, err := client.Snapshots.Count(ctx)

// Delete a snapshot
err := client.Snapshots.Delete(ctx, snapshotID)

// Access snapshot properties
fmt.Println(snapshot.ID)
fmt.Println(snapshot.SandboxID)
fmt.Println(snapshot.ClusterHostname)
fmt.Println(snapshot.CreatedAtUTC)
```

### File Operations

Read, write, and manage files directly in your sandbox.

```go
// Write a text file
sandbox.WriteFileText(ctx, "/workspace/hello.txt", "Hello, World!", nil)

// Write binary data
sandbox.WriteFile(ctx, "/workspace/data.bin", []byte{0x00, 0x01, 0x02, 0x03}, nil)

// Write with options: create parent directories and set permissions
sandbox.WriteFileText(ctx, "/workspace/scripts/setup.sh",
	"#!/bin/bash\necho 'Setup complete'",
	&models.WriteFileOptions{CreateDirs: true, Mode: 0755},
)

// Read a text file
content, err := sandbox.ReadFileText(ctx, "/workspace/hello.txt")
fmt.Println(content)  // "Hello, World!"

// Read a binary file
data, err := sandbox.ReadFile(ctx, "/workspace/data.bin")
fmt.Println(data)  // []byte containing binary data

// List files in a directory
files, err := sandbox.ListFiles(ctx, "/workspace")
for _, file := range files.Entries {
	fileType := "file"
	if file.IsDir {
		fileType = "dir"
	}
	fmt.Printf("%s (%s): %d bytes\n", file.Name, fileType, file.Size)
}

// Get file metadata
stat, err := sandbox.StatFile(ctx, "/workspace/hello.txt")
fmt.Printf("Name: %s\n", stat.Name)
fmt.Printf("Size: %d bytes\n", stat.Size)
fmt.Printf("Mode: %o\n", stat.Mode)
fmt.Printf("Is directory: %v\n", stat.IsDir)
fmt.Printf("Modified: %s\n", stat.ModTime)

// Create a directory
sandbox.Mkdir(ctx, "/workspace/new-folder", nil)

// Create nested directories
sandbox.Mkdir(ctx, "/workspace/path/to/nested", &models.MkdirOptions{CreateParents: true})

// Delete a file
sandbox.DeleteFile(ctx, "/workspace/hello.txt", false)

// Delete a directory recursively
sandbox.DeleteFile(ctx, "/workspace/new-folder", true)
```

## Authentication

### API Key

```go
client := adc.New(adc.Config{
	APIKey: "your-api-key",
})
```

### Token

```go
client := adc.New(adc.Config{
	Token: "your-bearer-token",
})
```

### GitHub Device Flow

For interactive CLI tools, use GitHub Device Flow authentication:

```go
import adc "github.com/coreai-microsoft/adc-sdk-go"

client := adc.New(adc.Config{})

// This opens a browser for GitHub authentication
err := client.Login(ctx, nil)
if err != nil {
	log.Fatal(err)
}

// Now you can use the API
images, err := client.DiskImages.List(ctx, nil)
```

You can also use a custom client ID or callback:

```go
err := client.Login(ctx, &adc.LoginOptions{
	ClientID: "your-github-client-id",
	Callback: func(userCode, verificationURI string) {
		fmt.Printf("Visit %s and enter code: %s\n", verificationURI, userCode)
	},
})
```

Or use the low-level authentication class directly:

```go
auth := adc.NewGitHubDeviceFlowAuth("your-client-id")
token, err := auth.Authenticate(ctx, nil)
if err != nil {
	log.Fatal(err)
}

// Use token with ADC client
client := adc.New(adc.Config{Token: token})
```

## Configuration

The SDK can be configured with the following options:

```go
type Config struct {
	// APIKey is your ADC API key (mutually exclusive with Token).
	APIKey string

	// Token is a bearer token for authentication (mutually exclusive with APIKey).
	Token string

	// APIURL is the API base URL (default: https://management.azuredevcompute.io).
	APIURL string

	// Timeout is the request timeout (default: 5 minutes).
	Timeout time.Duration

	// SandboxSpaceID scopes all operations to a specific sandbox space.
	SandboxSpaceID string
}
```

## Error Handling

The SDK uses custom error types for HTTP errors:

```go
import "errors"

sandbox, err := client.Sandboxes.Get(ctx, "invalid-id")
if err != nil {
	var httpErr *adc.HTTPError
	if errors.As(err, &httpErr) {
		fmt.Printf("HTTP Error: %d %s\n", httpErr.StatusCode, httpErr.Status)
		fmt.Printf("URL: %s\n", httpErr.URL)
		fmt.Printf("Response: %s\n", httpErr.ResponseBody)
	}
}
```

## Resource Specifications

### CPU

CPU is specified as a string representing cores or millicores:

- `"1"` - 1 CPU core
- `"2"` - 2 CPU cores
- `"1000m"` - 1000 millicores (= 1 core)
- `"500m"` - 500 millicores (= 0.5 cores)

### Memory

Memory is specified with units:

- `"1024Mi"` - 1024 mebibytes
- `"2Gi"` - 2 gibibytes
- `"512Mi"` - 512 mebibytes

### Egress Policies

Control outbound network traffic from sandboxes.

**Simple host-based policy:**

```go
err := sandbox.SetEgressPolicy(ctx, models.EgressPolicy{
    DefaultAction: models.EgressPolicyActionDeny,
    HostRules: []models.EgressHostRule{
        {Pattern: "*.github.com", Action: models.EgressPolicyActionAllow},
        {Pattern: "registry.npmjs.org", Action: models.EgressPolicyActionAllow},
    },
})
```

**Rule-based policy with Rewrite:**

```go
err := sandbox.SetEgressPolicy(ctx, models.EgressPolicy{
    DefaultAction: models.EgressPolicyActionDeny,
    Rules: []models.EgressPolicyRule{
        {
            Name: "rewrite-openai",
            Match: models.EgressPolicyRuleMatch{Host: "100.64.100.64", Path: "/openai/*"},
            Action: models.EgressPolicyRuleAction{
                Type:   models.EgressPolicyRuleActionTypeRewrite,
                Scheme: "https",
                Host:   "api.openai.com",
                Path:   "/v1/completions",
                Headers: []models.EgressPolicyHeaderTransform{
                    {
                        Operation: models.EgressPolicyHeaderOperationSet,
                        Name:      "Authorization",
                        ValueRef: &models.EgressPolicyValueRef{
                            SecretRef: &models.EgressPolicySecretRef{
                                SecretID: "my-secret",
                                SecretKey: "api-key",
                                Format:   "Bearer {value}",
                            },
                        },
                    },
                },
            },
        },
    },
})
```

## Examples

See the [examples](examples/) directory for complete working examples:

- [basic_usage](examples/basic_usage/) - Comprehensive example covering all SDK features
- [public_image](examples/public_image/) - Creating sandboxes from public disk images
- [github_auth](examples/github_auth/) - GitHub Device Flow authentication
- [streaming](examples/streaming/) - Real-time command output streaming via WebSocket

## Development

```bash
# Download dependencies
go mod download

# Build
go build ./...

# Run tests
go test ./...

# Run tests with verbose output
go test ./... -v

# Run a specific example (requires ADC_API_KEY)
go run ./examples/basic_usage
```

### Using just (optional)

If you have [just](https://github.com/casey/just) installed:

```bash
# Build the SDK
just build

# Run tests
just test

# Format code
just format

# Run all checks
just check
```

## Support

For issues and questions:

- GitHub Issues: [github.com/coreai-microsoft/adc](https://github.com/coreai-microsoft/adc)
- Documentation: [docs.azuredevcompute.io](https://docs.azuredevcompute.io)
