FROM node:24.15.0-bookworm-slim@sha256:152aceace5c03e2597988763165ee33e3fd3633636db0fc983cd2e126b02cfde AS dashboard
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY schemas/ /src/schemas/
RUN npm run build

FROM golang:1.26.4-bookworm@sha256:5a94593d87a066df5abb02969be911524963f53908292aa5a1a6096fc019012a AS binary
WORKDIR /src
ENV GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS="" GOEXPERIMENT="" \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 GOFIPS140=off
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
COPY --from=dashboard /src/web/dist/ web/dist/
RUN go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags '-s -w -X main.version=runner-reference-preparation' -o /waza ./cmd/waza

FROM scratch
ARG SOURCE_DATE_EPOCH
ARG REVISION
LABEL org.opencontainers.image.source="https://github.com/microsoft/waza" \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.description="Preparatory bounded runner reference; mock/text only, not a native runtime"
COPY --from=binary --chmod=0555 /waza /waza
ENV TMPDIR=/work HOME=/work \
    COPILOT_CLI_PATH=/native-runtime-unavailable ENABLE_COPILOT_TESTS=false
USER 65532:65532
WORKDIR /work
ENTRYPOINT ["/waza"]
CMD ["runner", "run"]
