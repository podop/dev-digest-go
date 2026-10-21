# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

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
