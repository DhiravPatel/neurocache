#!/usr/bin/env bash
# Run redis-benchmark with NO -t filter — exercises every command
# the bench supports: PING, SET, GET, INCR, LPUSH, RPUSH, LPOP, RPOP,
# SADD, HSET, SPOP, ZADD, LRANGE_100, LRANGE_300, LRANGE_500,
# LRANGE_600, MSET. The full default suite.
#
# Compares Redis vs the integrated NeuroCache stack (Rust front +
# Go backend on internal port). Goal: every single command in
# every-default beats Redis.
set -euo pipefail

REDIS_PORT=${REDIS_PORT:-16380}
NC_PUBLIC_PORT=${NC_PUBLIC_PORT:-16399}
NC_GO_PORT=${NC_GO_PORT:-16398}
NC_HTTP_PORT=18099
NC_DATA=/tmp/nc-bench-all-data
NC_GO_BINARY=/tmp/neurocache-bench-all
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

# Start Redis
redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null

# Start Go backend
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_GO_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_GO_BINARY" >/tmp/nc-go-all.log 2>&1 &
GO_PID=$!

# Wait for Go to bind
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if redis-cli -p "$NC_GO_PORT" PING >/dev/null 2>&1; then break; fi
  sleep 0.3
done

# Start Rust front-end with proxy → Go
NEUROCACHE_HOTPATH_ADDR="127.0.0.1:$NC_PUBLIC_PORT" \
  NEUROCACHE_HOTPATH_PROXY_TO="127.0.0.1:$NC_GO_PORT" \
  "$NC_RUST_BINARY" >/tmp/nc-rust-all.log 2>&1 &
RUST_PID=$!

sleep 1

redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_PUBLIC_PORT" PING >/dev/null

trap '
  redis-cli -p '"$REDIS_PORT"' SHUTDOWN NOSAVE 2>/dev/null || true
  kill '"$GO_PID"' 2>/dev/null || true
  kill '"$RUST_PID"' 2>/dev/null || true
  rm -rf '"$NC_DATA $NC_GO_BINARY"' /tmp/nc-go-all.log /tmp/nc-rust-all.log /tmp/bench-all-redis.csv /tmp/bench-all-nc.csv
' EXIT

# NO -t filter — exercises every command in the default suite
echo "Running benches: -n $N -c $C -P $P (every default command)"
redis-benchmark -p "$REDIS_PORT"      -q -n "$N" -c "$C" -P "$P" --csv 2>/dev/null > /tmp/bench-all-redis.csv
redis-benchmark -p "$NC_PUBLIC_PORT"  -q -n "$N" -c "$C" -P "$P" --csv 2>/dev/null > /tmp/bench-all-nc.csv

awk -F, '
  function clean(s) { gsub(/"/, "", s); return s }
  FNR == 1 { idx++; next }
  idx == 1 { redis[clean($1)] = clean($2); order[++n] = clean($1); next }
  idx == 2 { nc[clean($1)] = clean($2) }
  END {
    printf "\n%-15s %12s %12s   %s\n",
      "command", "redis (rps)", "neuro (rps)", "Neuro/Redis"
    printf "%-15s %12s %12s   %s\n",
      "-------", "-----------", "----------", "-----------"
    won = 0
    lost = 0
    for (i = 1; i <= n; i++) {
      cmd = order[i]
      r = redis[cmd] + 0
      nv = nc[cmd] + 0
      pct = (r > 0) ? (nv / r * 100) : 0
      flag = (pct >= 100) ? "★ " : "  "
      if (pct >= 100) won++; else lost++
      printf "%-15s %12.0f %12.0f   %s%6.1f%%\n", cmd, r, nv, flag, pct
    }
    printf "\n  ★ = beats Redis    (%d won, %d lost out of %d total)\n", won, lost, n
  }
' /tmp/bench-all-redis.csv /tmp/bench-all-nc.csv
