# syntax=docker/dockerfile:1

# Shared by the stages that download (corepack fetches pnpm, pnpm install, next/font/google).
# extra-ca (compose additional_contexts, BUILD_CA_DIR): *.crt PEM files of a TLS-inspecting proxy,
# appended to the system bundle; Node reads it through NODE_EXTRA_CA_CERTS. Empty by default.
FROM node:22-alpine AS base
RUN --mount=type=bind,from=extra-ca,target=/tmp/extra-ca \
    for f in /tmp/extra-ca/*.crt; do \
      [ -f "$f" ] || continue; { cat "$f"; echo; } >> /etc/ssl/certs/ca-certificates.crt; \
    done
ENV NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-certificates.crt
RUN corepack enable

FROM base AS deps
WORKDIR /app
COPY package.json pnpm-lock.yaml .npmrc ./
# Retries + lower concurrency make the install survive flaky registry connections.
ENV npm_config_fetch_retries=5 \
    npm_config_fetch_retry_mintimeout=10000 \
    npm_config_network_concurrency=8
RUN --mount=type=cache,target=/root/.local/share/pnpm/store pnpm install --frozen-lockfile

FROM base AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .
# Baked into the client bundle: the URL the *browser* uses to reach the API.
ARG NEXT_PUBLIC_API_BASE=http://localhost:3101
ENV NEXT_PUBLIC_API_BASE=$NEXT_PUBLIC_API_BASE \
    NEXT_TELEMETRY_DISABLED=1
RUN pnpm build

FROM node:22-alpine AS run
WORKDIR /app
ENV NODE_ENV=production \
    PORT=3000 \
    HOSTNAME=0.0.0.0 \
    NEXT_TELEMETRY_DISABLED=1
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
# next-intl reads messages/<locale>/*.json from the working directory at runtime.
COPY --from=build --chown=node:node /app/messages ./messages
USER node
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=5 \
  CMD wget -qO- http://127.0.0.1:3000/ >/dev/null || exit 1
CMD ["node", "server.js"]
