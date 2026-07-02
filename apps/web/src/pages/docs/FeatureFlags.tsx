import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function FeatureFlagsDocs() {
  return (
    <>
      <h1>Feature Flags</h1>
      <p className="lead">
        Percentage rollouts with <strong>sticky per-user bucketing</strong> plus
        allow/deny overrides. Flip a feature for a cohort without a deploy, ramp
        it gradually, and the same user always gets the same answer.
      </p>

      <h2>Define &amp; evaluate</h2>
      <Code lang="bash">{`# 25% rollout, always-on for one user, never for another
curl localhost:8080/api/flag/new-checkout \\
  -d '{"on":true,"percentage":25,"allow":["vip:1"],"deny":["bot:9"]}'

# Is it on for a given user? (deterministic + sticky)
curl "localhost:8080/api/flag/new-checkout/is?user=user-42"   # → {"enabled":false}
curl "localhost:8080/api/flag/new-checkout/is?user=vip:1"     # → {"enabled":true}

curl localhost:8080/api/flag/new-checkout    # full state + eval counts
curl localhost:8080/api/flag                  # list flags`}</Code>

      <h2>From the SDK</h2>
      <Code lang="ts">{`const f = cache.flags;
await f.set("new-checkout", { on: true, percentage: 25, allow: ["vip:1"] });

if ((await f.is("new-checkout", userId)).enabled) {
  return renderNewCheckout();
}

await f.allow("new-checkout", "vip:2");   // force-enable a user
await f.deny("new-checkout", "bot:9");    // force-disable a user`}</Code>
      <Callout type="info" title="Sticky rollouts, no client storage">
        Enablement hashes the (flag, user) pair, so a user stays in the same
        bucket as you ramp the percentage up — they won't flip in and out
        between requests, and you store nothing client-side. Allow/deny lists
        always win over the percentage.
      </Callout>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/flags">Feature Flags page</Link> toggles flags,
        drags the rollout percentage, evaluates a flag for a specific user, and
        shows live eval/enable counts.
      </p>
    </>
  );
}
