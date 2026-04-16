import { Link } from "react-router-dom";
import { Code } from "../../components/Code";

export default function MemoryDocs() {
  return (
    <>
      <h1>User Memory Store</h1>
      <p className="lead">
        The memory store gives every user a small, persistent context that
        your LLM app can recall by meaning. Think of it as a per-user
        knowledge base that grows over time and ranks itself by relevance
        at read time.
      </p>

      <h2>API</h2>
      <table>
        <thead><tr><th>Command</th><th>Effect</th></tr></thead>
        <tbody>
          <tr><td><code>MEMORY_ADD user "text"</code></td><td>Stores a memory. Embedding is computed automatically.</td></tr>
          <tr><td><code>MEMORY_QUERY user "query"</code></td><td>Top-k semantic recall + synthesized context string.</td></tr>
          <tr><td><code>MEMORY_LIST user</code></td><td>All memories for this user.</td></tr>
          <tr><td><code>MEMORY_DEL user id</code></td><td>Remove a single memory.</td></tr>
        </tbody>
      </table>

      <h2>Example: chat with memory</h2>
      <Code lang="ts">{`import { NeuroCache } from "@neurocache/sdk";
import OpenAI from "openai";

const cache  = new NeuroCache({ baseUrl: "http://localhost:8080" });
const openai = new OpenAI();

async function reply(userId: string, message: string) {
  // 1. Pull the most relevant memories for this message
  const { context } = await cache.memory.query(userId, message, 6);

  // 2. Inject them as a system message
  const res = await openai.chat.completions.create({
    model: "gpt-4o-mini",
    messages: [
      { role: "system", content:
        "You remember facts about the user:\\n" + (context || "(no memories yet)") },
      { role: "user", content: message },
    ],
  });
  const answer = res.choices[0].message.content!;

  // 3. Optionally persist new facts you learn
  if (message.startsWith("remember ")) {
    await cache.memory.add(userId, message.slice(9));
  }
  return answer;
}`}</Code>

      <h2>Synthesized context</h2>
      <p>
        <code>MEMORY_QUERY</code> returns both the ranked hits <em>and</em>{" "}
        a pre-formatted string ready to paste into a system prompt:
      </p>
      <Code lang="bash">{`> MEMORY_QUERY user:dhirav "what tools does this user prefer?"
Based on stored context:
- Prefers Go on the backend and React on the frontend
- Uses Tailwind CSS for styling`}</Code>

      <h2>Top-k and threshold</h2>
      <p>On the HTTP API:</p>
      <Code lang="bash">{`curl "http://localhost:8080/api/memory/dhirav?q=tech+preferences&k=5&threshold=0.3"`}</Code>
      <ul>
        <li><code>k</code> — max results (default 5)</li>
        <li><code>threshold</code> — minimum similarity (default 0.3, looser than semantic cache because memories are usually phrased differently from queries)</li>
      </ul>

      <h2>Namespacing</h2>
      <p>
        Use meaningful user ids — <code>user:123</code> or{" "}
        <code>tenant:acme:user:42</code>. Memory is strictly scoped; no cross-user leakage.
      </p>

      <h2>Inspect in the dashboard</h2>
      <p>
        The <Link to="/dashboard/memory">Memory page</Link> lets you switch
        users, add entries, query semantically, and see the synthesized
        context live.
      </p>

      <h2>Best practices</h2>
      <ul>
        <li><strong>Keep memories short and factual.</strong> One fact per entry ("prefers dark mode") retrieves better than a paragraph.</li>
        <li><strong>Deduplicate before writing.</strong> If you're about to add a memory, <code>MEMORY_QUERY</code> first and skip if something similar already exists.</li>
        <li><strong>Prune periodically.</strong> Use <code>MEMORY_LIST</code> + <code>MEMORY_DEL</code> to cap per-user memory size.</li>
      </ul>
    </>
  );
}
