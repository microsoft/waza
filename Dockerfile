# syntax=docker/dockerfile:1

# Dockerfile for waza - AI agent skill evaluation CLI
# This provides a containerized environment for running waza in CI/CD pipelines

# Stage 1: Build web dashboard
FROM node:22-alpine AS web-builder

WORKDIR /build/web

COPY web/package.json web/package-lock.json ./
RUN npm ci --silent

COPY web/ ./
COPY schemas/ /build/schemas/
COPY examples/runner/unit.json /build/examples/runner/unit.json
RUN npm run build

# Stage 2: Build Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /build

# Copy go mod files first for better layer caching
COPY go.mod go.sum ./

# Download dependencies
# Cache mounts persist on the same BuildKit builder, not in exported image layers.
RUN --mount=type=cache,id=waza-go-mod,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY . .

# Copy built web assets from web-builder stage
COPY --from=web-builder /build/web/dist/ web/dist/

# Build the binary with static linking
RUN --mount=type=cache,id=waza-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=waza-go-build,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -ldflags '-s -w' -o waza ./cmd/waza

# Verify the binary works
RUN test "$(od -An -tx1 -N4 ./waza | tr -d ' \n')" = 7f454c46 && \
    ldd /bin/sh && ./waza --version && \
    if ldd ./waza >/tmp/waza-ldd.txt 2>&1; then \
        cat /tmp/waza-ldd.txt; \
        echo "Expected a statically linked waza binary" >&2; \
        exit 1; \
    else \
        cat /tmp/waza-ldd.txt && \
        grep -Fq './waza: Not a valid dynamic program' /tmp/waza-ldd.txt && \
        rm /tmp/waza-ldd.txt; \
    fi

# Runtime stage - minimal alpine image
FROM alpine:3.19

# Install CA certificates for HTTPS
RUN apk --no-cache add ca-certificates

WORKDIR /workspace

# Copy the binary from builder
COPY --from=builder /build/waza /usr/local/bin/waza

# Verify installation
RUN waza --version && \
    for path in /go/pkg/mod /root/.cache/go-build /build; do \
        if [ -e "$path" ] || [ -L "$path" ]; then \
            echo "Build cache or source leaked into runtime image: $path" >&2; \
            exit 1; \
        fi; \
    done

# Default command shows help
ENTRYPOINT ["waza"]
CMD ["--help"]
