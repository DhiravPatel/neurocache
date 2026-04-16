import { Link } from "react-router-dom";
import { Code } from "../../components/Code";

export default function QuickStart() {
  return (
    <>
      <h1>Quick Start</h1>
      <p className="lead">
        Five minutes, six commands. We'll set a key, cache a value by
        meaning, wrap an LLM call, and store per-user memory.
      </p>

      <h2>1. Start the engine</h2>
      <Code lang="bash">{`docker run -d --name neurocache -p 8080:8080 -p 6379:6379 neurocache/engine:latest`}</Code>
      <p>
        Open <a href="/dashboard" target="_self">http://localhost:8080</a> — your
        dashboard is live.
      </p>

      <h2>2. A plain KV set / get</h2>
      <Code lang="bash">{`redis-cli -p 6379
> SET user:dhirav "hello"
OK
> GET user:dhirav
"hello"`}</Code>
      <p>
        Everything Redis does, NeuroCache does — same wire protocol, same
        clients.
      </p>

      <h2>3. Semantic cache</h2>
      <p>Now store a value by its <em>meaning</em>:</p>
      <Code lang="bash">{`> SEMANTIC_SET "best backend language for APIs" "Go is ideal for APIs"
OK
> SEMANTIC_GET "what language should I use for my backend?"
"Go is ideal for APIs"
> SEMANTIC_GET "top programming language for REST services"
"Go is ideal for APIs"`}</Code>
      <p>
        Three different phrasings, one cached answer. See{" "}
        <Link to="/docs/semantic-cache">Semantic Cache</Link> for how
        scoring works and how to tune the threshold.
      </p>

      <h2>4. LLM response cache</h2>
      <p>
        The common pattern: you have a prompt, you want to call OpenAI, but
        you'd rather not pay for the same completion twice.
      </p>
      <Code lang="ts">{`import { NeuroCache } from "@neurocache/sdk";
import OpenAI from "openai";

const cache  = new NeuroCache({ baseUrl: "http://localhost:8080" });
const openai = new OpenAI();

async function ask(prompt: string) {
  const { value } = await cache.cacheLLMAround(
    prompt,
    async () => {
      const res = await openai.chat.completions.create({
        model: "gpt-4o-mini",
        messages: [{ role: "user", content: prompt }],
      });
      return res.choices[0].message.content!;
    },
    { threshold: 0.88 },
  );
  return value;
}

await ask("Write a haiku about Mondays");
await ask("Compose a haiku themed around Mondays");   // cache hit!`}</Code>
      <p>
        Watch the hit rate and dollar savings climb on the{" "}
        <Link to="/dashboard/analytics">Analytics page</Link>.
      </p>

      <h2>5. Per-user memory</h2>
      <Code lang="bash">{`> MEMORY_ADD user:dhirav "Prefers Go on the backend and React on the frontend"
> MEMORY_ADD user:dhirav "Is building a product called NeuroCache"
> MEMORY_QUERY user:dhirav "what is this user working on?"
Based on stored context:
- Is building a product called NeuroCache
- Prefers Go on the backend and React on the frontend`}</Code>

      <h2>6. Open the dashboard</h2>
      <p>
        Everything you just did shows up live at{" "}
        <Link to="/dashboard/analytics">Dashboard → Analytics</Link> — hit
        rate, hot keys, command breakdown, estimated LLM savings, and a
        rolling 60-second timeline.
      </p>

      <p className="mt-8 text-sm text-slate-500">
        Ready to integrate properly? Go to{" "}
        <Link to="/docs/sdks">SDKs</Link> or browse the full{" "}
        <Link to="/docs/commands">command reference</Link>.
      </p>
    </>
  );
}
