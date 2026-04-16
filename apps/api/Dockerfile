FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/cli    ./cmd/cli

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 neurocache
WORKDIR /app
COPY --from=build /out/server /usr/local/bin/server
COPY --from=build /out/cli    /usr/local/bin/neurocache-cli
USER neurocache
ENV NEUROCACHE_HTTP_PORT=8080 \
    NEUROCACHE_RESP_PORT=6379 \
    NEUROCACHE_DATA_DIR=/data
EXPOSE 8080 6379
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -qO- http://localhost:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/server"]
