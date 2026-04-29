#!/usr/bin/env bash
# Head-to-head perf comparison: Redis vs NeuroCache.
#
# Run before merging anything that touches the store hot path. The
# absolute numbers vary by machine — what matters is the *ratio*.
# Pre-fix LPUSH was at 3% of Redis (regression bug in recomputeBytes);
# post-fix it should be 65-80%, in line with every other command.
#
# Usage:
#   scripts/bench-vs-redis.sh
#
# Requires: redis-server, redis-benchmark (brew install redis), and Go
# in PATH so the script can rebuild NeuroCache before running.

set -euo pipefail

REDIS_PORT=${REDIS_PORT:-16380}
NC_PORT=${NC_PORT:-16399}
NC_HTTP_PORT=${NC_HTTP_PORT:-18099}
NC_DATA=${NC_DATA:-/tmp/nc-bench-data}
NC_BINARY=${NC_BINARY:-/tmp/neurocache-bench}
N=${N:-100000}
C=${C:-50}

echo "Building neurocache..."
( cd "$(dirname "$0")/../apps/api" && go build -o "$NC_BINARY" ./cmd/server )

# clean state
pkill -f "$NC_BINARY" 2>/dev/null || true
redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE 2>/dev/null || true
rm -rf "$NC_DATA"

# start servers
redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_BINARY" >/tmp/nc-bench.log 2>&1 &
NC_PID=$!
sleep 1.5

# verify both are alive
redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_PORT" PING >/dev/null

cleanup() {
  redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE 2>/dev/null || true
  kill "$NC_PID" 2>/dev/null || true
  rm -rf "$NC_DATA" "$NC_BINARY" /tmp/nc-bench.log /tmp/bench-redis.csv /tmp/bench-nc.csv
}
trap cleanup EXIT

CMDS="set,get,incr,lpush,rpush,lpop,rpop,sadd,hset,zadd,spop,mset"

# --csv yields one line per command: "command","rps". Survives
# pipeline-buffering quirks that the human-readable output has.
redis-benchmark -p "$REDIS_PORT" -q -n "$N" -c "$C" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-redis.csv
redis-benchmark -p "$NC_PORT"    -q -n "$N" -c "$C" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-nc.csv

awk -F, '
  function clean(s) { gsub(/"/, "", s); return s }
  FNR==1 { idx++; next }                              # skip CSV header
  idx==1 { redis[clean($1)] = clean($2); order[++n] = clean($1); next }
  idx==2 { nc[clean($1)]    = clean($2) }
  END {
    printf "\n%-18s %12s %12s %10s   %s\n",
      "command", "redis (rps)", "neurocache", "nc/redis", "verdict"
    printf "%-18s %12s %12s %10s   %s\n",
      "-------", "-----------", "----------", "--------", "-------"
    for (i = 1; i <= n; i++) {
      cmd = order[i]
      r = redis[cmd] + 0
      m = (cmd in nc) ? (nc[cmd] + 0) : 0
      pct = (r > 0) ? (m / r * 100) : 0
      verdict = (pct >= 65) ? "ok" : (pct >= 30 ? "warn" : "REGRESSION")
      printf "%-18s %12.0f %12.0f %9.1f%%   %s\n", cmd, r, m, pct, verdict
    }
  }
' /tmp/bench-redis.csv /tmp/bench-nc.csv

echo
echo "Goal: NeuroCache should run at 65-80% of Redis throughput on every command."
echo "Anything flagged REGRESSION is a perf bug — fix before merging."
