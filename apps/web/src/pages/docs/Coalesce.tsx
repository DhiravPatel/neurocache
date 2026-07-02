import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function CoalesceDocs() {
  return (
    <>
      <h1>Request Coalescing</h1>
      <p className="lead">
        Single-flight protection against thundering herds. When a hundred users
        ask the same question in the same second, a naive cache fires a hundred
        upstream LLM calls on the miss — and pays a hundred times over. Coalescing
        lets the first caller do the one real call while everyone else waits and is
        handed the same answer.
      </p>

      <h2>The protocol</h2>
      <p>
        Coalescing is a three-verb protocol over a shared key — usually the same
        key you would cache the answer under:
      </p>
      <ul>
        <li>
          <code>lock(key)</code> — atomically become the owner. Returns{" "}
          <code>owner: true</code> plus a <code>token</code> for exactly one
          caller; every other concurrent caller gets <code>owner: false</code>.
        </li>
        <li>
          <code>publish(key, token, result)</code> — the owner posts the answer;
          every waiter wakes with it.
        </li>
        <li>
          <code>wait(key)</code> — a non-owner blocks until the result is published
          (or the wait times out), then reads it.
        </li>
      </ul>
      <p>
        Because the lock lives in the cache — the one process every replica already
        talks to — coalescing works <em>across</em> your whole fleet, not just
        within a single node's memory.
      </p>

      <h2>Wrapping an expensive call</h2>
      <Code lang="ts">{`async function answer(question: string) {
  const key = "llm:answer:" + hash(question);

  // 1. Fast path: already cached?
  const cached = await cache.llm.get(question);
  if (cached.hit) return cached.response;

  // 2. Coalesce the miss.
  const { owner, token } = await cache.coalesce.lock(key, 10_000);
  if (owner) {
    const result = await callLLM(question);      // only the winner pays
    await cache.coalesce.publish(key, token, result);
    await cache.llm.set(question, result);        // populate the real cache
    return result;
  }

  // 3. Everyone else waits for the winner's result.
  const { got, result } = await cache.coalesce.wait(key, 10_000);
  return got ? result : callLLM(question);        // fall back if it timed out
}`}</Code>

      <h2>Over HTTP</h2>
      <Code lang="bash">{`# First caller wins the lock:
curl localhost:8080/api/coalesce/lock -d '{"key":"q:42","timeout_ms":10000}'
# → {"owner": true, "token": "a1b2c3…"}

# Concurrent callers get owner:false and should wait:
curl localhost:8080/api/coalesce/lock -d '{"key":"q:42","timeout_ms":10000}'
# → {"owner": false, "token": ""}

# Owner publishes the one answer; every waiter wakes with it:
curl localhost:8080/api/coalesce/publish \\
  -d '{"key":"q:42","token":"a1b2c3…","result":"…the answer…"}'
# → {"published": true}`}</Code>

      <h2>Stale-owner recovery</h2>
      <p>
        If the owner crashes before publishing, the lock would otherwise wedge the
        herd forever. Each lock carries a <code>timeout_ms</code>; once it elapses
        without a publish, the entry becomes <strong>stale</strong> and the next{" "}
        <code>lock</code> caller reclaims ownership and retries the work. Set the
        timeout to a little more than your upstream's worst-case latency.
      </p>

      <Callout type="tip" title="Save rate is your dedup ratio">
        The coalescer tracks <code>save_rate</code> = contended locks ÷ total locks
        — the fraction of would-be upstream calls it eliminated. On bursty,
        popular-query traffic this is often the single biggest lever on both cost
        and tail latency.
      </Callout>

      <Callout type="warning" title="Coalesce misses, cache hits">
        Coalescing only helps <em>while a value is being computed</em>. Once the
        answer is cached, serve it from the{" "}
        <Link to="/docs/semantic-cache">semantic</Link> or LLM cache directly —
        don't route hits through <code>lock</code>. Use coalescing for the miss,
        the cache for the hit.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/coalesce">Coalescing console</Link> lets you
        simulate a herd of N concurrent callers against a key and watch the fan-in:
        one leader does the work, the rest are served its result, and the live save
        rate and "calls saved" counters update as you go.
      </p>
    </>
  );
}
