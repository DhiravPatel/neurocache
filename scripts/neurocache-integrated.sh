#!/usr/bin/env bash
# Integrated NeuroCache launcher — single command starts both
# processes for the production hot-path-front + Go-AI-backend setup.
#
# Architecture (single port from client perspective):
#
#   Client → :6379 (Rust hot path)
#                ├─ fast commands (GET, SET, INCR, LPUSH, HSET, …) → handled locally
#                └─ AI / advanced commands (SEMANTIC_GET, MEMORY.QUERY,
#                   TOOL.GET, GUARD.CHECK, XADD, EVAL, …) → proxied to:
#                                                                ▼
#                                                       :6378 (Go server)
#
# Why two processes: the Rust binary owns its event loop on its
# port. The Go server runs as the AI + standard-Redis backend on an
# internal port. Clients only ever touch :6379.
#
# Trapping SIGINT/SIGTERM here cleanly shuts down both children.
#
# Configure:
#   NC_PUBLIC_PORT=6379       (Rust front-end)
#   NC_GO_PORT=6378           (Go backend, internal)
#   NC_HTTP_PORT=8080         (dashboard)
#   NC_DATA=/var/lib/neurocache
#
# Usage:
#   scripts/neurocache-integrated.sh
#   NC_PUBLIC_PORT=7000 NC_HTTP_PORT=9090 scripts/neurocache-integrated.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NC_PUBLIC_PORT=${NC_PUBLIC_PORT:-6379}
NC_GO_PORT=${NC_GO_PORT:-6378}
NC_HTTP_PORT=${NC_HTTP_PORT:-8080}
NC_DATA=${NC_DATA:-"$REPO_ROOT/.data"}

GO_BIN="$REPO_ROOT/bin/neurocache"
RUST_BIN="$REPO_ROOT/apps/rust-hotpath/target/release/neurocache-hotpath"

# ── build if missing ───────────────────────────────────────────────
if [[ ! -x "$GO_BIN" ]]; then
  echo "→ building Go server (one-time)…"
  ( cd "$REPO_ROOT" && make build )
fi
if [[ ! -x "$RUST_BIN" ]]; then
  echo "→ building Rust hot path (one-time)…"
  ( cd "$REPO_ROOT" && make rust-hotpath )
fi

mkdir -p "$NC_DATA"

# ── spawn Go backend on the internal port ──────────────────────────
echo "→ Go backend on internal port :$NC_GO_PORT (HTTP :$NC_HTTP_PORT)"
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_GO_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$GO_BIN" &
GO_PID=$!

# ── wait until Go's RESP port is up before starting Rust ───────────
# Otherwise Rust's first proxied command would fail until Go binds.
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if redis-cli -p "$NC_GO_PORT" PING >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

# ── spawn Rust front-end with proxy to Go ──────────────────────────
echo "→ Rust hot path on public port :$NC_PUBLIC_PORT"
NEUROCACHE_HOTPATH_ADDR="0.0.0.0:$NC_PUBLIC_PORT" \
  NEUROCACHE_HOTPATH_PROXY_TO="127.0.0.1:$NC_GO_PORT" \
  "$RUST_BIN" &
RUST_PID=$!

# ── orchestration ──────────────────────────────────────────────────
shutdown() {
  echo
  echo "→ shutting down…"
  kill "$RUST_PID" 2>/dev/null || true
  kill "$GO_PID" 2>/dev/null || true
  wait 2>/dev/null || true
  exit 0
}
trap shutdown SIGINT SIGTERM

cat <<EOF

NeuroCache integrated stack ready:
  → redis-cli   -p $NC_PUBLIC_PORT
  → dashboard   http://localhost:$NC_HTTP_PORT

Hot-path commands (GET / SET / INCR / LPUSH / HSET / SADD / …) run on Rust.
AI commands (SEMANTIC_*, MEMORY.*, TOOL.*, GUARD.*, SEMNEG.*, PROMPT.*,
LLM.ROUTE.*, INJECT.*) and advanced standard commands transparently
proxy to Go. Clients only ever touch port $NC_PUBLIC_PORT.

Ctrl-C to stop.
EOF

# Wait on either process exiting; if one dies, kill the other.
wait -n "$GO_PID" "$RUST_PID"
shutdown
