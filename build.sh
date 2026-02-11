#!/bin/bash

# Get the directory of the script
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Change to the script directory
cd "$SCRIPT_DIR" || exit

# Binary name - must match SafeDashId() from extension.yaml id (microsoft.azd.waza -> microsoft-azd-waza)
BINARY_NAME="microsoft-azd-waza"

# Define output directory
OUTPUT_DIR="${OUTPUT_DIR:-$SCRIPT_DIR/bin}"

# Create output directory if it doesn't exist
mkdir -p "$OUTPUT_DIR"

# Get Git commit hash and build date
COMMIT=$(git rev-parse HEAD 2>/dev/null || echo "unknown")
BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION="${VERSION:-0.1.0}"

# List of OS and architecture combinations
if [ -n "$PLATFORM" ]; then
    PLATFORMS=("$PLATFORM")
else
    PLATFORMS=(
        "windows/amd64"
        "windows/arm64"
        "darwin/amd64"
        "darwin/arm64"
        "linux/amd64"
        "linux/arm64"
    )
fi

# Loop through platforms and build
for PLATFORM in "${PLATFORMS[@]}"; do
    OS=$(echo "$PLATFORM" | cut -d'/' -f1)
    ARCH=$(echo "$PLATFORM" | cut -d'/' -f2)

    OUTPUT_NAME="$OUTPUT_DIR/$BINARY_NAME-$OS-$ARCH"

    if [ "$OS" = "windows" ]; then
        OUTPUT_NAME+='.exe'
    fi

    echo "Building for $OS/$ARCH..."

    # Delete the output file if it already exists
    [ -f "$OUTPUT_NAME" ] && rm -f "$OUTPUT_NAME"

    # Set environment variables for Go build
    GOOS=$OS GOARCH=$ARCH go build \
        -ldflags="-X 'main.version=$VERSION'" \
        -o "$OUTPUT_NAME" \
        ./cmd/waza

    if [ $? -ne 0 ]; then
        echo "An error occurred while building for $OS/$ARCH"
        exit 1
    fi
done

echo "Build completed successfully!"
echo "Binaries are located in the $OUTPUT_DIR directory."
