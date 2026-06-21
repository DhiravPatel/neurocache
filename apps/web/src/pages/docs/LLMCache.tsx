import { Link } from "react-router-dom";
import { Code } from "../../components/Code";
import { Callout } from "../../components/docs/Callout";

export default function LLMCache() {
  return (
    <>
      <h1>LLM Response Cache</h1>
      <p className="lead">
        The LLM cache is a semantic cache tuned for wrapping LLM API calls.
        Similar prompts return the same cached answer instead of re-running
        the model — reducing latency and cost.
      </p>

      <h2>The minimum setup</h2>
      <Code lang="ts">{`import { NeuroCache } from "@neurocache/sdk";
import OpenAI from "openai";

const cache  = new NeuroCache({ baseUrl: "http://localhost:8080" });
const openai = new OpenAI();

export async function ask(prompt: string) {
  // Look up first
  const cached = await cache.cacheLLMGet(prompt, 0.88);
  if (cached.hit) return cached.response!;

  // Miss — call the LLM
  const res = await openai.chat.completions.create({
    model: "gpt-4o-mini",
    messages: [{ role: "user", content: prompt }],
  });
  const answer = res.choices[0].message.content!;

  // Write-through
  await cache.cacheLLM(prompt, answer);
  return answer;
}`}</Code>

      <h2>Helper: <code>cacheLLMAround</code></h2>
      <p>The SDK ships with a wrapper that does the get / on-miss / set dance for you:</p>
      <Code lang="ts">{`const { value, hit, score } = await cache.cacheLLMAround(
  prompt,
  async () => {
    const res = await openai.chat.completions.create({
      model: "gpt-4o-mini",
      messages: [{ role: "user", content: prompt }],
    });
    return res.choices[0].message.content!;
  },
  { threshold: 0.88 },
);`}</Code>
      <p>
        <code>hit</code> is <code>true</code> if the value came from cache,
        and <code>score</code> shows the similarity match. Use those in
        your own logging if you don't want to lean on the dashboard.
      </p>

      <h2>Tuning the threshold</h2>
      <p>
        <code>0.88</code> is the default for the LLM cache (higher than the
        generic semantic cache's <code>0.75</code>). LLM completions are
        more sensitive to wording than free-form knowledge base answers, so
        you want stricter matching by default.
      </p>
      <p>
        Drop it to <code>0.80–0.85</code> for conversational Q&amp;A bots
        where any "close enough" answer is fine. Keep it at <code>0.92+</code>{" "}
        for code generation or deterministic tasks where a subtle prompt
        change might mean a totally different output.
      </p>
      <Callout type="note" title="Picking a threshold">
        Because LLM completions are more sensitive to wording, the default
        starts stricter at <code>0.88</code> than the generic semantic cache's{" "}
        <code>0.75</code>. Loosen it for conversational Q&amp;A; tighten it for
        deterministic tasks.
      </Callout>

      <h2>What shows up in the dashboard</h2>
      <p>
        The <Link to="/dashboard">Dashboard</Link> reports live LLM cache
        hit rate and an <strong>estimated dollar savings</strong> figure.
        The estimate assumes 1,000 tokens per cached response at
        $10 / million tokens (OpenAI's <code>gpt-4o-mini</code> ballpark) by
        default — but you can now retune both <em>at runtime</em>, no restart
        required:
      </p>
      <Code lang="bash">{`# Read the current cost model
COST.MODEL
# → tokens_per_hit 1000  usd_per_million_tokens 10.000000

# Update it live (tokens-per-hit, usd-per-million)
COST.MODEL 1200 5.0

# Or over HTTP
curl -s localhost:8080/api/cost-model \\
  -d '{"tokens_per_hit":1200,"usd_per_million_tokens":5.0}'`}</Code>
      <Callout type="tip" title="Higher hit rate, lower bill">
        Every cache hit skips an LLM call, so a better hit rate directly cuts
        latency and cost. Watch the live hit rate, tune the cost model, and cap
        per-tenant spend on the{" "}
        <Link to="/docs/costs">Cost &amp; Budgets</Link> page — also surfaced in
        the <Link to="/dashboard/costs">dashboard</Link>.
      </Callout>

      <h2>Patterns</h2>
      <h3>Normalize prompts before caching</h3>
      <p>
        Trim whitespace, lowercase, strip trailing punctuation. Your hit
        rate will thank you.
      </p>
      <Code lang="ts">{`const key = prompt.trim().replace(/\\s+/g, " ").toLowerCase();`}</Code>

      <h3>Include relevant context in the key</h3>
      <p>
        If the answer depends on a user id or tenant, put that in the key —
        otherwise you may leak one user's answer to another:
      </p>
      <Code lang="ts">{`await cache.cacheLLM(\`[\${userId}] \${prompt}\`, answer);`}</Code>

      <h3>Skip caching for personalized output</h3>
      <p>
        If your prompt explicitly asks for creativity or fresh output
        ("generate 5 new ideas…"), don't cache it.
      </p>
    </>
  );
}
