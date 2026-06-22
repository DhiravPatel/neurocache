import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function PubSubDocs() {
  return (
    <>
      <h1>Pub/Sub</h1>
      <p className="lead">
        NeuroCache speaks the full Redis publish/subscribe surface over the
        RESP port, and bridges it to the web with a REST publish endpoint and a
        Server-Sent Events subscribe stream — so browsers and the SDK can join
        the same channels as your <code>redis-cli</code> clients.
      </p>

      <h2>Over RESP (any Redis client)</h2>
      <p>
        Everything Redis pub/sub does works unchanged — exact channels,
        glob patterns, and sharded pub/sub:
      </p>
      <Code lang="bash">{`# terminal 1 — subscribe
redis-cli -p 6379 SUBSCRIBE news
redis-cli -p 6379 PSUBSCRIBE 'room.*'      # glob patterns

# terminal 2 — publish
redis-cli -p 6379 PUBLISH news "hello"     # → (integer) 1  (receivers)

# introspect
redis-cli -p 6379 PUBSUB CHANNELS
redis-cli -p 6379 PUBSUB NUMSUB news
redis-cli -p 6379 PUBSUB NUMPAT

# sharded pub/sub
redis-cli -p 6379 SSUBSCRIBE shard.1
redis-cli -p 6379 SPUBLISH shard.1 "hi"`}</Code>

      <h2>Over HTTP &amp; the browser</h2>
      <p>
        Publishing is a plain <code>POST</code>. Subscribing needs a streaming
        transport, so it is exposed as <strong>Server-Sent Events</strong> —
        one long-lived <code>GET</code> that emits a JSON message per event:
      </p>
      <Code lang="bash">{`# Publish — returns how many subscribers received it
curl -s localhost:8080/api/publish \\
  -d '{"channel":"news","message":"hello"}'
# → {"receivers":1}

# Subscribe — a Server-Sent Events stream (one or more channels / patterns)
curl -N "localhost:8080/api/subscribe?channel=news&pattern=room.*"
# event: subscribed
# data: {"channels":["news"],"patterns":["room.*"]}
#
# data: {"channel":"news","pattern":"","payload":"hello"}

# Introspect
curl -s "localhost:8080/api/pubsub/channels?pattern=*"
# → {"channels":["news"],"num_subs":{"news":1},"num_patterns":1}`}</Code>
      <Callout type="info" title="One broker, two doors">
        RESP and HTTP share the same broker. A message <code>PUBLISH</code>ed
        from <code>redis-cli</code> is delivered to HTTP/SSE subscribers, and a
        message published over <code>/api/publish</code> reaches RESP
        subscribers — mix and match freely.
      </Callout>

      <h2>From the SDK</h2>
      <p>
        The TypeScript SDK wraps the stream so you get a typed callback and a
        handle to stop. It works in the browser and in Node 18+:
      </p>
      <Code lang="ts">{`import { NeuroCache } from "@neurocache/sdk";

const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

// Subscribe to channels and/or glob patterns
const sub = cache.subscribe(
  { channels: ["news"], patterns: ["room.*"] },
  (msg) => console.log(msg.channel, msg.payload),
  { onOpen: () => console.log("connected") },
);

// Publish — resolves with the receiver count
await cache.publish("news", "hello");      // → { receivers: 1 }

// Inspect active channels
await cache.pubsubChannels("*");           // → { channels, num_subs, num_patterns }

// Stop streaming
sub.close();`}</Code>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/pubsub">Pub/Sub console</Link> lets you
        subscribe to a channel or pattern, watch messages stream in live, and
        publish — handy for debugging event flow without wiring up a client.
      </p>

      <Callout type="warning" title="Slow subscribers drop messages, they don't stall publishers">
        Each subscriber has a bounded buffer. If a consumer can't keep up, its
        oldest messages are dropped rather than blocking everyone else — pub/sub
        is fire-and-forget, with no per-subscriber backlog or replay. Use{" "}
        <Link to="/docs/commands#streams">Streams</Link> when you need durable,
        replayable delivery.
      </Callout>
    </>
  );
}
