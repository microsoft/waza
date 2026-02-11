# Ensure script fails on any error
$ErrorActionPreference = 'Stop'

# Get the directory of the script
$SCRIPT_DIR = Split-Path -Parent $MyInvocation.MyCommand.Path

# Change to the script directory
Set-Location -Path $SCRIPT_DIR

# Binary name - must match SafeDashId() from extension.yaml id (microsoft.azd.waza -> microsoft-azd-waza)
$BINARY_NAME = "microsoft-azd-waza"

# Define output directory
$OUTPUT_DIR = if ($env:OUTPUT_DIR) { $env:OUTPUT_DIR } else { Join-Path $SCRIPT_DIR "bin" }

# Create output directory if it doesn't exist
if (-not (Test-Path -Path $OUTPUT_DIR)) {
    New-Item -ItemType Directory -Path $OUTPUT_DIR | Out-Null
}

# Get Git commit hash and build date
$COMMIT = git rev-parse HEAD 2>$null
if ($LASTEXITCODE -ne 0) {
    $COMMIT = "unknown"
}
$BUILD_DATE = (Get-Date -Format "yyyy-MM-ddTHH:mm:ssZ")
$VERSION = if ($env:VERSION) { $env:VERSION } else { "0.1.0" }

# List of OS and architecture combinations
if ($env:PLATFORM) {
    $PLATFORMS = @($env:PLATFORM)
}
else {
    $PLATFORMS = @(
        "windows/amd64",
        "windows/arm64",
        "darwin/amd64",
        "darwin/arm64",
        "linux/amd64",
        "linux/arm64"
    )
}

# Loop through platforms and build
foreach ($PLATFORM in $PLATFORMS) {
    $OS, $ARCH = $PLATFORM -split '/'

    $OUTPUT_NAME = Join-Path $OUTPUT_DIR "$BINARY_NAME-$OS-$ARCH"

    if ($OS -eq "windows") {
        $OUTPUT_NAME += ".exe"
    }

    Write-Host "Building for $OS/$ARCH..."

    # Delete the output file if it already exists
    if (Test-Path -Path $OUTPUT_NAME) {
        Remove-Item -Path $OUTPUT_NAME -Force
    }

    # Set environment variables for Go build
    $env:GOOS = $OS
    $env:GOARCH = $ARCH

    go build `
        -ldflags="-X 'main.version=$VERSION'" `
        -o $OUTPUT_NAME `
        ./cmd/waza

    if ($LASTEXITCODE -ne 0) {
        Write-Host "An error occurred while building for $OS/$ARCH"
        exit 1
    }
}

Write-Host "Build completed successfully!"
Write-Host "Binaries are located in the $OUTPUT_DIR directory."
