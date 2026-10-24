# syntax=docker/dockerfile:1

# CGO is required: repointel/adapters/treesitter links the tree-sitter C runtime and grammars.
# The C toolchain lives only in this stage; build (alpine3.23) and runtime (alpine:3.23) share
# the same musl, so the dynamically linked binary runs unchanged in the runtime image.
FROM golang:1.27-alpine3.23 AS build
RUN apk add --no-cache build-base
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# The CI runner is built first, static (cgo off) and reproducible, into the package that embeds
# it; the server then carries it (-tags devdigest_runner). DEVDIGEST_BUILD_ID is the source
# commit (12 hex); anything else becomes "dev".
ARG DEVDIGEST_BUILD_ID=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    id="$DEVDIGEST_BUILD_ID"; \
    case "$id" in *[!0-9a-f]*|"") id=dev ;; esac; \
    [ "$id" = dev ] || [ "${#id}" -eq 12 ] || id=dev; \
    dir=internal/ci/adapters/runnerbin; \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -buildid= -X main.buildID=$id" -o "$dir/devdigest-runner-linux-amd64" ./cmd/devdigest-runner; \
    printf '%s' "$id" > "$dir/build-id"
# The server reports both in GET /about: main.buildID is the sanitised id written above,
# main.version is DEVDIGEST_VERSION (letters, digits and . _ + - only; anything else becomes "dev").
ARG DEVDIGEST_VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    ver="$DEVDIGEST_VERSION"; \
    case "$ver" in *[!0-9A-Za-z._+-]*|"") ver=dev ;; esac; \
    id=$(cat internal/ci/adapters/runnerbin/build-id); \
    CGO_ENABLED=1 go build -trimpath -tags devdigest_runner \
      -ldflags "-s -w -X main.version=$ver -X main.buildID=$id" -o /out/server ./cmd/server

# Alpine, not distroless: the backend shells out to `git` (clone, fetch, diff, blame).
FROM alpine:3.23
RUN apk add --no-cache git ca-certificates \
 && adduser -D -H -u 10001 app \
 && mkdir -p /data \
 && chown app:app /data
COPY --from=build /out/server /app/server
USER app
# /data holds repository clones and the local secrets file (a named volume in compose).
ENV DEVDIGEST_DATA_DIR=/data
VOLUME /data
EXPOSE 3001
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 CMD ["/app/server", "healthcheck"]
ENTRYPOINT ["/app/server"]
