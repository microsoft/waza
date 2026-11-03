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

FROM scratch
COPY --from=build /waza /waza
USER 65532:65532
WORKDIR /
ENTRYPOINT ["/waza"]
CMD ["runner", "capabilities"]
