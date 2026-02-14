# Dockerfile for waza - AI agent skill evaluation CLI
# This provides a containerized environment for running waza in CI/CD pipelines

FROM golang:1.25-alpine AS builder

WORKDIR /build

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the binary with static linking
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags '-s -w' -o waza ./cmd/waza

# Verify the binary works
RUN ./waza --version

# Runtime stage - minimal alpine image
FROM alpine:3.19

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /workspace

# Copy the binary from builder
COPY --from=builder /build/waza /usr/local/bin/waza

# Verify installation
RUN waza --version

# Default command shows help
ENTRYPOINT ["waza"]
CMD ["--help"]
