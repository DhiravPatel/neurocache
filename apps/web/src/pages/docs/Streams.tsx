import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function StreamsDocs() {
  return (
    <>
      <h1>Streams</h1>
      <p className="lead">
        Streams are an append-only log keyed by monotonically increasing IDs —
        an event history you can range-scan, replay, and follow live. The full
        Redis stream surface (including consumer groups) is on the RESP port; the
        HTTP API adds JSON append/range plus a Server-Sent Events tail.
      </p>

      <h2>Append &amp; read</h2>
      <Code lang="bash">{`# RESP — the full stream command set
XADD events '*' type signup user 42      # '*' = auto id
XRANGE events - +                         # whole stream, oldest-first
XLEN events
XREAD COUNT 10 STREAMS events 0           # read after id 0
# consumer groups for at-least-once fan-out:
XGROUP CREATE events workers $
XREADGROUP GROUP workers w1 COUNT 10 STREAMS events '>'
XACK events workers 1690000000000-0

# HTTP — JSON append + range
curl localhost:8080/api/streams/events -d '{"fields":{"type":"signup","user":"42"}}'
# → {"id":"1690000000000-0"}
curl "localhost:8080/api/streams/events?count=50"       # newest-first range
curl localhost:8080/api/streams/events/len`}</Code>

      <h2>Follow live (SSE)</h2>
      <p>
        <code>GET /api/streams/&#123;key&#125;/tail</code> is a Server-Sent
        Events stream of new entries. <code>last=$</code> (default) sends only
        entries added from now on; <code>last=0</code> replays the whole log then
        follows.
      </p>
      <Code lang="ts">{`// Append
await cache.streams.add("events", { type: "signup", user: "42" });

// Range scan (defaults to the whole stream, oldest-first)
const { entries } = await cache.streams.range("events", { count: 50, reverse: true });

// Follow live — returns a handle; call close() to stop
const sub = cache.streams.tail("events", (entry) => {
  console.log(entry.id, entry.fields);
}, { last: "$" });
// …later
sub.close();`}</Code>
      <Callout type="tip" title="Streams vs. Pub/Sub vs. Queues">
        <Link to="/docs/pubsub">Pub/Sub</Link> is fire-and-forget (no history).
        Streams keep an append-only history you can replay and fan out with
        consumer groups. <Link to="/docs/queues">Queues</Link> add retry/DLQ/
        visibility for a worker pool. Pick streams when you need an auditable,
        replayable event log.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/streams">Streams inspector</Link> lets you
        append entries, scan history, and tail a stream live.
      </p>
    </>
  );
}
