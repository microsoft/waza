package scripts

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDockerGoCacheContract(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	check := func(source string) error {
		if !strings.HasPrefix(source, "# syntax=docker/dockerfile:1\n") {
			return fmt.Errorf("Dockerfile must select the cache-mount frontend")
		}
		for _, command := range []string{
			"RUN --mount=type=cache,id=waza-go-mod,target=/go/pkg/mod \\\n    go mod download\n",
			"RUN --mount=type=cache,id=waza-go-mod,target=/go/pkg/mod \\\n    --mount=type=cache,id=waza-go-build,target=/root/.cache/go-build \\\n    CGO_ENABLED=0 GOOS=linux go build -ldflags '-s -w' -o waza ./cmd/waza\n",
		} {
			if !strings.Contains(source, command) {
				return fmt.Errorf("Dockerfile cache/static-build contract changed: %s", command)
			}
		}
		_, runtime, found := strings.Cut(source, "# Runtime stage - minimal alpine image\n")
		if !found || runtime != dockerCacheRuntimeOracle {
			return fmt.Errorf("cache optimization must preserve the runtime stage without build caches")
		}
		return nil
	}
	if err := check(source); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		name, old, replacement string
	}{
		{"missing-frontend", "# syntax=docker/dockerfile:1\n", ""},
		{"missing-module-cache", "id=waza-go-mod", "id=another-module-cache"},
		{"missing-compiler-cache", "target=/root/.cache/go-build", "target=/unused"},
		{"forced-recompile", "go build -ldflags", "go build -a -ldflags"},
		{"obsolete-install-suffix", "go build -ldflags", "go build -installsuffix cgo -ldflags"},
		{"dynamic-linking", "CGO_ENABLED=0", "CGO_ENABLED=1"},
		{"lost-linker-flags", "-ldflags '-s -w'", ""},
		{"runtime-cache-leak", "COPY --from=builder /build/waza /usr/local/bin/waza",
			"COPY --from=builder /build/waza /usr/local/bin/waza\nCOPY --from=builder /go/pkg/mod /go/pkg/mod"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			mutated := strings.Replace(source, mutation.old, mutation.replacement, 1)
			if mutated == source {
				t.Fatal("mutation did not change the production Dockerfile")
			}
			if err := check(mutated); err == nil {
				t.Fatal("invalid Dockerfile must fail the cache contract")
			}
		})
	}
}

const dockerCacheRuntimeOracle = `FROM alpine:3.19

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
`
