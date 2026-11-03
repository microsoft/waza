FROM node:24-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85 AS web
WORKDIR /build/web
ARG NPM_CONFIG_REGISTRY=https://registry.npmjs.org/
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
COPY schemas/ /build/schemas/
COPY examples/runner/unit.json /build/examples/runner/unit.json
RUN npm run build

FROM golang:1.26.4-alpine@sha256:3ad57304ad93bbec8548a0437ad9e06a455660655d9af011d58b993f6f615648 AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /build/web/dist/ web/dist/
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -ldflags="-s -w" -o /waza ./cmd/waza
RUN /waza runner capabilities

FROM build AS controls-build
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go test -count=1 ./.github/scripts/testdata/runner-runtime
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -o /synthetic-runtime ./.github/scripts/testdata/runner-runtime
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go test -c -o /embedded.test ./internal/embedded

FROM build AS native-controls-build
COPY .github/scripts/testdata/native-bundle-probe/ /build/.github/scripts/testdata/native-bundle-probe/
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -o /native-bundle-probe ./.github/scripts/testdata/native-bundle-probe/harness
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -buildvcs=false -o /sdk-positive ./.github/scripts/testdata/native-bundle-probe/positive

FROM scratch AS synthetic-test
COPY --from=build /waza /waza
COPY --from=controls-build /synthetic-runtime /synthetic-runtime
USER 65532:65532
WORKDIR /
ENTRYPOINT ["/waza"]
CMD ["runner", "capabilities"]

FROM alpine:3.23@sha256:ff7f0c727dd796aa36787bfcd9a64bf35a6ce72a73451b3359fde849b6703d63 AS native-smoke
COPY --from=build /waza /waza
COPY --from=controls-build /embedded.test /embedded.test
USER 65532:65532
WORKDIR /
ENTRYPOINT ["/waza"]
CMD ["runner", "capabilities"]

FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3 AS native-reference
RUN apt-get update \
    && apt-get install --no-install-recommends -y bash ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /waza /waza
RUN groupadd --system --gid 65532 waza \
    && useradd --system --uid 65532 --gid 65532 --home-dir /home/waza --create-home --shell /usr/sbin/nologin waza \
    && mkdir -p /home/waza/.copilot \
    && chown -R 65532:65532 /home/waza
ENV HOME=/home/waza COPILOT_HOME=/home/waza/.copilot TMPDIR=/tmp
USER 65532:65532
WORKDIR /
ENTRYPOINT ["/waza"]
CMD ["runner", "capabilities"]

FROM native-reference AS native-bundle-test
COPY --from=native-controls-build /native-bundle-probe /native-bundle-probe
COPY --from=native-controls-build /sdk-positive /sdk-positive
COPY examples/runner/copilot-unit.json /input/copilot-unit.json
ENTRYPOINT ["/native-bundle-probe"]

FROM scratch AS reference
COPY --from=build /waza /waza
USER 65532:65532
WORKDIR /
ENTRYPOINT ["/waza"]
CMD ["runner", "capabilities"]
