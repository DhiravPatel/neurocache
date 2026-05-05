#!/usr/bin/env bash
# Pipelined head-to-head bench vs Redis — production-shaped workload.
#
# Real-world clients (ioredis, go-redis, jedis) all pipeline by default;
# the unpipelined bench-vs-redis.sh measures worst-case per-command
# round-trip cost, while this script measures what apps actually see.
# We default to -P 16 (16 commands per round-trip) which matches what
# most production drivers emit during burst traffic.
#
# Usage:
#   scripts/bench-pipelined-vs-redis.sh
#   P=64 N=500000 scripts/bench-pipelined-vs-redis.sh
#
# Requires: redis-server, redis-benchmark (brew install redis), Go.
set -euo pipefail
REDIS_PORT=${REDIS_PORT:-16380}
NC_PORT=${NC_PORT:-16399}
NC_HTTP_PORT=18099
NC_DATA=/tmp/nc-bench-data
NC_BINARY=/tmp/neurocache-bench
N=${N:-200000}
C=${C:-50}
P=${P:-16}

echo "Building neurocache..."
( cd /Users/dhiravpatel/Documents/Project\ 2/neurocache/apps/api && go build -o "$NC_BINARY" ./cmd/server )

pkill -f "$NC_BINARY" 2>/dev/null || true
redis-cli -p "$REDIS_PORT" SHUTDOWN NOSAVE 2>/dev/null || true
rm -rf "$NC_DATA"

redis-server --port "$REDIS_PORT" --daemonize yes --dir /tmp >/dev/null
NEUROCACHE_HTTP_PORT="$NC_HTTP_PORT" \
  NEUROCACHE_RESP_PORT="$NC_PORT" \
  NEUROCACHE_DATA_DIR="$NC_DATA" \
  "$NC_BINARY" >/tmp/nc-bench.log 2>&1 &
NC_PID=$!
sleep 1.5
redis-cli -p "$REDIS_PORT" PING >/dev/null
redis-cli -p "$NC_PORT" PING >/dev/null
trap "redis-cli -p $REDIS_PORT SHUTDOWN NOSAVE 2>/dev/null; kill $NC_PID 2>/dev/null; rm -rf $NC_DATA $NC_BINARY /tmp/nc-bench.log /tmp/bench-redis.csv /tmp/bench-nc.csv" EXIT

CMDS="set,get,incr,lpush,rpush,lpop,rpop,sadd,hset,zadd,spop,mset"
echo "Pipelined bench: -n $N -c $C -P $P"
redis-benchmark -p "$REDIS_PORT" -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-redis.csv
redis-benchmark -p "$NC_PORT"    -q -n "$N" -c "$C" -P "$P" -t "$CMDS" --csv 2>/dev/null > /tmp/bench-nc.csv

awk -F, 'function clean(s){gsub(/"/,"",s);return s}
  FNR==1{idx++;next}
  idx==1{redis[clean($1)]=clean($2);order[++n]=clean($1);next}
  idx==2{nc[clean($1)]=clean($2)}
  END{
    printf "\n%-18s %12s %12s %10s   %s\n","command","redis (rps)","neurocache","nc/redis","verdict"
    printf "%-18s %12s %12s %10s   %s\n","-------","-----------","----------","--------","-------"
    for(i=1;i<=n;i++){cmd=order[i];r=redis[cmd]+0;m=(cmd in nc)?(nc[cmd]+0):0
      pct=(r>0)?(m/r*100):0;v=(pct>=100)?"BEATS REDIS":(pct>=80?"ok":"warn")
      printf "%-18s %12.0f %12.0f %9.1f%%   %s\n",cmd,r,m,pct,v}}' /tmp/bench-redis.csv /tmp/bench-nc.csv
