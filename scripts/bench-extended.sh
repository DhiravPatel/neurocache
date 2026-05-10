#!/usr/bin/env bash
# Extended benchmark — exercises commands beyond the redis-benchmark
# default `-t` set. Built so the sales team can answer "does X beat
# Redis?" with empirical numbers for ~50 commands.
#
# How it works: redis-benchmark accepts a custom command via `-r N`
# (random key range) and the command itself as bare args. We use that
# to measure throughput of commands the default suite skips.
#
# Output: one line per command, ratio of NeuroCache / Redis.
set -euo pipefail

REDIS_PORT=${REDIS_PORT:-16380}
NC_PUBLIC_PORT=${NC_PUBLIC_PORT:-16399}
NC_GO_PORT=${NC_GO_PORT:-16398}
NC_HTTP_PORT=18099
NC_DATA=/tmp/nc-bench-ext-data
NC_GO_BINARY=/tmp/neurocache-bench-ext
NC_RUST_BINARY=/Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/rust-hotpath/target/release/neurocache-hotpath
N=${N:-100000}
C=${C:-50}
P=${P:-16}

echo "Building NeuroCache (Go)..."
( cd /Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/api && go build -o "$NC_GO_BINARY" ./cmd/server )

echo "Building NeuroCache hot path (Rust)..."
( cd /Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/rust-hotpath && cargo build --release 2>&1 | tail -3 )

# clean state
pkill -f "$NC_GO_BINARY" 2>/dev/null || true
pkill -f "neurocache-hotpath" 2>/dev/null || true
redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE 2>/dev/null || true
rm -rf "$NC_DATA"

# servers
redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_GO_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_GO_BINARY" >/tmp/nc-go-ext.log 2>&1 &
GO_PID=$!
for _ in 1 2 3 4 5; do
  redis-cli -p "$NC_GO_PORT" PING >/dev/null 2>&1 && break
  sleep 0.3
done
NEUROCACHE_HOTPATH_ADDR="127.0.0.1:$NC_PUBLIC_PORT" \
  NEUROCACHE_HOTPATH_PROXY_TO="127.0.0.1:$NC_GO_PORT" \
  "$NC_RUST_BINARY" >/tmp/nc-rust-ext.log 2>&1 &
RUST_PID=$!
sleep 1
redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_PUBLIC_PORT" PING >/dev/null

trap '
  redis-cli -p '"$REDIS_PORT"' SHUTDOWN NOSAVE 2>/dev/null || true
  kill '"$GO_PID"' 2>/dev/null || true
  kill '"$RUST_PID"' 2>/dev/null || true
  rm -rf '"$NC_DATA $NC_GO_BINARY"' /tmp/nc-go-ext.log /tmp/nc-rust-ext.log
' EXIT

# Pre-seed both servers with data the read-only commands need.
# Includes data for the extended bench (set algebra, zset extras,
# stream ops) so all rows have something to read.
seed() {
  local port=$1
  redis-cli -p "$port" SET strkey "hello world this is a test value" >/dev/null
  redis-cli -p "$port" SET counter 1000 >/dev/null
  redis-cli -p "$port" SET intkey 12345 >/dev/null
  redis-cli -p "$port" SET floatkey 3.14 >/dev/null
  redis-cli -p "$port" HMSET hkey f1 v1 f2 v2 f3 v3 f4 v4 f5 v5 >/dev/null 2>&1
  redis-cli -p "$port" HMSET hbig n1 1 n2 2 n3 3 n4 4 n5 5 n6 6 n7 7 n8 8 n9 9 n10 10 >/dev/null 2>&1
  redis-cli -p "$port" RPUSH lkey a b c d e f g h i j >/dev/null
  redis-cli -p "$port" RPUSH lkey2 1 2 3 4 5 >/dev/null
  redis-cli -p "$port" SADD skey a b c d e >/dev/null
  redis-cli -p "$port" SADD skey2 c d e f g >/dev/null
  redis-cli -p "$port" SADD skey3 a >/dev/null
  redis-cli -p "$port" ZADD zkey 100 a 200 b 300 c 400 d 500 e >/dev/null
  redis-cli -p "$port" ZADD zkey2 50 a 150 c 250 e 350 g >/dev/null
  # Seed range for RENAME source (we use random suffix in the bench)
  for i in $(seq 0 99); do
    redis-cli -p "$port" SET "pmove:$i" v >/dev/null
  done
}
seed "$REDIS_PORT"
seed "$NC_PUBLIC_PORT"

# Per-command bench helper. Returns rps as int.
bench_one() {
  local port=$1; shift
  local cmd="$*"
  local out
  out=$(redis-benchmark -p "$port" -q -n "$N" -c "$C" -P "$P" $cmd 2>/dev/null \
        | tail -1 \
        | grep -oE '[0-9]+\.[0-9]+ requests per second' \
        | awk '{print $1}')
  if [ -z "$out" ]; then echo 0; else echo "$out"; fi
}

