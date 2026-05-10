#!/usr/bin/env bash
# Three-way pipelined bench: Redis vs NeuroCache (Go) vs NeuroCache
# Rust hot path. Restricted to the commands the Phase-1 Rust binary
# implements (PING, GET, SET, INCR, DECR, INCRBY, DEL, EXISTS).
#
# Usage:
#   scripts/bench-rust-vs-go-vs-redis.sh
#   P=64 N=500000 scripts/bench-rust-vs-go-vs-redis.sh
#
# Requires: redis-server, redis-benchmark, Go, Rust toolchain.
set -euo pipefail

REDIS_PORT=${REDIS_PORT:-16380}
NC_GO_PORT=${NC_GO_PORT:-16399}
NC_RUST_PORT=${NC_RUST_PORT:-16398}
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

# Start all three servers
redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null

NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_GO_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_GO_BINARY" >/tmp/nc-go-bench.log 2>&1 &
NC_GO_PID=$!

NEUROCACHE_HOTPATH_ADDR="127.0.0.1:$NC_RUST_PORT" \
  "$NC_RUST_BINARY" >/tmp/nc-rust-bench.log 2>&1 &
NC_RUST_PID=$!

sleep 1.5

# Verify all three are alive
redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_GO_PORT" PING >/dev/null
redis-cli -p "$NC_RUST_PORT" PING >/dev/null

trap '
  redis-cli -p '"$REDIS_PORT"' SHUTDOWN NOSAVE 2>/dev/null || true
  kill '"$NC_GO_PID"' 2>/dev/null || true
  kill '"$NC_RUST_PID"' 2>/dev/null || true
  rm -rf '"$NC_DATA $NC_GO_BINARY"' /tmp/nc-go-bench.log /tmp/nc-rust-bench.log /tmp/bench-redis.csv /tmp/bench-go.csv /tmp/bench-rust.csv
' EXIT

# Phase-2 Rust commands — strings + lists + hashes + sets
CMDS="ping,get,set,incr,lpush,rpush,lpop,rpop,sadd,hset,spop,mset"

echo "Running benches: -n $N -c $C -P $P"
redis-benchmark -p "$REDIS_PORT"   -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-redis.csv
redis-benchmark -p "$NC_GO_PORT"   -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-go.csv
redis-benchmark -p "$NC_RUST_PORT" -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-rust.csv

awk -F, '
  function clean(s) { gsub(/"/, "", s); return s }
  FNR == 1 { idx++; next }
  idx == 1 { redis[clean($1)] = clean($2); order[++n] = clean($1); next }
  idx == 2 { go[clean($1)] = clean($2); next }
  idx == 3 { rust[clean($1)] = clean($2) }
  END {
    printf "\n%-12s %12s %12s %12s   %s   %s\n",
      "command", "redis (rps)", "Go (rps)", "Rust (rps)", "Go/Redis", "Rust/Redis"
    printf "%-12s %12s %12s %12s   %s   %s\n",
      "-------", "-----------", "----------", "-----------", "--------", "---------"
    for (i = 1; i <= n; i++) {
      cmd = order[i]
      r = redis[cmd] + 0
      g = go[cmd] + 0
      ru = rust[cmd] + 0
      gpct  = (r > 0) ? (g / r * 100) : 0
      rupct = (r > 0) ? (ru / r * 100) : 0
      gflag  = (gpct  >= 100) ? "★ " : "  "
      ruflag = (rupct >= 100) ? "★ " : "  "
      printf "%-12s %12.0f %12.0f %12.0f   %s%6.1f%%   %s%6.1f%%\n",
        cmd, r, g, ru, gflag, gpct, ruflag, rupct
    }
    print "\n  ★ = beats Redis"
  }
' /tmp/bench-redis.csv /tmp/bench-go.csv /tmp/bench-rust.csv
