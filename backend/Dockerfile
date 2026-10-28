# syntax=docker/dockerfile:1

# CGO is required: repointel/adapters/treesitter links the tree-sitter C runtime and grammars.
# The C toolchain lives only in this stage; build (alpine3.23) and runtime (alpine:3.23) share
# the same musl, so the dynamically linked binary runs unchanged in the runtime image.
FROM golang:1.27-alpine3.23 AS build
# extra-ca (compose additional_contexts, BUILD_CA_DIR): *.crt PEM files of a TLS-inspecting proxy,
# appended to the system bundle before anything downloads (apk, go mod). Empty by default.
# git: GOPROXY's `direct` fallback fetches modules with it.
RUN --mount=type=bind,from=extra-ca,target=/tmp/extra-ca \
    for f in /tmp/extra-ca/*.crt; do \
      [ -f "$f" ] || continue; { cat "$f"; echo; } >> /etc/ssl/certs/ca-certificates.crt; \
    done; \
    apk add --no-cache build-base git
WORKDIR /src
COPY go.mod go.sum ./
# Compose passes BUILD_GOPROXY. `https://proxy.golang.org|direct` (pipe, not comma) falls back to a
# git fetch on any proxy error, e.g. a 403 from a TLS-inspecting proxy that blocks a module zip as
# "unscannable"; go.sum still verifies every module.
ARG GOPROXY=https://proxy.golang.org,direct
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
# The extra CAs (see the build stage) also go to /usr/local/share/ca-certificates, so the
# ca-certificates install keeps them in the bundle git and the server trust at runtime.
RUN --mount=type=bind,from=extra-ca,target=/tmp/extra-ca \
    mkdir -p /usr/local/share/ca-certificates; \
    for f in /tmp/extra-ca/*.crt; do \
      [ -f "$f" ] || continue; cp "$f" /usr/local/share/ca-certificates/; \
      { cat "$f"; echo; } >> /etc/ssl/certs/ca-certificates.crt; \
    done; \
    apk add --no-cache git ca-certificates \
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
