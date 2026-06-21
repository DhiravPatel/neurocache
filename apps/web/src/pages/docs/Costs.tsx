import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function CostsDocs() {
  return (
    <>
      <h1>Cost &amp; Budgets</h1>
      <p className="lead">
        NeuroCache tracks the LLM spend your cache avoids, prices it with a
        cost model you can tune at runtime, and lets you cap per-tenant spend
        with budgets that reject over-budget calls <em>before</em> you pay for
        them.
      </p>

      <h2>The savings cost model</h2>
      <p>
        Every LLM cache hit skips a provider call. To turn that into a dollar
        figure, NeuroCache prices each hit with two numbers: the assumed{" "}
        <strong>tokens saved per hit</strong> and the <strong>price per
        million tokens</strong>. The defaults are 1,000 tokens at
        $10 / million (OpenAI <code>gpt-4o-mini</code> ballpark).
      </p>
      <p>
        These were historically fixed at start-up. They are now{" "}
        <strong>tunable at runtime</strong> — over RESP or HTTP — and apply to
        every subsequent hit immediately:
      </p>
      <Code lang="bash">{`# Read the model
COST.MODEL
# → tokens_per_hit 1000  usd_per_million_tokens 10.000000

# Set tokens-per-hit and usd-per-million (either > 0; 0 leaves it unchanged)
COST.MODEL 1200 5.0

# Over HTTP
curl -s localhost:8080/api/cost-model                       # read
curl -s localhost:8080/api/cost-model \\
  -d '{"tokens_per_hit":1200,"usd_per_million_tokens":5.0}' # update`}</Code>
      <Callout type="info" title="Runtime, not persisted">
        The cost model is node-local runtime state and resets to the defaults
        on restart. Set your baseline at start-up via configuration; use{" "}
        <code>COST.MODEL</code> to adjust live. Changing the price re-prices
        future hits only — already-accrued savings are not retroactively
        recalculated.
      </Callout>

      <h2>Per-tenant budgets</h2>
      <p>
        For multi-tenant AI products, a single runaway loop can burn real
        money. A budget caps a tenant's spend over a sliding time window; once
        the cap is reached, further charges are rejected so your code can
        short-circuit before calling the model.
      </p>
      <Code lang="bash">{`# $5.00 cap over a 60s sliding window
COST.BUDGET acme 5.0 60000

# Record a spend — returns whether it fit, and the remainder
COST.CHARGE acme 0.40
# → allowed 1  remaining 4.6000

# A charge that would exceed the cap is rejected and NOT recorded
COST.CHARGE acme 100
# → allowed 0  remaining 4.6000

COST.USAGE acme        # used / remaining / max / window_ms
COST.RESET acme        # zero the spend log (keeps the budget)
COST.LIST              # every configured tenant`}</Code>
      <Callout type="warning" title="Charges over budget are rejected, not clamped">
        <code>COST.CHARGE</code> is all-or-nothing: if a charge would push a
        tenant past its cap, it returns <code>allowed: false</code> and records
        nothing. Check the result and skip the LLM call — don't assume the
        spend went through.
      </Callout>

      <h2>From the SDK</h2>
      <p>
        The TypeScript SDK exposes a typed <code>cost</code> namespace plus a{" "}
        <code>guardedSpend</code> helper that reserves budget before an
        expensive call and throws <code>BudgetExceededError</code> when there's
        no headroom:
      </p>
      <Code lang="ts">{`import { NeuroCache, BudgetExceededError } from "@neurocache/sdk";

const cache = new NeuroCache({ baseUrl: "http://localhost:8080" });

// Cap a tenant, then read usage
await cache.cost.setBudget("acme", 5.0, 60_000);
const usage = await cache.cost.usage("acme");   // { used, remaining, max, window_ms }

// Tune the savings cost model live
await cache.cost.setModel({ tokensPerHit: 1200, usdPerMillion: 5.0 });

// Reserve budget before an expensive call — throws if over budget
try {
  const answer = await cache.guardedSpend("acme", 0.4, async () => {
    const res = await openai.chat.completions.create(/* … */);
    return res.choices[0].message.content!;
  });
} catch (e) {
  if (e instanceof BudgetExceededError) {
    // e.tenant, e.attemptedUsd, e.remaining — fall back / queue / 429
  }
}`}</Code>

      <h2>In the dashboard</h2>
      <p>
        The <Link to="/dashboard/costs">Cost &amp; Budgets</Link> page shows
        cumulative and projected savings, a live savings-rate chart, an editor
        for the cost model, and a per-tenant budget table with usage bars — plus
        a "record a charge" control to watch the budget gate in action.
      </p>

      <Callout type="tip" title="Pair budgets with the LLM cache">
        Budgets bound your <em>worst case</em>; the{" "}
        <Link to="/docs/llm-cache">LLM response cache</Link> lowers your{" "}
        <em>average</em> case. Together they make spend both predictable and
        smaller.
      </Callout>
    </>
  );
}
