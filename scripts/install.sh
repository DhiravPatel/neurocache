#!/usr/bin/env sh
# NeuroCache one-line installer.
#
# Usage:
#   curl -fsSL https://neurocache.dev/install.sh | sh
#   curl -fsSL https://neurocache.dev/install.sh | sh -s -- --http-port 9090
#
# Installs NeuroCache via Docker on the local machine and starts it.
# Ports:   8080 (dashboard + HTTP API), 6379 (RESP / redis-cli compatible)
# Volume:  neurocache-data (persistent)

set -eu

IMAGE="${NEUROCACHE_IMAGE:-neurocache/engine:latest}"
NAME="${NEUROCACHE_NAME:-neurocache}"
HTTP_PORT="${NEUROCACHE_HTTP_PORT:-8080}"
RESP_PORT="${NEUROCACHE_RESP_PORT:-6379}"
MAX_MEMORY="${NEUROCACHE_MAX_MEMORY:-512mb}"
EVICTION="${NEUROCACHE_EVICTION_POLICY:-ai-smart}"

# ─── parse flags ───
while [ $# -gt 0 ]; do
  case "$1" in
    --http-port)     HTTP_PORT="$2"; shift 2 ;;
    --resp-port)     RESP_PORT="$2"; shift 2 ;;
    --image)         IMAGE="$2"; shift 2 ;;
    --name)          NAME="$2"; shift 2 ;;
    --max-memory)    MAX_MEMORY="$2"; shift 2 ;;
    --eviction)      EVICTION="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

say()  { printf "\033[1;35m•\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m!\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[1;31m✗\033[0m %s\n" "$*" >&2; exit 1; }

# ─── docker check ───
if ! command -v docker >/dev/null 2>&1; then
  die "Docker is required. Install from https://docs.docker.com/get-docker/"
fi
if ! docker info >/dev/null 2>&1; then
  die "Docker is installed but not running. Start Docker Desktop (or the docker daemon) and retry."
fi

# ─── handle existing container ───
if docker ps -a --format '{{.Names}}' | grep -q "^${NAME}$"; then
  if docker ps --format '{{.Names}}' | grep -q "^${NAME}$"; then
    say "Container '${NAME}' is already running. Restarting it…"
    docker restart "$NAME" >/dev/null
  else
    say "Removing stopped container '${NAME}'…"
    docker rm "$NAME" >/dev/null
  fi
fi

# ─── pull image ───
say "Pulling image ${IMAGE}…"
if ! docker pull "$IMAGE" >/dev/null 2>&1; then
  warn "docker pull failed — falling back to existing local image (if any)."
fi

# ─── run ───
if ! docker ps --format '{{.Names}}' | grep -q "^${NAME}$"; then
  say "Starting NeuroCache on :${HTTP_PORT} (dashboard) and :${RESP_PORT} (RESP)…"
  docker run -d \
    --name "$NAME" \
    --restart unless-stopped \
    -p "${HTTP_PORT}:8080" \
    -p "${RESP_PORT}:6379" \
    -v neurocache-data:/data \
    -e "NEUROCACHE_MAX_MEMORY=${MAX_MEMORY}" \
    -e "NEUROCACHE_EVICTION_POLICY=${EVICTION}" \
    -e "NEUROCACHE_CORS_ORIGINS=*" \
    "$IMAGE" >/dev/null
fi

# ─── verify ───
say "Waiting for health check…"
tries=0
until curl -fsS "http://localhost:${HTTP_PORT}/api/health" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ $tries -ge 30 ]; then
    warn "Container started but /api/health never responded. Check: docker logs ${NAME}"
    exit 1
  fi
  sleep 1
done

cat <<EOF

  \033[1;32m✓\033[0m NeuroCache is running.

  Dashboard   : \033[1;36mhttp://localhost:${HTTP_PORT}\033[0m
  HTTP API    : \033[1;36mhttp://localhost:${HTTP_PORT}/api\033[0m
  RESP (cli)  : \033[1;36mredis-cli -p ${RESP_PORT}\033[0m

  Try it:
    curl -s http://localhost:${HTTP_PORT}/api/info | jq
    docker exec -it ${NAME} neurocache-cli PING
    docker logs -f ${NAME}

  Stop:       docker stop ${NAME}
  Remove:     docker rm -f ${NAME}
  Persist:    data is stored in the 'neurocache-data' volume.

EOF
