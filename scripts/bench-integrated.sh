#!/usr/bin/env bash
# Bench the integrated stack (Rust front-end + Go backend on one
# public port) vs Redis. Exercises the production deployment shape:
# every client connects to a single port; fast commands run on the
# Rust hot path, AI/advanced commands proxy to Go.
#
# Usage:
#   scripts/bench-integrated.sh
#   P=64 N=500000 scripts/bench-integrated.sh
set -euo pipefail

REDIS_PORT=${REDIS_PORT:-16380}
NC_INT_PORT=${NC_INT_PORT:-16399}
NC_GO_PORT=${NC_GO_PORT:-16398}
NC_HTTP_PORT=18099
NC_DATA=/tmp/nc-bench-data
NC_GO_BINARY=/tmp/neurocache-bench
NC_RUST_BINARY=/Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/rust-hotpath/target/release/neurocache-hotpath
N=${N:-200000}
C=${C:-50}
P=${P:-16}

echo "Building NeuroCache (Go)..."
( cd /Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/api && go build -o "$NC_GO_BINARY" ./cmd/server )

echo "Building NeuroCache hot path (Rust)..."
( cd /Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/rust-hotpath && cargo build --release 2>&1 | tail -3 )

# Clean state
pkill -f "$NC_GO_BINARY" 2>/dev/null || true
pkill -f "neurocache-hotpath" 2>/dev/null || true
redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE 2>/dev/null || true
rm -rf "$NC_DATA"

# Start Redis (the comparison baseline)
redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null

# Start Go backend (internal, will receive proxied AI cmds)
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_GO_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_GO_BINARY" >/tmp/nc-go-bench.log 2>&1 &
NC_GO_PID=$!

# Wait for Go to be reachable before starting Rust (Rust opens lazy
# proxy conns; nothing to wait on there, but the Go side needs to be
# listening before the first AI command arrives).
for _ in 1 2 3 4 5; do
  if redis-cli -p "$NC_GO_PORT" PING >/dev/null 2>&1; then break; fi
  sleep 0.3
done

# Start Rust front-end with proxy → Go
NEUROCACHE_HOTPATH_ADDR="127.0.0.1:$NC_INT_PORT" \
  NEUROCACHE_HOTPATH_PROXY_TO="127.0.0.1:$NC_GO_PORT" \
  "$NC_RUST_BINARY" >/tmp/nc-rust-bench.log 2>&1 &
NC_RUST_PID=$!

sleep 1

# Verify both alive
redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_INT_PORT" PING >/dev/null

trap '
  redis-cli -p '"$REDIS_PORT"' SHUTDOWN NOSAVE 2>/dev/null || true
  kill '"$NC_GO_PID"' 2>/dev/null || true
  kill '"$NC_RUST_PID"' 2>/dev/null || true
  rm -rf '"$NC_DATA $NC_GO_BINARY"' /tmp/nc-go-bench.log /tmp/nc-rust-bench.log /tmp/bench-redis.csv /tmp/bench-int.csv
' EXIT

# Phase-2 fast-path commands the bench exercises
CMDS="ping,get,set,incr,lpush,rpush,lpop,rpop,sadd,hset,spop,mset"

echo "Running benches: -n $N -c $C -P $P"
redis-benchmark -p "$REDIS_PORT"     -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-redis.csv
redis-benchmark -p "$NC_INT_PORT"    -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-int.csv

awk -F, '
  function clean(s) { gsub(/"/, "", s); return s }
  FNR == 1 { idx++; next }
  idx == 1 { redis[clean($1)] = clean($2); order[++n] = clean($1); next }
  idx == 2 { integ[clean($1)] = clean($2) }
  END {
    printf "\n%-12s %12s %12s   %s\n",
      "command", "redis (rps)", "integ (rps)", "Integrated/Redis"
    printf "%-12s %12s %12s   %s\n",
      "-------", "-----------", "----------", "----------------"
    for (i = 1; i <= n; i++) {
      cmd = order[i]
      r = redis[cmd] + 0
      it = integ[cmd] + 0
      pct = (r > 0) ? (it / r * 100) : 0
      flag = (pct >= 100) ? "★ " : "  "
      printf "%-12s %12.0f %12.0f   %s%6.1f%%\n", cmd, r, it, flag, pct
    }
    print "\n  ★ = beats Redis (integrated stack: Rust front, Go backend)"
  }
' /tmp/bench-redis.csv /tmp/bench-int.csv
