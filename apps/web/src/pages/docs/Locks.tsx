import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function LocksDocs() {
  return (
    <>
      <h1>Distributed Locks</h1>
      <p className="lead">
        NeuroCache ships lease-based distributed locks with monotonic{" "}
        <strong>fencing tokens</strong> — the correctness mechanism that plain
        <code> SETNX</code> locks famously lack. Every acquire returns a
        strictly increasing token so downstream systems can reject a stale lock
        holder, even one paused by a GC or network blip.
      </p>

      <Callout type="info" title="Why fencing tokens matter">
        A lease can expire while its holder is paused (GC, VM freeze), letting a
        second worker acquire the same lock — now two processes believe they
        hold it. A fencing token makes this safe: each acquire's token is
        higher than the last, so a resource that records the highest token it
        has seen will reject the older, fenced writer.
      </Callout>

      <h2>The commands</h2>
      <p>Over RESP, the lock is a single command family:</p>
      <Code lang="bash">{`# Acquire a 30s lease — returns a fencing token (integer), or nil if held
LOCK ACQUIRE job:reindex worker-a 30000
# → (integer) 41

# Renew the lease while you work (token stays the same)
LOCK EXTEND  job:reindex worker-a 30000      # → 1

# Inspect without taking the lock
LOCK CHECK   job:reindex                     # → [worker-a, 41, 28640]

# List everything currently held
LOCK LIST                                    # → [[job:reindex, worker-a, 41, 28640]]

# Release (only the owner can)
LOCK RELEASE job:reindex worker-a            # → 1`}</Code>
      <p>
        Acquire is <strong>reentrant by owner</strong> (the holder re-acquiring
        refreshes the TTL and bumps the token) and refuses a different owner
        while the lease is live. A crashed holder can never deadlock the
        lock — the lease simply expires.
      </p>

      <h2>Over HTTP</h2>
      <Code lang="bash">{`# Acquire
curl -s localhost:8080/api/locks/job:reindex/acquire \\
  -d '{"owner":"worker-a","ttl_ms":30000}'
# → {"acquired":true,"token":41}

curl -s localhost:8080/api/locks/job:reindex/extend  -d '{"owner":"worker-a","ttl_ms":30000}'
curl -s localhost:8080/api/locks/job:reindex/release -d '{"owner":"worker-a"}'
curl -s localhost:8080/api/locks/job:reindex          # → {"held":true,"owner":"worker-a","token":41,"remaining_ms":28640}
curl -s localhost:8080/api/locks                       # → {"locks":[ … ]}`}</Code>

      <h2>From the SDK</h2>
      <p>
        The SDK exposes the raw <code>locks</code> calls plus{" "}
        <code>withLock()</code> — which acquires (optionally waiting), keeps the
        lease alive in the background while your function runs, and always
        releases afterward. Your callback receives the fencing token to forward
        downstream:
      </p>
      <Code lang="ts">{`import { NeuroCache, LockAcquireTimeoutError } from "@neurocache/sdk";

const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

try {
  const result = await cache.withLock(
    "job:reindex",
    async (token) => {
      // critical section — pass \`token\` to any system that must fence writers
      return await reindexEverything(token);
    },
    { ttlMs: 30_000, waitMs: 5_000 },   // wait up to 5s to acquire; auto-extends
  );
} catch (e) {
  if (e instanceof LockAcquireTimeoutError) {
    // someone else holds it — back off and retry later
  }
}

// …or drive the lease yourself
const { acquired, token } = await cache.locks.acquire("job:reindex", "worker-a", 30_000);
await cache.locks.extend("job:reindex", "worker-a", 30_000);
await cache.locks.release("job:reindex", "worker-a");`}</Code>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/locks">Locks page</Link> shows every held lock
        with its owner, fencing token, and time to expiry, and gives you a
        console to acquire, extend, release, and check locks live.
      </p>

      <Callout type="warning" title="The token is the source of truth, not the lease">
        Auto-extension is best-effort — a long stall can still let a lease lapse.
        Don't rely on "I hold the lock so it's safe"; rely on the fencing token.
        Have the protected resource record the highest token it has accepted and
        reject anything lower. That's what makes the lock correct under pauses.
      </Callout>
    </>
  );
}
