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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

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
