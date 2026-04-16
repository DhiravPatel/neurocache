# ─────────────────────────────────────────────────────────────
# Stage 1 — build the React dashboard
# ─────────────────────────────────────────────────────────────
FROM node:20-alpine AS web
WORKDIR /src
RUN corepack enable && corepack prepare pnpm@9.12.0 --activate

# Copy workspace manifests first for layer caching.
COPY package.json pnpm-workspace.yaml .npmrc ./
COPY apps/web/package.json apps/web/
COPY packages/sdk-js/package.json packages/sdk-js/
RUN pnpm install --no-frozen-lockfile

# Copy sources and build.
COPY apps/web apps/web
COPY packages/sdk-js packages/sdk-js
RUN pnpm --filter @neurocache/web build

# ─────────────────────────────────────────────────────────────
# Stage 2 — build the Go binary with the embedded dashboard
# ─────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS go
WORKDIR /src
COPY apps/api/go.mod apps/api/
WORKDIR /src/apps/api
RUN go mod download

COPY apps/api .
# Wipe the dev placeholder and replace with the freshly-built dashboard so
# `//go:embed all:dist` bundles the real UI into the binary.
RUN rm -rf internal/webui/dist
COPY --from=web /src/apps/web/dist internal/webui/dist

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/cli    ./cmd/cli

# ─────────────────────────────────────────────────────────────
# Stage 3 — tiny runtime image
# ─────────────────────────────────────────────────────────────
FROM alpine:3.20
LABEL org.opencontainers.image.title="NeuroCache"
LABEL org.opencontainers.image.description="AI-aware, Redis-compatible in-memory data store with built-in dashboard."
LABEL org.opencontainers.image.source="https://github.com/dhiravpatel/neurocache"

RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 neurocache
WORKDIR /app
COPY --from=go /out/server /usr/local/bin/neurocache
COPY --from=go /out/cli    /usr/local/bin/neurocache-cli
RUN mkdir -p /data && chown neurocache:neurocache /data
USER neurocache

ENV NEUROCACHE_HTTP_PORT=8080 \
    NEUROCACHE_RESP_PORT=6379 \
    NEUROCACHE_DATA_DIR=/data \
    NEUROCACHE_CORS_ORIGINS=*

EXPOSE 8080 6379
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://localhost:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/neurocache"]