# Each row: HUMAN_NAME COMMAND_AND_ARGS
# Includes commands handled locally by Rust + commands proxied to
# Go. Proxy commands exercise the batched-pipelined upstream path,
# so the bench surface what's actually achievable end-to-end.
declare -a TESTS=(
  # ── strings (Rust local) ──
  "STRLEN|STRLEN strkey"
  "APPEND|APPEND strkey x"
  "GETRANGE|GETRANGE strkey 0 10"
  "SETNX|SETNX newkey:__rand_int__ v"
  "GETSET|GETSET strkey v"
  "GETDEL|GETDEL gdkey:__rand_int__"
  "BITCOUNT|BITCOUNT strkey"
  "INCRBY|INCRBY counter 1"
  "DECRBY|DECRBY counter 1"
  "DECR|DECR counter"
  "EXISTS|EXISTS strkey"
  "DEL|DEL delkey:__rand_int__"
  # ── hashes (Rust local) ──
  "HMGET|HMGET hkey f1 f2 f3"
  "HINCRBY|HINCRBY hkey counter 1"
  "HSETNX|HSETNX hkey newf:__rand_int__ v"
  "HLEN|HLEN hkey"
  "HEXISTS|HEXISTS hkey f1"
  "HKEYS|HKEYS hkey"
  "HVALS|HVALS hkey"
  "HGETALL|HGETALL hkey"
  "HGET|HGET hkey f1"
  "HDEL|HDEL hkey newf:__rand_int__"
  # ── lists (Rust local) ──
  "LSET|LSET lkey 2 X"
  "LREM|LREM lkey 0 missing"
  "LTRIM|LTRIM lkey 0 9"
  "LINSERT|LINSERT lkey BEFORE c Y"
  "LLEN|LLEN lkey"
  "LINDEX|LINDEX lkey 5"
  "LRANGE_FULL|LRANGE lkey 0 -1"
  "LPUSH|LPUSH pq:__rand_int__ x"
  "RPUSH|RPUSH pq:__rand_int__ x"
  "LPOP|LPOP pq:__rand_int__"
  "RPOP|RPOP pq:__rand_int__"
  # ── sets (Rust local) ──
  "SCARD|SCARD skey"
  "SISMEMBER|SISMEMBER skey a"
  "SRANDMEMBER|SRANDMEMBER skey"
  "SADD|SADD ps:__rand_int__ x"
  "SREM|SREM ps:__rand_int__ x"
  "SPOP|SPOP ps:__rand_int__"
  "SMEMBERS|SMEMBERS skey"
  # ── sorted sets (Rust local) ──
  "ZCARD|ZCARD zkey"
  "ZSCORE|ZSCORE zkey a"
  "ZINCRBY|ZINCRBY zkey 1 a"
  "ZRANGEBYSCORE|ZRANGEBYSCORE zkey 100 400"
  "ZRANK|ZRANK zkey c"
  "ZREVRANK|ZREVRANK zkey c"
  "ZRANGE|ZRANGE zkey 0 -1"
  "ZADD|ZADD pz:__rand_int__ 1 a"
  "ZREM|ZREM pz:__rand_int__ a"
  "ZPOPMIN|ZPOPMIN pz:__rand_int__"
  # ── streams (Rust local) ──
  "XADD|XADD pxs:__rand_int__ * f v"
  "XLEN|XLEN pxs:__rand_int__"
  # ── connection / introspection (proxied to Go) ──
  "TYPE|TYPE strkey"
  "TTL|TTL strkey"
  "PTTL|PTTL strkey"
  "OBJECT_REFCOUNT|OBJECT REFCOUNT strkey"
  "OBJECT_ENCODING|OBJECT ENCODING strkey"
  "DBSIZE|DBSIZE"
  "RANDOMKEY|RANDOMKEY"
  # ── proxied modifiers ──
  "EXPIRE|EXPIRE strkey 1000"
  "PEXPIRE|PEXPIRE strkey 1000000"
  "PERSIST|PERSIST strkey"
  "RENAME|RENAME pmove:__rand_int__ pdest:__rand_int__"
  # ── float ops (proxied) ──
  "INCRBYFLOAT|INCRBYFLOAT floatkey 0.1"
  "HINCRBYFLOAT|HINCRBYFLOAT hkey f1 0.1"
)

echo
printf "%-20s %12s %12s   %s\n" "command" "redis (rps)" "neuro (rps)" "Neuro/Redis"
printf "%-20s %12s %12s   %s\n" "-------" "-----------" "-----------" "-----------"
won=0
total=0
for row in "${TESTS[@]}"; do
  name=${row%%|*}
  args=${row#*|}
  r=$(bench_one "$REDIS_PORT" $args)
  n=$(bench_one "$NC_PUBLIC_PORT" $args)
  if [ "$r" = "0" ] || [ "$n" = "0" ]; then
    printf "%-20s %12s %12s   ?? skipped (bench unsupported)\n" "$name" "$r" "$n"
    continue
  fi
  total=$((total + 1))
  pct=$(awk "BEGIN { printf \"%.1f\", ($n / $r) * 100 }")
  flag="  "
  case 1 in
    $(echo "$pct >= 100" | bc -l)) flag="★ "; won=$((won + 1)) ;;
  esac
  printf "%-20s %12.0f %12.0f   %s%6.1f%%\n" "$name" "$r" "$n" "$flag" "$pct"
done
printf "\n  ★ = beats Redis    (%d won, %d behind out of %d benchable commands)\n" "$won" "$((total - won))" "$total"
