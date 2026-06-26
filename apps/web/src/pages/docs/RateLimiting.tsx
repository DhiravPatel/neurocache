import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function RateLimitingDocs() {
  return (
    <>
      <h1>Rate Limiting</h1>
      <p className="lead">
        NeuroCache ships a <strong>GCRA</strong> rate limiter (Generic Cell Rate
        Algorithm) — smooth bursts up to your cap, an exact recovery rate, and
        O(1) memory per key. Throttle by any key: user id, IP, tenant, or route.
      </p>

      <h2>The decision</h2>
      <p>Every check returns four values — allow plus the hints you need for back-off:</p>
      <Code lang="bash">{`# RESP — RATELIMIT key window-ms max [COST n]
RATELIMIT user:42 60000 100
# → [allowed=1, remaining=99, retry-after-ms=0, reset-ms=600]

# HTTP — returns 200 when allowed, 429 when denied
curl -i localhost:8080/api/ratelimit \\
  -d '{"key":"user:42","window_ms":60000,"max":100}'
# → {"allowed":true,"remaining":99,"retry_after_ms":0,"reset_ms":600}

curl localhost:8080/api/ratelimit/reset -d '{"key":"user:42"}'`}</Code>
      <Callout type="tip" title="429 is a feature, not an error">
        The HTTP endpoint returns <code>200</code> when allowed and{" "}
        <code>429 Too Many Requests</code> when denied, with{" "}
        <code>retry_after_ms</code> in the body — so you can proxy the status
        and set a <code>Retry-After</code> header directly.
      </Callout>

      <h2>From the SDK</h2>
      <p>
        <code>rateLimit()</code> returns the decision without throwing;{" "}
        <code>limit()</code> wraps a function and throws{" "}
        <code>RateLimitedError</code> when over the cap:
      </p>
      <Code lang="ts">{`import { NeuroCache, RateLimitedError } from "@neurocache/sdk";
const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

// Inspect the decision
const r = await cache.rateLimit("user:42", { windowMs: 60_000, max: 100 });
if (!r.allowed) return res.status(429).set("Retry-After", \`\${r.retry_after_ms}\`);

// …or gate a function
try {
  await cache.limit("ip:1.2.3.4", { windowMs: 1000, max: 5 }, () => handle(req));
} catch (e) {
  if (e instanceof RateLimitedError) return res.status(429).end(); // e.retryAfterMs
}`}</Code>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/ratelimit">Rate Limiting console</Link> lets you
        fire single requests or a burst against a key and watch the allow/deny
        decisions and recovery in real time.
      </p>
    </>
  );
}
