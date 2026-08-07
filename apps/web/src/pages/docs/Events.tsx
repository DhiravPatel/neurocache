import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function EventsDocs() {
  return (
    <>
      <h1>Event Sourcing</h1>
      <p className="lead">
        An append-only event log with continuously-maintained projections. Append
        domain events, register a reducer (sum, count, last, …), and read the live
        aggregate at any time — the projection updates on every append, so there's
        no batch job to recompute.
      </p>

      <h2>Append &amp; project</h2>
      <Code lang="bash">{`# Append JSON events to a stream
curl localhost:8080/api/event/orders -d '{"type":"placed","amount":50}'
curl localhost:8080/api/event/orders -d '{"type":"placed","amount":30}'

# Register a projection — a reducer over a field, maintained incrementally
curl localhost:8080/api/event/orders/project \\
  -d '{"name":"revenue","reducer":"sum","field":"amount"}'

# Read the live aggregate
curl localhost:8080/api/event/orders/projection/revenue   # → {"projection":{"_total":80}}

curl "localhost:8080/api/event/orders/range?start=0&end=0" # raw events
curl localhost:8080/api/event/orders/len`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const e = cache.events;

await e.append("orders", { type: "placed", amount: 50 });
await e.project("orders", "revenue", "sum", { field: "amount" });
await e.project("orders", "count", "count");

const { projection } = await e.projection("orders", "revenue"); // live total
const { events } = await e.range("orders");                     // raw log`}</Code>
      <Callout type="tip" title="Projections vs. streams">
        Use <Link to="/docs/streams">Streams</Link> when you want the raw log with
        consumer groups; use event projections when you want a derived value
        (running total, count, last-seen, grouped tallies) kept up to date for
        you. Reducers include <code>count</code>, <code>sum</code>,{" "}
        <code>avg</code>, <code>min</code>, <code>max</code>, and{" "}
        <code>last</code>, optionally grouped by a field.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/events">Event Sourcing page</Link> appends events,
        registers projections, and shows the live aggregate alongside the raw
        event log.
      </p>
    </>
  );
}
