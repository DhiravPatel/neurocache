import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function PipeliningDocs() {
  return (
    <>
      <h1>Pipelining &amp; throughput</h1>
      <p className="lead">
        The single biggest throughput lever for any Redis-style store is
        pipelining — sending many commands without waiting for each reply.
        NeuroCache pipelines on the RESP wire automatically, and exposes the
        same idea over HTTP so the SDK can collapse N round-trips into one.
      </p>

      <Callout type="tip" title="Why it's so much faster">
        Per-command latency is dominated by the round-trip, not the work: the
        engine serves a <code>SET</code> in microseconds, but a network
        round-trip (plus HTTP headers and TLS) is often a millisecond or more.
        Batch 500 commands into one request and you pay that overhead once
        instead of 500 times.
      </Callout>

      <h2>On the RESP wire</h2>
      <p>
        Any Redis client already pipelines — write multiple commands, then read
        the replies in order. NeuroCache reads as many commands as are buffered
        before flushing replies, so standard client pipelining "just works":
      </p>
      <Code lang="bash">{`# redis-cli pipe mode — thousands of commands, one connection, one flush
(for i in $(seq 1 100000); do echo "SET key:$i $i"; done) | redis-cli -p 6379 --pipe`}</Code>

      <h2>Over HTTP</h2>
      <p>
        <code>/api/exec</code> runs one command per request. For many ops, use{" "}
        <code>/api/pipeline</code> — an ordered list of commands in a single
        request, with one result per command returned in order:
      </p>
      <Code lang="bash">{`curl -s localhost:8080/api/pipeline -d '{
  "commands": [
    ["SET", "user:1:name", "Ada"],
    ["INCR", "visits"],
    ["GET", "user:1:name"]
  ]
}'
# → {"results":[{"ok":true,"result":"OK"},{"ok":true,"result":1},{"ok":true,"result":"Ada"}]}`}</Code>
      <p>
        Each command is guarded exactly like <code>/api/exec</code> (admin /
        replication verbs are refused), and writes are recorded for persistence
        and replication. Pass <code>"stop_on_error": true</code> to abort at the
        first failing command.
      </p>

      <h2>From the SDK</h2>
      <p>
        <code>pipeline()</code> returns a builder — chain commands, then{" "}
        <code>exec()</code> sends them together and resolves to the results in
        order:
      </p>
      <Code lang="ts">{`const results = await cache.pipeline()
  .set("user:1:name", "Ada")
  .incr("visits")
  .get("user:1:name")
  .add("EXPIRE", "user:1:name", 3600)   // any command via .add()
  .exec();

// results[2] → { ok: true, result: "Ada" }`}</Code>

      <h2>Measure it</h2>
      <p>
        The repo ships a benchmark that times N individual requests against the
        same N commands pipelined into one:
      </p>
      <Code lang="bash">{`N=2000 node scripts/bench-pipeline.mjs
#   individual /api/exec : … ms   (…  ops/sec)
#   single /api/pipeline : … ms   (… ops/sec)
#   → N× faster wall-clock, N× the throughput`}</Code>
      <Callout type="info" title="What about transactions?">
        Pipelining batches commands for throughput; it does not make them
        atomic. For all-or-nothing semantics use{" "}
        <code>MULTI</code> / <code>EXEC</code> (with optional{" "}
        <code>WATCH</code> for optimistic locking) on the RESP connection, or a{" "}
        <Link to="/docs/locks">distributed lock</Link> to serialize a critical
        section across processes.
      </Callout>

      <h2>Command coverage</h2>
      <p>
        Pipelines accept the full command surface — the complete Redis 8.6 /
        Valkey 8.0 / DiceDB set (~640 commands, all 12 data types, the stack
        modules) plus NeuroCache's AI-native commands. See the{" "}
        <Link to="/docs/commands">command reference</Link> for everything you
        can batch.
      </p>
    </>
  );
}
