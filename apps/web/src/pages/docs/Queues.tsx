import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function QueuesDocs() {
  return (
    <>
      <h1>Queues</h1>
      <p className="lead">
        A durable job queue — not just a list. Priorities, automatic retries
        with backoff, a visibility timeout for at-least-once delivery, an
        idempotency key to dedupe enqueues, and a dead-letter queue for jobs
        that exhaust their attempts.
      </p>

      <Callout type="info" title="When to use this vs. a list or a stream">
        Plain lists (<code>LPUSH</code>/<code>BRPOP</code>) and streams are great
        building blocks, but this queue adds the operational pieces a worker pool
        needs: retry policy, visibility timeout, DLQ, and priorities. Reach for
        lists when you want a simple FIFO, and <Link to="/docs/streams">streams</Link>{" "}
        when you need a replayable log with consumer groups.
      </Callout>

      <h2>The lifecycle</h2>
      <Code lang="bash">{`# Enqueue (higher priority runs first; idempotency_key dedupes in-flight dupes)
curl localhost:8080/api/worker/emails \\
  -d '{"payload":"{\\"to\\":\\"a@b.com\\"}","priority":5,"idempotency_key":"welcome:42"}'
# → {"id":1}

# Reserve the next job for 30s (visibility timeout)
curl "localhost:8080/api/worker/emails/next?visibility_ms=30000"   # → {"job":{…}}

# Finish it…
curl -X POST localhost:8080/api/worker/emails/ack/1
# …or fail it (retries with backoff; dead-letters after max attempts)
curl -X POST localhost:8080/api/worker/emails/nack/1 -d '{"error":"smtp 500","delay_ms":5000}'

curl localhost:8080/api/worker/emails/stats   # {pending, reserved, dlq, …}
curl localhost:8080/api/worker/emails/dlq     # dead-lettered jobs
curl -X POST localhost:8080/api/worker/emails/requeue/1   # revive a DLQ job`}</Code>

      <h2>From the SDK</h2>
      <p>
        Drive jobs by hand, or let <code>work()</code> run the consumer loop —
        it reserves a job, runs your handler, then ACKs on success or NACKs (with
        the error) on throw:
      </p>
      <Code lang="ts">{`// Producer
await cache.queue.enqueue("emails", JSON.stringify(msg), { priority: 5, idempotencyKey: "welcome:42" });

// Consumer — auto ACK/NACK, with a visibility timeout
const worker = cache.work("emails", async (job) => {
  await sendEmail(JSON.parse(job.payload));      // throw to retry / eventually DLQ
}, { visibilityMs: 30_000, onError: console.error });

// …on shutdown
worker.stop();

await cache.queue.stats("emails");   // { pending, reserved, dlq, … }
await cache.queue.dlq("emails");     // inspect dead-lettered jobs`}</Code>
      <Callout type="warning" title="At-least-once, so make handlers idempotent">
        If a worker dies before ACKing, the visibility timeout returns the job to
        the queue and another worker picks it up — so a job can run more than
        once. Make handler side-effects idempotent (e.g. key on the job id).
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/queues">Queues monitor</Link> shows live
        pending / in-flight / dead-letter counts, lets you enqueue and process
        jobs, and requeue from the DLQ.
      </p>
    </>
  );
}
